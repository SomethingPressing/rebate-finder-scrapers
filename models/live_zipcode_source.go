package models

import "time"

// LiveRebateZipcodeSource tracks every scraper source that covers a
// (rebate, zipcode) pair.  While rebate_zipcodes holds one row per
// (rebate, zip), this table holds one row per (rebate, zip, source),
// so you can see all scrapers that contributed a given zip to a program.
//
// Prisma column names (no @map() on the model) — must match exactly.
type LiveRebateZipcodeSource struct {
	RebateID        string    `gorm:"column:rebateId;primaryKey"`
	ZipcodeCode     string    `gorm:"column:zipcodeCode;primaryKey"`
	Source          string    `gorm:"column:source;primaryKey"`
	StagingSourceID *string   `gorm:"column:stagingSourceId"`
	CreatedAt       time.Time `gorm:"column:createdAt;autoCreateTime"`
}

func (LiveRebateZipcodeSource) TableName() string { return "rebate_zipcode_sources" }
