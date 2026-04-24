package models

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Incentive is the in-memory row shape all scrapers return.
// cmd/scraper persists it only via models.FromIncentive → rebates_staging
// (never directly to rebates). cmd/promoter copies pending staging rows into
// rebates. Column layout matches rebates / rebates_staging for that handoff.
//
// Column correspondence:
//
//	ID                  → id                   (uuid PK)
//	ProgramName         → program_name          (text NOT NULL)
//	UtilityCompany      → utility_company       (text NOT NULL)
//	Status              → status                (rebate_status enum — always "draft" on scrape)
//	IncentiveDescription→ incentive_description (text)
//	IncentiveAmount     → incentive_amount      (float8)
//	MaximumAmount       → maximum_amount        (float8)
//	PercentValue        → percent_value         (float8)
//	PerUnitAmount       → per_unit_amount       (float8)
//	IncentiveFormat     → incentive_format      (incentive_format_type enum)
//	UnitType            → unit_type             (incentive_unit_type enum)
//	State               → state                 (text)
//	ProgramHash         → stg_program_hash      (text — SHA-256 of normalize(name|utility|source))
//	ZipCode             → zip_code              (text — staging only; becomes RebateZipcode on promotion)
//	ServiceTerritory    → service_territory     (text)
//	AvailableNationwide → available_nationwide  (bool)
//	CategoryTag         → category_tag          (text[])
//	Segment             → segment               (text[])
//	Portfolio           → portfolio             (text[])
//	CustomerType        → customer_type         (text)
//	ProductCategory     → product_category      (text)
//	Administrator       → administrator         (text)
//	Source              → source                (text)
//	StartDate           → start_date            (text)
//	EndDate             → end_date              (text)
//	WhileFundsLast      → while_funds_last      (bool)
//	ApplicationURL      → application_url       (text)
//	ApplicationProcess  → application_process   (text)
//	ProgramURL          → program_url           (text)
//	ContactEmail        → contact_email         (text)
//	ContactPhone        → contact_phone         (text)
//	IsFeatured          → is_featured           (bool — always false on scrape)
//	ImageURL            → image_url             (text)
//	ImageURLs           → image_urls            (text[])
//	ContractorRequired  → contractor_required   (bool)
//	EnergyAuditRequired → energy_audit_required (bool)
//	ScraperVersion      → scraper_version       (text)
//	Processed           → processed             (bool — false until admin reviews)
//	CreatedAt           → created_at            (timestamptz)
//	UpdatedAt           → updated_at            (timestamptz)
type Incentive struct {
	ID                   string
	ProgramName          string
	UtilityCompany       string
	Status               string
	IncentiveDescription *string
	IncentiveAmount      *float64
	MaximumAmount        *float64
	PercentValue         *float64
	PerUnitAmount        *float64
	IncentiveFormat      *string
	UnitType             *string
	// ProgramHash is the zipcode-agnostic dedup key — SHA-256 hex of
	// normalize(ProgramName)|normalize(UtilityCompany).
	// Source is excluded so the same program scraped by multiple sources merges.
	// Pre-computed here so the promoter can rely on it without recomputing.
	ProgramHash          string
	State                *string
	ZipCode              *string   // primary / discovery ZIP (single, legacy)
	ZipCodes             []string  // full list of ZIPs this incentive covers
	ServiceTerritory     *string
	AvailableNationwide  *bool
	CategoryTag          []string
	Segment              []string
	Portfolio            []string
	CustomerType         *string
	ProductCategory      *string
	Administrator        *string
	Source               string
	StartDate            *string
	EndDate              *string
	WhileFundsLast       *bool
	ApplicationURL       *string
	ApplicationProcess   *string
	ProgramURL           *string
	ContactEmail         *string
	ContactPhone         *string
	IsFeatured           bool
	ImageURL             *string
	ImageURLs            []string
	ContractorRequired   *bool
	EnergyAuditRequired  *bool
	RateTiers            []RateTier
	ScraperVersion       string
	Processed            bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// NewIncentive returns an Incentive pre-filled with safe defaults.
// source is the scraper identifier (e.g. "dsireusa", "rewiring_america").
// version is written to scraper_version.
// Callers must set ProgramName, UtilityCompany, and any source-specific fields.
func NewIncentive(source, version string) Incentive {
	now := time.Now().UTC()
	return Incentive{
		ID:             uuid.New().String(),
		Status:         "draft",
		Source:         source,
		IsFeatured:     false,
		Processed:      false,
		ScraperVersion: version,
		CategoryTag:    []string{},
		Segment:        []string{},
		Portfolio:      []string{},
		ZipCodes:       []string{},
		ImageURLs:      []string{},
		RateTiers:      []RateTier{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// normalizePart lowercases, collapses whitespace, and trims s so minor
// formatting differences don't produce phantom duplicates.
func normalizePart(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(strings.ToLower(s)), " "))
}

// ComputeProgramHash returns the SHA-256 hex of
//
//	normalize(programName) | normalize(utilityCompany)
//
// Source is intentionally excluded — the same program offered by the same
// utility may be scraped by multiple sources; excluding source lets those rows
// merge into a single rebate.
//
// This is the zipcode-agnostic dedup key written to stg_program_hash /
// rebates.program_hash.  Must match the identical algorithm in the TypeScript
// promoter (prisma/scripts/promote-staging.ts: computeProgramHash).
func ComputeProgramHash(programName, utilityCompany string) string {
	key := normalizePart(programName) + "|" + normalizePart(utilityCompany)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}

// DeterministicID returns a UUID v5 string derived from a namespace prefix and
// a source-specific stable key. Use this when the external source has its own
// integer or string ID so that re-scraping the same program always produces the
// same UUID (preventing duplicate rows).
//
// Example:
//
//	id := models.DeterministicID("dsireusa", "917")
func DeterministicID(namespace, key string) string {
	ns := uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://incenva.com/scrapers/"+namespace))
	return uuid.NewSHA1(ns, []byte(key)).String()
}

// PtrString returns a pointer to s, or nil if s is empty.
func PtrString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// PtrBool returns a pointer to b.
func PtrBool(b bool) *bool { return &b }

// PtrFloat returns a pointer to f, or nil if f is 0.
func PtrFloat(f float64) *float64 {
	if f == 0 {
		return nil
	}
	return &f
}
