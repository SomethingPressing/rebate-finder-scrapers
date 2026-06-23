package db

import "github.com/incenva/rebate-scraper/models"

// PendingBySource returns the count of pending staging rows grouped by source.
// Used by the promoter to create one job record per source before promoting.
func PendingBySource(d *DB) (map[string]int, error) {
	schema := models.ScraperSchema
	stgTable := schema + ".rebates_staging"

	type row struct {
		Source string
		Count  int
	}
	var rows []row
	if err := d.gorm.
		Raw(`SELECT stg_source AS source, COUNT(*) AS count FROM `+stgTable+
			` WHERE stg_promotion_status = ? AND deleted_at IS NULL GROUP BY stg_source`,
			models.PromotionPending).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	result := make(map[string]int, len(rows))
	for _, r := range rows {
		result[r.Source] = r.Count
	}
	return result, nil
}
