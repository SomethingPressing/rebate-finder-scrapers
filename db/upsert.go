package db

import (
	"fmt"

	"github.com/incenva/rebate-scraper/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpsertResult summarises a batch staging upsert.
type UpsertResult struct {
	Upserted int // rows inserted or updated in rebates_staging
}

// upsertBatchSize is the maximum number of rows per INSERT statement.
// PostgreSQL limits prepared-statement parameters to 65 535; with ~40 columns
// per StagedRebate row that gives a safe ceiling of 65535/40 ≈ 1638.
// We use 500 to stay well within the limit with room for future column growth.
const upsertBatchSize = 500

// UpsertToStaging writes all items into the rebates_staging table.
//
// Normal scrape (forceRefresh=false):
//   On conflict the lifecycle columns (stg_promotion_status, stg_promoted_at,
//   stg_rebate_id) are excluded from the update so a re-scrape never resets
//   promotion state set by the promoter or an admin.
//
// Force-refresh (forceRefresh=true):
//   stg_promotion_status and stg_promoted_at are included in the conflict
//   update and reset to 'pending' / NULL atomically inside the upsert — no
//   separate SQL call needed, no race condition possible.
//
// When forceURLUpdate is true, an additional UPDATE overwrites program_url and
// application_url regardless of promotion status (lifecycle cols untouched).
//
// Large batches are split to stay within PostgreSQL's 65 535 parameter limit.
func UpsertToStaging(d *DB, items []models.Incentive, forceURLUpdate bool, forceRefresh ...bool) (UpsertResult, error) {
	if len(items) == 0 {
		return UpsertResult{}, nil
	}
	doRefresh := len(forceRefresh) > 0 && forceRefresh[0]

	rows := make([]models.StagedRebate, len(items))
	for i, inc := range items {
		rows[i] = models.FromIncentive(inc)
		if doRefresh {
			// Ensure EXCLUDED.stg_promotion_status = 'pending' and
			// EXCLUDED.stg_promoted_at = NULL so the conflict update resets them.
			rows[i].PromotionStatus = models.PromotionPending
			rows[i].PromotedAt = nil
		}
	}

	// Columns to update on conflict — all data columns only.
	// stg_rebate_id is always preserved; stg_promotion_status and
	// stg_promoted_at are added only on force-refresh.
	updateCols := []string{
		"program_name", "utility_company", "incentive_description",
		"incentive_amount", "maximum_amount", "percent_value", "per_unit_amount",
		"incentive_format", "unit_type", "state", "zip_code", "zip_codes", "service_territory",
		"available_nationwide", "category_tag", "segment", "portfolio",
		"customer_type", "product_category", "administrator", "source",
		"start_date", "end_date", "while_funds_last",
		"application_url", "application_process", "program_url",
		"contact_email", "contact_phone",
		"image_url", "image_urls",
		"contractor_required", "energy_audit_required", "source_url", "implementing_sector",
		"rate_tiers", "scraper_version", "stg_program_hash",
		"stg_raw_response", "stg_raw_content_type", "stg_tenant_ids", "updated_at",
	}
	if doRefresh {
		// Include lifecycle columns so the upsert atomically resets status.
		updateCols = append(updateCols, "stg_promotion_status", "stg_promoted_at")
	}

	total := 0
	for start := 0; start < len(rows); start += upsertBatchSize {
		end := start + upsertBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]

		result := d.gorm.
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "stg_source_id"}},
				DoUpdates: clause.AssignmentColumns(updateCols),
			}).
			Create(&batch)

		if result.Error != nil {
			return UpsertResult{Upserted: total}, fmt.Errorf("upsert staging (batch %d-%d): %w", start, end, result.Error)
		}
		total += int(result.RowsAffected)

		// When --force-url-update is active, overwrite program_url and
		// application_url for every row in this batch regardless of its
		// stg_promotion_status (including already-promoted / skipped rows).
		// We issue one UPDATE per batch keyed on stg_source_id so the query
		// stays parameterised and safe. Lifecycle columns are never touched.
		if forceURLUpdate {
			schema := models.ScraperSchema
			stgTable := schema + ".rebates_staging"
			for _, row := range batch {
				if err := d.gorm.
					Table(stgTable).
					Where("stg_source_id = ?", row.SourceID).
					Updates(map[string]interface{}{
						"program_url":     row.ProgramURL,
						"application_url": row.ApplicationURL,
					}).Error; err != nil {
					return UpsertResult{Upserted: total}, fmt.Errorf("force url update (source %s): %w", row.SourceID, err)
				}
			}
		}
	}

	// ── Upsert rebate_tenant_status rows for multi-tenant tagging ────────────
	// Collect all source IDs that have at least one tenant tag.
	taggedSourceIDs := make([]string, 0, len(items))
	for _, inc := range items {
		if len(inc.TenantIDs) > 0 {
			taggedSourceIDs = append(taggedSourceIDs, inc.ID)
		}
	}

	if len(taggedSourceIDs) > 0 {
		schema := models.ScraperSchema
		stgTable := schema + ".rebates_staging"

		// Look up staging row IDs by source ID so we can reference them.
		var stagingRows []struct {
			ID       uint   `gorm:"column:id"`
			SourceID string `gorm:"column:stg_source_id"`
		}
		if err := d.gorm.
			Table(stgTable).
			Select("id, stg_source_id").
			Where("stg_source_id IN ?", taggedSourceIDs).
			Find(&stagingRows).Error; err != nil {
			return UpsertResult{Upserted: total}, fmt.Errorf("upsert tenant status: lookup staging ids: %w", err)
		}

		sourceToStagingID := make(map[string]uint, len(stagingRows))
		for _, r := range stagingRows {
			sourceToStagingID[r.SourceID] = r.ID
		}

		// Build incentive TenantIDs lookup.
		incTenants := make(map[string][]string, len(items))
		for _, inc := range items {
			if len(inc.TenantIDs) > 0 {
				incTenants[inc.ID] = inc.TenantIDs
			}
		}

		statusRows := make([]models.RebateTenantStatus, 0, len(taggedSourceIDs))
		for sourceID, tenantIDs := range incTenants {
			stagingID, ok := sourceToStagingID[sourceID]
			if !ok {
				continue
			}
			for _, tenantID := range tenantIDs {
				statusRows = append(statusRows, models.RebateTenantStatus{
					StagingID:       stagingID,
					TenantID:        tenantID,
					PromotionStatus: models.PromotionPending,
				})
			}
		}

		if len(statusRows) > 0 {
			// ON CONFLICT DO NOTHING: never reset a row that's already promoted.
			if err := d.gorm.
				Clauses(clause.OnConflict{DoNothing: true}).
				CreateInBatches(statusRows, 500).Error; err != nil {
				return UpsertResult{Upserted: total}, fmt.Errorf("upsert tenant status: insert: %w", err)
			}
		}
	}

	return UpsertResult{Upserted: total}, nil
}

// ResetToPending resets staging rows back to "pending".
//
// When sourceIDs is non-nil, only those specific rows are reset (keyed by
// stg_source_id). This is used after a successful scrape to mark fresh rows
// for re-promotion.
//
// When sourceIDs is nil and source is non-empty, ALL promoted rows for that
// source are reset. This handles zero-output scrapes (scraper ran but found
// no programs) so existing data stays available for promotion.
func ResetToPending(d *DB, sourceIDs []string, source ...string) error {
	schema := models.ScraperSchema
	stgTable := schema + ".rebates_staging"

	if sourceIDs != nil {
		if len(sourceIDs) == 0 {
			return nil
		}
		return d.gorm.Exec(
			"UPDATE "+stgTable+
				" SET stg_promotion_status = 'pending', stg_promoted_at = NULL"+
				" WHERE stg_source_id IN ?",
			sourceIDs,
		).Error
	}

	// sourceIDs == nil: reset all promoted rows for the given source.
	if len(source) == 0 || source[0] == "" {
		return fmt.Errorf("ResetToPending: source required when sourceIDs is nil")
	}
	return d.gorm.Exec(
		"UPDATE "+stgTable+
			" SET stg_promotion_status = 'pending', stg_promoted_at = NULL"+
			" WHERE source = ? AND stg_promotion_status = 'promoted'",
		source[0],
	).Error
}

// MarkStale marks promoted staging rows for a given source as "stale" when
// their stg_source_id was NOT seen in the latest full scrape of that source.
// Only rows with stg_promotion_status = 'promoted' are affected — pending and
// skipped rows are left untouched.
//
// Call this after a force-refresh (with no fetch limit) has completed so that
// programs removed from the upstream source are flagged rather than silently
// kept as if they still exist.
//
// Returns the number of rows marked stale.
func MarkStale(d *DB, source string, seenSourceIDs []string) (int64, error) {
	if source == "" {
		return 0, fmt.Errorf("MarkStale: source must not be empty")
	}
	schema := models.ScraperSchema
	stgTable := schema + ".rebates_staging"

	// When the source has no promoted rows at all, skip the query.
	if len(seenSourceIDs) == 0 {
		// No IDs seen → mark ALL promoted rows for this source as stale.
		result := d.gorm.Exec(
			"UPDATE "+stgTable+
				" SET stg_promotion_status = 'stale'"+
				" WHERE source = ? AND stg_promotion_status = 'promoted'",
			source,
		)
		return result.RowsAffected, result.Error
	}

	result := d.gorm.Exec(
		"UPDATE "+stgTable+
			" SET stg_promotion_status = 'stale'"+
			" WHERE source = ?"+
			" AND stg_promotion_status = 'promoted'"+
			" AND stg_source_id NOT IN ?",
		source, seenSourceIDs,
	)
	return result.RowsAffected, result.Error
}

// PendingCount returns the number of rows in rebates_staging that have not yet
// been promoted.  Useful for health-check logging.
func PendingCount(d *DB) (int64, error) {
	var count int64
	err := d.gorm.Model(&models.StagedRebate{}).
		Where("stg_promotion_status = ?", models.PromotionPending).
		Count(&count).Error
	return count, err
}

// MarkPromoted marks a single staging row as promoted, records the timestamp,
// and stores the UUID of the live rebates row it was promoted into.
//
// rebateID is the UUID that was used as rebates.id (= sourceID for scraper rows).
// Pass it explicitly so callers that use a different ID scheme are also covered.
func MarkPromoted(d *DB, sourceID, rebateID string) error {
	return d.gorm.Model(&models.StagedRebate{}).
		Where("stg_source_id = ?", sourceID).
		Updates(map[string]interface{}{
			"stg_promotion_status": models.PromotionPromoted,
			"stg_promoted_at":      gorm.Expr("now()"),
			"stg_rebate_id":        rebateID,
		}).Error
}

// MarkSkipped marks a staging row as skipped (e.g. already up-to-date).
func MarkSkipped(d *DB, sourceID string) error {
	return d.gorm.Model(&models.StagedRebate{}).
		Where("stg_source_id = ?", sourceID).
		Update("stg_promotion_status", models.PromotionSkipped).Error
}
