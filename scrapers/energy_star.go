//Federal.  It's more like a certfication program. this refrigator is energy star efficient

package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// scraperVersion is bumped whenever the mapping logic changes.
const energyStarScraperVersion = "1.0"

// energyStarAPIPath is the fixed path for the rebate search endpoint.
const energyStarAPIPath = "/productfinder/api/imp_rebate_results/search"

// ── Scraper ───────────────────────────────────────────────────────────────────

// EnergyStarScraper queries the Energy Star rebate-finder REST API without any
// geographic filter and paginates through the full result set (~2 900 records).
//
// API: https://www.energystar.gov/productfinder/api/imp_rebate_results/search
//
// No ZIP or state filter is needed — the endpoint returns all active rebates
// nationwide when queried without a zip_code_filter parameter.
type EnergyStarScraper struct {
	// BaseURL is the scheme+host, e.g. "https://www.energystar.gov".
	BaseURL string
	// PageDelay is the sleep duration between successive page requests.
	// Defaults to 500 ms.
	PageDelay time.Duration
	// MaxConcurrency limits how many page-fetch goroutines run in parallel.
	// Defaults to 3.
	MaxConcurrency int
	// ScraperVersion is written to the scraper_version column.
	ScraperVersion string
	// StateZIPs maps state abbreviation → ordered list of ZIP codes.
	// When set, each incentive's ZipCodes field is populated from the program's
	// service territory state so downstream systems can resolve ZIP coverage.
	StateZIPs map[string][]string

	Logger     *zap.Logger
	HTTPClient *http.Client
}

// Name implements Scraper.
func (s *EnergyStarScraper) Name() string { return "energy_star" }

// Scrape implements Scraper.
// It fetches all Energy Star rebates by paginating the full result set without
// any geographic filter.
func (s *EnergyStarScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	s.Logger.Info("energy_star scrape starting — fetching all programs without ZIP filter")

	// ── Phase 1: probe page 0 to get total count ──────────────────────────────
	probe, err := s.fetchPage(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("energy_star: probe page 0: %w", err)
	}

	if probe.ResultsCount == 0 || probe.PageSize == 0 {
		s.Logger.Warn("energy_star: empty result set")
		return nil, nil
	}

	totalPages := int(math.Ceil(float64(probe.ResultsCount) / float64(probe.PageSize)))

	s.Logger.Info("energy_star pages",
		zap.Int("total_results", probe.ResultsCount),
		zap.Int("page_size", probe.PageSize),
		zap.Int("total_pages", totalPages),
	)

	// ── Phase 2: full fetch (bounded concurrency) ─────────────────────────────
	rawPages := make([][]models.EnergyStarRawResult, totalPages)
	rawPages[0] = probe.Results

	bar := NewProgressBar(totalPages, "energy_star")
	bar.Add(1) //nolint:errcheck — page 0 already fetched

	if totalPages > 1 {
		conc := s.maxConcurrency()
		sem := make(chan struct{}, conc)
		g, gctx := errgroup.WithContext(ctx)

		for page := 1; page < totalPages; page++ {
			page := page
			sem <- struct{}{}
			g.Go(func() error {
				defer func() { <-sem }()

				if s.pageDelay() > 0 {
					select {
					case <-gctx.Done():
						return gctx.Err()
					case <-time.After(s.pageDelay()):
					}
				}

				resp, err := s.fetchPage(gctx, page)
				if err != nil {
					return fmt.Errorf("page %d: %w", page, err)
				}
				rawPages[page] = resp.Results
				bar.Add(1) //nolint:errcheck
				s.Logger.Info("energy_star: page fetched",
					zap.Int("page", page+1),
					zap.Int("total_pages", totalPages),
					zap.Int("results_on_page", len(resp.Results)),
				)
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return nil, fmt.Errorf("energy_star: fetch pages: %w", err)
		}
	}
	bar.Finish() //nolint:errcheck

	// ── Phase 3: parse + map ──────────────────────────────────────────────────
	version := s.ScraperVersion
	if version == "" {
		version = energyStarScraperVersion
	}

	seen := make(map[string]bool)
	var incentives []models.Incentive
	for _, page := range rawPages {
		for _, result := range page {
			inc, ok := mapEnergyStarRecord(result, version, s.StateZIPs, s.Logger)
			if !ok {
				continue
			}
			if seen[inc.ID] {
				continue
			}
			seen[inc.ID] = true
			incentives = append(incentives, inc)
		}
	}

	s.Logger.Info("energy_star scrape complete",
		zap.Int("unique_incentives", len(incentives)),
		zap.Int("pages_fetched", totalPages),
		zap.Int("duplicates_skipped", probe.ResultsCount-len(incentives)),
	)

	return incentives, nil
}

// fetchPage calls the Energy Star search API for the given page number.
// No geographic filter is applied — the API returns all active rebates.
func (s *EnergyStarScraper) fetchPage(ctx context.Context, page int) (*models.EnergyStarSearchResponse, error) {
	baseURL := strings.TrimRight(s.BaseURL, "/")

	u, err := url.Parse(baseURL + energyStarAPIPath)
	if err != nil {
		return nil, fmt.Errorf("energy_star: build url: %w", err)
	}

	q := url.Values{}
	q.Set("page_number", strconv.Itoa(page))
	q.Set("sort_by", "utility")
	q.Set("sort_direction", "asc")
	q.Set("scrollTo", "0")
	q.Set("lastpage", "0")
	q.Set("search_text", "")
	q.Set("product_general_isopen", "0")
	u.RawQuery = q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "IncenvaBot/1.0 (+https://incenva.com/bot)")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("energy_star: GET %s: %w", u.String(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("energy_star: HTTP %d page %d", resp.StatusCode, page)
	}

	var result models.EnergyStarSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("energy_star: decode page %d: %w", page, err)
	}

	return &result, nil
}

// httpClient returns the configured client or a default one.
func (s *EnergyStarScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *EnergyStarScraper) pageDelay() time.Duration {
	if s.PageDelay > 0 {
		return s.PageDelay
	}
	return 500 * time.Millisecond
}

func (s *EnergyStarScraper) maxConcurrency() int {
	if s.MaxConcurrency > 0 {
		return s.MaxConcurrency
	}
	return 3
}

// ── Record mapping ────────────────────────────────────────────────────────────

// mapEnergyStarRecord maps a single raw API result + its parsed incentivedata
// blob into a models.Incentive.  Returns (incentive, true) on success or
// (zero, false) if the record should be skipped.
func mapEnergyStarRecord(
	result models.EnergyStarRawResult,
	scraperVersion string,
	stateZIPs map[string][]string,
	log *zap.Logger,
) (models.Incentive, bool) {
	// Parse the nested incentivedata JSON blob.
	var idata models.EnergyStarIncentiveData
	if result.IncentiveData != "" {
		if err := json.Unmarshal([]byte(result.IncentiveData), &idata); err != nil {
			log.Warn("energy_star: failed to parse incentivedata",
				zap.String("incentive_id", result.IncentiveID),
				zap.Error(err),
			)
			// Continue with partial data; don't skip the whole record.
		}
	}

	inc := models.NewIncentive("Energy Star", scraperVersion)

	// ── Deterministic ID ───────────────────────────────────────────────────
	inc.ID = models.DeterministicID("energy_star", result.IncentiveID)

	// ── Geography ──────────────────────────────────────────────────────────
	// No zip_code — queried without a ZIP filter; state comes from service territory.
	if idata.ServiceTerritory != nil {
		if idata.ServiceTerritory.StateCode != "" {
			stateCode := strings.ToUpper(idata.ServiceTerritory.StateCode)
			inc.State = models.PtrString(stateCode)
			// Populate ZipCodes with every ZIP in the program's state.
			if zips := stateZIPs[stateCode]; len(zips) > 0 {
				inc.ZipCodes = zips
			}
		}
		svcName := idata.ServiceTerritory.Name
		if svcName == "" {
			svcName = idata.ServiceTerritory.Desc
		}
		if svcName != "" {
			inc.ServiceTerritory = models.PtrString(svcName)
		}
	}

	availNationwide := strings.EqualFold(result.AvailableNationwide, "yes")
	inc.AvailableNationwide = models.PtrBool(availNationwide)

	// ── Utility / program name ──────────────────────────────────────────────
	utility := result.Utility
	if utility == "" {
		utility = "Energy Star"
	}
	inc.UtilityCompany = utility

	productGeneralName := result.ProductGeneral
	if idata.ProductSubcategory != nil && idata.ProductSubcategory.General != nil {
		if idata.ProductSubcategory.General.Name != "" {
			productGeneralName = idata.ProductSubcategory.General.Name
		}
	}

	inc.ProgramName = fmt.Sprintf("%s %s Rebate", utility, productGeneralName)

	// ── Category / type ─────────────────────────────────────────────────────
	inc.ProductCategory = models.PtrString(result.ProductCategory)

	if idata.IncentiveType != nil {
		if t := idata.IncentiveType.BestName(); t != "" {
			inc.CategoryTag = []string{t}
		}
	}
	if len(inc.CategoryTag) == 0 && result.ProductCategory != "" {
		inc.CategoryTag = []string{result.ProductCategory}
	}

	// ── Customer / market segment ────────────────────────────────────────────
	if idata.IncentiveMarketSector != nil {
		if ms := idata.IncentiveMarketSector.BestName(); ms != "" {
			inc.CustomerType = models.PtrString(ms)
			inc.Segment = []string{ms}
		}
	}

	// ── Building type ────────────────────────────────────────────────────────
	if idata.IncentiveBuildingSector != nil {
		if bs := idata.IncentiveBuildingSector.BestName(); bs != "" {
			// Store building sector via Portfolio field to match schema
			inc.Portfolio = []string{bs}
		}
	}

	// ── Items (product sub-category) ─────────────────────────────────────────
	// UnitType is used to note the applicable product items when not "All".
	if result.Product != "" && !strings.EqualFold(result.Product, "all") {
		inc.UnitType = models.PtrString(result.Product)
	} else if idata.ProductSubcategory != nil {
		override := idata.ProductSubcategory.Override
		if override == "" {
			override = idata.ProductSubcategory.Name
		}
		if override != "" && !strings.EqualFold(override, "all") {
			inc.UnitType = models.PtrString(override)
		}
	}

	// ── Amount ───────────────────────────────────────────────────────────────
	amountStr := result.IncentiveAmount
	if amountStr == "" {
		amountStr = idata.IncentiveAmount
	}
	parseIncentiveAmountInto(&inc, amountStr)

	// ── Incentive description ─────────────────────────────────────────────────
	incentiveTypeName := ""
	if idata.IncentiveType != nil {
		incentiveTypeName = idata.IncentiveType.BestName()
	}
	if incentiveTypeName == "" {
		incentiveTypeName = "Rebate"
	}
	desc := fmt.Sprintf("%s: %s on %s", incentiveTypeName, amountStr, productGeneralName)
	inc.IncentiveDescription = models.PtrString(desc)

	// ── Recipient ────────────────────────────────────────────────────────────
	if idata.IncentiveRecipient != nil {
		if r := idata.IncentiveRecipient.BestName(); r != "" {
			// Map recipient into Administrator as a reasonable field fit.
			inc.Administrator = models.PtrString(r)
		}
	}

	// ── Income qualification ──────────────────────────────────────────────────
	if idata.IncomeQualification != nil {
		applyIncomeQualification(&inc, idata.IncomeQualification.BestName())
	}

	// ── Energy audit ─────────────────────────────────────────────────────────
	auditRequired := strings.EqualFold(idata.EnergyAuditRequired, "y")
	inc.EnergyAuditRequired = models.PtrBool(auditRequired)

	// ── Delivery mechanics → application process ──────────────────────────────
	appProcess, instantRebate := parseDeliveryMechanics(idata.DeliveryMechanics, log)
	if appProcess != "" {
		inc.ApplicationProcess = models.PtrString(appProcess)
	}
	inc.ContractorRequired = models.PtrBool(false)
	if instantRebate {
		// Store InstantRebateAvailable via WhileFundsLast is not ideal; use
		// ApplicationURL hint instead since there is no dedicated bool field.
		// We record it in the description prefix.
		if inc.IncentiveDescription != nil {
			updated := "[Instant Rebate Available] " + *inc.IncentiveDescription
			inc.IncentiveDescription = &updated
		}
	}

	// ── URLs / contact ────────────────────────────────────────────────────────
	if idata.ProgramWebAddress != "" {
		inc.ProgramURL = models.PtrString(idata.ProgramWebAddress)
		inc.ApplicationURL = models.PtrString(idata.ProgramWebAddress)
	}
	if idata.ContactEmail != "" {
		inc.ContactEmail = models.PtrString(idata.ContactEmail)
	}
	if idata.ContactPhone != "" {
		inc.ContactPhone = models.PtrString(idata.ContactPhone)
	}

	// ── Dates ─────────────────────────────────────────────────────────────────
	// Prefer outer result dates; fall back to inner idata dates.
	startMS := string(result.IncentiveStartDate)
	if startMS == "" {
		startMS = string(idata.StartDate)
	}
	endMS := string(result.IncentiveEndDate)
	if endMS == "" {
		endMS = string(idata.EndDate)
	}

	if d := parseUnixMillisToDate(startMS); d != "" {
		inc.StartDate = models.PtrString(d)
	}
	if d := parseUnixMillisToDate(endMS); d != "" {
		inc.EndDate = models.PtrString(d)
	}

	// ── Active status ─────────────────────────────────────────────────────────
	// Stored via the Status field; only override to "active" if confirmed.
	if idata.IncentiveStatus != nil {
		if idata.IncentiveStatus.ActiveStatus != nil {
			if strings.EqualFold(idata.IncentiveStatus.ActiveStatus.BestName(), "active") {
				inc.Status = "active"
			}
		}
	}

	// ── Program hash ──────────────────────────────────────────────────────────
	inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)

	return inc, true
}

// ── Income qualification ──────────────────────────────────────────────────────

// applyIncomeQualification sets income eligibility flags on inc based on the
// income qualification name from the API.
//
//	"General"                → HighIncomeEligible (via CategoryTag hint only — no dedicated bool field)
//	"Low-Income"             → LowIncomeEligible
//	"Moderate-Income"        → ModerateIncomeEligible
//	"Low-to-Moderate Income" → LowIncomeEligible + ModerateIncomeEligible
//
// The models.Incentive struct does not have dedicated HighIncomeEligible /
// LowIncomeEligible / ModerateIncomeEligible fields, so we encode these as
// additional CategoryTag entries.
func applyIncomeQualification(inc *models.Incentive, name string) {
	switch strings.TrimSpace(name) {
	case "General":
		inc.CategoryTag = appendUnique(inc.CategoryTag, "General Income")
	case "Low-Income":
		inc.CategoryTag = appendUnique(inc.CategoryTag, "Low-Income Eligible")
	case "Moderate-Income":
		inc.CategoryTag = appendUnique(inc.CategoryTag, "Moderate-Income Eligible")
	case "Low-to-Moderate Income":
		inc.CategoryTag = appendUnique(inc.CategoryTag, "Low-Income Eligible")
		inc.CategoryTag = appendUnique(inc.CategoryTag, "Moderate-Income Eligible")
	}
}

func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

// ── Delivery mechanics ────────────────────────────────────────────────────────

// parseDeliveryMechanics parses the incentivedeliverymechanics JSON array and
// returns a human-readable application process description and whether an
// instant rebate is available.
func parseDeliveryMechanics(raw json.RawMessage, log *zap.Logger) (appProcess string, instantRebate bool) {
	if len(raw) == 0 {
		return defaultAppProcess(), false
	}

	// The field can be either an array of objects or a single object — try array first.
	var mechanics []models.ESTDeliveryMechanic
	if err := json.Unmarshal(raw, &mechanics); err != nil {
		// Try single object.
		var single models.ESTDeliveryMechanic
		if err2 := json.Unmarshal(raw, &single); err2 != nil {
			log.Debug("energy_star: could not parse delivery mechanics", zap.Error(err))
			return defaultAppProcess(), false
		}
		mechanics = []models.ESTDeliveryMechanic{single}
	}

	if len(mechanics) == 0 {
		return defaultAppProcess(), false
	}

	mechanicName := strings.ToLower(mechanics[0].Name)

	if strings.Contains(mechanicName, "retailer") || strings.Contains(mechanicName, "instant") {
		return "Purchase through participating retailers to receive instant rebate at point of sale. Visit program website for list of participating retailers.", true
	}
	if strings.Contains(mechanicName, "rebate application") {
		return "Submit rebate application after purchase. Visit program website for application form and requirements.", false
	}
	return defaultAppProcess(), false
}

func defaultAppProcess() string {
	return "Visit the program website for application details and requirements."
}

// ── Amount parsing ────────────────────────────────────────────────────────────

var (
	esReRange   = regexp.MustCompile(`\$\s*([0-9,]+(?:\.[0-9]+)?)\s*-\s*\$\s*([0-9,]+(?:\.[0-9]+)?)`)
	esReUpTo    = regexp.MustCompile(`(?i)up\s+to\s+\$\s*([0-9,]+(?:\.[0-9]+)?)`)
	esReDollar  = regexp.MustCompile(`\$\s*([0-9,]+(?:\.[0-9]+)?)`)
	esRePercent = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*%`)
	esRePlain   = regexp.MustCompile(`^([0-9,]+(?:\.[0-9]+)?)$`)
)

// parseIncentiveAmountInto parses the incentiveamount string and populates the
// relevant amount fields on inc.
//
//   - "1500" or "$1500"  → dollar_amount, IncentiveAmount=1500
//   - "$100 - $500"      → dollar_amount, IncentiveAmount=100, MaximumAmount=500
//   - "Up to $1000"      → dollar_amount, MaximumAmount=1000
//   - "30%"              → percent, PercentValue=30
//   - "Varies" / ""      → narrative
func parseIncentiveAmountInto(inc *models.Incentive, s string) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "varies") || strings.EqualFold(s, "n/a") {
		inc.IncentiveFormat = models.PtrString("narrative")
		return
	}

	// Range: "$100 - $500"
	if m := esReRange.FindStringSubmatch(s); len(m) == 3 {
		lo := parseCommaFloat(m[1])
		hi := parseCommaFloat(m[2])
		inc.IncentiveFormat = models.PtrString("dollar_amount")
		if lo != 0 {
			inc.IncentiveAmount = models.PtrFloat(lo)
		}
		if hi != 0 {
			inc.MaximumAmount = models.PtrFloat(hi)
		}
		return
	}

	// Up to $X
	if m := esReUpTo.FindStringSubmatch(s); len(m) == 2 {
		if f := parseCommaFloat(m[1]); f != 0 {
			inc.IncentiveFormat = models.PtrString("dollar_amount")
			inc.MaximumAmount = models.PtrFloat(f)
		}
		return
	}

	// Percent
	if m := esRePercent.FindStringSubmatch(s); len(m) == 2 {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil && f != 0 {
			inc.IncentiveFormat = models.PtrString("percent")
			inc.PercentValue = models.PtrFloat(f)
		}
		return
	}

	// Dollar with $ sign
	if m := esReDollar.FindStringSubmatch(s); len(m) == 2 {
		if f := parseCommaFloat(m[1]); f != 0 {
			inc.IncentiveFormat = models.PtrString("dollar_amount")
			inc.IncentiveAmount = models.PtrFloat(f)
		}
		return
	}

	// Plain numeric string without $ sign (e.g. "1500")
	if m := esRePlain.FindStringSubmatch(s); len(m) == 2 {
		if f := parseCommaFloat(m[1]); f != 0 {
			inc.IncentiveFormat = models.PtrString("dollar_amount")
			inc.IncentiveAmount = models.PtrFloat(f)
		}
		return
	}

	// Fallback: narrative
	inc.IncentiveFormat = models.PtrString("narrative")
}

// ── Timestamp helper ──────────────────────────────────────────────────────────

// parseUnixMillisToDate converts a Unix millisecond timestamp string to
// "YYYY-MM-DD".  Returns "" on any parse failure.
func parseUnixMillisToDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return ""
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return ""
	}
	if ms <= 0 {
		return ""
	}
	t := time.UnixMilli(ms).UTC()
	return t.Format("2006-01-02")
}
