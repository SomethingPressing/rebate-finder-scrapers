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
// On conflict (same source_id) every column except promotion_status and
// promoted_at is overwritten — so a re-scrape refreshes the data without
// resetting any manually edited promotion state, and does not create a second
// row for the same source_id (requires unique index on source_id).
//
// Items that are already "promoted" stay promoted; only their data columns
// are refreshed so the promoter can optionally re-promote changed records.
//
// When forceURLUpdate is true, an additional UPDATE is issued after each batch
// to overwrite program_url and application_url on ALL rows whose stg_source_id
// matches the batch, regardless of stg_promotion_status.  The lifecycle columns
// (stg_promotion_status, stg_promoted_at, stg_rebate_id) are never touched.
//
// Large batches are automatically split to stay within PostgreSQL's 65 535
// parameter limit.
func UpsertToStaging(d *DB, items []models.Incentive, forceURLUpdate bool) (UpsertResult, error) {
	if len(items) == 0 {
		return UpsertResult{}, nil
	}

	rows := make([]models.StagedRebate, len(items))
	for i, inc := range items {
		rows[i] = models.FromIncentive(inc)
	}

	// Columns to update on conflict — all data columns only.
	// The stg_ lifecycle columns (stg_promotion_status, stg_promoted_at,
	// stg_rebate_id) are intentionally excluded: a re-scrape must never reset
	// promotion state that was already set by the promoter or an admin.
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
		"contractor_required", "energy_audit_required",
		"rate_tiers", "scraper_version", "stg_program_hash",
		"stg_raw_response", "stg_raw_content_type", "stg_tenant_ids", "updated_at",
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
