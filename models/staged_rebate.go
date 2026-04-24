package models

import (
	"time"

	"gorm.io/gorm"
)

// PromotionStatus values for StagedRebate.PromotionStatus.
const (
	PromotionPending   = "pending"   // not yet reviewed by the promoter
	PromotionPromoted  = "promoted"  // successfully upserted into rebates
	PromotionSkipped   = "skipped"   // duplicate / unchanged; deliberately not promoted
)

// StagedRebate is the GORM model for the rebates_staging table.
//
// Scrapers never write directly to the live `rebates` table.
// Instead they upsert into this staging table (keyed on source_id).
// The promoter command (cmd/promoter) then picks up pending rows,
// calls the Next.js API (or writes directly) to create/update rebates,
// and flips promotion_status to "promoted".
//
// This design means:
//   - A bad scrape run can be rolled back by deleting staging rows.
//   - Admin-promoted statuses in rebates are never blindly overwritten.
//   - You can inspect / filter / transform rows before they go live.
type StagedRebate struct {
	gorm.Model // adds: id (uint PK), created_at, updated_at, deleted_at

	// stg_source_id — deterministic UUID produced by models.DeterministicID.
	// Stable external key: re-scraping the same program always produces the
	// same value, so ON CONFLICT (stg_source_id) updates instead of inserting.
	SourceID string `gorm:"column:stg_source_id;uniqueIndex;not null"`

	// ── Core program fields ──────────────────────────────────────────────────

	ProgramName          string      `gorm:"column:program_name;not null"`
	UtilityCompany       string      `gorm:"column:utility_company;not null"`
	IncentiveDescription *string     `gorm:"column:incentive_description"`
	IncentiveAmount      *float64    `gorm:"column:incentive_amount"`
	MaximumAmount        *float64    `gorm:"column:maximum_amount"`
	PercentValue         *float64    `gorm:"column:percent_value"`
	PerUnitAmount        *float64    `gorm:"column:per_unit_amount"`
	IncentiveFormat      *string     `gorm:"column:incentive_format"`
	UnitType             *string     `gorm:"column:unit_type"`
	State                *string     `gorm:"column:state"`
	ZipCode              *string     `gorm:"column:zip_code"`
	// ZipCodes holds all ZIP codes this incentive covers (state-wide programs
	// get every ZIP in the state; utility programs get the utility's ZIP list).
	ZipCodes             StringSlice `gorm:"column:zip_codes;type:text[]"`
	ServiceTerritory     *string     `gorm:"column:service_territory"`
	AvailableNationwide  *bool       `gorm:"column:available_nationwide"`
	CategoryTag          StringSlice `gorm:"column:category_tag;type:text[]"`
	Segment              StringSlice `gorm:"column:segment;type:text[]"`
	Portfolio            StringSlice `gorm:"column:portfolio;type:text[]"`
	CustomerType         *string     `gorm:"column:customer_type"`
	ProductCategory      *string     `gorm:"column:product_category"`
	Administrator        *string     `gorm:"column:administrator"`
	Source               string      `gorm:"column:source;not null"`
	StartDate            *string     `gorm:"column:start_date"`
	EndDate              *string     `gorm:"column:end_date"`
	WhileFundsLast       *bool       `gorm:"column:while_funds_last"`
	ApplicationURL       *string     `gorm:"column:application_url"`
	ApplicationProcess   *string     `gorm:"column:application_process"`
	ProgramURL           *string     `gorm:"column:program_url"`
	ContactEmail         *string     `gorm:"column:contact_email"`
	ContactPhone         *string     `gorm:"column:contact_phone"`
	ImageURL             *string     `gorm:"column:image_url"`
	ImageURLs            StringSlice `gorm:"column:image_urls;type:text[]"`
	ContractorRequired   *bool          `gorm:"column:contractor_required"`
	EnergyAuditRequired  *bool          `gorm:"column:energy_audit_required"`
	RateTiers            RateTiersJSON  `gorm:"column:rate_tiers;type:jsonb"`
	ScraperVersion       string         `gorm:"column:scraper_version"`

	// ── Staging lifecycle fields ─────────────────────────────────────────────

	// ── Staging lifecycle columns (stg_ prefix) ─────────────────────────────
	// These columns are staging-specific metadata.  They never mirror a column
	// in the live rebates table — the stg_ prefix makes that immediately clear
	// when reading queries or inspecting the table in Prisma Studio / psql.

	// stg_promotion_status: "pending" on insert → "promoted" or "skipped" after the promoter runs.
	PromotionStatus string `gorm:"column:stg_promotion_status;default:'pending';index"`

	// stg_promoted_at: timestamp set (in the same transaction as the rebates upsert)
	// the moment this row is successfully promoted.  NULL while pending / skipped.
	PromotedAt *time.Time `gorm:"column:stg_promoted_at"`

	// stg_rebate_id: UUID of the live rebates row this staging row was promoted into.
	// NULL while pending or skipped.  Set atomically with the rebates upsert so
	// you can always join back:
	//
	//   -- staging → live rebate
	//   SELECT r.* FROM rebates_staging s
	//   JOIN   rebates r ON r.id = s.stg_rebate_id
	//   WHERE  s.stg_source_id = '<uuid>';
	//
	//   -- live rebate → all scrape versions
	//   SELECT s.* FROM rebates_staging s
	//   WHERE  s.stg_rebate_id = '<rebate-uuid>'
	//   ORDER  BY s.created_at DESC;
	//
	// Indexed for fast reverse lookups.
	RebateID *string `gorm:"column:stg_rebate_id;index"`

	// stg_program_hash: SHA-256 of normalize(program_name|utility_company|source).
	// Zipcode-agnostic dedup key.  Pre-computed by the scraper so the promoter
	// can use it directly (and fall back to computing on-the-fly for older rows).
	ProgramHash string `gorm:"column:stg_program_hash"`
}

// TableName tells GORM to use rebates_staging instead of the default "staged_rebates".
func (StagedRebate) TableName() string { return "rebates_staging" }

// FromIncentive converts an Incentive (scraper output) into a StagedRebate.
func FromIncentive(inc Incentive) StagedRebate {
	return StagedRebate{
		SourceID:             inc.ID,
		ProgramName:          inc.ProgramName,
		UtilityCompany:       inc.UtilityCompany,
		ProgramHash:          ComputeProgramHash(inc.ProgramName, inc.UtilityCompany),
		IncentiveDescription: inc.IncentiveDescription,
		IncentiveAmount:      inc.IncentiveAmount,
		MaximumAmount:        inc.MaximumAmount,
		PercentValue:         inc.PercentValue,
		PerUnitAmount:        inc.PerUnitAmount,
		IncentiveFormat:      inc.IncentiveFormat,
		UnitType:             inc.UnitType,
		State:                inc.State,
		ZipCode:              inc.ZipCode,
		ZipCodes:             StringSlice(inc.ZipCodes),
		ServiceTerritory:     inc.ServiceTerritory,
		AvailableNationwide:  inc.AvailableNationwide,
		CategoryTag:          StringSlice(inc.CategoryTag),
		Segment:              StringSlice(inc.Segment),
		Portfolio:            StringSlice(inc.Portfolio),
		CustomerType:         inc.CustomerType,
		ProductCategory:      inc.ProductCategory,
		Administrator:        inc.Administrator,
		Source:               inc.Source,
		StartDate:            inc.StartDate,
		EndDate:              inc.EndDate,
		WhileFundsLast:       inc.WhileFundsLast,
		ApplicationURL:       inc.ApplicationURL,
		ApplicationProcess:   inc.ApplicationProcess,
		ProgramURL:           inc.ProgramURL,
		ContactEmail:         inc.ContactEmail,
		ContactPhone:         inc.ContactPhone,
		ImageURL:             inc.ImageURL,
		ImageURLs:            StringSlice(inc.ImageURLs),
		ContractorRequired:   inc.ContractorRequired,
		EnergyAuditRequired:  inc.EnergyAuditRequired,
		RateTiers:            RateTiersJSON(inc.RateTiers),
		ScraperVersion:       inc.ScraperVersion,
		PromotionStatus:      PromotionPending,
	}
}
