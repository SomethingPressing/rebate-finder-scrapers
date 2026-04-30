// xcel_energy.go — Xcel Energy multi-state rebate and incentive scraper.
//
// Xcel Energy operates in Colorado, Minnesota, Texas (SPS), and other states.
// All rebate program pages live on the main corporate website:
//
//	https://www.xcelenergy.com/programs_and_rebates/...
//
// The sitemap used is the static corporate sitemap:
//
//	https://www.xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml
//
// URL filtering mirrors the exclusion-first logic from the SmythOS
// rf-crawler-pnm-srp-coned-xcel-peninsul LLM prompt for Xcel Energy.
// Hub-page detection is implemented via MinPathSegments = 3 — generic
// category pages (depth 2) are excluded; specific program pages (depth 3+)
// are included.
//
// Source defaults:
//   - Source:         "xcel_energy"
//   - UtilityCompany: "Xcel Energy"
//   - State:          extracted from page content (CO, MN, etc.)
package scrapers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	// xcelSitemapURL is the static XML sitemap for the main Xcel Energy site.
	xcelSitemapURL   = "https://www.xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml"
	xcelUtility      = "Xcel Energy"
	xcelSourceName   = "xcel_energy"
	xcelDefaultApply = "Visit the official Xcel Energy program website to learn about eligibility requirements and submit your application."
)

// xcelFilterCfg mirrors the Xcel Energy URL decision logic from the SmythOS
// crawler LLM prompt.  Exclusion-first: corporate/infrastructure patterns are
// checked before any inclusion keyword.  MinPathSegments = 3 rejects hub
// pages that end with a generic category name.
var xcelFilterCfg = FilterConfig{
	// ── ABSOLUTE EXCLUSIONS — Corporate/Company ────────────────────────────
	// Any URL containing these segments is ALWAYS rejected.
	ExcludeKeywords: []string{
		// Corporate / company info
		"/company/",
		"/about_us/",
		"/investor_relations/",
		"/board_of_directors/",
		"/leadership/",
		"/media_room/",
		"/news_releases/",
		"/careers/",
		"/corporate_governance/",
		"/corporate_responsibility",

		// Infrastructure & operations
		"/rates_and_regulations/",
		"/filings/",
		"/rate_cases/",
		"/outages_and_emergencies/",
		"/storm_center/",
		"/customer_support/",
		"/vegetation_management/",
		"/contact_us",
		"/billing_and_payment/",
		"/start,_stop,_transfer/",
		"/installing_and_connecting",
		"/energy_portfolio/",
		"/power_plants/",
		"/natural_gas/projects/",
		"/working_with_us/",
		"/trade_partners/",
		"/suppliers/",
		"/municipalities/",
		"/landlords/",
		"/builders/",
		"/renewable_developer",
		"/how_to_interconnect/",

		// Community & environment (non-program)
		"/community/",
		"/environment/",
		"/renewable_development_fund/",
		"/stakeholder_group/",

		// Pattern exclusions — tools, FAQs, guides (not rebate programs)
		"/assessment",
		"/audit",
		"/analysis",
		"/profile",
		"_tool",
		"/tool/",
		"_finder",
		"_calculator",
		"_advisor",
		"/ways_to_save",
		"/keeping_your_bill_low",
		"/energy_saving_tips",
		"_sign_up",
		"_enrollment",
		"_registration",
		"_alerts",
		"_application_process",
		"_how_it_works",
		"/get_started",
		"_buying_guide",
		"_customer_guide",
		"/resources",
		"/faq",
		"_faq",
		"/terms_and_conditions",
		"_pricing_terms",
		"_product_content_label",
		"/project_-_",
		"_case_study",
		"_success_stories",
		"/completed_projects/",
		"/active_projects/",
		"/scheduling",
		"/pay_arrangements",
		"/net_metering",
		"/stray_voltage",
		"_financing",
		"/pilot",
		"/working_with_third_party",
		"is_solar_right",
		"_participant_portal",
		"/my_account",
		"/mobile_app",
	},

	// ── Inclusions ─────────────────────────────────────────────────────────
	// At least one must match after all exclusion checks pass.
	IncludeKeywords: []string{
		// Money-back / incentive
		"rebate",
		"rebates",
		"incentive",
		"incentives",
		"reward",
		"rewards",
		"credit",
		"cashback",
		"refund",
		"bonus",
		"discount",

		// Reduced costs / savings
		"savings",
		"affordable",
		"low-cost",
		"free",
		"assistance",
		"income_qualified",
		"low_income",
		"income-qualified",
		"poweron",
		"affordability",

		// Energy efficiency
		"efficient",
		"efficiency",
		"upgrade",
		"improvement",
		"conservation",
		"optimize",
		"insulation",
		"weatheriz",

		// Equipment programs
		"heat_pump",
		"heat-pump",
		"hvac",
		"mini-split",
		"ground_source",
		"geothermal",
		"appliance",
		"water_heater",
		"thermostat",
		"lighting",
		"furnace",
		"boiler",
		"cooling",
		"heating",
		"refrigeration",

		// Clean energy
		"solar",
		"wind",
		"renewable",
		"battery_storage",
		"battery-storage",

		// EV
		"electric_vehicle",
		"electric-vehicle",
		"_ev_",
		"/ev_",
		"ev_rate",
		"ev-rate",

		// Demand response / rate programs
		"demand_response",
		"demand-response",
		"peak_partner",
		"peak_reward",
		"saver",
		"saver's_switch",
		"time-of-use",
		"time_of_use",
		"load_management",
		"interruptible",

		// Home services
		"homesmart",

		// Programs
		"programs_and_rebates",
		"program",
	},

	// Minimum path depth to reject hub/category pages.
	// ❌ /programs_and_rebates/equipment_rebates           (depth 2 — hub)
	// ✅ /programs_and_rebates/equipment_rebates/lighting  (depth 3 — program)
	MinPathSegments: 3,
}

// xcelSeedURLs are well-known Xcel program pages used as fallback.
func xcelSeedURLs() []string {
	return []string{
		"https://www.xcelenergy.com/programs_and_rebates/residential_programs_and_rebates/heating_and_cooling/heat_pump_rebates",
		"https://www.xcelenergy.com/programs_and_rebates/residential_programs_and_rebates/equipment_and_appliances/water_heater_rebates",
		"https://www.xcelenergy.com/programs_and_rebates/residential_programs_and_rebates/equipment_and_appliances/energy_star_appliance_rebates",
		"https://www.xcelenergy.com/programs_and_rebates/residential_programs_and_rebates/heating_and_cooling/ac_rewards_smart_thermostat_program",
		"https://www.xcelenergy.com/programs_and_rebates/residential_programs_and_rebates/solar/solar_rewards_for_residences",
		"https://www.xcelenergy.com/programs_and_rebates/residential_programs_and_rebates/affordable_energy/income-qualified_rebate_programs",
		"https://www.xcelenergy.com/programs_and_rebates/business_programs_and_rebates/equipment_rebates/lighting_efficiency",
		"https://www.xcelenergy.com/programs_and_rebates/business_programs_and_rebates/equipment_rebates/heating_efficiency",
		"https://www.xcelenergy.com/programs_and_rebates/business_programs_and_rebates/rate_programs/peak_partner_rewards",
	}
}

// ── State detection ───────────────────────────────────────────────────────────

// xcelStateFromText tries to infer the state from page text / URL.
// Returns a 2-letter state code or empty string if unknown.
func xcelStateFromText(urlAndText string) string {
	lower := strings.ToLower(urlAndText)
	switch {
	case strings.Contains(lower, "colorado"):
		return "CO"
	case strings.Contains(lower, "minnesota"):
		return "MN"
	case strings.Contains(lower, "wisconsin"):
		return "WI"
	case strings.Contains(lower, "north dakota"):
		return "ND"
	case strings.Contains(lower, "south dakota"):
		return "SD"
	case strings.Contains(lower, "new mexico"):
		return "NM"
	case strings.Contains(lower, "wyoming"):
		return "WY"
	default:
		return ""
	}
}

// xcelTerritoryFromState returns the service territory label for a state.
func xcelTerritoryFromState(state string) string {
	switch state {
	case "CO":
		return "Xcel Energy Colorado Service Area"
	case "MN":
		return "Xcel Energy Minnesota Service Area"
	case "WI":
		return "Xcel Energy Wisconsin Service Area"
	case "ND", "SD":
		return "Xcel Energy Northern States Power Service Area"
	case "NM":
		return "Xcel Energy New Mexico Service Area"
	default:
		return "Xcel Energy Service Area"
	}
}

// xcelZIPFromState returns a representative ZIP for a state.
func xcelZIPFromState(state string) string {
	switch state {
	case "CO":
		return "80202" // Denver
	case "MN":
		return "55401" // Minneapolis
	case "WI":
		return "53202" // Milwaukee
	case "ND":
		return "58102" // Fargo
	case "SD":
		return "57101" // Sioux Falls
	case "NM":
		return "87501" // Santa Fe
	default:
		return ""
	}
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// XcelEnergyScraper discovers and scrapes rebate programs from xcelenergy.com.
type XcelEnergyScraper struct {
	CollyBase
	ScraperVersion string
	Logger         *zap.Logger
	HTTPClient     *http.Client
}

// Name implements Scraper.
func (s *XcelEnergyScraper) Name() string { return xcelSourceName }

// Scrape implements Scraper.
func (s *XcelEnergyScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	client := s.httpClient()

	// Step 1: fetch and filter URLs from the corporate sitemap.
	allURLs, err := FetchSitemapURLs(ctx, client, xcelSitemapURL)
	var urls []string
	if err != nil || len(allURLs) == 0 {
		if err != nil {
			s.Logger.Warn("xcel_energy: sitemap fetch failed, using seed URLs", zap.Error(err))
		}
		urls = xcelSeedURLs()
	} else {
		urls = FilterSitemapURLs(allURLs, xcelFilterCfg)
		if len(urls) == 0 {
			s.Logger.Warn("xcel_energy: no URLs passed filter, using seed URLs")
			urls = xcelSeedURLs()
		}
	}

	s.Logger.Info("xcel_energy: scraping URLs", zap.Int("count", len(urls)))

	// Step 2: visit each page and extract incentive data.
	seen := make(map[string]bool)
	var all []models.Incentive

	pdfOpts := PDFIncentiveOpts{
		Source:         xcelSourceName,
		ScraperVersion: s.ScraperVersion,
		UtilityCompany: xcelUtility,
		DefaultApply:   xcelDefaultApply,
		// State/ZipCode/Territory omitted — Xcel is multi-state; HTML path
		// infers these from page content.  For PDFs we leave them blank.
	}

	c := s.newCollector("www.xcelenergy.com")

	c.OnHTML("html", func(e *colly.HTMLElement) {
		pageURL := e.Request.URL.String()
		inc := s.extractPage(e, pageURL)
		if inc == nil {
			return
		}
		if seen[inc.ID] {
			return
		}
		seen[inc.ID] = true
		all = append(all, *inc)
	})

	for _, u := range urls {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		if IsPDFURL(u) {
			text, err := ExtractPDFPages(u, nil)
			if err != nil {
				s.Logger.Warn("xcel_energy: pdf extract failed", zap.String("url", u), zap.Error(err))
				continue
			}
			inc := ExtractIncentiveFromPDFText(text, u, pdfOpts)
			if inc != nil && !seen[inc.ID] {
				seen[inc.ID] = true
				all = append(all, *inc)
			}
			continue
		}
		if err := c.Visit(u); err != nil {
			s.Logger.Warn("xcel_energy: visit failed",
				zap.String("url", u), zap.Error(err))
		}
	}

	s.Logger.Info("xcel_energy: scrape complete", zap.Int("programs", len(all)))
	return all, nil
}

// extractPage extracts a single Incentive from an Xcel Energy rebate page.
func (s *XcelEnergyScraper) extractPage(
	e *colly.HTMLElement,
	pageURL string,
) *models.Incentive {
	programName := strings.TrimSpace(e.ChildText("h1"))
	if programName == "" {
		programName = strings.TrimSpace(e.ChildText("title"))
		if idx := strings.Index(programName, "|"); idx > 0 {
			programName = strings.TrimSpace(programName[:idx])
		}
		if idx := strings.Index(programName, " - "); idx > 0 {
			programName = strings.TrimSpace(programName[:idx])
		}
	}
	if programName == "" || len(programName) < 5 {
		return nil
	}

	titleLower := strings.ToLower(programName)
	for _, p := range []string{"page not found", "404", "error", "home", "login"} {
		if strings.Contains(titleLower, p) {
			return nil
		}
	}

	// Description.
	description := strings.TrimSpace(e.ChildAttr(`meta[name="description"]`, "content"))
	if description == "" {
		e.ForEach("p", func(_ int, el *colly.HTMLElement) {
			if description != "" {
				return
			}
			text := strings.TrimSpace(el.Text)
			if len(text) > 40 {
				description = text
			}
		})
	}
	if description == "" {
		description = programName
	}
	if len(description) > 500 {
		description = description[:497] + "..."
	}

	// Full page text for regex extraction.
	pageText := e.Text

	// State detection — infer from page text + URL (Xcel is multi-state).
	state := xcelStateFromText(pageURL + " " + pageText)
	territory := xcelTerritoryFromState(state)
	repZIP := xcelZIPFromState(state)

	// Amount extraction.
	format, amount := ParseAmount(pageText)
	if format == "narrative" {
		e.ForEach("p, li, td, h2, h3, strong", func(_ int, el *colly.HTMLElement) {
			if format != "narrative" {
				return
			}
			f, a := ParseAmount(el.Text)
			if f != "narrative" {
				format = f
				amount = a
			}
		})
	}

	// Application URL.
	applicationURL := ""
	e.ForEach("a[href]", func(_ int, el *colly.HTMLElement) {
		if applicationURL != "" {
			return
		}
		href := el.Attr("href")
		text := strings.ToLower(el.Text + " " + href)
		if strings.Contains(text, "apply") || strings.Contains(text, "application") ||
			strings.Contains(text, "submit") || strings.Contains(text, "enroll") {
			if strings.HasPrefix(href, "http") {
				applicationURL = href
			} else if strings.HasPrefix(href, "/") {
				applicationURL = "https://www.xcelenergy.com" + href
			}
		}
	})

	// ── Boolean / structured field extraction (from html_helpers.go) ────────
	contractorRequired := extractContractorRequired(pageText)
	energyAuditRequired := extractEnergyAuditRequired(pageText)
	customerType := extractCustomerType(pageURL + " " + programName)
	startDate := extractStartDate(pageText)
	endDate := extractEndDate(pageText)

	// Contact info.
	contactPhone := extractPhone(pageText)
	contactEmail := extractEmail(pageText)

	// Category inference.
	categories := inferCategories(pageURL + " " + strings.ToLower(programName))

	if format == "" {
		format = "narrative"
	}

	id := models.DeterministicID(xcelSourceName, pageURL)

	inc := models.NewIncentive(xcelSourceName, s.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = xcelUtility
	inc.ServiceTerritory = models.PtrString(territory)
	inc.IncentiveDescription = models.PtrString(description)
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(xcelDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, xcelUtility)

	// Only set state / ZIP if we detected them from the page.
	if state != "" {
		inc.State = models.PtrString(state)
	}
	if repZIP != "" {
		inc.ZipCode = models.PtrString(repZIP)
	}

	if amount != nil {
		inc.IncentiveAmount = amount
	}
	if applicationURL != "" {
		inc.ApplicationURL = models.PtrString(applicationURL)
	}
	if contactPhone != "" {
		inc.ContactPhone = models.PtrString(contactPhone)
	}
	if contactEmail != "" {
		inc.ContactEmail = models.PtrString(contactEmail)
	}
	if contractorRequired != nil {
		inc.ContractorRequired = contractorRequired
	}
	if energyAuditRequired != nil {
		inc.EnergyAuditRequired = energyAuditRequired
	}
	if customerType != "" {
		inc.CustomerType = models.PtrString(customerType)
	}
	if startDate != "" {
		inc.StartDate = models.PtrString(startDate)
	}
	if endDate != "" {
		inc.EndDate = models.PtrString(endDate)
	}

	return &inc
}

func (s *XcelEnergyScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *XcelEnergyScraper) newCollector(domain string) *colly.Collector {
	s.CollyBase.AllowedDomain = domain
	s.CollyBase.Parallelism = 2
	s.CollyBase.Delay = 600 * time.Millisecond
	s.CollyBase.Logger = s.Logger
	return s.CollyBase.NewCollector()
}
