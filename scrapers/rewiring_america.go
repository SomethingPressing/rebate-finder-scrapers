// Federal (takes about the government), National (takes about the states)
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
//	  "more_info_url":   "https://...",
//	  "items":           ["heat_pump_clothes_dryer"],
//	  "amount":          { "type": "percent", "number": 0.5, "maximum": 840 },
//	  "owner_status":    ["homeowner", "renter"],
//	  "start_date":      "2024-11-26",
//	  "end_date":        "2025-12-31",
//	  "short_description": "..."
//	}

// raCalculatorResponse is the top-level response from the calculator endpoint.
type raCalculatorResponse struct {
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
	Type           string   `json:"type"` // "percent" | "dollar_amount" | "dollars_per_unit"
	Number         float64  `json:"number"`
	Maximum        float64  `json:"maximum"`        // 0 = no cap
	Representative *float64 `json:"representative"` // typical/minimum value when != maximum
}

// raIncentive is one incentive row from the calculator response.
type raIncentive struct {
	PaymentMethods   []string `json:"payment_methods"`
	AuthorityType    string   `json:"authority_type"` // "federal" | "state" | "utility" | ...
	AuthorityKey     string   `json:"authority"`      // key into top-level authorities map
	Program          string   `json:"program"`
	ProgramURL       string   `json:"program_url"`
	MoreInfoURL      string   `json:"more_info_url"`
	Items            []string `json:"items"`
	Amount           raAmount `json:"amount"`
	OwnerStatus      []string `json:"owner_status"`
	StartDate        string   `json:"start_date"`
	EndDate          string   `json:"end_date"`
	ShortDescription string   `json:"short_description"`
}

// raSweepProfile is one (income, owner_status) combination to query per ZIP.
type raSweepProfile struct {
	HouseholdIncome int
	OwnerStatus     string // "homeowner" | "renter"
}

// defaultSweepProfiles queries three profiles per ZIP to capture:
//   - low-income programs (income=30000, homeowner)
//   - general programs    (income=80000, homeowner)
//   - renter programs     (income=80000, renter)
var defaultSweepProfiles = []raSweepProfile{
	{HouseholdIncome: 30000, OwnerStatus: "homeowner"},
	{HouseholdIncome: 80000, OwnerStatus: "homeowner"},
	{HouseholdIncome: 80000, OwnerStatus: "renter"},
}

// ── Representative ZIP codes ──────────────────────────────────────────────────

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
	StateZIPs zipdata.StateZIPs
	// Concurrency controls how many ZIP requests run in parallel.
	// Configured via REWIRING_AMERICA_CONCURRENCY (default 3).
	Concurrency int
	// ZIPs overrides StateZIPs and the built-in list (useful for testing).
	ZIPs []string
	// SweepProfiles is the list of (income, owner_status) pairs to query per ZIP.
	// Defaults to defaultSweepProfiles if empty.
	SweepProfiles []raSweepProfile
	// Limit caps how many ZIPs are queried before stopping. 0 means no limit.
	Limit int
}

// Name implements Scraper.
func (s *RewiringAmericaScraper) Name() string { return "rewiring_america" }

// Scrape implements Scraper.
// All ZIPs from uszips.csv are queried concurrently (REWIRING_AMERICA_CONCURRENCY,
// default 3) to capture every utility-territory program across the US.
// Each ZIP is queried with multiple income/owner_status profiles to capture
// income-restricted and renter-specific programs.
// Scrape implements Scraper (collects all results then returns).
func (s *RewiringAmericaScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	var all []models.Incentive
	err := s.ScrapeStream(ctx, func(batch []models.Incentive) {
		all = append(all, batch...)
	})
	return all, err
}

// ScrapeStream implements StreamScraper — flushes new unique incentives to the
// sink every flushEvery ZIP requests so staging is updated periodically
// throughout the run instead of only at the end.
func (s *RewiringAmericaScraper) ScrapeStream(ctx context.Context, sink func([]models.Incentive)) error {
	if s.APIKey == "" {
		s.Logger.Warn("rewiring_america: REWIRING_AMERICA_API_KEY not set — skipping")
		return nil
	}

	// ZIP selection priority:
	//   1. s.ZIPs      — explicit override (tests / CLI)
	//   2. s.StateZIPs — all US ZIPs from uszips.csv
	//   3. representativeZIPs — built-in fallback
	var zips []string
	switch {
	case len(s.ZIPs) > 0:
		zips = s.ZIPs
	case len(s.StateZIPs) > 0:
		zips = zipdata.Sample(s.StateZIPs, 0) // all ZIPs from the file
	default:
		zips = representativeZIPs
	}

	profiles := s.SweepProfiles
	if len(profiles) == 0 {
		profiles = defaultSweepProfiles
	}

	concurrency := s.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}

	if s.Limit > 0 && len(zips) > s.Limit {
		zips = zips[:s.Limit]
	}

	// Build the full work queue: (zip, profile) pairs.
	type task struct {
		zip     string
		profile raSweepProfile
	}
	var tasks []task
	for _, z := range zips {
		for _, p := range profiles {
			tasks = append(tasks, task{zip: z, profile: p})
		}
	}
	nTask := len(tasks)
	nZip := len(zips)

	s.Logger.Info("rewiring_america scrape starting",
		zap.Int("zip_count", nZip),
		zap.Int("profiles_per_zip", len(profiles)),
		zap.Int("total_requests", nTask),
		zap.Int("concurrency", concurrency),
	)

	bar := NewProgressBar(nTask, "rewiring_america")
	client := s.httpClient()

	type result struct {
		zip        string
		profile    raSweepProfile
		incentives []models.Incentive
		err        error
	}

	taskCh := make(chan task, nTask)
	for _, t := range tasks {
		taskCh <- t
	}
	close(taskCh)

	resultCh := make(chan result, concurrency*2)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range taskCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				resp, err := s.fetchZIP(ctx, client, t.zip, t.profile)
				if err != nil {
					resultCh <- result{zip: t.zip, profile: t.profile, err: err}
					continue
				}
				resultCh <- result{zip: t.zip, profile: t.profile, incentives: s.toIncentives(resp, t.zip)}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results, dedup by deterministic ID, and flush periodically.
	const flushEvery = 500 // flush to staging after every N completed requests

	seen := make(map[string]bool)
	var pending []models.Incentive // new unique items since last flush
	uniqueTotal := 0
	done := 0
	errors := 0

	flush := func() {
		if len(pending) > 0 {
			sink(pending)
			uniqueTotal += len(pending)
			pending = nil
		}
	}

	for r := range resultCh {
		done++
		bar.Add(1) //nolint:errcheck
		if r.err != nil {
			errors++
			s.Logger.Warn("rewiring_america zip error",
				zap.String("zip", r.zip),
				zap.String("owner_status", r.profile.OwnerStatus),
				zap.Int("income", r.profile.HouseholdIncome),
				zap.Int("completed", done),
				zap.Int("total", nTask),
				zap.Error(r.err),
			)
		} else {
			for _, inc := range r.incentives {
				if !seen[inc.ID] {
					seen[inc.ID] = true
					pending = append(pending, inc)
				}
			}
		}

		if done%flushEvery == 0 || done == nTask {
			flush()
			s.Logger.Info("rewiring_america progress",
				zap.Int("completed", done),
				zap.Int("total", nTask),
				zap.Int("unique_incentives", uniqueTotal),
				zap.Int("errors", errors),
			)
		}
	}
	flush() // drain any remaining items
	bar.Finish() //nolint:errcheck

	s.Logger.Info("rewiring_america scrape complete",
		zap.Int("unique_incentives", uniqueTotal),
		zap.Int("zips_queried", nZip),
		zap.Int("total_requests", nTask),
		zap.Int("errors", errors),
	)

	return nil
}

func (s *RewiringAmericaScraper) fetchZIP(
	ctx context.Context,
	client *http.Client,
	zip string,
	profile raSweepProfile,
) (*raCalculatorResponse, error) {
	baseURL := strings.TrimRight(s.BaseURL, "/")
	u := fmt.Sprintf(
		"%s?zip=%s&owner_status=%s&tax_filing=joint&household_income=%d&household_size=4&utility=",
		baseURL, zip, profile.OwnerStatus, profile.HouseholdIncome,
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
// model. Each raIncentive produces exactly one Incentive; the stable ID is
// derived from authority key + program name + first item type so identical
// programs discovered via different ZIPs or profiles collapse to the same record.
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

		// Store the raw item alongside the resolved authority name so the evaluator
		// can reconstruct the full program context during re-extraction.
		type storedRA struct {
			Item          raIncentive `json:"item"`
			AuthorityName string      `json:"authority_name"`
		}
		if raw, err := json.Marshal(storedRA{Item: item, AuthorityName: authorityName}); err == nil {
			inc.RawResponse = string(raw)
			inc.RawContentType = "application/json"
		}

		// ── Program identity ──────────────────────────────────────────────
		inc.ProgramName = fmt.Sprintf("%s — %s", authorityName, item.Program)
		inc.UtilityCompany = authorityName
		inc.Administrator = models.PtrString(authorityName)

		// ── Description ──────────────────────────────────────────────────
		if item.ShortDescription != "" {
			inc.IncentiveDescription = models.PtrString(item.ShortDescription)
		}

		// ── Amount ───────────────────────────────────────────────────────
		// representative (when present) = typical/minimum value; number = maximum.
		switch item.Amount.Type {
		case "percent":
			pct := item.Amount.Number
			if pct > 0 && pct <= 1.0 {
				// API sends 0–1 fraction → convert to 0–100 display value.
				pct *= 100
			}
			inc.PercentValue = models.PtrFloat(pct)
			if item.Amount.Maximum > 0 {
				inc.MaximumAmount = models.PtrFloat(item.Amount.Maximum)
			}
			inc.IncentiveFormat = models.PtrString("percent")
		case "dollar_amount":
			if item.Amount.Representative != nil && *item.Amount.Representative > 0 {
				// representative = typical value, number = cap
				inc.IncentiveAmount = item.Amount.Representative
				inc.MaximumAmount = models.PtrFloat(item.Amount.Number)
			} else {
				inc.IncentiveAmount = models.PtrFloat(item.Amount.Number)
				if item.Amount.Maximum > 0 {
					inc.MaximumAmount = models.PtrFloat(item.Amount.Maximum)
				}
			}
			inc.IncentiveFormat = models.PtrString("dollar_amount")
		case "dollars_per_unit":
			inc.PerUnitAmount = models.PtrFloat(item.Amount.Number)
			// Mirror into incentive_amount so callers get a plain dollar value.
			inc.IncentiveAmount = models.PtrFloat(item.Amount.Number)
			inc.UnitType = models.PtrString("unit")
			inc.IncentiveFormat = models.PtrString("per_unit")
		default:
			if item.Amount.Number > 0 {
				inc.IncentiveAmount = models.PtrFloat(item.Amount.Number)
				inc.IncentiveFormat = models.PtrString("dollar_amount")
			} else {
				inc.IncentiveFormat = models.PtrString("narrative")
			}
		}

		// Refine incentive_format from payment_methods when the amount type alone
		// is ambiguous (tax credits and loans require payment-method context).
		for _, pm := range item.PaymentMethods {
			switch pm {
			case "tax_credit":
				inc.IncentiveFormat = models.PtrString("tax_credit")
			case "loan":
				inc.IncentiveFormat = models.PtrString("financing")
			}
		}

		// ── Dates (normalized to YYYY-MM-DD) ─────────────────────────────
		if item.StartDate != "" {
			inc.StartDate = models.PtrString(normalizeRADate(item.StartDate))
		}
		if item.EndDate != "" {
			inc.EndDate = models.PtrString(normalizeRADate(item.EndDate))
		}

		// ── Currently active ─────────────────────────────────────────────
		// Programs returned by the API are active by definition; mark them
		// "active" unless they have a past end date.
		if raIsCurrentlyActive(inc.StartDate, inc.EndDate) {
			inc.Status = "active"
		}

		// ── URLs ─────────────────────────────────────────────────────────
		if item.ProgramURL != "" {
			inc.ProgramURL = models.PtrString(item.ProgramURL)
			inc.ApplicationURL = models.PtrString(item.ProgramURL)
			inc.SourceURL = models.PtrString(item.ProgramURL)
		} else if item.MoreInfoURL != "" {
			inc.ProgramURL = models.PtrString(item.MoreInfoURL)
			inc.ApplicationURL = models.PtrString(item.MoreInfoURL)
			inc.SourceURL = models.PtrString(item.MoreInfoURL)
		}
		// MoreInfoURL stored as additional context; prefer it as source URL
		// when it differs from ProgramURL (more specific).
		if item.MoreInfoURL != "" && item.MoreInfoURL != item.ProgramURL {
			inc.ApplicationURL = models.PtrString(item.MoreInfoURL)
			inc.SourceURL = models.PtrString(item.MoreInfoURL)
		}

		// ── Application process ───────────────────────────────────────────
		appProcess := raGenerateApplicationProcess(item.PaymentMethods)
		inc.ApplicationProcess = models.PtrString(appProcess)

		// ── Geography ────────────────────────────────────────────────────
		inc.AvailableNationwide = models.PtrBool(item.AuthorityType == "federal")
		inc.ZipCode = models.PtrString(zip)

		// Service territory
		switch item.AuthorityType {
		case "federal":
			inc.ServiceTerritory = models.PtrString("Nationwide")
		case "state":
			inc.ServiceTerritory = models.PtrString(authorityName + " Statewide")
		default:
			inc.ServiceTerritory = models.PtrString(authorityName + " Service Area")
		}

		// ── Implementing sector ───────────────────────────────────────────────
		inc.ImplementingSector = models.PtrString(raAuthorityTypeLabel(item.AuthorityType))

		// ── Portfolio (WHAT the program does — derived from category tags) ────

		// ── Application process from payment methods ──────────────────────
		// (CustomerType is set below from owner_status, not payment methods)

		// ── Items → categories + ProductCategory ─────────────────────────
		if len(item.Items) > 0 {
			seen := make(map[string]bool)
			cats := make([]string, 0, len(item.Items))
			for _, it := range item.Items {
				cat := raProductCategory(it)
				if cat != "" && !seen[cat] {
					seen[cat] = true
					cats = append(cats, cat)
				}
			}
			inc.CategoryTag = cats
			inc.ProductCategory = models.PtrString(raProductCategory(item.Items[0]))
		}

		// ── Owner status → Segment + CustomerType ────────────────────────
		inc.Segment = item.OwnerStatus
		if ct := raOwnerStatusToCustomerType(item.OwnerStatus); ct != "" {
			inc.CustomerType = models.PtrString(ct)
		}

		// ── Program hash ──────────────────────────────────────────────────
		inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)

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

// ── Helper functions ──────────────────────────────────────────────────────────

// normalizeRADate normalizes Rewiring America's flexible date formats to YYYY-MM-DD.
//
//	"2023"       → "2023-01-01"
//	"2024-12"    → "2024-12-01"
//	"2026-06-30" → "2026-06-30"
//	""           → ""
func normalizeRADate(s string) string {
	s = strings.TrimSpace(s)
	switch len(s) {
	case 4: // "2023"
		return s + "-01-01"
	case 7: // "2024-12"
		return s + "-01"
	default:
		return s
	}
}

// raIsCurrentlyActive returns true if the program is currently active based on
// its start and end dates. Programs returned by the Rewiring America API are
// active by definition; this function only returns false for a known past end date.
func raIsCurrentlyActive(startDate, endDate *string) bool {
	now := time.Now()
	if endDate != nil && *endDate != "" {
		t, err := time.Parse("2006-01-02", *endDate)
		if err == nil && t.Before(now) {
			return false // expired
		}
	}
	if startDate != nil && *startDate != "" {
		t, err := time.Parse("2006-01-02", *startDate)
		if err == nil && t.After(now) {
			return false // not yet started
		}
	}
	return true
}

// raGenerateApplicationProcess returns a human-readable application process
// description derived from the payment methods. Priority order:
// pos_rebate > tax_credit > rebate > account_credit > assistance_program > default.
func raGenerateApplicationProcess(methods []string) string {
	set := make(map[string]bool, len(methods))
	for _, m := range methods {
		set[m] = true
	}
	switch {
	case set["pos_rebate"]:
		return "Discount applied at point of sale through participating retailers or contractors."
	case set["tax_credit"]:
		return "Claim when filing federal taxes using the relevant IRS form. Consult a tax professional for guidance."
	case set["rebate"]:
		return "Apply through the program website. Check eligibility requirements before purchasing equipment."
	case set["account_credit"]:
		return "Contact your utility to enroll. Credits will be applied to your account statement."
	case set["assistance_program"]:
		return "Apply through the program website. Income verification may be required."
	default:
		return "Visit the program website for application details and requirements."
	}
}

// raMapPaymentMethods converts Rewiring America payment method API values to a
// comma-separated human-readable string.
//
//	["tax_credit", "pos_rebate"] → "Tax Credit, Point of Sale Rebate"
func raMapPaymentMethods(methods []string) string {
	labels := make([]string, 0, len(methods))
	for _, m := range methods {
		switch m {
		case "tax_credit":
			labels = append(labels, "Tax Credit")
		case "rebate":
			labels = append(labels, "Rebate")
		case "pos_rebate":
			labels = append(labels, "Point of Sale Rebate")
		case "account_credit":
			labels = append(labels, "Account Credit")
		case "assistance_program":
			labels = append(labels, "Assistance Program")
		case "performance_rebate":
			labels = append(labels, "Performance Rebate")
		case "loan":
			labels = append(labels, "Loan")
		default:
			// Unknown payment method — title-case it as a fallback.
			labels = append(labels, raHuman(m))
		}
	}
	return strings.Join(labels, ", ")
}

// raItemHuman converts a Rewiring America item key to a human-readable name.
// Implements the full 17-item readable name table from the SmythOS spec.
func raItemHuman(key string) string {
	switch key {
	case "electric_vehicle_charger":
		return "Electric Vehicle Charger"
	case "heat_pump":
		return "Heat Pump"
	case "heat_pump_air_to_air":
		return "Air-to-Air Heat Pump"
	case "heat_pump_mini_split":
		return "Mini-Split Heat Pump"
	case "heat_pump_water_heater":
		return "Heat Pump Water Heater"
	case "electric_panel":
		return "Electric Panel"
	case "electric_wiring":
		return "Electric Wiring"
	case "weatherization", "insulation_air_sealing":
		return "Weatherization / Insulation"
	case "insulation":
		return "Insulation"
	case "air_sealing":
		return "Air Sealing"
	case "rooftop_solar_panels", "rooftop_solar":
		return "Rooftop Solar"
	case "community_solar":
		return "Community Solar"
	case "battery_storage_installation", "battery_storage":
		return "Energy Storage"
	case "electric_stove":
		return "Electric Stove"
	case "heat_pump_clothes_dryer":
		return "Heat Pump Clothes Dryer"
	case "electric_vehicle":
		return "Electric Vehicle"
	case "ebike":
		return "E-Bike"
	case "geothermal_heating_installation", "geothermal":
		return "Geothermal Heat Pump"
	case "ductless_heat_pump":
		return "Ductless Heat Pump"
	case "central_air_conditioner":
		return "Central Air Conditioner"
	case "efficient_windows_skylights_doors":
		return "Efficient Windows, Skylights & Doors"
	case "non_heat_pump_water_heater":
		return "Water Heater (Non-Heat Pump)"
	case "electric_panel_upgrade":
		return "Electric Panel Upgrade"
	default:
		return raHuman(key)
	}
}

// raAuthorityTypeLabel maps a Rewiring America authority_type to a human-readable
// implementing sector label consistent with DSIRE and the consumer app.
func raAuthorityTypeLabel(authorityType string) string {
	switch authorityType {
	case "federal":
		return "Federal"
	case "state":
		return "State"
	case "utility":
		return "Utility"
	case "city", "county":
		return "Local Government"
	default:
		if authorityType != "" {
			return strings.Title(authorityType) //nolint:staticcheck
		}
		return "Utility"
	}
}

// raOwnerStatusToCustomerType converts RA owner_status values to a normalized
// customer-type string (Residential / Commercial / both).
func raOwnerStatusToCustomerType(statuses []string) string {
	set := make(map[string]bool)
	for _, s := range statuses {
		set[strings.ToLower(s)] = true
	}
	var types []string
	if set["homeowner"] || set["renter"] {
		types = append(types, "Residential")
	}
	if set["business"] || set["commercial_industrial"] || set["commercial"] {
		types = append(types, "Commercial")
	}
	return strings.Join(types, ", ")
}

// raHuman converts snake_case API keys to readable title-case labels.
// Used as a fallback for unknown item/payment keys.
func raHuman(key string) string {
	replacer := strings.NewReplacer("_", " ")
	return strings.Title(replacer.Replace(key)) //nolint:staticcheck
}

// raProductCategory maps a Rewiring America item key to a product category tag.
// Matches the Items Mapping table from the SmythOS Rewiring America LLM prompt.
func raProductCategory(item string) string {
	switch item {
	case "electric_vehicle_charger", "electric_vehicle", "ebike":
		return "Electric Vehicles"
	case "heat_pump", "heat_pump_air_to_air", "heat_pump_mini_split",
		"ductless_heat_pump", "central_air_conditioner",
		"geothermal_heating_installation", "geothermal":
		return "HVAC"
	case "heat_pump_water_heater", "non_heat_pump_water_heater":
		return "Water Heating"
	case "electric_panel", "electric_wiring", "electric_panel_upgrade":
		return "Electrical"
	case "weatherization", "insulation_air_sealing", "insulation",
		"air_sealing", "efficient_windows_skylights_doors":
		return "Weatherization"
	case "rooftop_solar_panels", "rooftop_solar", "community_solar":
		return "Solar"
	case "battery_storage_installation", "battery_storage":
		return "Energy Storage"
	case "electric_stove", "heat_pump_clothes_dryer":
		return "Appliances"
	default:
		return raHuman(item)
	}
}
