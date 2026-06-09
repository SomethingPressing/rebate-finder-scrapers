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
	if err != nil {
		return nil, err // real DB error → caller falls back to tenants.json
	}
	if rows == nil {
		return nil, nil // table doesn't exist yet → fall back to tenants.json
	}
	// rows is non-nil (possibly empty): DB is set up, respect it even if no
	// sources are active — return an empty slice so the scraper runs nothing
	// instead of falling back to the file config.
	configs := make([]TenantConfig, 0, len(rows))
	for _, row := range rows {
		maxIncentives := 0
		if row.MaxIncentives != nil {
			maxIncentives = *row.MaxIncentives
		}
		configs = append(configs, TenantConfig{
			ID:      row.ClientID,   // client_id is the tenant ID — used to tag incentives
			Name:    row.ClientID,
			Active:  row.Active,
			Sources: []string{row.Source},
			DBURLEnv: "DATABASE_URL",
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
