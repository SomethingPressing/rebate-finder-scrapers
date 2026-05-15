// State, Utility, Local, Federal (Means all of the US)
package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── DSIRE v1 API response shapes ──────────────────────────────────────────────
//
// Endpoint: GET https://programs.dsireusa.org/api/v1/programs
//           ?zipcode[]={zip}&category[]=1&draw=1&start=0&length=-1
//
// Response:
//
//	{
//	  "data": [ ...dsireProgram... ],
//	  "recordsTotal":    N,
//	  "recordsFiltered": N
//	}

type dsireV1Response struct {
	Data            []dsireProgram `json:"data"`
	RecordsTotal    int            `json:"recordsTotal"`
	RecordsFiltered int            `json:"recordsFiltered"`
}

type dsireProgram struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	WebsiteURL    string `json:"websiteUrl"`
	Summary       string `json:"summary"` // HTML — must be stripped
	Administrator string `json:"administrator"`
	FundingSource string `json:"fundingSource"`
	Budget        string `json:"budget"`
	StartDate     string `json:"startDate"`
	EndDate       string `json:"endDate"`
	UpdatedTs     string `json:"updatedTs"` // "MM/DD/YYYY"
	Published     string `json:"published"` // "Yes" / "No"
	EntireState   bool   `json:"entireState"`

	StateObj struct {
		Abbreviation string `json:"abbreviation"`
		Name         string `json:"name"`
	} `json:"stateObj"`

	TypeObj struct {
		Name string `json:"name"` // "Rebate Program", "Tax Credit", etc.
	} `json:"typeObj"`

	CategoryObj struct {
		Name string `json:"name"` // "Financial Incentive"
	} `json:"categoryObj"`

	SectorObj struct {
		Name string `json:"name"` // "Utility", "State", "Local Government"
	} `json:"sectorObj"`

	// ParameterSets holds the incentive tiers/amounts + eligible technologies.
	ParameterSets []dsireParameterSet `json:"parameterSets"`
}

type dsireParameterSet struct {
	Parameters   []dsireParameter `json:"parameters"`
	Technologies []struct {
		Name     string `json:"name"`
		Category string `json:"category"`
	} `json:"technologies"`
	Sectors []struct {
		Name string `json:"name"`
	} `json:"sectors"`
}

type dsireParameter struct {
	Source    string `json:"source"`    // "Incentive"
	Qualifier string `json:"qualifier"` // "min" | "max" | ""
	Amount    string `json:"amount"`    // decimal string e.g. "110.0000"
	Units     string `json:"units"`     // "$" | "%" | "$/kW" | "$/kWh" | "$/Unit" | "$/Port"
}

// ── Detail page scraper ───────────────────────────────────────────────────────

// dsireDetail holds data extracted from the DSIRE program detail HTML page.
type dsireDetail struct {
	ContactPhone string
	ContactEmail string
	Requirements string
	ProcessNotes string
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// usStateAbbrs is the canonical list of 50 US states + DC used for per-state
// DSIRE queries.  Ordered alphabetically so log output is predictable.
var usStateAbbrs = []string{
	"AL", "AK", "AZ", "AR", "CA", "CO", "CT", "DE", "DC", "FL",
	"GA", "HI", "ID", "IL", "IN", "IA", "KS", "KY", "LA", "ME",
	"MD", "MA", "MI", "MN", "MS", "MO", "MT", "NE", "NV", "NH",
	"NJ", "NM", "NY", "NC", "ND", "OH", "OK", "OR", "PA", "RI",
	"SC", "SD", "TN", "TX", "UT", "VT", "VA", "WA", "WV", "WI", "WY",
}

// DSIREScraper fetches incentive programs from the DSIRE USA v1 API,
// issuing one request per US state + DC:
//
//	GET https://programs.dsireusa.org/api/v1/programs
//	    ?state[]={abbr}&category[]=1&draw=1&start=0&length=-1
//
// Programs are deduplicated by DSIRE ID across states so that federal and
// multi-state programs are not stored twice.
//
// When ScrapeDetails is true (default false), each program's detail page is
// also scraped via Colly to extract contact info, equipment requirements, etc.
type DSIREScraper struct {
	CollyBase
	BaseURL        string
	ScraperVersion string
	Logger         *zap.Logger
	HTTPClient     *http.Client
	// ScrapeDetails enables per-program detail page scraping via Colly.
	// Richer data but significantly slower (one HTTP request per program).
	ScrapeDetails bool
	// PageDelay is the polite delay between per-state requests. Defaults to 300 ms.
	PageDelay time.Duration
	// StateZIPs maps state abbreviation → ordered list of ZIP codes.
	// When set, each incentive's ZipCodes field is populated with the ZIPs for
	// its state so downstream systems can associate the program with specific areas.
	// Load from data/uszips.csv via the zipdata package.
	StateZIPs map[string][]string
	// Limit caps how many unique programs are collected before stopping the
	// state loop early. 0 means no limit (collect all states).
	Limit int
}

// Name implements Scraper.
func (s *DSIREScraper) Name() string { return "dsireusa" }

// Scrape implements Scraper.
func (s *DSIREScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	client := s.httpClient()
	seen := make(map[int]bool)
	var all []models.Incentive
	total := len(usStateAbbrs)

	s.Logger.Info("dsireusa scrape starting",
		zap.Int("states", total),
		zap.Bool("scrape_details", s.ScrapeDetails),
	)

	bar := NewProgressBar(total, "dsireusa")
	for i, state := range usStateAbbrs {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}

		bar.Describe(padDescription("dsireusa [" + state + "]"))

		programs, err := s.fetchState(ctx, client, state)
		if err != nil {
			s.Logger.Warn("dsireusa state error",
				zap.String("state", state),
				zap.Error(err),
			)
			bar.Add(1) //nolint:errcheck
			continue
		}

		before := len(all)
		for _, prog := range programs {
			if seen[prog.ID] {
				continue
			}
			seen[prog.ID] = true

			stateZIPs := s.StateZIPs[state]
			inc := s.toIncentive(prog, stateZIPs)
			if s.ScrapeDetails {
				detail := s.scrapeDetail(ctx, prog.ID)
				s.applyDetail(&inc, detail)
			}
			all = append(all, inc)
		}

		s.Logger.Info("dsireusa state progress",
			zap.Int("state_index", i+1),
			zap.Int("state_total", total),
			zap.String("state", state),
			zap.Int("programs_in_response", len(programs)),
			zap.Int("new_this_state", len(all)-before),
			zap.Int("unique_total", len(all)),
		)

		bar.Add(1) //nolint:errcheck

		if s.Limit > 0 && len(all) >= s.Limit {
			s.Logger.Debug("dsireusa: fetch limit reached, stopping early",
				zap.Int("limit", s.Limit),
				zap.Int("collected", len(all)),
				zap.String("stopped_at_state", state),
			)
			break
		}

		time.Sleep(s.pageDelay())
	}
	bar.Finish() //nolint:errcheck

	s.Logger.Info("dsireusa scrape complete",
		zap.Int("unique_programs", len(all)),
		zap.Int("states_queried", total),
	)

	return all, nil
}

func (s *DSIREScraper) pageDelay() time.Duration {
	if s.PageDelay > 0 {
		return s.PageDelay
	}
	return 300 * time.Millisecond
}

// fetchState calls the v1 API for a single state abbreviation and returns all
// matching programs.  length=-1 returns the full result set in one response.
func (s *DSIREScraper) fetchState(
	ctx context.Context,
	client *http.Client,
	state string,
) ([]dsireProgram, error) {
	baseURL := strings.TrimRight(s.BaseURL, "/")

	u := fmt.Sprintf("%s?state[]=%s&category[]=1&draw=1&start=0&length=-1", baseURL, state)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://programs.dsireusa.org/")
	req.Header.Set("Origin", "https://programs.dsireusa.org")
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (compatible; IncenvaBot/1.0; +https://incenva.com/bot)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for state %s", resp.StatusCode, state)
	}

	var page dsireV1Response
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode state %s: %w", state, err)
	}

	return page.Data, nil
}

// toIncentive maps a dsireProgram → models.Incentive.
// stateZIPs is the full list of ZIP codes for the program's state — stored in
// ZipCodes so downstream systems can associate the incentive with specific areas.
func (s *DSIREScraper) toIncentive(p dsireProgram, stateZIPs []string) models.Incentive {
	inc := models.NewIncentive(s.Name(), s.ScraperVersion)
	inc.ID = models.DeterministicID(s.Name(), strconv.Itoa(p.ID))

	if raw, err := json.Marshal(p); err == nil {
		inc.RawResponse = string(raw)
		inc.RawContentType = "application/json"
	}

	// ── Core ──────────────────────────────────────────────────────────────────
	inc.ProgramName = p.Name

	// UtilityCompany: prefer administrator, fall back to sector-derived label.
	switch {
	case p.Administrator != "":
		inc.UtilityCompany = p.Administrator
	case p.SectorObj.Name == "State":
		inc.UtilityCompany = p.StateObj.Name + " State Programs"
	case p.SectorObj.Name == "Local Government":
		inc.UtilityCompany = p.StateObj.Name + " Local Government"
	default:
		inc.UtilityCompany = extractUtilityFromName(p.Name, p.StateObj.Name)
	}

	if p.Administrator != "" {
		inc.Administrator = models.PtrString(p.Administrator)
	}

	// ── Geography ─────────────────────────────────────────────────────────────
	if p.StateObj.Abbreviation != "" {
		inc.State = models.PtrString(p.StateObj.Abbreviation)
	}
	// ZipCodes: full list of ZIPs for this program's state (from uszips.csv).
	if len(stateZIPs) > 0 {
		inc.ZipCodes = stateZIPs
	}

	// service_territory
	territory := serviceTerritory(p)
	if territory != "" {
		inc.ServiceTerritory = models.PtrString(territory)
	}

	nationwide := p.StateObj.Abbreviation == "" || p.EntireState && p.SectorObj.Name == "Federal"
	inc.AvailableNationwide = models.PtrBool(nationwide)

	// ── Dates ─────────────────────────────────────────────────────────────────
	if p.StartDate != "" {
		inc.StartDate = models.PtrString(parseDate(p.StartDate))
	}
	if p.EndDate != "" {
		inc.EndDate = models.PtrString(parseDate(p.EndDate))
	}

	// ── Description ───────────────────────────────────────────────────────────
	// Convert the DSIRE HTML summary to Markdown so lists, bold text, and links
	// are preserved instead of being collapsed to a flat paragraph.
	if p.Summary != "" {
		if desc := HTMLToMarkdown(p.Summary); desc != "" {
			inc.IncentiveDescription = models.PtrString(desc)
		}
	}

	// ── URLs ──────────────────────────────────────────────────────────────────
	if p.WebsiteURL != "" {
		inc.ProgramURL = models.PtrString(p.WebsiteURL)
		inc.ApplicationURL = models.PtrString(p.WebsiteURL)
	}
	// SourceURL: the DSIRE program detail page where this data was sourced from.
	inc.SourceURL = models.PtrString(fmt.Sprintf("https://programs.dsireusa.org/system/program/detail/%d", p.ID))

	// ── Amount / format from parameterSets ────────────────────────────────────
	format, incAmt, maxAmt, pctVal, perUnit, unitType := parseParameterSets(p.ParameterSets)
	// When parameters are ambiguous (narrative or dollar_amount), let the program
	// type narrow the format. For financing programs the dollar amount is a loan
	// ceiling — move it to maximum_amount so it isn't mistaken for a rebate.
	if typeFormat := formatFromProgramType(p.TypeObj.Name); typeFormat != "" &&
		(format == "narrative" || format == "dollar_amount") {
		format = typeFormat
		if format == "financing" && incAmt != nil {
			if maxAmt == nil {
				maxAmt = incAmt
			}
			incAmt = nil
		}
	}
	inc.IncentiveFormat = models.PtrString(format)
	inc.IncentiveAmount = incAmt
	inc.MaximumAmount = maxAmt
	inc.PercentValue = pctVal
	inc.PerUnitAmount = perUnit
	inc.UnitType = unitType

	// Flat $/unit or $/port amounts represent a single-installation rebate value —
	// mirror into incentive_amount so callers don't have to special-case per_unit.
	if inc.IncentiveAmount == nil && inc.PerUnitAmount != nil &&
		inc.UnitType != nil && (*inc.UnitType == "unit" || *inc.UnitType == "port") {
		v := *inc.PerUnitAmount
		inc.IncentiveAmount = &v
	}

	// ── Category tags from technologies ───────────────────────────────────────
	inc.CategoryTag = extractTechnologies(p.ParameterSets)

	// ── Segment / customer type from sectors ──────────────────────────────────
	inc.Segment = extractSectors(p.ParameterSets)
	if ct := joinSectors(p.ParameterSets); ct != "" {
		inc.CustomerType = models.PtrString(ct)
	}

	// ── Portfolio (program level) ─────────────────────────────────────────────
	if pl := programLevel(p.SectorObj.Name); pl != "" {
		inc.Portfolio = []string{pl}
	}

	// ── Product category ─────────────────────────────────────────────────────
	if pc := topTechCategory(p.ParameterSets); pc != "" {
		inc.ProductCategory = models.PtrString(pc)
	}

	// ── Percent value from description when not set by parameters ────────────
	// Some DSIRE programs describe a percentage rebate in prose but have no %
	// parameter in their ParameterSets. Extract the first percentage mentioned.
	if inc.PercentValue == nil && inc.IncentiveDescription != nil {
		if pct := extractPercentFromText(*inc.IncentiveDescription); pct > 0 {
			inc.PercentValue = models.PtrFloat(pct)
			if inc.IncentiveFormat != nil && *inc.IncentiveFormat == "narrative" {
				inc.IncentiveFormat = models.PtrString("percent")
			}
		}
	}

	// ── Requirements derived from description ────────────────────────────────
	// Scan the summary text for keywords so these fields are populated even when
	// detail-page scraping (ScrapeDetails) is disabled.
	if inc.IncentiveDescription != nil {
		descLow := strings.ToLower(*inc.IncentiveDescription)
		if strings.Contains(descLow, "energy audit") {
			inc.EnergyAuditRequired = models.PtrBool(true)
		}
		if strings.Contains(descLow, "certified contractor") ||
			strings.Contains(descLow, "licensed contractor") ||
			strings.Contains(descLow, "qualified contractor") ||
			strings.Contains(descLow, "certified installer") ||
			strings.Contains(descLow, "licensed installer") {
			inc.ContractorRequired = models.PtrBool(true)
		}
	}

	// ── Active status (schema: currently_active = published == "Yes") ────────
	// Map Published → Status.  Only "Yes" is treated as active; anything else
	// (including "") leaves the default "draft".
	if strings.EqualFold(p.Published, "yes") {
		inc.Status = "active"
	}

	// ── Program hash ──────────────────────────────────────────────────────────
	inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)

	return inc
}

// scrapeDetail fetches the DSIRE program detail page and extracts supplementary
// fields that are not available in the API response.
func (s *DSIREScraper) scrapeDetail(ctx context.Context, id int) dsireDetail {
	var detail dsireDetail
	detailURL := fmt.Sprintf(
		"http://programs.dsireusa.org/system/program/detail/%d", id)

	c := s.NewCollector()

	c.OnHTML(".field-name-field-contact-email a", func(e *colly.HTMLElement) {
		if detail.ContactEmail == "" {
			detail.ContactEmail = strings.TrimSpace(e.Text)
		}
	})

	c.OnHTML(".field-name-field-contact-phone", func(e *colly.HTMLElement) {
		if detail.ContactPhone == "" {
			detail.ContactPhone = strings.TrimSpace(e.ChildText(".field-item"))
		}
	})

	c.OnHTML(".field-name-field-equipment-requirements", func(e *colly.HTMLElement) {
		if detail.Requirements == "" {
			detail.Requirements = strings.TrimSpace(e.ChildText(".field-item"))
		}
	})

	c.OnHTML(".field-name-field-installation-requirements", func(e *colly.HTMLElement) {
		if detail.ProcessNotes == "" {
			detail.ProcessNotes = strings.TrimSpace(e.ChildText(".field-item"))
		}
	})

	// Run with a per-request timeout (don't let one bad page stall everything).
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	_ = reqCtx // colly uses its own context internally via c.SetRequestTimeout
	c.SetRequestTimeout(15 * time.Second)

	if err := c.Visit(detailURL); err != nil {
		s.Logger.Debug("dsireusa detail scrape failed",
			zap.Int("program_id", id),
			zap.Error(err),
		)
	}
	c.Wait()
	return detail
}

// applyDetail merges detail-page data into an already-built Incentive.
func (s *DSIREScraper) applyDetail(inc *models.Incentive, d dsireDetail) {
	if d.ContactEmail != "" {
		inc.ContactEmail = models.PtrString(d.ContactEmail)
	}
	if d.ContactPhone != "" {
		inc.ContactPhone = models.PtrString(d.ContactPhone)
	}
	if d.Requirements != "" {
		reqLow := strings.ToLower(d.Requirements)
		if strings.Contains(reqLow, "energy audit") {
			inc.EnergyAuditRequired = models.PtrBool(true)
		}
		if strings.Contains(reqLow, "contractor") || strings.Contains(reqLow, "certified installer") {
			inc.ContractorRequired = models.PtrBool(true)
		}
		if inc.IncentiveDescription != nil {
			merged := *inc.IncentiveDescription + " Requirements: " + d.Requirements
			inc.IncentiveDescription = models.PtrString(merged)
		}
	}
}

// ── Amount / format parsing ───────────────────────────────────────────────────

// parseParameterSets inspects all parameterSets and returns the best
// (incentive_format, amount fields) representation.
//
// Priority: per_unit > percent > dollar_amount > narrative
func parseParameterSets(sets []dsireParameterSet) (
	format string,
	incAmt, maxAmt, pctVal, perUnit *float64,
	unitType *string,
) {
	format = "narrative"

	for _, ps := range sets {
		for _, p := range ps.Parameters {
			if p.Source != "Incentive" {
				continue
			}
			amt, err := strconv.ParseFloat(strings.TrimSpace(p.Amount), 64)
			if err != nil || amt == 0 {
				continue
			}

			units := strings.ToLower(strings.TrimSpace(p.Units))

			switch {
			case units == "$/kw" || units == "$/kilowatt":
				format = "per_unit"
				perUnit = models.PtrFloat(amt)
				ut := "kilowatt"
				unitType = &ut

			case units == "$/kwh":
				format = "per_unit"
				perUnit = models.PtrFloat(amt)
				ut := "kwh"
				unitType = &ut

			case units == "$/w" || units == "$/watt":
				format = "per_unit"
				perUnit = models.PtrFloat(amt)
				ut := "watt"
				unitType = &ut

			case units == "$/unit":
				format = "per_unit"
				perUnit = models.PtrFloat(amt)
				ut := "unit"
				unitType = &ut

			case units == "$/port":
				format = "per_unit"
				perUnit = models.PtrFloat(amt)
				ut := "port"
				unitType = &ut

			case units == "%":
				if format != "per_unit" {
					format = "percent"
					pctVal = models.PtrFloat(amt)
				}

			case units == "$":
				if format == "narrative" {
					format = "dollar_amount"
				}
				switch strings.ToLower(p.Qualifier) {
				case "max":
					maxAmt = models.PtrFloat(amt)
				default: // "min" or ""
					if incAmt == nil {
						incAmt = models.PtrFloat(amt)
					}
				}
			}
		}
	}

	return
}

// ── Category / sector helpers ─────────────────────────────────────────────────

func extractTechnologies(sets []dsireParameterSet) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(label string) {
		if label != "" && !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	for _, ps := range sets {
		for _, t := range ps.Technologies {
			// Always try normalizing the technology name — it often carries more
			// semantic detail than the category (e.g. "Solar Water Heat" → "Water Heating"
			// even though its category is "Solar Technologies" → "Solar").
			nameLabel := techNameLabel(t.Name)
			add(nameLabel)

			// Also add the category-derived label when it differs from the name label
			// so we capture both dimensions (e.g. Solar + Water Heating).
			if t.Category != "" {
				catLabel := techCategoryLabel(t.Category)
				add(catLabel)
			}
		}
	}
	return out
}

func extractSectors(sets []dsireParameterSet) []string {
	seen := make(map[string]bool)
	var out []string
	for _, ps := range sets {
		for _, sc := range ps.Sectors {
			if sc.Name != "" && !seen[sc.Name] {
				seen[sc.Name] = true
				out = append(out, sc.Name)
			}
		}
	}
	return out
}

func joinSectors(sets []dsireParameterSet) string {
	secs := extractSectors(sets)
	return strings.Join(secs, ", ")
}

func topTechCategory(sets []dsireParameterSet) string {
	counts := make(map[string]int)
	for _, ps := range sets {
		for _, t := range ps.Technologies {
			if t.Category != "" {
				counts[techCategoryLabel(t.Category)]++
			}
		}
	}
	top, topN := "", 0
	for k, n := range counts {
		if n > topN {
			top, topN = k, n
		}
	}
	return top
}

func techCategoryLabel(cat string) string {
	switch strings.ToLower(cat) {
	case "lighting":
		return "Lighting"
	case "hvac":
		return "HVAC"
	case "building envelope":
		return "Weatherization"
	case "appliances":
		return "Appliances"
	case "solar technologies":
		return "Solar"
	case "wind":
		return "Wind"
	case "geothermal technologies":
		return "Geothermal"
	case "industrial equipment", "processing and manufacturing equipment":
		return "Industrial Equipment"
	case "electric vehicles":
		return "Electric Vehicles"
	case "water heating":
		return "Water Heating"
	case "energy storage":
		return "Energy Storage"
	case "renewables", "renewable energy":
		return "Renewable Energy"
	case "alternative fuel vehicles":
		return "Alternative Fuel Vehicles"
	case "combined heat & power", "combined heat and power", "chp":
		return "Combined Heat & Power"
	case "biomass":
		return "Biomass"
	case "fuel cells":
		return "Fuel Cells"
	case "comprehensive measures/whole building", "whole building":
		return "Whole Building"
	case "other ee", "other":
		return "Other"
	default:
		// Unknown DSIRE category — drop rather than pass through raw strings.
		return ""
	}
}

// techNameLabel maps a raw DSIRE technology name to a normalized category label.
// Returns "" for names that don't match a known pattern so they are silently
// dropped rather than polluting the categories list with raw DSIRE strings.
func techNameLabel(name string) string {
	low := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(low, "solar photovoltaic") || strings.Contains(low, "solar pv"):
		return "Solar"
	case strings.Contains(low, "solar water heat") || strings.Contains(low, "solar thermal"):
		return "Water Heating"
	case strings.Contains(low, "solar"):
		return "Solar"
	case strings.Contains(low, "wind"):
		return "Wind"
	case strings.Contains(low, "geothermal"):
		return "Geothermal"
	case strings.Contains(low, "heat pump"):
		return "HVAC"
	case strings.Contains(low, "air condition") || strings.Contains(low, "furnace") ||
		strings.Contains(low, "boiler") || strings.Contains(low, "hvac"):
		return "HVAC"
	case strings.Contains(low, "water heat") || strings.Contains(low, "water heater") ||
		strings.Contains(low, "tankless"):
		return "Water Heating"
	case strings.Contains(low, "insulation") || strings.Contains(low, "weatheri") ||
		strings.Contains(low, "caulk") || strings.Contains(low, "air seal") ||
		strings.Contains(low, "duct seal") || strings.Contains(low, "window"):
		return "Weatherization"
	case strings.Contains(low, "led") || strings.Contains(low, "lighting"):
		return "Lighting"
	case strings.Contains(low, "electric vehicle") || strings.Contains(low, "ev charger") ||
		strings.Contains(low, "evse"):
		return "Electric Vehicles"
	case strings.Contains(low, "battery") || strings.Contains(low, "energy storage"):
		return "Energy Storage"
	case strings.Contains(low, "refrigerat") || strings.Contains(low, "appliance"):
		return "Appliances"
	case strings.Contains(low, "biomass"):
		return "Biomass"
	case strings.Contains(low, "fuel cell"):
		return "Fuel Cells"
	case strings.Contains(low, "combined heat"):
		return "Combined Heat & Power"
	case strings.Contains(low, "whole building") || strings.Contains(low, "comprehensive"):
		return "Whole Building"
	default:
		// Unknown technology name — drop rather than pass through raw DSIRE strings.
		return ""
	}
}

// ── Geography helpers ─────────────────────────────────────────────────────────

func serviceTerritory(p dsireProgram) string {
	switch {
	case p.SectorObj.Name == "State":
		return p.StateObj.Name + " Statewide"
	case p.EntireState:
		return p.StateObj.Name + " Statewide"
	case p.Administrator != "":
		return p.Administrator + " Service Area"
	default:
		return extractUtilityFromName(p.Name, p.StateObj.Name) + " Service Territory"
	}
}

// extractUtilityFromName tries to derive a utility name from the program title.
// e.g. "CenterPoint Energy — Commercial Program" → "CenterPoint Energy"
func extractUtilityFromName(name, stateName string) string {
	if idx := strings.Index(name, " — "); idx > 0 {
		return strings.TrimSpace(name[:idx])
	}
	if idx := strings.Index(name, " - "); idx > 0 {
		return strings.TrimSpace(name[:idx])
	}
	if stateName != "" {
		return stateName + " State Programs"
	}
	return "DSIRE USA"
}

// programLevel maps a DSIRE implementing-sector name to the canonical portfolio
// label stored in the DB.  The DSIRE API itself uses "Local" in some responses
// and "Local Government" in others; both are normalised to "Local Government" so
// downstream filters have a single value to match against.
func programLevel(sector string) string {
	switch sector {
	case "Federal":
		return "Federal"
	case "State":
		return "State"
	case "Utility":
		return "Utility"
	case "Local", "Local Government":
		return "Local Government"
	case "Non-Profit":
		return "Non-Profit"
	case "Other":
		return "Other"
	default:
		if sector != "" {
			return sector // preserve any new sector values DSIRE adds in future
		}
		return ""
	}
}

// ── Text extraction helpers ───────────────────────────────────────────────────

// extractPercentFromText scans desc for common patterns like "50%", "up to 25 percent",
// "covers 30% of cost" and returns the value (0–100). Returns 0 if not found.
// Skips values that are clearly not rebate percentages (e.g. efficiency ratings).
func extractPercentFromText(desc string) float64 {
	// Simple numeric% pattern: look for "NN%" where NN is 1–100.
	rePercent := regexp.MustCompile(`\b([1-9][0-9]?|100)\s*%`)
	if m := rePercent.FindStringSubmatch(desc); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 1 && v <= 100 {
			return v
		}
	}
	return 0
}

// ── Date / HTML helpers ───────────────────────────────────────────────────────

// parseDate normalises common DSIRE date formats to YYYY-MM-DD.
// Handles: "MM/YYYY", "MM/DD/YYYY", "YYYY-MM-DD".
func parseDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// Already ISO
	if len(raw) == 10 && raw[4] == '-' {
		return raw
	}

	parts := strings.Split(raw, "/")
	switch len(parts) {
	case 2: // MM/YYYY
		return fmt.Sprintf("%s-%s-01", parts[1], zeroPad(parts[0]))
	case 3: // MM/DD/YYYY
		return fmt.Sprintf("%s-%s-%s", parts[2], zeroPad(parts[0]), zeroPad(parts[1]))
	}
	return raw
}

func zeroPad(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

// stripHTML removes common HTML tags from a string.
func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br/>", " ")
	s = strings.ReplaceAll(s, "<br>", " ")
	s = strings.ReplaceAll(s, "</p>", " ")

	var out strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			out.WriteRune(r)
		}
	}

	// Decode HTML entities (e.g. &#10; → newline, &amp; → &) then collapse whitespace.
	result := html.UnescapeString(out.String())
	result = strings.ReplaceAll(result, "\n", " ")
	result = strings.ReplaceAll(result, "\t", " ")
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return strings.TrimSpace(result)
}

// formatFromProgramType maps a DSIRE program type name to an incentive_format
// value when parameters alone are ambiguous. Returns "" when the type doesn't
// imply a specific format (let parseParameterSets decide).
func formatFromProgramType(typeName string) string {
	low := strings.ToLower(typeName)
	switch {
	case strings.Contains(low, "loan"):
		return "financing"
	case strings.Contains(low, "tax credit"):
		return "tax_credit"
	case strings.Contains(low, "tax deduction"):
		return "tax_deduction"
	case strings.Contains(low, "tax exemption"):
		return "tax_exemption"
	case strings.Contains(low, "performance"):
		return "performance"
	case strings.Contains(low, "grant"):
		return "dollar_amount"
	default:
		return ""
	}
}

// ── HTTP client helper ────────────────────────────────────────────────────────

func (s *DSIREScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
