package db

import "github.com/incenva/rebate-scraper/models"

// StagingRecord holds the fields needed to rehydrate a staging row from
// its original source without re-discovering programs from scratch.
type StagingRecord struct {
	SourceID   string
	State      *string
	ProgramURL *string
	SourceURL  *string
}

// FetchStagingRecords returns all non-deleted staging rows for the given
// source, with only the fields needed for rehydration.
func FetchStagingRecords(d *DB, source string) ([]StagingRecord, error) {
	schema := models.ScraperSchema
	type row struct {
		SourceID   string  `gorm:"column:source_id"`
		State      *string `gorm:"column:state"`
		ProgramURL *string `gorm:"column:program_url"`
		SourceURL  *string `gorm:"column:source_url"`
	}
	var rows []row
	if err := d.gorm.
		Table(schema+".rebates_staging").
		Select("source_id, state, program_url, source_url").
		Where("source = ? AND deleted_at IS NULL", source).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]StagingRecord, len(rows))
	for i, r := range rows {
		out[i] = StagingRecord{
			SourceID:   r.SourceID,
			State:      r.State,
			ProgramURL: r.ProgramURL,
			SourceURL:  r.SourceURL,
		}
	}
	return out, nil
}
