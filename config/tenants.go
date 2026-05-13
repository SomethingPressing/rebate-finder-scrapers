package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// LocationFilter defines the geographic scope for a tenant's incentives.
// An incentive matches if it satisfies at least one criterion.
// An empty filter (all fields zero/empty) means the tenant receives everything.
type LocationFilter struct {
	States       []string `json:"states"`        // 2-letter state codes, e.g. ["CA","NY"]
	Utilities    []string `json:"utilities"`     // utility names (case-insensitive substring match)
	ServiceAreas []string `json:"service_areas"` // service territory names (substring match)
	ZipCodes     []string `json:"zip_codes"`     // explicit ZIP codes for precise service territory
}

func (f LocationFilter) empty() bool {
	return len(f.States) == 0 &&
		len(f.Utilities) == 0 &&
		len(f.ServiceAreas) == 0 &&
		len(f.ZipCodes) == 0
}

// TenantConfig describes a single tenant served by this scraper instance.
// It defines the destination database that scraped data is promoted into.
// The scraper's own staging DB is configured separately via DATABASE_URL and
// SCRAPER_DB_SCHEMA in the .env file — it is not a per-tenant concern.
type TenantConfig struct {
	ID                    string         `json:"id"`                       // lowercase slug, e.g. "acme"
	Name                  string         `json:"name"`                     // display name for logs
	Active                bool           `json:"active"`                   // false = skip without removing
	Sources               []string       `json:"sources"`                  // scraper names; empty = all
	DBURLEnv              string         `json:"db_url_env"`               // DSN or env var name for the tenant's destination DB
	LocationFilter        LocationFilter `json:"location_filter"`          // geographic filter; empty = all
	MaxIncentivesPerSource int           `json:"max_incentives_per_source"` // 0 = unlimited
}

// DBUrl resolves the tenant's database URL.
// If DBURLEnv already looks like a DSN (starts with "postgres"), it is used
// directly. Otherwise it is treated as an environment variable name and
// resolved via os.Getenv. This lets tenants.json hold either a raw connection
// string or an env var name.
func (t TenantConfig) DBUrl() string {
	if strings.HasPrefix(t.DBURLEnv, "postgres") {
		return t.DBURLEnv
	}
	return os.Getenv(t.DBURLEnv)
}

// MatchesIncentive returns true if this tenant should receive the incentive.
//
// Matching rules — any one is sufficient:
//   - Filter is empty → match everything
//   - Incentive is available nationwide
//   - State matches a filter state (case-insensitive)
//   - Utility company matches a filter utility (case-insensitive substring)
//   - Service territory matches a filter service area (substring)
//   - Any of incZipCodes falls within the tenant's resolved zip set (from ZipCodesFile)
func (t TenantConfig) MatchesIncentive(state, utility, serviceTerritory *string, nationwide *bool, incZipCodes []string) bool {
	if t.LocationFilter.empty() {
		return true
	}
	if nationwide != nil && *nationwide {
		return true
	}
	if state != nil {
		up := strings.ToUpper(strings.TrimSpace(*state))
		for _, s := range t.LocationFilter.States {
			if strings.ToUpper(strings.TrimSpace(s)) == up {
				return true
			}
		}
	}
	if utility != nil && *utility != "" {
		uLow := strings.ToLower(*utility)
		for _, u := range t.LocationFilter.Utilities {
			fLow := strings.ToLower(u)
			if strings.Contains(uLow, fLow) || strings.Contains(fLow, uLow) {
				return true
			}
		}
	}
	if serviceTerritory != nil && *serviceTerritory != "" {
		stLow := strings.ToLower(*serviceTerritory)
		for _, sa := range t.LocationFilter.ServiceAreas {
			saLow := strings.ToLower(sa)
			if strings.Contains(stLow, saLow) || strings.Contains(saLow, stLow) {
				return true
			}
		}
	}
	if len(t.LocationFilter.ZipCodes) > 0 {
		zipSet := make(map[string]bool, len(t.LocationFilter.ZipCodes))
		for _, z := range t.LocationFilter.ZipCodes {
			zipSet[z] = true
		}
		for _, z := range incZipCodes {
			if zipSet[z] {
				return true
			}
		}
	}
	return false
}

// LoadTenants reads TENANTS_FILE and returns only active tenants.
// For each tenant that has a ZipCodesFile, the zip codes are loaded from that
// file (filtered by the tenant's states list if set) and cached for matching.
// Returns an empty slice (not an error) if the file does not exist —
// this enables backward-compatible single-tenant mode.
func LoadTenants(path string) ([]TenantConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load tenants %s: %w", path, err)
	}
	var all []TenantConfig
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("load tenants %s: parse: %w", path, err)
	}

	active := all[:0]
	for _, t := range all {
		if t.Active {
			active = append(active, t)
		}
	}

	return active, nil
}

// ActiveSources returns the union of Sources across all tenants.
// Returns nil if any tenant has an empty Sources list (meaning: run all scrapers).
func ActiveSources(tenants []TenantConfig) []string {
	seen := make(map[string]bool)
	for _, t := range tenants {
		if len(t.Sources) == 0 {
			return nil // at least one tenant wants all scrapers
		}
		for _, s := range t.Sources {
			seen[s] = true
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}
