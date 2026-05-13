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

// DefaultSkipPhrases is the shared page-title blocklist used by every HTML
// scraper to reject non-incentive pages (login walls, error pages, feedback
// forms, etc.).  Each scraper's extractPage uses this instead of a local copy
// so a single update covers all sources.
var DefaultSkipPhrases = []string{
	// Structural errors
	"page not found", "404", "error", "site map",
	// Home / generic pages
	"home",
	// Login / auth walls — various spellings
	"login", "log in", "sign in", "extra verification",
	"two-factor", "verify your identity",
	// Junk pages that slip through URL filters
	"we welcome your feedback", "welcome your feedback",
	"access denied", "403 forbidden",
	"session expired", "account suspended", "permission denied",
}

var (
	// rePhone matches US phone numbers that have at least one separator
	// between digit groups — e.g. (800) 752-6633 or 800.752.6633.
	// Requiring separators prevents matching large integers (timestamps,
	// dollar amounts, ZIP+4 codes) that happen to be 10 digits long.
	rePhone = regexp.MustCompile(`(?:^|[^\d])(?:\+1[\s.\-]?)?(?:\(\d{3}\)|\d{3})[\s.\-]\d{3}[\s.\-]\d{4}(?:[^\d]|$)`)
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
	// Trim the boundary non-digit characters captured by the lookaround workaround.
	m = strings.TrimFunc(m, func(r rune) bool { return r < '0' || r > '9' && r != '+' && r != '(' })
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

// resPhrases and bizPhrases are specific body-text patterns for
// extractCustomerTypeWithBody. Longer phrases are used to avoid false
// positives from navigation or boilerplate text.
var resPhrases = []string{
	"residential customers", "residential customer",
	"for homeowners", "for renters", "for your home",
	"home energy", "single-family", "multifamily residential",
	"apartment residents", "residential program", "residential rebate",
	"residential rate", "residential electricity",
	"qualifying households", "eligible households",
}

var bizPhrases = []string{
	"business customers", "business customer",
	"commercial customers", "commercial customer",
	"for your business", "non-residential",
	"commercial buildings", "commercial building",
	"small business", "commercial and industrial",
	"c&i customers", "business program",
	"commercial program", "business rebate",
	"commercial rebate", "commercial electricity",
	"eligible businesses", "qualifying businesses",
}

// extractCustomerTypeWithBody extends extractCustomerType by also scanning
// the first 2000 characters of page body text for specific eligibility phrases
// when the URL and title do not contain a clear signal.
func extractCustomerTypeWithBody(urlAndTitle, body string) string {
	if ct := extractCustomerType(urlAndTitle); ct != "" {
		return ct
	}
	lower := strings.ToLower(body[:min(len(body), 2000)])
	var hasRes, hasBiz bool
	for _, p := range resPhrases {
		if strings.Contains(lower, p) {
			hasRes = true
			break
		}
	}
	for _, p := range bizPhrases {
		if strings.Contains(lower, p) {
			hasBiz = true
			break
		}
	}
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

// categoryKeywords maps lowercase substrings to category tag names.
// Rules:
//   - More specific phrases listed before shorter/more-generic ones to avoid
//     false positives (e.g. "heat pump water heater" before "heat pump").
//   - All keywords are lowercase; inferCategories lowercases the input.
//   - Multiple keywords can map to the same category; duplicates are deduplicated.
var categoryKeywords = []struct {
	keyword  string
	category string
}{
	// ── Water Heating (before HVAC to avoid "heat pump" stealing the match) ──
	{"heat pump water heater", "Water Heating"},
	{"hpwh", "Water Heating"},
	{"water heater", "Water Heating"},
	{"hot water heater", "Water Heating"},
	{"tankless water", "Water Heating"},
	{"instantaneous water", "Water Heating"},
	{"domestic hot water", "Water Heating"},
	{"dhw", "Water Heating"},
	{"water heating", "Water Heating"},
	{"solar water heat", "Water Heating"},

	// ── HVAC ─────────────────────────────────────────────────────────────────
	{"heat pump", "HVAC"},
	{"mini-split", "HVAC"},
	{"minisplit", "HVAC"},
	{"ductless", "HVAC"},
	{"geothermal", "HVAC"},
	{"ground source", "HVAC"},
	{"air source", "HVAC"},
	{"hvac", "HVAC"},
	{"air conditioner", "HVAC"},
	{"air conditioning", "HVAC"},
	{"central air", "HVAC"},
	{"cooling system", "HVAC"},
	{"heating system", "HVAC"},
	{"furnace", "HVAC"},
	{"boiler", "HVAC"},
	{"radiant heat", "HVAC"},
	{"forced air", "HVAC"},
	{"rooftop unit", "HVAC"},
	{"/rtu", "HVAC"},
	{"heat recovery ventil", "HVAC"},
	{"hrv", "HVAC"},
	{"erv", "HVAC"},
	{"evaporative cooler", "HVAC"},
	{"swamp cooler", "HVAC"},
	{"variable refrigerant", "HVAC"},
	{"vrf system", "HVAC"},
	{"chiller", "HVAC"},
	{"ductwork", "HVAC"},

	// ── Solar ─────────────────────────────────────────────────────────────────
	{"solar panel", "Solar"},
	{"solar power", "Solar"},
	{"solar energy", "Solar"},
	{"solar install", "Solar"},
	{"solar system", "Solar"},
	{"rooftop solar", "Solar"},
	{"community solar", "Solar"},
	{"solar garden", "Solar"},
	{"solar array", "Solar"},
	{"photovoltaic", "Solar"},
	{"pv system", "Solar"},
	{"pv installation", "Solar"},
	{"net metering", "Solar"},
	{"net energy metering", "Solar"},
	{"solar thermal", "Solar"},
	{"solar lease", "Solar"},
	{"solar ppa", "Solar"},
	{"solar rebate", "Solar"},
	{"solar incentive", "Solar"},
	{"/solar", "Solar"},
	{"solar", "Solar"},

	// ── Battery Storage ───────────────────────────────────────────────────────
	{"battery storage", "Battery Storage"},
	{"energy storage system", "Battery Storage"},
	{"home battery", "Battery Storage"},
	{"residential storage", "Battery Storage"},
	{"backup battery", "Battery Storage"},
	{"stationary storage", "Battery Storage"},
	{"virtual power plant", "Battery Storage"},
	{"vpp", "Battery Storage"},
	{"powerwall", "Battery Storage"},
	{"energy storage", "Battery Storage"},
	{"battery backup", "Battery Storage"},
	{"battery", "Battery Storage"},

	// ── Electric Vehicles ─────────────────────────────────────────────────────
	{"electric vehicle", "Electric Vehicles"},
	{"plug-in hybrid", "Electric Vehicles"},
	{"phev", "Electric Vehicles"},
	{"level 2 charg", "Electric Vehicles"},
	{"l2 charg", "Electric Vehicles"},
	{"dc fast charg", "Electric Vehicles"},
	{"dcfc", "Electric Vehicles"},
	{"ev charger", "Electric Vehicles"},
	{"ev charging", "Electric Vehicles"},
	{"ev rebate", "Electric Vehicles"},
	{"ev incentive", "Electric Vehicles"},
	{"ev-ready", "Electric Vehicles"},
	{"charging station", "Electric Vehicles"},
	{"charging infrastructure", "Electric Vehicles"},
	{"electric car", "Electric Vehicles"},
	{"electric truck", "Electric Vehicles"},
	{"electric bus", "Electric Vehicles"},
	{"transportation electrif", "Electric Vehicles"},
	{"chargepoint", "Electric Vehicles"},
	{"blink charg", "Electric Vehicles"},
	{"e-bike", "Electric Vehicles"},
	{"ebike", "Electric Vehicles"},
	{"electric bike", "Electric Vehicles"},
	{"electric motorcycle", "Electric Vehicles"},
	{"/ev/", "Electric Vehicles"},
	{"/ev-", "Electric Vehicles"},
	{"ev_", "Electric Vehicles"},

	// ── Weatherization ────────────────────────────────────────────────────────
	{"weatherization", "Weatherization"},
	{"weatheriz", "Weatherization"},
	{"air sealing", "Weatherization"},
	{"air seal", "Weatherization"},
	{"insulation", "Weatherization"},
	{"blower door", "Weatherization"},
	{"building envelope", "Weatherization"},
	{"thermal envelope", "Weatherization"},
	{"crawlspace insul", "Weatherization"},
	{"attic insul", "Weatherization"},
	{"wall insul", "Weatherization"},
	{"rim joist", "Weatherization"},
	{"draft proof", "Weatherization"},
	{"caulking", "Weatherization"},
	{"weatherstripping", "Weatherization"},
	{"weather strip", "Weatherization"},
	{"window replacement", "Weatherization"},
	{"window rebate", "Weatherization"},
	{"door replacement", "Weatherization"},
	{"weather-ready", "Weatherization"},

	// ── Lighting ──────────────────────────────────────────────────────────────
	{"lighting", "Lighting"},
	{"led bulb", "Lighting"},
	{"led lamp", "Lighting"},
	{"led light", "Lighting"},
	{"led fixture", "Lighting"},
	{"led retrofit", "Lighting"},
	{"led upgrade", "Lighting"},
	{"cfl bulb", "Lighting"},
	{"fluorescent", "Lighting"},
	{"luminaire", "Lighting"},
	{"light fixture", "Lighting"},
	{"lighting control", "Lighting"},
	{"occupancy sensor", "Lighting"},
	{"daylight sensor", "Lighting"},
	{"daylighting", "Lighting"},
	{"led", "Lighting"},

	// ── Appliances ───────────────────────────────────────────────────────────
	{"appliance rebate", "Appliances"},
	{"appliance recycl", "Appliances"},
	{"appliance upgrade", "Appliances"},
	{"appliance replac", "Appliances"},
	{"clothes washer", "Appliances"},
	{"clothes dryer", "Appliances"},
	{"dishwasher", "Appliances"},
	{"refrigerator", "Appliances"},
	{"freezer", "Appliances"},
	{"dehumidifier", "Appliances"},
	{"induction cooktop", "Appliances"},
	{"induction range", "Appliances"},
	{"electric range", "Appliances"},
	{"electric stove", "Appliances"},
	{"electric oven", "Appliances"},
	{"electric cooktop", "Appliances"},
	{"pool pump", "Appliances"},
	{"spa pump", "Appliances"},
	{"hot tub", "Appliances"},
	{"vending machine", "Appliances"},
	{"smart appliance", "Appliances"},
	{"washer", "Appliances"},
	{"dryer", "Appliances"},
	{"appliance", "Appliances"},

	// ── Smart Thermostat ─────────────────────────────────────────────────────
	{"smart thermostat", "Smart Thermostat"},
	{"programmable thermostat", "Smart Thermostat"},
	{"connected thermostat", "Smart Thermostat"},
	{"wifi thermostat", "Smart Thermostat"},
	{"thermostat rebate", "Smart Thermostat"},
	{"thermostat program", "Smart Thermostat"},
	{"nest thermostat", "Smart Thermostat"},
	{"ecobee", "Smart Thermostat"},
	{"thermostat", "Smart Thermostat"},

	// ── Demand Response ───────────────────────────────────────────────────────
	{"demand response", "Demand Response"},
	{"demand management", "Demand Response"},
	{"demand reduction", "Demand Response"},
	{"demand charge", "Demand Response"},
	{"time-of-use", "Demand Response"},
	{"time of use", "Demand Response"},
	{"time-of-day", "Demand Response"},
	{"tou rate", "Demand Response"},
	{"peak reward", "Demand Response"},
	{"peak saver", "Demand Response"},
	{"peak partner", "Demand Response"},
	{"peak reduction", "Demand Response"},
	{"off-peak", "Demand Response"},
	{"load management", "Demand Response"},
	{"load control", "Demand Response"},
	{"load shifting", "Demand Response"},
	{"smart usage", "Demand Response"},
	{"direct load control", "Demand Response"},
	{"interruptible rate", "Demand Response"},
	{"curtailment", "Demand Response"},
	{"grid-interactive", "Demand Response"},
	{"bring your own thermostat", "Demand Response"},
	{"byot", "Demand Response"},
	{"flexible load", "Demand Response"},
	{"demand flexibility", "Demand Response"},
	{"saver's switch", "Demand Response"},
	{"savers switch", "Demand Response"},
	{"dynamic pricing", "Demand Response"},
	{"critical peak", "Demand Response"},
	{"virtual peaker", "Demand Response"},
	{"rate program", "Demand Response"},

	// ── Income Qualified ─────────────────────────────────────────────────────
	{"income-qualified", "Income Qualified"},
	{"income qualified", "Income Qualified"},
	{"income-eligible", "Income Qualified"},
	{"income eligible", "Income Qualified"},
	{"income restricted", "Income Qualified"},
	{"low-income", "Income Qualified"},
	{"low income", "Income Qualified"},
	{"limited income", "Income Qualified"},
	{"liheap", "Income Qualified"},
	{"good neighbor fund", "Income Qualified"},
	{"good neighbor", "Income Qualified"},
	{"care program", "Income Qualified"},
	{"fera program", "Income Qualified"},
	{"affordability program", "Income Qualified"},
	{"bill assistance", "Income Qualified"},
	{"bill relief", "Income Qualified"},
	{"help paying", "Income Qualified"},
	{"financial assistance", "Income Qualified"},
	{"rate assistance", "Income Qualified"},
	{"lifeline rate", "Income Qualified"},
	{"discount program", "Income Qualified"},
	{"energy assistance", "Income Qualified"},
	{"emergency assistance", "Income Qualified"},
	{"means-tested", "Income Qualified"},
	{"subsidi", "Income Qualified"},
	{"affordable rate", "Income Qualified"},

	// ── Electrification ──────────────────────────────────────────────────────
	{"building electrification", "Electrification"},
	{"home electrification", "Electrification"},
	{"electrification program", "Electrification"},
	{"electrification rebate", "Electrification"},
	{"electrification incentive", "Electrification"},
	{"convert to electric", "Electrification"},
	{"electric conversion", "Electrification"},
	{"switching from gas", "Electrification"},
	{"gas to electric", "Electrification"},
	{"fossil fuel free", "Electrification"},
	{"all-electric", "Electrification"},
	{"going electric", "Electrification"},
	{"electrif", "Electrification"},

	// ── Financing ─────────────────────────────────────────────────────────────
	{"0% apr", "Financing"},
	{"zero percent apr", "Financing"},
	{"zero-interest loan", "Financing"},
	{"interest-free loan", "Financing"},
	{"on-bill financing", "Financing"},
	{"on-bill repayment", "Financing"},
	{"property assessed clean energy", "Financing"},
	{"c-pace", "Financing"},
	{"r-pace", "Financing"},
	{"hero program", "Financing"},
	{"clean energy loan", "Financing"},
	{"green loan", "Financing"},
	{"energy loan", "Financing"},
	{"home improvement loan", "Financing"},
	{"revolving loan fund", "Financing"},
	{"zero percent loan", "Financing"},
	{"financing", "Financing"},

	// ── Energy Efficiency (explicit match only, never a fallback) ─────────────
	{"energy efficiency program", "Energy Efficiency"},
	{"energy efficient upgrade", "Energy Efficiency"},
	{"efficiency rebate", "Energy Efficiency"},
	{"efficiency program", "Energy Efficiency"},
	{"efficiency upgrade", "Energy Efficiency"},
	{"efficiency improvement", "Energy Efficiency"},
	{"energy audit", "Energy Efficiency"},
	{"home energy checkup", "Energy Efficiency"},
	{"home energy assessment", "Energy Efficiency"},
	{"building performance", "Energy Efficiency"},
	{"energy upgrade", "Energy Efficiency"},
	{"energy improvement", "Energy Efficiency"},
	{"energy-saving", "Energy Efficiency"},
	{"energy saving", "Energy Efficiency"},
	{"retrofit program", "Energy Efficiency"},
}

// inferCategories returns category tags inferred from the given text.
// Pass URL + page title + a body-text snippet for best results.
// Returns an empty slice when no category can be reliably determined —
// callers should handle this gracefully (do NOT substitute a generic fallback).
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
	customerType := extractCustomerTypeWithBody(pageURL+" "+programName, text)
	startDate := extractStartDate(text)
	endDate := extractEndDate(text)

	// ── Categories ───────────────────────────────────────────────────────────
	categories := inferCategories(pageURL + " " + strings.ToLower(programName) + " " + strings.ToLower(text[:min(len(text), 2000)]))

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
