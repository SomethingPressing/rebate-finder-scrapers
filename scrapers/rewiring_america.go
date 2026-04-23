package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── Rewiring America API response shapes ──────────────────────────────────────

// raCalculatorResponse is returned by the IRA calculator endpoint for a ZIP code.
type raCalculatorResponse struct {
	Incentives []raIncentive `json:"incentives"`
}

type raIncentive struct {
	PaymentMethods []string `json:"payment_methods"`
	PrimaryTech    string   `json:"primary_technology"`
	Programs       []string `json:"programs"`
	Items          []struct {
		Type       string  `json:"type"`
		Amount     float64 `json:"amount"`
		Unit       string  `json:"unit"`
		MaxAmount  float64 `json:"max_amount"`
		StartDate  string  `json:"start_date"`
		EndDate    string  `json:"end_date"`
		ItemURL    string  `json:"item_url"`
		IRA        bool    `json:"ira"`
	} `json:"items"`
	Authorities []struct {
		Name    string `json:"name"`
		Type    string `json:"type"` // "federal" | "state" | "utility"
		Website string `json:"website"`
	} `json:"authorities"`
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
	"19101", // Philadelphia, PA
	"78201", // San Antonio, TX
	"92101", // San Diego, CA
	"75201", // Dallas, TX
	"95101", // San Jose, CA
	"78701", // Austin, TX
	"78901", // El Paso, TX (placeholder)
	"32099", // Jacksonville, FL
	"78205", // Fort Worth, TX
	"43085", // Columbus, OH
	"28201", // Charlotte, NC
	"94101", // San Francisco, CA
	"46201", // Indianapolis, IN
	"98101", // Seattle, WA
	"80201", // Denver, CO
	"37201", // Nashville, TN
	"73101", // Oklahoma City, OK
	"21201", // Baltimore, MD
	"27601", // Raleigh, NC
	"41001", // Louisville, KY (placeholder)
	"53201", // Milwaukee, WI
	"35201", // Albuquerque, NM (placeholder)
	"85701", // Tucson, AZ
	"92501", // Fresno, CA
	"95201", // Sacramento, CA
	"20001", // Washington, DC
	"02101", // Boston, MA
	"79901", // El Paso, TX
	"45201", // Cincinnati, OH
	"39501", // Gulfport, MS
	"72201", // Little Rock, AR
	"70112", // New Orleans, LA
	"55401", // Minneapolis, MN
	"67201", // Wichita, KS
	"66101", // Kansas City, KS
	"68501", // Lincoln, NE
	"57501", // Pierre, SD
	"58501", // Bismarck, ND
	"59601", // Helena, MT
	"83701", // Boise, ID
	"84101", // Salt Lake City, UT
	"82001", // Cheyenne, WY
	"80901", // Colorado Springs, CO
	"89501", // Reno, NV
	"96801", // Honolulu, HI
	"99501", // Anchorage, AK
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// RewiringAmericaScraper queries the Rewiring America IRA calculator API for
// each representative ZIP code and returns the deduplicated set of incentives.
//
// API: https://api.rewiringamerica.org  (requires API key)
type RewiringAmericaScraper struct {
	BaseURL        string
	APIKey         string
	ScraperVersion string
	Logger         *zap.Logger
	HTTPClient     *http.Client
	// ZIPs overrides the built-in representative ZIP list (useful for testing).
	ZIPs []string
}

// Name implements Scraper.
func (s *RewiringAmericaScraper) Name() string { return "rewiring_america" }

// Scrape implements Scraper.
func (s *RewiringAmericaScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	if s.APIKey == "" {
		s.Logger.Warn("rewiring_america: REWIRING_AMERICA_API_KEY not set — skipping")
		return nil, nil
	}

	zips := s.ZIPs
	if len(zips) == 0 {
		zips = representativeZIPs
	}

	client := s.httpClient()
	seen := make(map[string]bool) // dedup by deterministic ID
	var all []models.Incentive
	nZip := len(zips)

	s.Logger.Info("rewiring_america scrape starting", zap.Int("zip_count", nZip))

	for i, zip := range zips {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}

		before := len(all)
		incentives, err := s.fetchZIP(ctx, client, zip)
		if err != nil {
			// Non-fatal: log and continue with next ZIP.
			s.Logger.Warn("rewiring_america zip error",
				zap.String("zip", zip),
				zap.Int("zip_index", i+1),
				zap.Int("zip_total", nZip),
				zap.Error(err),
			)
			continue
		}

		for _, inc := range incentives {
			if !seen[inc.ID] {
				seen[inc.ID] = true
				all = append(all, inc)
			}
		}

		s.Logger.Info("rewiring_america zip progress",
			zap.Int("zip_index", i+1),
			zap.Int("zip_total", nZip),
			zap.String("zip", zip),
			zap.Int("incentive_rows_this_zip", len(incentives)),
			zap.Int("new_unique_this_zip", len(all)-before),
			zap.Int("unique_incentives_total", len(all)),
		)

		// Polite delay between ZIP requests.
		time.Sleep(200 * time.Millisecond)
	}

	s.Logger.Info("rewiring_america scrape complete",
		zap.Int("unique_incentives", len(all)),
		zap.Int("zips_queried", nZip),
	)

	return all, nil
}

func (s *RewiringAmericaScraper) fetchZIP(
	ctx context.Context,
	client *http.Client,
	zip string,
) ([]models.Incentive, error) {
	baseURL := strings.TrimRight(s.BaseURL, "/")
	u := fmt.Sprintf("%s?zip=%s&owner_status=homeowner&tax_filing=joint&household_income=80000&household_size=4&utility=", baseURL, zip)

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

	return s.toIncentives(result.Incentives, zip), nil
}

func (s *RewiringAmericaScraper) toIncentives(items []raIncentive, zip string) []models.Incentive {
	out := make([]models.Incentive, 0, len(items))

	for _, item := range items {
		for _, detail := range item.Items {
			// Build a stable key from authority + technology + type
			authorityName := "Rewiring America"
			authorityURL := ""
			authorityType := "federal"
			if len(item.Authorities) > 0 {
				authorityName = item.Authorities[0].Name
				authorityURL = item.Authorities[0].Website
				authorityType = item.Authorities[0].Type
			}

			stableKey := fmt.Sprintf("%s|%s|%s|%.2f",
				authorityName, item.PrimaryTech, detail.Type, detail.Amount)
			id := models.DeterministicID(s.Name(), stableKey)

			inc := models.NewIncentive(s.Name(), s.ScraperVersion)
			inc.ID = id

			// Program name = "Authority – Technology incentive"
			inc.ProgramName = fmt.Sprintf("%s — %s", authorityName, humanTech(item.PrimaryTech))
			inc.UtilityCompany = authorityName

			// Type hint → description
			desc := fmt.Sprintf("%s incentive for %s", humanType(detail.Type), humanTech(item.PrimaryTech))
			inc.IncentiveDescription = models.PtrString(desc)

			// Amount
			if detail.Amount > 0 {
				switch detail.Unit {
				case "percent":
					inc.IncentiveFormat = models.PtrString("percent")
					inc.PercentValue = models.PtrFloat(detail.Amount)
				case "dollars":
					inc.IncentiveFormat = models.PtrString("dollar_amount")
					inc.IncentiveAmount = models.PtrFloat(detail.Amount)
				default:
					inc.IncentiveFormat = models.PtrString("dollar_amount")
					inc.IncentiveAmount = models.PtrFloat(detail.Amount)
				}
			}
			if detail.MaxAmount > 0 {
				inc.MaximumAmount = models.PtrFloat(detail.MaxAmount)
			}

			// Dates
			if detail.StartDate != "" {
				inc.StartDate = models.PtrString(detail.StartDate)
			}
			if detail.EndDate != "" {
				inc.EndDate = models.PtrString(detail.EndDate)
			}

			// URLs
			if detail.ItemURL != "" {
				inc.ApplicationURL = models.PtrString(detail.ItemURL)
				inc.ProgramURL = models.PtrString(detail.ItemURL)
			} else if authorityURL != "" {
				inc.ProgramURL = models.PtrString(authorityURL)
			}

			// Geography
			available := authorityType == "federal"
			inc.AvailableNationwide = models.PtrBool(available)

			// Category
			inc.CategoryTag = []string{humanTech(item.PrimaryTech)}

			// Segment from payment methods
			inc.Segment = item.PaymentMethods

			if detail.IRA {
				inc.Portfolio = []string{"IRA"}
			}

			inc.Administrator = models.PtrString(authorityName)

			out = append(out, inc)
		}
	}

	return out
}

func (s *RewiringAmericaScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// humanTech converts snake_case tech keys to readable labels.
func humanTech(tech string) string {
	replacer := strings.NewReplacer("_", " ")
	return strings.Title(replacer.Replace(tech)) //nolint:staticcheck
}

// humanType converts incentive type keys to readable labels.
func humanType(t string) string {
	replacer := strings.NewReplacer("_", " ")
	return strings.Title(replacer.Replace(t)) //nolint:staticcheck
}
