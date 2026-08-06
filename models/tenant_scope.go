package models

import "time"

// TenantScope is one tenant's declared subscription: which sources it wants
// collected and which territories it covers.
//
// This table is a CACHE, never the master copy. Each tenant's own database
// (ScraperSourceConfig in the rebate-finder app) remains authoritative; the
// tenant's site pushes its full current scope to the queue endpoint
// (PUT /v1/subscription/scope) on every drain cycle, and the broker overwrites
// this row with whatever arrives. The table can be rebuilt from tenant
// declarations at any time.
//
// Read by the promoter (matching staged rows to tenants) and by the scope
// compiler (unioning all rows into per-source demand envelopes that carry no
// tenant identities). See rebate-finder
// docs/specifications/shared-scraper-hub.md §6.2.
type TenantScope struct {
	TenantID     string      `gorm:"column:tenant_id;primaryKey"`
	Sources      StringSlice `gorm:"column:sources;type:text[]"`
	States       StringSlice `gorm:"column:states;type:text[]"`
	Utilities    StringSlice `gorm:"column:utilities;type:text[]"`
	ServiceAreas StringSlice `gorm:"column:service_areas;type:text[]"`
	ZipCodes     StringSlice `gorm:"column:zip_codes;type:text[]"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (TenantScope) TableName() string { return ScraperSchema + ".tenant_scopes" }
