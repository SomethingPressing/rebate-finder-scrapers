package db

// promoter.go — moves pending rows from scraper.rebates_staging into the live
// public.rebates table using GORM for all data writes (no hand-written SQL for
// inserts or updates).
//
// Pipeline
// ────────
//  1. Fetch all pending rows from scraper.rebates_staging.
//  2. Group by stg_program_hash; sort each group by source priority (highest first).
//  3. Merge each group into one canonical LiveRebate struct.
//  4. Batch-upsert into public.rebates (ON CONFLICT program_hash → update data cols).
//  5. Look up the actual rebate IDs (ON CONFLICT may have kept existing IDs).
//  6. Bulk-upsert public.zipcodes + public.rebate_zipcodes (ON CONFLICT do nothing).
//  7. Bulk-update staging rows to stg_promotion_status = 'promoted'.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/models"
	"gorm.io/gorm/clause"
)

// DefaultSourcePriority is the fallback when PROMOTER_SOURCE_PRIORITY is not set.
var DefaultSourcePriority = []string{"rewiring_america", "dsireusa", "energy_star"}

// validIncentiveFormats mirrors the incentive_format_type PostgreSQL enum.
var validIncentiveFormats = map[string]bool{
	"dollar_amount": true, "percent": true, "per_unit": true,
	"tiered": true, "tax_credit": true, "tax_deduction": true,
	"tax_exemption": true, "financing": true, "bill_credit": true,
	"free_product": true, "performance": true, "narrative": true,
}

// validUnitTypes mirrors the incentive_unit_type PostgreSQL enum.
var validUnitTypes = map[string]bool{
	"watt": true, "kilowatt": true, "kwh": true, "mmbtu": true,
	"sqft": true, "linear_ft": true, "ton": true, "unit": true,
	"port": true, "month": true, "year": true, "percent_saved": true,
	"other": true,
}

// PromoteOptions controls a promotion run.
type PromoteOptions struct {
	// DryRun prints what would be promoted without writing anything.
	DryRun bool
	// SourcePriority is an ordered list of scraper names; highest priority first.
	// Falls back to DefaultSourcePriority when nil/empty.
	SourcePriority []string
}

// PromoteResult summarises a completed promotion run.
type PromoteResult struct {
	StagingRows  int // total pending rows found in staging
	Programs     int // unique programs (groups by stg_program_hash)
	Promoted     int // staging rows moved to 'promoted'
	Merged       int // groups that had more than one source
	Failed       int // groups that could not be matched after upsert
	ZipsWritten  int // unique zipcodes upserted
	LinksWritten int // rebate↔zipcode links inserted
}

// Promote runs the full staging → live promotion pipeline.
func Promote(d *DB, opts PromoteOptions) (*PromoteResult, error) {
	priority := opts.SourcePriority
	if len(priority) == 0 {
		priority = DefaultSourcePriority
	}

	schema := models.ScraperSchema
	stgTable := schema + ".rebates_staging"

	// ── Phase 1: fetch all pending staging rows ───────────────────────────────
	var pending []models.StagedRebate
	if err := d.gorm.
		Table(stgTable).
		Where("stg_promotion_status = ? AND deleted_at IS NULL", models.PromotionPending).
		Order("id ASC").
		Find(&pending).Error; err != nil {
		return nil, fmt.Errorf("promote: fetch pending: %w", err)
	}

	result := &PromoteResult{StagingRows: len(pending)}
	if len(pending) == 0 {
		return result, nil
	}

	// ── Phase 2: group by stg_program_hash, sort by source priority ───────────
	type group struct {
		hash string
		rows []models.StagedRebate
	}

	hashOrder := make([]string, 0, len(pending))
	groupMap := make(map[string]*group, len(pending))

	for _, row := range pending {
		hash := row.ProgramHash
		if hash == "" {
			hash = models.ComputeProgramHash(row.ProgramName, row.UtilityCompany)
		}
		g, ok := groupMap[hash]
		if !ok {
			hashOrder = append(hashOrder, hash)
			g = &group{hash: hash}
			groupMap[hash] = g
		}
		g.rows = append(g.rows, row)
	}

	for _, g := range groupMap {
		sort.SliceStable(g.rows, func(i, j int) bool {
			return promoterSourceRank(g.rows[i].Source, priority) <
				promoterSourceRank(g.rows[j].Source, priority)
		})
	}

	result.Programs = len(hashOrder)
	for _, h := range hashOrder {
		if len(groupMap[h].rows) > 1 {
			result.Merged++
		}
	}

	// ── Dry-run: print preview and return ────────────────────────────────────
	if opts.DryRun {
		for _, h := range hashOrder {
			g := groupMap[h]
			if len(g.rows) == 1 {
				r := g.rows[0]
				fmt.Printf("  · [%s] %q\n", r.Source, r.ProgramName)
			} else {
				prefix := h
				if len(prefix) > 12 {
					prefix = prefix[:12]
				}
				fmt.Printf("  · MERGE (%d sources) hash=%s…\n", len(g.rows), prefix)
				for _, r := range g.rows {
					fmt.Printf("      %-22s %q\n", r.Source, r.ProgramName)
				}
			}
		}
		return result, nil
	}

	// ── Phase 3: build LiveRebate structs ─────────────────────────────────────
	now := time.Now()
	draft := "draft"
	isFeatured := false
	processed := false

	type groupMeta struct {
		hash    string
		rows    []models.StagedRebate
		allZips []string
	}

	rebates := make([]models.LiveRebate, 0, len(hashOrder))
	metas := make([]groupMeta, 0, len(hashOrder))

	for _, h := range hashOrder {
		g := groupMap[h]
		merged := mergePromoterGroup(g.rows)
		allZips := collectPromoterZips(g.rows)
		primary := g.rows[0]
		hash := h

		lr := models.LiveRebate{
			ID:          primary.SourceID,
			ProgramHash: &hash,

			// INSERT defaults — excluded from ON CONFLICT update list.
			Status:     &draft,
			IsFeatured: &isFeatured,
			Processed:  &processed,
			CreatedAt:  &now,

			// Data columns — updated on conflict.
			ProgramName:          merged.programName,
			UtilityCompany:       merged.utilityCompany,
			IncentiveDescription: merged.incentiveDescription,
			IncentiveAmount:      merged.incentiveAmount,
			MaximumAmount:        merged.maximumAmount,
			PercentValue:         merged.percentValue,
			PerUnitAmount:        merged.perUnitAmount,
			IncentiveFormat:      toValidFormatType(merged.incentiveFormat),
			UnitType:             toValidUnitType(merged.unitType),
			State:                merged.state,
			ServiceTerritory:     merged.serviceTerritory,
			AvailableNationwide:  merged.availableNationwide,
			CategoryTag:          models.StringSlice(merged.categoryTag),
			Segment:              models.StringSlice(merged.segment),
			Portfolio:            models.StringSlice(merged.portfolio),
			CustomerType:         merged.customerType,
			ProductCategory:      merged.productCategory,
			Administrator:        merged.administrator,
			Source:               ptrStr(primary.Source),
			Sources:              models.StringSlice(merged.sources),
			StartDate:            merged.startDate,
			EndDate:              merged.endDate,
			WhileFundsLast:       merged.whileFundsLast,
			ApplicationURL:       merged.applicationURL,
			ApplicationProcess:   merged.applicationProcess,
			ProgramURL:           merged.programURL,
			ContactEmail:         merged.contactEmail,
			ContactPhone:         merged.contactPhone,
			ImageURL:             merged.imageURL,
			ImageURLs:            models.StringSlice(merged.imageURLs),
			ContractorRequired:   merged.contractorRequired,
			EnergyAuditRequired:  merged.energyAuditRequired,
			RateTiers:            models.RateTiersJSON(merged.rateTiers),
			ScraperVersion:       ptrStr(primary.ScraperVersion),
			UpdatedAt:            &now,
		}

		rebates = append(rebates, lr)
		metas = append(metas, groupMeta{hash: h, rows: g.rows, allZips: allZips})
	}

	// ── Phase 4: batch-upsert into public.rebates ────────────────────────────
	// ON CONFLICT (program_hash) → update data columns only.
	// status / is_featured / processed / created_at are never overwritten.
	if err := d.gorm.
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "program_hash"}},
			DoUpdates: clause.AssignmentColumns(models.LiveRebateUpdateCols()),
		}).
		CreateInBatches(rebates, 500).Error; err != nil {
		return result, fmt.Errorf("promote: upsert rebates: %w", err)
	}

	// ── Phase 5: fetch actual rebate IDs ─────────────────────────────────────
	// ON CONFLICT may have kept the original ID on existing rows, so we can't
	// assume the IDs we tried to insert are the ones that were used.
	hashes := make([]string, len(hashOrder))
	copy(hashes, hashOrder)

	var idRows []struct {
		ID          string `gorm:"column:id"`
		ProgramHash string `gorm:"column:program_hash"`
	}
	if err := d.gorm.
		Table("rebates").
		Select("id, program_hash").
		Where("program_hash IN ?", hashes).
		Find(&idRows).Error; err != nil {
		return result, fmt.Errorf("promote: fetch rebate ids: %w", err)
	}

	hashToID := make(map[string]string, len(idRows))
	for _, r := range idRows {
		hashToID[r.ProgramHash] = r.ID
	}

	// ── Phase 6: bulk-upsert zipcodes, rebate_zipcodes, rebate_zipcode_sources ──
	//
	// For each (rebate, zip) pair we write:
	//   • rebate_zipcodes        — one row, stagingSourceId = highest-priority source
	//   • rebate_zipcode_sources — one row PER source that covers this zip

	type zipEntry struct {
		rebateID string
		zip      string
		// All sources that cover this zip for this rebate, in priority order.
		// Each entry: (scraper name, stg_source_id)
		sources []struct{ name, stagingID string }
	}

	// Build a map keyed by (rebateID, zip) so we can accumulate sources.
	type zipKey struct{ rebateID, zip string }
	zipMap := make(map[zipKey]*zipEntry)
	zipOrder := make([]zipKey, 0)

	for _, meta := range metas {
		rebateID, ok := hashToID[meta.hash]
		if !ok {
			continue
		}
		// meta.rows is already sorted by source priority (highest first).
		for _, row := range meta.rows {
			zips := meta.allZips
			// Collect only the zips this specific row contributed.
			rowZips := make(map[string]struct{})
			if row.ZipCode != nil && *row.ZipCode != "" {
				rowZips[*row.ZipCode] = struct{}{}
			}
			for _, z := range row.ZipCodes {
				rowZips[z] = struct{}{}
			}
			_ = zips
			for z := range rowZips {
				k := zipKey{rebateID, z}
				if _, exists := zipMap[k]; !exists {
					zipMap[k] = &zipEntry{rebateID: rebateID, zip: z}
					zipOrder = append(zipOrder, k)
				}
				zipMap[k].sources = append(zipMap[k].sources, struct{ name, stagingID string }{
					name:      row.Source,
					stagingID: row.SourceID,
				})
			}
		}
	}

	if len(zipOrder) > 0 {
		// 6a. Upsert unique zip codes into public.zipcodes.
		uniqueZips := make(map[string]struct{}, len(zipOrder))
		for _, k := range zipOrder {
			uniqueZips[k.zip] = struct{}{}
		}
		zipcodeRows := make([]models.LiveZipcode, 0, len(uniqueZips))
		for z := range uniqueZips {
			zipcodeRows = append(zipcodeRows, models.LiveZipcode{Code: z})
		}
		if err := d.gorm.
			Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(zipcodeRows, 5000).Error; err != nil {
			return result, fmt.Errorf("promote: insert zipcodes: %w", err)
		}
		result.ZipsWritten = len(uniqueZips)

		// 6b. Insert rebate_zipcodes — one row per (rebate, zip).
		// stagingSourceId = highest-priority source (first in the sources slice).
		linkRows := make([]models.LiveRebateZipcode, 0, len(zipOrder))
		for _, k := range zipOrder {
			e := zipMap[k]
			link := models.LiveRebateZipcode{RebateID: e.rebateID, ZipcodeCode: e.zip}
			if len(e.sources) > 0 && e.sources[0].stagingID != "" {
				link.StagingSourceID = ptrStr(e.sources[0].stagingID)
			}
			linkRows = append(linkRows, link)
		}
		if err := d.gorm.
			Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(linkRows, 5000).Error; err != nil {
			return result, fmt.Errorf("promote: insert rebate_zipcodes: %w", err)
		}
		result.LinksWritten = len(linkRows)

		// 6c. Insert rebate_zipcode_sources — one row per (rebate, zip, source).
		// ON CONFLICT (rebateId, zipcodeCode, source) DO NOTHING — safe to re-run.
		sourceRows := make([]models.LiveRebateZipcodeSource, 0, len(zipOrder)*2)
		for _, k := range zipOrder {
			e := zipMap[k]
			for _, src := range e.sources {
				row := models.LiveRebateZipcodeSource{
					RebateID:    e.rebateID,
					ZipcodeCode: e.zip,
					Source:      src.name,
				}
				if src.stagingID != "" {
					row.StagingSourceID = ptrStr(src.stagingID)
				}
				sourceRows = append(sourceRows, row)
			}
		}
		if err := d.gorm.
			Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(sourceRows, 5000).Error; err != nil {
			return result, fmt.Errorf("promote: insert rebate_zipcode_sources: %w", err)
		}
	}

	// ── Phase 7: mark staging rows as promoted ────────────────────────────────
	type stagingUpdate struct {
		rebateID   string
		stagingIDs []uint
	}
	var stagingUpdates []stagingUpdate
	for _, meta := range metas {
		rebateID, ok := hashToID[meta.hash]
		if !ok {
			result.Failed += len(meta.rows)
			continue
		}
		ids := make([]uint, len(meta.rows))
		for i, r := range meta.rows {
			ids[i] = r.ID // gorm.Model.ID is uint
		}
		stagingUpdates = append(stagingUpdates, stagingUpdate{rebateID: rebateID, stagingIDs: ids})
		result.Promoted += len(meta.rows)
	}

	for _, u := range stagingUpdates {
		if err := d.gorm.
			Table(stgTable).
			Where("id IN ? AND stg_promotion_status = ?", u.stagingIDs, models.PromotionPending).
			Updates(map[string]interface{}{
				"stg_promotion_status": models.PromotionPromoted,
				"stg_promoted_at":      now,
				"stg_rebate_id":        u.rebateID,
			}).Error; err != nil {
			return result, fmt.Errorf("promote: update staging status: %w", err)
		}
	}

	return result, nil
}

// ── Merge helpers ─────────────────────────────────────────────────────────────

// promoterMerged holds the canonical field set after merging a group.
type promoterMerged struct {
	programName          string
	utilityCompany       string
	incentiveDescription *string
	incentiveAmount      *float64
	maximumAmount        *float64
	percentValue         *float64
	perUnitAmount        *float64
	incentiveFormat      *string
	unitType             *string
	state                *string
	serviceTerritory     *string
	availableNationwide  *bool
	categoryTag          []string
	segment              []string
	portfolio            []string
	customerType         *string
	productCategory      *string
	administrator        *string
	sources              []string
	startDate            *string
	endDate              *string
	whileFundsLast       *bool
	applicationURL       *string
	applicationProcess   *string
	programURL           *string
	contactEmail         *string
	contactPhone         *string
	imageURL             *string
	imageURLs            []string
	contractorRequired   *bool
	energyAuditRequired  *bool
	rateTiers            models.RateTiersJSON
}

// mergePromoterGroup merges a priority-sorted slice of staging rows into one
// canonical field set.  rows[0] is the highest-priority source.
func mergePromoterGroup(rows []models.StagedRebate) promoterMerged {
	m := promoterMerged{
		// Non-nullable fields — always take from the primary (highest priority) row.
		programName:    rows[0].ProgramName,
		utilityCompany: rows[0].UtilityCompany,

		// Nullable scalars — first non-null from priority-sorted rows.
		incentiveDescription: pickText(rows, func(r models.StagedRebate) *string { return r.IncentiveDescription }),
		incentiveAmount:      pickFloat(rows, func(r models.StagedRebate) *float64 { return r.IncentiveAmount }),
		maximumAmount:        pickFloat(rows, func(r models.StagedRebate) *float64 { return r.MaximumAmount }),
		percentValue:         pickFloat(rows, func(r models.StagedRebate) *float64 { return r.PercentValue }),
		perUnitAmount:        pickFloat(rows, func(r models.StagedRebate) *float64 { return r.PerUnitAmount }),
		incentiveFormat:      pickText(rows, func(r models.StagedRebate) *string { return r.IncentiveFormat }),
		unitType:             pickText(rows, func(r models.StagedRebate) *string { return r.UnitType }),
		state:                pickText(rows, func(r models.StagedRebate) *string { return r.State }),
		serviceTerritory:     pickText(rows, func(r models.StagedRebate) *string { return r.ServiceTerritory }),
		availableNationwide:  pickBool(rows, func(r models.StagedRebate) *bool { return r.AvailableNationwide }),
		customerType:         pickText(rows, func(r models.StagedRebate) *string { return r.CustomerType }),
		productCategory:      pickText(rows, func(r models.StagedRebate) *string { return r.ProductCategory }),
		administrator:        pickText(rows, func(r models.StagedRebate) *string { return r.Administrator }),
		startDate:            pickText(rows, func(r models.StagedRebate) *string { return r.StartDate }),
		endDate:              pickText(rows, func(r models.StagedRebate) *string { return r.EndDate }),
		whileFundsLast:       pickBool(rows, func(r models.StagedRebate) *bool { return r.WhileFundsLast }),
		applicationURL:       pickText(rows, func(r models.StagedRebate) *string { return r.ApplicationURL }),
		applicationProcess:   pickText(rows, func(r models.StagedRebate) *string { return r.ApplicationProcess }),
		programURL:           pickText(rows, func(r models.StagedRebate) *string { return r.ProgramURL }),
		contactEmail:         pickText(rows, func(r models.StagedRebate) *string { return r.ContactEmail }),
		contactPhone:         pickText(rows, func(r models.StagedRebate) *string { return r.ContactPhone }),
		imageURL:             pickText(rows, func(r models.StagedRebate) *string { return r.ImageURL }),

		// Arrays — union across all sources, dedup case-insensitively.
		categoryTag: unionStrings(rows, func(r models.StagedRebate) []string { return r.CategoryTag }),
		segment:     unionStrings(rows, func(r models.StagedRebate) []string { return r.Segment }),
		portfolio:   unionStrings(rows, func(r models.StagedRebate) []string { return r.Portfolio }),
		imageURLs:   unionStrings(rows, func(r models.StagedRebate) []string { return r.ImageURLs }),
	}

	// Sources — collect all unique source names (preserves priority order).
	seenSrc := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if _, ok := seenSrc[r.Source]; !ok {
			seenSrc[r.Source] = struct{}{}
			m.sources = append(m.sources, r.Source)
		}
	}

	// RateTiers — take from primary if non-empty, else first non-empty.
	if len(rows[0].RateTiers) > 0 {
		m.rateTiers = rows[0].RateTiers
	} else {
		for _, r := range rows[1:] {
			if len(r.RateTiers) > 0 {
				m.rateTiers = r.RateTiers
				break
			}
		}
	}

	return m
}

// collectPromoterZips gathers all ZIP codes from every row in a group,
// deduplicating and sorting the result.
func collectPromoterZips(rows []models.StagedRebate) []string {
	seen := make(map[string]struct{}, 64)
	var zips []string
	add := func(z string) {
		if z == "" {
			return
		}
		if _, ok := seen[z]; !ok {
			seen[z] = struct{}{}
			zips = append(zips, z)
		}
	}
	for _, r := range rows {
		if r.ZipCode != nil {
			add(*r.ZipCode)
		}
		for _, z := range r.ZipCodes {
			add(z)
		}
	}
	sort.Strings(zips)
	return zips
}

// ── Field-pick helpers ────────────────────────────────────────────────────────

func pickText(rows []models.StagedRebate, get func(models.StagedRebate) *string) *string {
	for _, r := range rows {
		v := get(r)
		if v != nil && strings.TrimSpace(*v) != "" {
			return v
		}
	}
	return nil
}

func pickFloat(rows []models.StagedRebate, get func(models.StagedRebate) *float64) *float64 {
	for _, r := range rows {
		if v := get(r); v != nil {
			return v
		}
	}
	return nil
}

func pickBool(rows []models.StagedRebate, get func(models.StagedRebate) *bool) *bool {
	for _, r := range rows {
		if v := get(r); v != nil {
			return v
		}
	}
	return nil
}

func unionStrings(rows []models.StagedRebate, get func(models.StagedRebate) []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, r := range rows {
		for _, s := range get(r) {
			lc := strings.ToLower(s)
			if _, ok := seen[lc]; !ok {
				seen[lc] = struct{}{}
				out = append(out, s)
			}
		}
	}
	return out
}

// ── Enum guards ───────────────────────────────────────────────────────────────

// toValidFormatType returns v if it is a valid incentive_format_type enum value,
// otherwise nil.  Prevents PostgreSQL from rejecting free-text strings from
// scrapers that produce non-standard values.
func toValidFormatType(v *string) *string {
	if v != nil && validIncentiveFormats[*v] {
		return v
	}
	return nil
}

// toValidUnitType returns v if it is a valid incentive_unit_type enum value,
// otherwise nil.
func toValidUnitType(v *string) *string {
	if v != nil && validUnitTypes[*v] {
		return v
	}
	return nil
}

// ── Misc helpers ──────────────────────────────────────────────────────────────

func promoterSourceRank(source string, priority []string) int {
	for i, p := range priority {
		if p == source {
			return i
		}
	}
	return len(priority) // unlisted → lowest priority
}

func ptrStr(s string) *string { return &s }
