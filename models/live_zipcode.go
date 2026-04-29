package models

import "time"

// LiveZipcode is the GORM model for public.zipcodes.
type LiveZipcode struct {
	Code      string    `gorm:"column:code;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (LiveZipcode) TableName() string { return "zipcodes" }

// LiveRebateZipcode is the GORM model for public.rebate_zipcodes.
//
// Important: Prisma created this table without @map() directives on the
// relation fields, so the actual PostgreSQL column names are camelCase
// (rebateId, zipcodeCode, stagingSourceId, createdAt).
// GORM column tags must match exactly.
type LiveRebateZipcode struct {
	RebateID        string    `gorm:"column:rebateId;primaryKey"`
	ZipcodeCode     string    `gorm:"column:zipcodeCode;primaryKey"`
	StagingSourceID *string   `gorm:"column:stagingSourceId"`
	CreatedAt       time.Time `gorm:"column:createdAt;autoCreateTime"`
}

func (LiveRebateZipcode) TableName() string { return "rebate_zipcodes" }
