package config

import "github.com/incenva/rebate-scraper/db"

// LoadTenantsFromDB reads scraper_source_configs from the database and converts
// them to a []TenantConfig with per-source location filters.
// Returns nil (not error) when the table is empty or missing — callers fall back
// to tenants.json.
func LoadTenantsFromDB(database interface {
	LoadActiveSourceConfigs() ([]db.ScraperSourceConfigRow, error)
}) ([]TenantConfig, error) {
	rows, err := database.LoadActiveSourceConfigs()
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	var configs []TenantConfig
	for _, row := range rows {
		maxIncentives := 0
		if row.MaxIncentives != nil {
			maxIncentives = *row.MaxIncentives
		}
		configs = append(configs, TenantConfig{
			ID:      row.Source,
			Name:    row.Source,
			Active:  row.Active,
			Sources: []string{row.Source},
			DBURLEnv: "DATABASE_URL", // same DB
			LocationFilter: LocationFilter{
				States:       row.States,
				Utilities:    row.Utilities,
				ServiceAreas: row.ServiceAreas,
				ZipCodes:     row.ZipCodes,
			},
			MaxIncentivesPerSource: maxIncentives,
		})
	}
	return configs, nil
}
