package models

import "time"

// LiveRebate is the GORM model for public.rebates — the live incentive catalog.
//
// Only columns the scraper/promoter writes are included here.  Admin-managed
// columns (faq_data, seo_meta, iq_context, image_focal_points, etc.) and
// computed columns (fts / tsvector) are intentionally omitted: GORM only
// touches columns that appear in the struct, so omitted columns are never
// overwritten.
//
// Schema ownership: public (Prisma's schema).  The promoter is the only Go
// binary that writes here; the scraper never writes directly to this table.
type LiveRebate struct {
	// ── Identity ─────────────────────────────────────────────────────────────
	ID          string  `gorm:"column:id;primaryKey"`
	ProgramHash *string `gorm:"column:program_hash;uniqueIndex"`

	// ── Status (INSERT default only — never updated on conflict) ─────────────
	// Status is set to "draft" on first insert and must never be overwritten so
	// that admin-approved / published records keep their status across re-scrapes.
	Status     *string `gorm:"column:status"`
	IsFeatured *bool   `gorm:"column:is_featured"`
	Processed  *bool   `gorm:"column:processed"`

	// ── Core program data ─────────────────────────────────────────────────────
	ProgramName          string  `gorm:"column:program_name;not null"`
	UtilityCompany       string  `gorm:"column:utility_company;not null"`
	IncentiveDescription *string `gorm:"column:incentive_description"`

	// ── Amounts ───────────────────────────────────────────────────────────────
	IncentiveAmount *float64 `gorm:"column:incentive_amount"`
	MaximumAmount   *float64 `gorm:"column:maximum_amount"`
	PercentValue    *float64 `gorm:"column:percent_value"`
	PerUnitAmount   *float64 `gorm:"column:per_unit_amount"`

	// ── Classification (enum columns — stored as *string; PostgreSQL casts) ──
	IncentiveFormat *string `gorm:"column:incentive_format"`
	UnitType        *string `gorm:"column:unit_type"`

	// ── Geography ─────────────────────────────────────────────────────────────
	State               *string `gorm:"column:state"`
	ServiceTerritory    *string `gorm:"column:service_territory"`
	AvailableNationwide *bool   `gorm:"column:available_nationwide"`

	// ── Array fields ──────────────────────────────────────────────────────────
	CategoryTag StringSlice `gorm:"column:category_tag;type:text[]"`
	Segment     StringSlice `gorm:"column:segment;type:text[]"`
	Portfolio   StringSlice `gorm:"column:portfolio;type:text[]"`
	ImageURLs   StringSlice `gorm:"column:image_urls;type:text[]"`
	Sources     StringSlice `gorm:"column:sources;type:text[]"`

	// ── Audience / metadata ───────────────────────────────────────────────────
	CustomerType    *string `gorm:"column:customer_type"`
	ProductCategory *string `gorm:"column:product_category"`
	Administrator   *string `gorm:"column:administrator"`

	// ── Source tracking ───────────────────────────────────────────────────────
	Source         *string `gorm:"column:source"`
	ScraperVersion *string `gorm:"column:scraper_version"`

	// ── Dates ─────────────────────────────────────────────────────────────────
	StartDate      *string `gorm:"column:start_date"`
	EndDate        *string `gorm:"column:end_date"`
	WhileFundsLast *bool   `gorm:"column:while_funds_last"`

	// ── URLs ──────────────────────────────────────────────────────────────────
	ApplicationURL     *string `gorm:"column:application_url"`
	ApplicationProcess *string `gorm:"column:application_process"`
	ProgramURL         *string `gorm:"column:program_url"`
	ContactEmail       *string `gorm:"column:contact_email"`
	ContactPhone       *string `gorm:"column:contact_phone"`
	ImageURL           *string `gorm:"column:image_url"`

	// ── Requirements ─────────────────────────────────────────────────────────
	ContractorRequired  *bool        `gorm:"column:contractor_required"`
	EnergyAuditRequired *bool        `gorm:"column:energy_audit_required"`
	RateTiers           RateTiersJSON `gorm:"column:rate_tiers;type:jsonb"`

	// ── Audit ─────────────────────────────────────────────────────────────────
	CreatedAt *time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt *time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName tells GORM to use the public.rebates table (no schema prefix —
// the DB connection uses public as the default search_path).
func (LiveRebate) TableName() string { return "rebates" }

// LiveRebateUpdateCols returns the columns that ARE updated on program_hash
// conflict.  Excluded: status, is_featured, processed, created_at,
// created_by, updated_by — these are admin-managed and must never be reset
// by a re-scrape.  updated_at is included so the timestamp refreshes.
func LiveRebateUpdateCols() []string { return liveRebateUpdateCols }

var liveRebateUpdateCols = []string{
	"program_name", "utility_company", "incentive_description",
	"incentive_amount", "maximum_amount", "percent_value", "per_unit_amount",
	"incentive_format", "unit_type",
	"state", "service_territory", "available_nationwide",
	"category_tag", "segment", "portfolio",
	"customer_type", "product_category", "administrator",
	"source", "sources",
	"start_date", "end_date", "while_funds_last",
	"application_url", "application_process", "program_url",
	"contact_email", "contact_phone",
	"image_url", "image_urls",
	"contractor_required", "energy_audit_required",
	"rate_tiers", "scraper_version",
	"updated_at",
}
