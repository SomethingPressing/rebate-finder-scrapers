package models

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ComputeContentHash returns the SHA-256 hex fingerprint of a staging row's
// DATA fields — the fields a tenant would actually receive.
//
// Two rows with the same content hash carry identical program data, so the
// promoter can skip rows that did not really change and tenant importers can
// skip no-op writes. During reconciliation, same id + different hash means the
// two sides disagree and someone should look.
//
// Deliberately excluded:
//   - lifecycle columns (stg_*, release status, timestamps) — state, not data
//   - source and scraper_version — identity/provenance, not content
//   - stg_raw_response — the verbatim payload can change (whitespace, field
//     order) without any user-visible data changing
//
// Array fields are sorted before hashing so scrape-order differences do not
// produce phantom changes. The serialization is versioned by construction: any
// change to the field list or encoding changes every hash, which forces a
// deliberate re-baseline (re-run cmd/migrate 002_backfill_content_hash).
func ComputeContentHash(sr *StagedRebate) string {
	var b strings.Builder

	str := func(key, v string) {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	strp := func(key string, v *string) {
		if v != nil {
			str(key, *v)
		} else {
			str(key, "")
		}
	}
	numP := func(key string, v *float64) {
		if v != nil {
			str(key, strconv.FormatFloat(*v, 'f', -1, 64))
		} else {
			str(key, "")
		}
	}
	boolP := func(key string, v *bool) {
		if v != nil {
			str(key, strconv.FormatBool(*v))
		} else {
			str(key, "")
		}
	}
	slice := func(key string, v StringSlice) {
		sorted := make([]string, len(v))
		copy(sorted, v)
		sort.Strings(sorted)
		str(key, strings.Join(sorted, ","))
	}

	str("program_name", sr.ProgramName)
	str("utility_company", sr.UtilityCompany)
	strp("incentive_description", sr.IncentiveDescription)
	numP("incentive_amount", sr.IncentiveAmount)
	numP("maximum_amount", sr.MaximumAmount)
	numP("percent_value", sr.PercentValue)
	numP("per_unit_amount", sr.PerUnitAmount)
	strp("incentive_format", sr.IncentiveFormat)
	strp("unit_type", sr.UnitType)
	strp("state", sr.State)
	strp("zip_code", sr.ZipCode)
	slice("zip_codes", sr.ZipCodes)
	strp("service_territory", sr.ServiceTerritory)
	boolP("available_nationwide", sr.AvailableNationwide)
	slice("category_tag", sr.CategoryTag)
	slice("segment", sr.Segment)
	strp("customer_type", sr.CustomerType)
	strp("product_category", sr.ProductCategory)
	strp("administrator", sr.Administrator)
	strp("start_date", sr.StartDate)
	strp("end_date", sr.EndDate)
	boolP("while_funds_last", sr.WhileFundsLast)
	strp("application_url", sr.ApplicationURL)
	strp("application_process", sr.ApplicationProcess)
	strp("program_url", sr.ProgramURL)
	strp("contact_email", sr.ContactEmail)
	strp("contact_phone", sr.ContactPhone)
	strp("image_url", sr.ImageURL)
	slice("image_urls", sr.ImageURLs)
	boolP("contractor_required", sr.ContractorRequired)
	boolP("energy_audit_required", sr.EnergyAuditRequired)
	strp("source_url", sr.SourceURL)
	strp("implementing_sector", sr.ImplementingSector)

	// rate_tiers: []RateTier marshals deterministically (fixed struct field order).
	if len(sr.RateTiers) > 0 {
		tiers, err := json.Marshal(sr.RateTiers)
		if err != nil {
			// Marshalling a plain struct slice cannot realistically fail; fall
			// back to a stable non-empty marker rather than silently equating
			// "has tiers" with "no tiers".
			tiers = []byte("unmarshalable")
		}
		str("rate_tiers", string(tiers))
	} else {
		str("rate_tiers", "")
	}

	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}
