package db

import (
	"github.com/incenva/rebate-scraper/models"
	"gorm.io/gorm/clause"
)

// EnsureClient upserts a row into public.clients in the given database.
// ON CONFLICT (id) updates name, region, utility_type, and is_demo so the
// record stays in sync with tenants.json without overwriting anything else
// an admin may have set directly.
//
// Called by the promoter before writing rebates so the clients row always
// exists when promoted rebates reference it via client_id.
func EnsureClient(d *DB, c models.ClientRow) error {
	if !c.IsSet() {
		return nil
	}

	type row struct {
		ID          string  `gorm:"column:id;primaryKey"`
		Name        string  `gorm:"column:name"`
		Region      *string `gorm:"column:region"`
		UtilityType *string `gorm:"column:utility_type"`
		IsDemo      *bool   `gorm:"column:is_demo"`
	}

	r := row{ID: c.ID, Name: c.Name}
	if c.Region != "" {
		r.Region = &c.Region
	}
	if c.UtilityType != "" {
		r.UtilityType = &c.UtilityType
	}
	if c.IsDemo {
		r.IsDemo = &c.IsDemo
	}

	return d.gorm.Table("clients").
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "region", "utility_type", "is_demo"}),
		}).
		Create(&r).Error
}
