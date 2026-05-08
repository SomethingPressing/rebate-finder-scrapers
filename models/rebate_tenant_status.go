package models

import "time"

// RebateTenantStatus tracks per-tenant promotion state for each staging row.
// One row exists per (staging row, tenant) pair, written by the scraper when
// it tags an incentive for a tenant. The promoter reads and updates this table.
//
// This design allows the same incentive to be independently promoted (or not)
// to each tenant without duplicating staging data.
type RebateTenantStatus struct {
	ID              uint       `gorm:"primarykey;autoIncrement"`
	StagingID       uint       `gorm:"column:staging_id;not null;uniqueIndex:idx_staging_tenant"`
	TenantID        string     `gorm:"column:tenant_id;not null;uniqueIndex:idx_staging_tenant"`
	PromotionStatus string     `gorm:"column:promotion_status;default:'pending';index"`
	PromotedAt      *time.Time `gorm:"column:promoted_at"`
	RebateID        *string    `gorm:"column:rebate_id"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (RebateTenantStatus) TableName() string { return ScraperSchema + ".rebate_tenant_status" }
