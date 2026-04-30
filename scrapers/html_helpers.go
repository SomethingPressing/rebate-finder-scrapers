// html_helpers.go — shared HTML/text extraction helpers for utility scrapers.
//
// All helpers are derived directly from the field extraction rules in the
// SmythOS rf-scraper-pnm-srp-coned-xcel-peninsul LLM prompt schema.
//
// Also contains ExtractIncentiveFromPDFText — the shared PDF extraction path
// mirroring the SmythOS scraper agent's is_pdf_url → LLM Attachment branch.
package scrapers

import (
	"regexp"
	"strings"

	"github.com/incenva/rebate-scraper/models"
)

// ── Regex patterns ────────────────────────────────────────────────────────────

var (
	rePhone = regexp.MustCompile(`(?:\+1[\s.\-]?)?(?:\(?\d{3}\)?[\s.\-]?\d{3}[\s.\-]?\d{4})`)
	reEmail = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

	// Boolean field patterns from the LLM schema rules.
	reContractorRequired   = regexp.MustCompile(`(?i)(?:must be (?:installed|completed) by|licensed contractor|approved contractor|trade ally|contractor required|participating contractor)`)
	reEnergyAuditRequired  = regexp.MustCompile(`(?i)(?:energy audit required|home assessment required|home energy assessment|home energy checkup required|pre-inspection required)`)
	reCurrentlyActive      = regexp.MustCompile(`(?i)(?:expired|program ended|no longer available|program closed|funding exhausted|wait ?list)`)
	reIncomeQualified      = regexp.MustCompile(`(?i)(?:low.income|income.qualified|income.eligible|CARE|FERA|LIHEAP|Good Neighbor|affordable|<\s*\d+%\s*AMI|\d+%\s*(?:of\s+)?(?:area\s+median|AMI))`)
	reStartDate            = regexp.MustCompile(`(?i)(?:effective|start(?:s|ing)?|begin(?:s|ning)?|as of|from)\s+(\d{4}-\d{2}-\d{2}|\w+ \d{1,2},?\s*\d{4})`)
	reEndDate              = regexp.MustCompile(`(?i)(?:end(?:s|ing)?|expires?|through|until|deadline|valid\s+through|offer\s+ends?)\s+(\d{4}-\d{2}-\d{2}|\w+ \d{1,2},?\s*\d{4}|\w+ \d{4}|December 31,?\s*\d{4}|April 30,?\s*\d{4})`)
)

// extractPhone finds the first US phone number in text.
func extractPhone(text string) string {
	m := rePhone.FindString(text)
	return strings.TrimSpace(m)
}

// extractEmail finds the first email address in text.
func extractEmail(text string) string {
	m := reEmail.FindString(text)
	return strings.ToLower(strings.TrimSpace(m))
}

// extractContractorRequired returns true if page text indicates a licensed
// contractor is required (schema field: contractor_required).
func extractContractorRequired(text string) *bool {
	if reContractorRequired.MatchString(text) {
		b := true
		return &b
	}
	return nil
}

// extractEnergyAuditRequired returns true if page text indicates an energy
// audit is required (schema field: energy_audit_required).
func extractEnergyAuditRequired(text string) *bool {
	if reEnergyAuditRequired.MatchString(text) {
		b := true
		return &b
	}
	return nil
}

// extractCurrentlyActive returns false if the page text signals the program
// is no longer available.  Returns nil (unknown) otherwise — callers should
// default to true when nil.
func extractCurrentlyActive(text string) *bool {
	if reCurrentlyActive.MatchString(text) {
		b := false
		return &b
	}
	b := true
	return &b
}

// extractLowIncomeEligible returns true if the page mentions income-qualified
// eligibility (schema field: low_income_eligible).
func extractLowIncomeEligible(text string) *bool {
	if reIncomeQualified.MatchString(text) {
		b := true
		return &b
	}
	return nil
}

// extractCustomerType returns "Residential", "Commercial", or "" inferred from
// the URL path and page title (schema field: customer_type).
func extractCustomerType(urlAndTitle string) string {
	lower := strings.ToLower(urlAndTitle)
	hasRes := strings.Contains(lower, "residential") || strings.Contains(lower, "/home") ||
		strings.Contains(lower, "homeowner") || strings.Contains(lower, "renter")
	hasBiz := strings.Contains(lower, "business") || strings.Contains(lower, "commercial") ||
		strings.Contains(lower, "industrial") || strings.Contains(lower, "multifamily")
	if hasRes && hasBiz {
		return "Residential, Commercial"
	}
	if hasBiz {
		return "Commercial"
	}
	if hasRes {
		return "Residential"
	}
	return ""
}

// extractRecipient returns a recipient label (schema field: recipient) from
// the page text: "Homeowner", "Renter", "Business Owner", etc.
func extractRecipient(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "homeowner"):
		return "Homeowner"
	case strings.Contains(lower, "renter") || strings.Contains(lower, "tenant"):
		return "Renter"
	case strings.Contains(lower, "landlord"):
		return "Landlord"
	case strings.Contains(lower, "small business"):
		return "Small Business Owner"
	case strings.Contains(lower, "business"):
		return "Business Owner"
	default:
		return ""
	}
}

// extractStartDate finds a start or effective date in page text.
// Returns the matched date string or empty string if none found.
func extractStartDate(text string) string {
	m := reStartDate.FindStringSubmatch(text)
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// extractEndDate finds an expiry or end date in page text.
// Returns the matched date string or empty string if none found.
func extractEndDate(text string) string {
	m := reEndDate.FindStringSubmatch(text)
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// ── Category inference ────────────────────────────────────────────────────────

// categoryKeywords maps keyword substrings to category tag names.
// Ordered from most specific to most general to ensure precise matching.
// Derived from the product_category / items / technologies fields in the
// SmythOS scraper LLM schema.
var categoryKeywords = []struct {
	keyword  string
	category string
}{
	{"heat pump water heater", "Water Heating"},
	{"hpwh", "Water Heating"},
	{"water heater", "Water Heating"},
	{"heat pump", "HVAC"},
	{"mini-split", "HVAC"},
	{"geothermal", "HVAC"},
	{"hvac", "HVAC"},
	{"air condition", "HVAC"},
	{"cooling", "HVAC"},
	{"heating", "HVAC"},
	{"furnace", "HVAC"},
	{"boiler", "HVAC"},
	{"insulation", "Weatherization"},
	{"weatheriz", "Weatherization"},
	{"weather-ready", "Weatherization"},
	{"window", "Weatherization"},
	{"door replacement", "Weatherization"},
	{"air seal", "Weatherization"},
	{"solar", "Solar"},
	{"photovoltaic", "Solar"},
	{"pv system", "Solar"},
	{"battery storage", "Battery Storage"},
	{"battery", "Battery Storage"},
	{"electric vehicle", "Electric Vehicles"},
	{"ev charger", "Electric Vehicles"},
	{"ev-ready", "Electric Vehicles"},
	{"ev rebate", "Electric Vehicles"},
	{"charging station", "Electric Vehicles"},
	{"e-bike", "Electric Vehicles"},
	{"ebike", "Electric Vehicles"},
	{"smart thermostat", "Smart Thermostat"},
	{"thermostat", "Smart Thermostat"},
	{"lighting", "Lighting"},
	{"led", "Lighting"},
	{"appliance", "Appliances"},
	{"refrigerator", "Appliances"},
	{"washer", "Appliances"},
	{"dryer", "Appliances"},
	{"dishwasher", "Appliances"},
	{"evaporative cooler", "HVAC"},
	{"pool pump", "Appliances"},
	{"demand response", "Demand Response"},
	{"time-of-use", "Demand Response"},
	{"peak reward", "Demand Response"},
	{"load management", "Demand Response"},
	{"smart usage", "Demand Response"},
	{"low income", "Income Qualified"},
	{"income-qualified", "Income Qualified"},
	{"income-eligible", "Income Qualified"},
	{"liheap", "Income Qualified"},
	{"good neighbor", "Income Qualified"},
	{"care program", "Income Qualified"},
	{"fera program", "Income Qualified"},
	{"weatherization", "Weatherization"},
	{"financing", "Financing"},
	{"zero percent loan", "Financing"},
}

// inferCategories returns a list of category tags inferred from the given text.
// The text should be a concatenation of URL path, page title, and/or description.
func inferCategories(text string) []string {
	lower := strings.ToLower(text)
	seen := make(map[string]bool)
	var cats []string
	for _, ck := range categoryKeywords {
		if strings.Contains(lower, ck.keyword) && !seen[ck.category] {
			seen[ck.category] = true
			cats = append(cats, ck.category)
		}
	}
	if len(cats) == 0 {
		cats = []string{"Energy Efficiency"}
	}
	return cats
}

// ── PDF extraction path ───────────────────────────────────────────────────────
//
// ExtractIncentiveFromPDFText mirrors the SmythOS scraper agent's
// is_pdf_url → LLM Attachment branch.  Instead of passing the PDF binary to
// an LLM, we extract plain text with pdf_extractor.go and apply the same
// deterministic helpers used for HTML pages.
//
// Parameters match the per-scraper defaults (source, utility, territory, etc.)
// so each utility scraper can call this with its own constants.

// PDFIncentiveOpts carries the per-source defaults needed to populate fixed fields.
type PDFIncentiveOpts struct {
	Source         string
	ScraperVersion string
	UtilityCompany string
	State          string // 2-letter code, "" for multi-state
	ZipCode        string // representative ZIP, "" if unknown
	Territory      string // service territory label
	DefaultApply   string // fallback application_process text
}

// ExtractIncentiveFromPDFText creates a models.Incentive from raw PDF text
// extracted from pageURL.  Returns nil if the text doesn't look like a
// meaningful incentive program (e.g. no title found, error/404 content).
func ExtractIncentiveFromPDFText(text, pageURL string, opts PDFIncentiveOpts) *models.Incentive {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// ── Program name ─────────────────────────────────────────────────────────
	// Try the first non-blank line that is long enough to be a title.
	programName := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= 5 && len(line) <= 200 {
			programName = line
			break
		}
	}
	// Fall back to filename extracted from URL.
	if programName == "" {
		parts := strings.Split(pageURL, "/")
		if len(parts) > 0 {
			last := parts[len(parts)-1]
			last = strings.TrimSuffix(strings.ToLower(last), ".pdf")
			last = strings.NewReplacer("-", " ", "_", " ").Replace(last)
			programName = strings.Title(strings.TrimSpace(last)) //nolint:staticcheck
		}
	}
	if programName == "" || len(programName) < 5 {
		return nil
	}

	// Skip error pages.
	titleLower := strings.ToLower(programName)
	for _, p := range []string{"page not found", "404", "error", "access denied"} {
		if strings.Contains(titleLower, p) {
			return nil
		}
	}

	// ── Description ──────────────────────────────────────────────────────────
	// First paragraph that is longer than 40 characters.
	description := ""
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if len(para) > 40 {
			description = para
			break
		}
	}
	if description == "" {
		description = programName
	}
	if len(description) > 500 {
		description = description[:497] + "..."
	}

	// ── Amount ───────────────────────────────────────────────────────────────
	format, amount := ParseAmount(text)
	if format == "" {
		format = "narrative"
	}

	// ── Contact ───────────────────────────────────────────────────────────────
	contactPhone := extractPhone(text)
	contactEmail := extractEmail(text)

	// ── Boolean / structured fields ──────────────────────────────────────────
	contractorRequired := extractContractorRequired(text)
	energyAuditRequired := extractEnergyAuditRequired(text)
	customerType := extractCustomerType(pageURL + " " + programName)
	startDate := extractStartDate(text)
	endDate := extractEndDate(text)

	// ── Categories ───────────────────────────────────────────────────────────
	categories := inferCategories(pageURL + " " + strings.ToLower(programName) + " " + strings.ToLower(text[:min(500, len(text))]))

	// ── Build incentive ───────────────────────────────────────────────────────
	id := models.DeterministicID(opts.Source, pageURL)

	inc := models.NewIncentive(opts.Source, opts.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = opts.UtilityCompany
	inc.IncentiveDescription = models.PtrString(description)
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(opts.DefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, opts.UtilityCompany)

	if opts.State != "" {
		inc.State = models.PtrString(opts.State)
	}
	if opts.ZipCode != "" {
		inc.ZipCode = models.PtrString(opts.ZipCode)
	}
	if opts.Territory != "" {
		inc.ServiceTerritory = models.PtrString(opts.Territory)
	}
	if amount != nil {
		inc.IncentiveAmount = amount
	}
	if contactPhone != "" {
		inc.ContactPhone = models.PtrString(contactPhone)
	}
	if contactEmail != "" {
		inc.ContactEmail = models.PtrString(contactEmail)
	}
	if contractorRequired != nil {
		inc.ContractorRequired = contractorRequired
	}
	if energyAuditRequired != nil {
		inc.EnergyAuditRequired = energyAuditRequired
	}
	if customerType != "" {
		inc.CustomerType = models.PtrString(customerType)
	}
	if startDate != "" {
		inc.StartDate = models.PtrString(startDate)
	}
	if endDate != "" {
		inc.EndDate = models.PtrString(endDate)
	}

	return &inc
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
