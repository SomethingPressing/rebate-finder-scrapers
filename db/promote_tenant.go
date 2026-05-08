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
	var pending []models.StagedRebate
	if err := stagingDB.gorm.
		Table(stgTable+" rs").
		Joins("JOIN "+statusTable+" rts ON rts.staging_id = rs.id").
		Where("rts.tenant_id = ? AND rts.promotion_status = ? AND rs.deleted_at IS NULL",
			tenantID, models.PromotionPending).
		Order("rs.id ASC").
		Select("rs.*").
		Find(&pending).Error; err != nil {
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
	if err := tenantDB.gorm.
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "program_hash"}},
			DoUpdates: clause.AssignmentColumns(models.LiveRebateUpdateCols()),
		}).
		CreateInBatches(rebates, 500).Error; err != nil {
		return result, fmt.Errorf("promote_tenant %s: upsert rebates: %w", tenantID, err)
	}

	// ── Phase 5: fetch actual rebate IDs from tenant DB ───────────────────────
	hashes := make([]string, len(hashOrder))
	copy(hashes, hashOrder)

	var idRows []struct {
		ID          string `gorm:"column:id"`
		ProgramHash string `gorm:"column:program_hash"`
	}
	if err := tenantDB.gorm.
		Table("rebates").
		Select("id, program_hash").
		Where("program_hash IN ?", hashes).
		Find(&idRows).Error; err != nil {
		return result, fmt.Errorf("promote_tenant %s: fetch rebate ids: %w", tenantID, err)
	}

	hashToID := make(map[string]string, len(idRows))
	for _, r := range idRows {
		hashToID[r.ProgramHash] = r.ID
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
	}

	return result, nil
}
