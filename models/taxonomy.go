package models

import (
	"strings"

	"github.com/google/uuid"
)

// CategoryPortfolioMap maps a normalized category name to the canonical
// portfolio(s) it belongs to, mirroring PORTFOLIO_CATEGORIES in taxonomyConfig.ts.
// Both the scraper (derivePortfolios) and the promoter (auto-create categories)
// use this map so the taxonomy stays consistent across the Go codebase.
var CategoryPortfolioMap = map[string][]string{
	// Energy Efficiency
	"HVAC":                 {"Energy Efficiency"},
	"Weatherization":       {"Energy Efficiency"},
	"Lighting":             {"Energy Efficiency"},
	"Appliances":           {"Energy Efficiency"},
	"Water Heating":        {"Energy Efficiency"},
	"Industrial Equipment": {"Energy Efficiency"},
	"Whole Building":       {"Energy Efficiency"},
	"Smart Thermostat":     {"Energy Efficiency", "Demand Response"},

	// Distributed Energy Resources
	"Solar":                 {"Distributed Energy Resources"},
	"Geothermal":            {"Distributed Energy Resources"},
	"Energy Storage":        {"Distributed Energy Resources"},
	"Wind":                  {"Distributed Energy Resources"},
	"Biomass":               {"Distributed Energy Resources"},
	"Fuel Cells":            {"Distributed Energy Resources"},
	"Combined Heat & Power": {"Distributed Energy Resources"},
	"Renewable Energy":      {"Distributed Energy Resources"},

	// Electric Vehicles
	"Electric Vehicles":         {"Electric Vehicles"},
	"Alternative Fuel Vehicles": {"Electric Vehicles"},

	// Demand Response
	"Demand Response": {"Demand Response"},

	// Building Electrification
	"Electrification": {"Building Electrification"},

	// Income Qualified
	"Income Qualified": {"Income Qualified"},

	// Financing
	"Financing": {"Financing"},
}

// PortfolioAbbrev maps portfolio names to their short codes, mirroring
// PORTFOLIO_ABBREV in taxonomyConfig.ts.
var PortfolioAbbrev = map[string]string{
	"Energy Efficiency":            "EE",
	"Distributed Energy Resources": "DER",
	"Demand Response":              "DR",
	"Electric Vehicles":            "EV",
	"Income Qualified":             "LMI",
	"Financing":                    "FIN",
	"Building Electrification":     "BE",
	"Rates":                        "RATES",
}

// PortfolioSlug converts a portfolio name to a URL-safe slug.
func PortfolioSlug(name string) string {
	return CategorySlug(name) // same logic
}

// AllPortfolioNames returns all unique portfolio names from CategoryPortfolioMap,
// ordered for consistent seeding.
func AllPortfolioNames() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, portfolios := range CategoryPortfolioMap {
		for _, p := range portfolios {
			if _, ok := seen[p]; !ok {
				seen[p] = struct{}{}
				out = append(out, p)
			}
		}
	}
	// Also include portfolios that have no categories yet (Rates, Financing).
	for p := range PortfolioAbbrev {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// CategorySlug converts a category name to a URL-safe slug.
// "Water Heating" → "water-heating", "Combined Heat & Power" → "combined-heat-power"
func CategorySlug(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " & ", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

// CategoryPrimaryPortfolio returns the first portfolio for the given category
// name, or an empty string if it is not in the map.
func CategoryPrimaryPortfolio(name string) string {
	if ps, ok := CategoryPortfolioMap[name]; ok && len(ps) > 0 {
		return ps[0]
	}
	return ""
}

// NewCategoryID generates a deterministic UUID v5 for a category name so that
// repeated upserts produce the same ID and do not accumulate duplicates when
// the ON CONFLICT clause is unavailable.
func NewCategoryID(name string) string {
	return DeterministicID("category", name)
}

// ensure uuid is used (DeterministicID uses it)
var _ = uuid.New
