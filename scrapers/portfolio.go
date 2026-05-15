package scrapers

import "github.com/incenva/rebate-scraper/models"

// derivePortfolios returns the canonical portfolio names for a set of category
// tags using the shared CategoryPortfolioMap from the models package.
func derivePortfolios(categories []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, cat := range categories {
		for _, p := range models.CategoryPortfolioMap[cat] {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
