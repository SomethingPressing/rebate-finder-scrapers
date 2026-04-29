package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/incenva/rebate-scraper/internal/zipdata"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── Rewiring America API response shapes ──────────────────────────────────────
//
// Endpoint: GET /api/v1/calculator?zip=<zip>&owner_status=...
//
// Top-level response:
//
//	{
//	  "authorities": { "<key>": { "name": "..." }, ... },
//	  "incentives":  [ raIncentive, ... ]
//	}
//
// Each incentive:
//
//	{
//	  "payment_methods": ["pos_rebate"],
//	  "authority_type":  "state",            // "federal" | "state" | "utility" | "city" | "county"
//	  "authority":       "ny-nyserda",       // key into top-level authorities map
//	  "program":         "Appliance Upgrade Program",
//	  "program_url":     "https://...",
//	  "items":           ["heat_pump_clothes_dryer"],   // product-type strings
//	  "amount":          { "type": "percent", "number": 0.5, "maximum": 840 },
//	  "owner_status":    ["homeowner", "renter"],
//	  "start_date":      "2024-11-26",        // optional
//	  "end_date":        "2025-12-31",        // optional
//	  "short_description": "..."
//	}

// raCalculatorResponse is the top-level response from the calculator endpoint.
type raCalculatorResponse struct {
	// Authorities maps authority key → authority info.
	Authorities map[string]raAuthority `json:"authorities"`
	Incentives  []raIncentive          `json:"incentives"`
}

// raAuthority holds metadata for a rebate-granting authority.
type raAuthority struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// raAmount describes the incentive value.
type raAmount struct {
	Type    string  `json:"type"`    // "percent" | "dollar_amount" | "dollars_per_unit"
	Number  float64 `json:"number"`
	Maximum float64 `json:"maximum"` // 0 = no cap
}

// raIncentive is one incentive row from the calculator response.
type raIncentive struct {
	PaymentMethods   []string `json:"payment_methods"`
	AuthorityType    string   `json:"authority_type"` // "federal" | "state" | "utility" | ...
	AuthorityKey     string   `json:"authority"`      // key into top-level authorities map
	Program          string   `json:"program"`
	ProgramURL       string   `json:"program_url"`
	Items            []string `json:"items"`    // product-type strings, e.g. "heat_pump_water_heater"
	Amount           raAmount `json:"amount"`
	OwnerStatus      []string `json:"owner_status"`
	StartDate        string   `json:"start_date"`
	EndDate          string   `json:"end_date"`
	ShortDescription string   `json:"short_description"`
}

// ── Representative ZIP codes ──────────────────────────────────────────────────
//
// The Rewiring America API is ZIP-based.  To get broad national coverage we
// sample one ZIP per state (state capital or largest city).  A full production
// implementation would iterate every ZIP, but this balanced set avoids rate
// limits while still discovering most unique federal and state programs.

var representativeZIPs = []string{
	"10001", // New York, NY
	"90012", // Los Angeles, CA
	"60601", // Chicago, IL
	"77001", // Houston, TX
	"85001", // Phoenix, AZ
	"19103", // Philadelphia, PA
	"78201", // San Antonio, TX
	"92101", // San Diego, CA
	"75201", // Dallas, TX
	"95101", // San Jose, CA
	"78701", // Austin, TX
	"79901", // El Paso, TX
	"32202", // Jacksonville, FL
	"76102", // Fort Worth, TX
	"43215", // Columbus, OH
	"28202", // Charlotte, NC
	"94102", // San Francisco, CA
	"46204", // Indianapolis, IN
	"98101", // Seattle, WA
	"80202", // Denver, CO
	"37201", // Nashville, TN
	"73102", // Oklahoma City, OK
	"21202", // Baltimore, MD
	"27601", // Raleigh, NC
	"40202", // Louisville, KY
	"53202", // Milwaukee, WI
	"87102", // Albuquerque, NM
	"85701", // Tucson, AZ
	"93721", // Fresno, CA
	"95814", // Sacramento, CA
	"20001", // Washington, DC
	"02108", // Boston, MA
	"45202", // Cincinnati, OH
	"39501", // Gulfport, MS
	"72201", // Little Rock, AR
	"70112", // New Orleans, LA
	"55401", // Minneapolis, MN
	"67202", // Wichita, KS
	"66102", // Kansas City, KS
	"68508", // Lincoln, NE
	"57501", // Pierre, SD
	"58501", // Bismarck, ND
	"59601", // Helena, MT
	"83702", // Boise, ID
	"84101", // Salt Lake City, UT
	"82001", // Cheyenne, WY
	"80903", // Colorado Springs, CO
	"89501", // Reno, NV
	"96813", // Honolulu, HI
	"99501", // Anchorage, AK
	"33101", // Miami, FL
	"30301", // Atlanta, GA
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// RewiringAmericaScraper queries the Rewiring America IRA calculator API for
// every ZIP code in the US and returns the deduplicated set of incentives.
//
// API: https://api.rewiringamerica.org  (requires API key)
type RewiringAmericaScraper struct {
	BaseURL        string
	APIKey         string
	ScraperVersion string
	Logger         *zap.Logger
	HTTPClient     *http.Client
	// StateZIPs is the US ZIP dataset (50 states + DC, no territories).
	// All ZIPs are queried to capture every utility-territory program.
	StateZIPs zipdata.StateZIPs
	// Concurrency controls how many ZIP requests run in parallel.
	// Configured via REWIRING_AMERICA_CONCURRENCY (default 3).
	Concurrency int
	// ZIPs overrides StateZIPs and the built-in list (useful for testing).
	ZIPs []string
}

// Name implements Scraper.
func (s *RewiringAmericaScraper) Name() string { return "rewiring_america" }

// Scrape implements Scraper.
// All ZIPs from uszips.csv are queried concurrently (REWIRING_AMERICA_CONCURRENCY,
// default 3) to capture every utility-territory program across the US.
func (s *RewiringAmericaScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	if s.APIKey == "" {
		s.Logger.Warn("rewiring_america: REWIRING_AMERICA_API_KEY not set — skipping")
		return nil, nil
	}

	// ZIP selection priority:
	//   1. s.ZIPs      — explicit override (tests / CLI)
	//   2. s.StateZIPs — all US ZIPs from uszips.csv (Sample n=0 = no limit)
	//   3. representativeZIPs — built-in fallback if uszips.csv wasn't loaded
	var zips []string
	switch {
	case len(s.ZIPs) > 0:
		zips = s.ZIPs
	case len(s.StateZIPs) > 0:
		zips = zipdata.Sample(s.StateZIPs, 0) // 0 = all ZIPs, no limit
	default:
		zips = representativeZIPs
	}

	concurrency := s.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}

	nZip := len(zips)
	s.Logger.Info("rewiring_america scrape starting",
		zap.Int("zip_count", nZip),
		zap.Int("concurrency", concurrency),
	)

	client := s.httpClient()

	// Worker pool — feed ZIPs through a channel, collect results.
	type result struct {
		zip        string
		incentives []models.Incentive
		err        error
	}

	zipCh := make(chan string, nZip)
	for _, z := range zips {
		zipCh <- z
	}
	close(zipCh)

	resultCh := make(chan result, concurrency*2)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for zip := range zipCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				resp, err := s.fetchZIP(ctx, client, zip)
				if err != nil {
					resultCh <- result{zip: zip, err: err}
					continue
				}
				resultCh <- result{zip: zip, incentives: s.toIncentives(resp, zip)}
			}
		}()
	}

	// Close resultCh once all workers are done.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results, dedup by deterministic ID.
	seen := make(map[string]bool)
	var all []models.Incentive
	done := 0
	errors := 0

	for r := range resultCh {
		done++
		if r.err != nil {
			errors++
			s.Logger.Warn("rewiring_america zip error",
				zap.String("zip", r.zip),
				zap.Int("completed", done),
				zap.Int("total", nZip),
				zap.Error(r.err),
			)
			continue
		}
		newThisZip := 0
		for _, inc := range r.incentives {
			if !seen[inc.ID] {
				seen[inc.ID] = true
				all = append(all, inc)
				newThisZip++
			}
		}
		if done%500 == 0 || done == nZip {
			s.Logger.Info("rewiring_america zip progress",
				zap.Int("completed", done),
				zap.Int("total", nZip),
				zap.Int("unique_incentives_total", len(all)),
				zap.Int("errors", errors),
			)
		}
	}

	s.Logger.Info("rewiring_america scrape complete",
		zap.Int("unique_incentives", len(all)),
		zap.Int("zips_queried", nZip),
		zap.Int("errors", errors),
	)

	return all, nil
}

func (s *RewiringAmericaScraper) fetchZIP(
	ctx context.Context,
	client *http.Client,
	zip string,
) (*raCalculatorResponse, error) {
	baseURL := strings.TrimRight(s.BaseURL, "/")
	u := fmt.Sprintf(
		"%s?zip=%s&owner_status=homeowner&tax_filing=joint&household_income=80000&household_size=4&utility=",
		baseURL, zip,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "IncenvaBot/1.0 (+https://incenva.com/bot)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("rewiring_america: invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rewiring_america: HTTP %d for ZIP %s", resp.StatusCode, zip)
	}

	var result raCalculatorResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("rewiring_america: decode zip %s: %w", zip, err)
	}

	return &result, nil
}

// toIncentives converts a raw calculator response into our canonical Incentive
// model.  Each raIncentive produces exactly one Incentive; the stable ID is
// derived from authority key + program name + first item type so identical
// programs discovered via different ZIPs collapse to the same record.
func (s *RewiringAmericaScraper) toIncentives(result *raCalculatorResponse, zip string) []models.Incentive {
	if result == nil {
		return nil
	}

	out := make([]models.Incentive, 0, len(result.Incentives))

	for _, item := range result.Incentives {
		// Resolve authority name from the top-level map.
		authorityName := item.AuthorityKey
		if auth, ok := result.Authorities[item.AuthorityKey]; ok && auth.Name != "" {
			authorityName = auth.Name
		}

		// Stable dedup key: authority key + program + primary item type.
		// Excludes ZIP so the same program found in multiple ZIPs merges.
		primaryItem := ""
		if len(item.Items) > 0 {
			primaryItem = item.Items[0]
		}
		stableKey := fmt.Sprintf("%s|%s|%s", item.AuthorityKey, item.Program, primaryItem)
		id := models.DeterministicID(s.Name(), stableKey)

		inc := models.NewIncentive(s.Name(), s.ScraperVersion)
		inc.ID = id

		// Program identity
		inc.ProgramName = fmt.Sprintf("%s — %s", authorityName, item.Program)
		inc.UtilityCompany = authorityName
		inc.Administrator = models.PtrString(authorityName)

		// Description
		if item.ShortDescription != "" {
			inc.IncentiveDescription = models.PtrString(item.ShortDescription)
		}

		// Amount
		switch item.Amount.Type {
		case "percent":
			inc.IncentiveFormat = models.PtrString("percent")
			inc.PercentValue = models.PtrFloat(item.Amount.Number * 100) // API sends 0–1
		case "dollar_amount", "dollars_per_unit":
			inc.IncentiveFormat = models.PtrString("dollar_amount")
			inc.IncentiveAmount = models.PtrFloat(item.Amount.Number)
		default:
			if item.Amount.Number > 0 {
				inc.IncentiveFormat = models.PtrString("dollar_amount")
				inc.IncentiveAmount = models.PtrFloat(item.Amount.Number)
			}
		}
		if item.Amount.Maximum > 0 {
			inc.MaximumAmount = models.PtrFloat(item.Amount.Maximum)
		}

		// Dates
		if item.StartDate != "" {
			inc.StartDate = models.PtrString(item.StartDate)
		}
		if item.EndDate != "" {
			inc.EndDate = models.PtrString(item.EndDate)
		}

		// URL
		if item.ProgramURL != "" {
			inc.ProgramURL = models.PtrString(item.ProgramURL)
			inc.ApplicationURL = models.PtrString(item.ProgramURL)
		}

		// Geography — federal incentives are available nationwide
		available := item.AuthorityType == "federal"
		inc.AvailableNationwide = models.PtrBool(available)

		// ZIP — attach the ZIP that discovered this incentive so the promoter
		// can link it to a geography even if it's a state/utility program.
		inc.ZipCode = models.PtrString(zip)

		// Categories from product-type strings
		if len(item.Items) > 0 {
			cats := make([]string, 0, len(item.Items))
			for _, it := range item.Items {
				cats = append(cats, raHuman(it))
			}
			inc.CategoryTag = cats
		}

		// Payment methods → segment
		inc.Segment = item.PaymentMethods

		out = append(out, inc)
	}

	return out
}

func (s *RewiringAmericaScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// raHuman converts snake_case API keys to readable title-case labels.
func raHuman(key string) string {
	replacer := strings.NewReplacer("_", " ")
	return strings.Title(replacer.Replace(key)) //nolint:staticcheck
}
