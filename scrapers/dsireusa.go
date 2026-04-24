package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	ID           int    `json:"id"`
	Name         string `json:"name"`
	WebsiteURL   string `json:"websiteUrl"`
	Summary      string `json:"summary"` // HTML — must be stripped
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
	Parameters []dsireParameter `json:"parameters"`
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

// DSIREScraper fetches all incentive programs from the DSIRE USA v1 API in a
// single request — no ZIP or state filter needed.
//
//	GET https://programs.dsireusa.org/api/v1/programs
//	    ?category[]=1&draw=1&start=0&length=-1
//
// length=-1 instructs the API to return the complete result set at once
// (~1 800 programs as of 2025).  No pagination or per-ZIP iteration required.
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
}

// Name implements Scraper.
func (s *DSIREScraper) Name() string { return "dsireusa" }

// Scrape implements Scraper.
func (s *DSIREScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	s.Logger.Info("dsireusa scrape starting — fetching all programs in one request",
		zap.Bool("scrape_details", s.ScrapeDetails),
	)

	programs, err := s.fetchAll(ctx, s.httpClient())
	if err != nil {
		return nil, fmt.Errorf("dsireusa: fetch all: %w", err)
	}

	var all []models.Incentive
	for _, prog := range programs {
		inc := s.toIncentive(prog)

		if s.ScrapeDetails {
			detail := s.scrapeDetail(ctx, prog.ID)
			s.applyDetail(&inc, detail)
		}

		all = append(all, inc)
	}

	s.Logger.Info("dsireusa scrape complete",
		zap.Int("programs", len(all)),
	)

	return all, nil
}

// fetchAll calls the v1 API with no geographic filter and returns every program.
// length=-1 instructs the server to return the complete result set at once.
func (s *DSIREScraper) fetchAll(
	ctx context.Context,
	client *http.Client,
) ([]dsireProgram, error) {
	baseURL := strings.TrimRight(s.BaseURL, "/")

	u := fmt.Sprintf("%s?category[]=1&draw=1&start=0&length=-1", baseURL)

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
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var page dsireV1Response
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	return page.Data, nil
}

// toIncentive maps a dsireProgram → models.Incentive.
func (s *DSIREScraper) toIncentive(p dsireProgram) models.Incentive {
	inc := models.NewIncentive(s.Name(), s.ScraperVersion)
	inc.ID = models.DeterministicID(s.Name(), strconv.Itoa(p.ID))

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
	// ZipCode is not set — DSIRE programs are state/utility scoped, not ZIP-specific.

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
	if cleaned := stripHTML(p.Summary); cleaned != "" {
		inc.IncentiveDescription = models.PtrString(cleaned)
	}

	// ── URLs ──────────────────────────────────────────────────────────────────
	if p.WebsiteURL != "" {
		inc.ProgramURL = models.PtrString(p.WebsiteURL)
		inc.ApplicationURL = models.PtrString(p.WebsiteURL)
	}

	// ── Amount / format from parameterSets ────────────────────────────────────
	format, incAmt, maxAmt, pctVal, perUnit, unitType := parseParameterSets(p.ParameterSets)
	inc.IncentiveFormat = models.PtrString(format)
	inc.IncentiveAmount = incAmt
	inc.MaximumAmount = maxAmt
	inc.PercentValue = pctVal
	inc.PerUnitAmount = perUnit
	inc.UnitType = unitType

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
	if d.Requirements != "" && inc.IncentiveDescription != nil {
		// Append requirements to description so it surfaces in search.
		merged := *inc.IncentiveDescription + " Requirements: " + d.Requirements
		inc.IncentiveDescription = models.PtrString(merged)
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
	for _, ps := range sets {
		for _, t := range ps.Technologies {
			if t.Name != "" && !seen[t.Name] {
				seen[t.Name] = true
				out = append(out, t.Name)
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
	case "industrial equipment":
		return "Industrial Equipment"
	case "electric vehicles":
		return "Electric Vehicles"
	default:
		return cat
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

func programLevel(sector string) string {
	switch sector {
	case "Federal":
		return "Federal"
	case "State":
		return "State"
	case "Utility":
		return "Utility"
	case "Local Government":
		return "Local"
	default:
		return ""
	}
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

	// Collapse multiple spaces
	result := out.String()
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return strings.TrimSpace(result)
}

// ── HTTP client helper ────────────────────────────────────────────────────────

func (s *DSIREScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
