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

// UpsertToStaging writes all items into the rebates_staging table.
//
// On conflict (same source_id) every column except promotion_status and
// promoted_at is overwritten — so a re-scrape refreshes the data without
// resetting any manually edited promotion state, and does not create a second
// row for the same source_id (requires unique index on source_id).
//
// Items that are already "promoted" stay promoted; only their data columns
// are refreshed so the promoter can optionally re-promote changed records.
func UpsertToStaging(d *DB, items []models.Incentive) (UpsertResult, error) {
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
		"incentive_format", "unit_type", "state", "zip_code", "service_territory",
		"available_nationwide", "category_tag", "segment", "portfolio",
		"customer_type", "product_category", "administrator", "source",
		"start_date", "end_date", "while_funds_last",
		"application_url", "application_process", "program_url",
		"contact_email", "contact_phone",
		"image_url", "image_urls",
		"contractor_required", "energy_audit_required",
		"rate_tiers", "scraper_version", "stg_program_hash", "updated_at",
	}

	result := d.gorm.
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "stg_source_id"}},
			DoUpdates: clause.AssignmentColumns(updateCols),
		}).
		Create(&rows)

	if result.Error != nil {
		return UpsertResult{}, fmt.Errorf("upsert staging: %w", result.Error)
	}

	return UpsertResult{Upserted: int(result.RowsAffected)}, nil
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
