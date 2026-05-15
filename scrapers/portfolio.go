package scrapers

// categoryToPortfolios maps CategoryTag values to the canonical portfolio names
// defined in the consumer app's taxonomyConfig.ts PORTFOLIOS constant.
//
// Portfolio = WHAT the program does (Energy Efficiency, Electric Vehicles, …)
// Implementing sector (Utility / State / Federal) belongs in ImplementingSector.
var categoryToPortfolios = map[string][]string{
	// ── Energy Efficiency ────────────────────────────────────────────────────
	"HVAC":                 {"Energy Efficiency"},
	"Weatherization":       {"Energy Efficiency"},
	"Lighting":             {"Energy Efficiency"},
	"Appliances":           {"Energy Efficiency"},
	"Water Heating":        {"Energy Efficiency"},
	"Industrial Equipment": {"Energy Efficiency"},
	"Whole Building":       {"Energy Efficiency"},
	// Smart Thermostat straddles EE and Demand Response.
	"Smart Thermostat": {"Energy Efficiency", "Demand Response"},

	// ── Distributed Energy Resources ─────────────────────────────────────────
	"Solar":                 {"Distributed Energy Resources"},
	"Geothermal":            {"Distributed Energy Resources"},
	"Energy Storage":        {"Distributed Energy Resources"},
	"Wind":                  {"Distributed Energy Resources"},
	"Biomass":               {"Distributed Energy Resources"},
	"Fuel Cells":            {"Distributed Energy Resources"},
	"Combined Heat & Power": {"Distributed Energy Resources"},
	"Renewable Energy":      {"Distributed Energy Resources"},

	// ── Electric Vehicles ─────────────────────────────────────────────────────
	"Electric Vehicles":         {"Electric Vehicles"},
	"Alternative Fuel Vehicles": {"Electric Vehicles"},

	// ── Demand Response ───────────────────────────────────────────────────────
	"Demand Response": {"Demand Response"},

	// ── Building Electrification ─────────────────────────────────────────────
	"Electrification": {"Building Electrification"},

	// ── Income Qualified ─────────────────────────────────────────────────────
	"Income Qualified": {"Income Qualified"},

	// ── Financing ─────────────────────────────────────────────────────────────
	"Financing": {"Financing"},
}

// derivePortfolios returns the canonical portfolio names for a set of category
// tags, deduplicating the output.
func derivePortfolios(categories []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, cat := range categories {
		for _, p := range categoryToPortfolios[cat] {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
