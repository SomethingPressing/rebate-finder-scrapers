package db

// promote_tenant.go — promotes pending staging rows for a specific tenant
// from the shared staging DB into that tenant's dedicated database.
//
// Unlike Promote() (which assumes staging and live tables share one DB),
// PromoteTenant uses two DB handles:
//   - stagingDB: the scraper's own DB — reads scraper.rebates_staging and
//                updates scraper.rebate_tenant_status
//   - tenantDB:  the tenant's dedicated DB — writes public.rebates,
//                public.zipcodes, public.rebate_zipcodes, etc.

import (
	"fmt"
	"sort"
	"time"

	"github.com/incenva/rebate-scraper/models"
	"gorm.io/gorm/clause"
)

// PromoteTenant promotes incentives tagged for tenantID from stagingDB into tenantDB.
// It is safe to call for multiple tenants sequentially — one failure does not
// affect subsequent tenants.
func PromoteTenant(stagingDB *DB, tenantDB *DB, tenantID string, opts PromoteOptions) (*PromoteResult, error) {
	priority := opts.SourcePriority
	if len(priority) == 0 {
		priority = DefaultSourcePriority
	}

	schema := models.ScraperSchema
	stgTable := schema + ".rebates_staging"
	statusTable := schema + ".rebate_tenant_status"

	// ── Phase 1: fetch pending staging rows for this tenant ───────────────────
	// Use Raw() instead of Table(...).Joins(...) to avoid GORM injecting its own
	// `"rebates_staging"."deleted_at" IS NULL` clause that conflicts with the "rs" alias.
	var pending []models.StagedRebate
	if err := stagingDB.gorm.Raw(`
		SELECT rs.*
		FROM `+stgTable+` rs
		JOIN `+statusTable+` rts ON rts.staging_id = rs.id
		WHERE rts.tenant_id = ?
		  AND rts.promotion_status = ?
		  AND rs.deleted_at IS NULL
		ORDER BY rs.id ASC`,
		tenantID, models.PromotionPending).
		Scan(&pending).Error; err != nil {
		return nil, fmt.Errorf("promote_tenant %s: fetch pending: %w", tenantID, err)
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

	// ── Dry-run: preview and return ───────────────────────────────────────────
	if opts.DryRun {
		for _, h := range hashOrder {
			g := groupMap[h]
			if len(g.rows) == 1 {
				r := g.rows[0]
				fmt.Printf("  · [tenant=%s][%s] %q\n", tenantID, r.Source, r.ProgramName)
			} else {
				prefix := h
				if len(prefix) > 12 {
					prefix = prefix[:12]
				}
				fmt.Printf("  · MERGE [tenant=%s] (%d sources) hash=%s…\n", tenantID, len(g.rows), prefix)
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

	// Look up which program_hashes already have a rebate row in the tenant DB
	// so we can reuse the existing ID.  Without this, a re-promoted program
	// whose primary source changed (and therefore has a different SourceID)
	// would try to INSERT a new row that conflicts with the existing unique
	// index on program_hash.
	type hashIDRow struct {
		ID          string `gorm:"column:id"`
		ProgramHash string `gorm:"column:program_hash"`
	}
	var existingHashRows []hashIDRow
	if err := tenantDB.gorm.
		Table("rebates").
		Select("id, program_hash").
		Where("program_hash IN ?", hashOrder).
		Find(&existingHashRows).Error; err != nil {
		return nil, fmt.Errorf("promote_tenant %s: lookup existing hashes: %w", tenantID, err)
	}
	existingIDByHash := make(map[string]string, len(existingHashRows))
	for _, r := range existingHashRows {
		existingIDByHash[r.ProgramHash] = r.ID
	}

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

		// Prefer the ID the tenant DB already has for this hash so the
		// ON CONFLICT (id) clause updates rather than inserts.
		id := primary.SourceID
		if existingID, ok := existingIDByHash[h]; ok {
			id = existingID
		}

		lr := models.LiveRebate{
			ID:          id,
			ProgramHash: &hash,

			Status:     &draft,
			IsFeatured: &isFeatured,
			Processed:  &processed,
			CreatedAt:  &now,

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

	// ── Phase 4: batch-upsert into tenant's public.rebates ────────────────────
	// Conflict target is "id" (primary key), not "program_hash".
	// Using program_hash as the conflict target causes a PK violation when a
	// staging row's SourceID already exists in rebates with a different hash
	// (e.g. scraper re-fetched the same program and the name/utility changed
	// slightly, producing a new hash). Targeting "id" ensures that any
	// re-promoted row updates the existing rebate row regardless of hash drift.
	// Cross-source deduplication is already handled upstream in Phase 2 by
	// grouping pending rows by program_hash before building the rebates slice.
	if err := tenantDB.gorm.
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns(models.LiveRebateUpdateCols()),
		}).
		CreateInBatches(rebates, 500).Error; err != nil {
		return result, fmt.Errorf("promote_tenant %s: upsert rebates: %w", tenantID, err)
	}

	// ── Phase 5: build hash→ID map from Phase 3 data ────────────────────────
	// We already know exactly which ID each hash resolved to in Phase 3
	// (either an existing rebate ID or primary.SourceID for new programs).
	// A hash-based DB lookup would miss rows where the hash drifted between
	// scrape runs (program_hash is intentionally excluded from the update
	// columns so admin edits are never overwritten).
	hashToID := make(map[string]string, len(rebates))
	for _, lr := range rebates {
		if lr.ProgramHash != nil {
			hashToID[*lr.ProgramHash] = lr.ID
		}
	}

	// ── Phase 6: bulk-upsert zipcodes and rebate_zipcodes in tenant DB ────────
	type zipEntry struct {
		rebateID string
		zip      string
		sources  []struct{ name, stagingID string }
	}
	type zipKey struct{ rebateID, zip string }
	zipMap := make(map[zipKey]*zipEntry)
	zipOrder := make([]zipKey, 0)

	for _, meta := range metas {
		rebateID, ok := hashToID[meta.hash]
		if !ok {
			continue
		}
		for _, row := range meta.rows {
			rowZips := make(map[string]struct{})
			if row.ZipCode != nil && *row.ZipCode != "" {
				rowZips[*row.ZipCode] = struct{}{}
			}
			for _, z := range row.ZipCodes {
				rowZips[z] = struct{}{}
			}
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
		uniqueZips := make(map[string]struct{}, len(zipOrder))
		for _, k := range zipOrder {
			uniqueZips[k.zip] = struct{}{}
		}
		zipcodeRows := make([]models.LiveZipcode, 0, len(uniqueZips))
		for z := range uniqueZips {
			zipcodeRows = append(zipcodeRows, models.LiveZipcode{Code: z})
		}
		if err := tenantDB.gorm.
			Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(zipcodeRows, 5000).Error; err != nil {
			return result, fmt.Errorf("promote_tenant %s: insert zipcodes: %w", tenantID, err)
		}
		result.ZipsWritten = len(uniqueZips)

		linkRows := make([]models.LiveRebateZipcode, 0, len(zipOrder))
		for _, k := range zipOrder {
			e := zipMap[k]
			link := models.LiveRebateZipcode{RebateID: e.rebateID, ZipcodeCode: e.zip}
			if len(e.sources) > 0 && e.sources[0].stagingID != "" {
				link.StagingSourceID = ptrStr(e.sources[0].stagingID)
			}
			linkRows = append(linkRows, link)
		}
		if err := tenantDB.gorm.
			Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(linkRows, 5000).Error; err != nil {
			return result, fmt.Errorf("promote_tenant %s: insert rebate_zipcodes: %w", tenantID, err)
		}
		result.LinksWritten = len(linkRows)

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
		if err := tenantDB.gorm.
			Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(sourceRows, 5000).Error; err != nil {
			return result, fmt.Errorf("promote_tenant %s: insert rebate_zipcode_sources: %w", tenantID, err)
		}
	}

	// ── Phase 7: update rebate_tenant_status in staging DB ───────────────────
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
			ids[i] = r.ID
		}
		stagingUpdates = append(stagingUpdates, stagingUpdate{rebateID: rebateID, stagingIDs: ids})
		result.Promoted += len(meta.rows)
	}

	// Collect all staging row IDs that were successfully promoted (across all groups).
	allPromotedStagingIDs := make([]uint, 0, result.Promoted)

	for _, u := range stagingUpdates {
		if err := stagingDB.gorm.
			Table(statusTable).
			Where("staging_id IN ? AND tenant_id = ? AND promotion_status = ?",
				u.stagingIDs, tenantID, models.PromotionPending).
			Updates(map[string]interface{}{
				"promotion_status": models.PromotionPromoted,
				"promoted_at":      now,
				"rebate_id":        u.rebateID,
			}).Error; err != nil {
			return result, fmt.Errorf("promote_tenant %s: update tenant status: %w", tenantID, err)
		}
		allPromotedStagingIDs = append(allPromotedStagingIDs, u.stagingIDs...)
	}

	// Also mark the staging rows themselves as promoted so the staging stats
	// reflect the true state.  Without this, rebates_staging.stg_promotion_status
	// stays 'pending' even after the tenant status is marked 'promoted'.
	if len(allPromotedStagingIDs) > 0 {
		if err := stagingDB.gorm.
			Table(stgTable).
			Where("id IN ?", allPromotedStagingIDs).
			Updates(map[string]interface{}{
				"stg_promotion_status": models.PromotionPromoted,
				"stg_promoted_at":      now,
			}).Error; err != nil {
			return result, fmt.Errorf("promote_tenant %s: update staging status: %w", tenantID, err)
		}
	}

	return result, nil
}
