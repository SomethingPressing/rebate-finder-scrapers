// Utility (Covers multiple states)
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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/internal/categoryinfer"
	"github.com/incenva/rebate-scraper/internal/segmentinfer"
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

// xcelTarget pairs a Salesforce Experience Cloud URL with the state label to
// pass to FetchHTMLWithStateSelect (used only if a region-selector interstitial
// appears; state-specific subdomains usually skip it entirely).
type xcelTarget struct {
	url   string
	state string // e.g. "Colorado", "Minnesota"
}

// xcelDirectTargets returns the canonical Salesforce Experience Cloud program
// pages for all Xcel Energy service territories.
//
// Background: the old www.xcelenergy.com/programs_and_rebates/... pages
// 301-redirect to generic my.xcelenergy.com/s/... Salesforce pages that show a
// region-selector interstitial.  State-specific subdomains (co.my.*, mn.my.*)
// return HTTP 200 directly and serve state content without the interstitial.
//
// Only depth-3+ paths are included here — hub pages (depth 2, e.g.
// /residential/heating-cooling) are category listings, not individual programs.
func xcelDirectTargets() []xcelTarget {
	return []xcelTarget{
		// ── Colorado (co.my.xcelenergy.com) ──────────────────────────────────
		// Residential — heating/cooling sub-programs
		{"https://co.my.xcelenergy.com/s/residential/heating-cooling/heat-pumps", "Colorado"},
		{"https://co.my.xcelenergy.com/s/residential/heating-cooling/air-conditioners", "Colorado"},
		{"https://co.my.xcelenergy.com/s/residential/heating-cooling/furnaces", "Colorado"},
		{"https://co.my.xcelenergy.com/s/residential/heating-cooling/ac-rewards", "Colorado"},
		// Residential — home rebates sub-programs
		{"https://co.my.xcelenergy.com/s/residential/home-rebates/home-lighting", "Colorado"},
		{"https://co.my.xcelenergy.com/s/residential/home-rebates/appliances", "Colorado"},
		{"https://co.my.xcelenergy.com/s/residential/home-rebates/insulation-air-sealing", "Colorado"},
		{"https://co.my.xcelenergy.com/s/residential/home-rebates/water-heaters", "Colorado"},
		{"https://co.my.xcelenergy.com/s/residential/home-rebates/windows-doors", "Colorado"},
		// Home services
		{"https://co.my.xcelenergy.com/s/residential/home-services/homesmart", "Colorado"},
		// Income-qualified / assistance
		{"https://co.my.xcelenergy.com/s/billing-payment/energy-assistance", "Colorado"},
		// Renewable
		{"https://co.my.xcelenergy.com/s/renewable/solar", "Colorado"},
		{"https://co.my.xcelenergy.com/s/renewable/wind", "Colorado"},
		// EV
		{"https://ev.xcelenergy.com/home-wiring-rebate", "Colorado"},
		// Business
		{"https://co.my.xcelenergy.com/s/business/lighting-equipment-rebates", "Colorado"},
		{"https://co.my.xcelenergy.com/s/business/new-building-programs", "Colorado"},
		{"https://co.my.xcelenergy.com/s/business/farm-programs", "Colorado"},
		// Legacy CMS — PowerOn income-qualified program
		{"https://www.xcelenergy.com/billing_and_payment/understanding_your_bill/energy_assistance_options/poweron_and_gas_affordability_program", "Colorado"},

		// ── Minnesota (mn.my.xcelenergy.com) ─────────────────────────────────
		{"https://mn.my.xcelenergy.com/s/residential/heating-cooling/heat-pumps", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/residential/heating-cooling/air-conditioners", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/residential/heating-cooling/furnaces", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/residential/heating-cooling/ac-rewards", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/residential/home-rebates/home-lighting", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/residential/home-rebates/appliances", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/residential/home-rebates/insulation-air-sealing", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/residential/home-rebates/water-heaters", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/billing-payment/energy-assistance", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/renewable/solar", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/business/lighting-equipment-rebates", "Minnesota"},
		{"https://mn.my.xcelenergy.com/s/business/new-building-programs", "Minnesota"},
	}
}

// xcelDirectURLs returns just the URL strings (used for seeding / legacy callers).
func xcelDirectURLs() []string {
	targets := xcelDirectTargets()
	urls := make([]string, len(targets))
	for i, t := range targets {
		urls[i] = t.url
	}
	return urls
}

// xcelSeedURLs returns the same set as xcelDirectURLs for use as fallback.
func xcelSeedURLs() []string { return xcelDirectURLs() }

// ── State detection ───────────────────────────────────────────────────────────

// xcelStateFromText tries to infer the state from page text / URL.
// Checks URL subdomain first (co.my.*, mn.my.*, etc.) for a reliable signal,
// then falls back to state name keywords in the text body.
// Returns a 2-letter state code or empty string if unknown.
func xcelStateFromText(urlAndText string) string {
	lower := strings.ToLower(urlAndText)
	// Subdomain-first detection — most reliable for state-specific portals.
	switch {
	case strings.Contains(lower, "co.my.xcelenergy.com"):
		return "CO"
	case strings.Contains(lower, "mn.my.xcelenergy.com"):
		return "MN"
	case strings.Contains(lower, "wi.my.xcelenergy.com"):
		return "WI"
	case strings.Contains(lower, "nd.my.xcelenergy.com"), strings.Contains(lower, "sd.my.xcelenergy.com"):
		return "ND"
	case strings.Contains(lower, "nm.my.xcelenergy.com"):
		return "NM"
	}
	// Fall back to text keywords.
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
	HTTPClient     *http.Client // optional override for tests

	// ProxyURL routes sitemap fetches and Colly visits through a proxy.
	// Format: "http://user:pass@host:port" or "socks5://host:port".
	// Env var: SCRAPER_PROXY_URL
	ProxyURL string
	// Limit caps how many URLs are visited. 0 means no limit.
	Limit int
	// CategoryInferrer enables smart embedding+LLM category inference as a fallback.
	CategoryInferrer *categoryinfer.CategoryInferrer
	// SegmentInferrer enables smart embedding+LLM segment inference as a fallback.
	SegmentInferrer *segmentinfer.SegmentInferrer
}

// xcelExtractCfg is the shared goquery extraction config for Xcel Energy.
// Xcel is multi-state so StateDetector infers state/territory/zip from page text.
var xcelExtractCfg = PageExtractConfig{
	Source:          xcelSourceName,
	UtilityCompany:  xcelUtility,
	DefaultApply:    xcelDefaultApply,
	BaseURL:         "https://www.xcelenergy.com",
	AmountSelectors: "strong",
	// Skip Salesforce region-selector interstitial pages and error pages.
	SkipPhrases: append(append([]string{}, DefaultSkipPhrases...),
		"select a service area to explore",
		"select a service area",
		"choose your service area",
		"which state do you live in",
		"uh-oh",         // Salesforce error page ("Uh-oh. We may have left this page unplugged.")
		"page unplugged", // same error, alternate phrasing
	),
	StateDetector: func(text string) (state, territory, zip string) {
		s := xcelStateFromText(text)
		return s, xcelTerritoryFromState(s), xcelZIPFromState(s)
	},
}

// Name implements Scraper.
func (s *XcelEnergyScraper) Name() string { return xcelSourceName }

// Scrape implements Scraper.
//
// Xcel Energy migrated its program pages to Salesforce Experience Cloud
// (my.xcelenergy.com/s/...) which is 100% JavaScript-rendered.  The old
// www.xcelenergy.com sitemap URLs all 301-redirect to ~11 unique Salesforce
// section pages.  Visiting those redirects in Chromium triggers cross-origin
// navigation which destroys the execution context before WaitLoad completes.
//
// Solution: skip the sitemap entirely.  Use xcelDirectTargets() — state-specific
// Salesforce destination URLs for CO and MN — as the authoritative source.
// Each page is visited with FetchHTMLWithStateSelect(stateLabel) so any
// region-selector interstitial is dismissed with the correct state before
// content is read.
func (s *XcelEnergyScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	// Start the headless browser eagerly — every page needs JavaScript execution.
	bf, err := newBrowserFetcher(s.Logger)
	if err != nil {
		return nil, fmt.Errorf("xcel_energy: start headless browser: %w", err)
	}
	defer bf.Close()

	targets := xcelDirectTargets()
	s.Logger.Info("xcel_energy: using direct Salesforce targets", zap.Int("count", len(targets)))

	if s.Limit > 0 && len(targets) > s.Limit {
		targets = targets[:s.Limit]
	}
	s.Logger.Info("xcel_energy: scraping targets", zap.Int("count", len(targets)))

	extractCfg := xcelExtractCfg
	extractCfg.ScraperVersion = s.ScraperVersion
	extractCfg.CategoryInferrer = s.CategoryInferrer
	extractCfg.SegmentInferrer = s.SegmentInferrer

	pdfOpts := PDFIncentiveOpts{
		Source:         xcelSourceName,
		ScraperVersion: s.ScraperVersion,
		UtilityCompany: xcelUtility,
		DefaultApply:   xcelDefaultApply,
	}

	seen := make(map[string]bool)
	var all []models.Incentive

	total := len(targets)
	bar := NewProgressBar(total, "xcel_energy")

	for i, target := range targets {
		u := target.url
		select {
		case <-ctx.Done():
			bar.Finish() //nolint:errcheck
			return all, ctx.Err()
		default:
		}
		s.Logger.Info("xcel_energy: visiting URL",
			zap.Int("i", i+1),
			zap.Int("total", total),
			zap.String("url", u),
			zap.String("state", target.state),
		)

		if IsPDFURL(u) {
			text, pdfErr := ExtractPDFPages(u, nil)
			if pdfErr != nil {
				s.Logger.Warn("xcel_energy: pdf extract failed", zap.String("url", u), zap.Error(pdfErr))
				bar.Add(1) //nolint:errcheck
				continue
			}
			inc := ExtractIncentiveFromPDFText(text, u, pdfOpts)
			if inc != nil && !seen[inc.ID] {
				seen[inc.ID] = true
				all = append(all, *inc)
				s.Logger.Info("xcel_energy: program found (pdf)",
					zap.String("name", inc.ProgramName),
					zap.Int("total_so_far", len(all)),
				)
			}
			bar.Add(1) //nolint:errcheck
			continue
		}

		// Pass the correct state label so FetchHTMLWithStateSelect dismisses
		// any region-selector interstitial with the right state before reading.
		html, fetchErr := bf.FetchHTMLWithStateSelect(ctx, u, target.state)
		if fetchErr != nil {
			s.Logger.Warn("xcel_energy: browser fetch failed",
				zap.String("url", u), zap.Error(fetchErr))
			bar.Add(1) //nolint:errcheck
			continue
		}

		doc, parseErr := goquery.NewDocumentFromReader(strings.NewReader(html))
		if parseErr != nil {
			s.Logger.Warn("xcel_energy: parse failed",
				zap.String("url", u), zap.Error(parseErr))
			bar.Add(1) //nolint:errcheck
			continue
		}

		inc := ExtractPageGoquery(doc, u, extractCfg)
		if inc != nil && !seen[inc.ID] {
			seen[inc.ID] = true
			all = append(all, *inc)
			s.Logger.Info("xcel_energy: program found",
				zap.String("name", inc.ProgramName),
				zap.Stringp("state", inc.State),
				zap.Strings("categories", inc.CategoryTag),
				zap.Int("total_so_far", len(all)),
			)
		}
		bar.Add(1) //nolint:errcheck
	}
	bar.Finish() //nolint:errcheck

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
	for _, p := range DefaultSkipPhrases {
		if strings.Contains(titleLower, p) {
			return nil
		}
	}

	description := CollyDescriptionMarkdown(e, programName)
	imageURL := CollyImageURL(e, "https://www.xcelenergy.com")

	// Full page text for regex extraction.
	pageText := e.Text

	// State detection — infer from page text + URL (Xcel is multi-state).
	state := xcelStateFromText(pageURL + " " + pageText)
	territory := xcelTerritoryFromState(state)
	repZIP := xcelZIPFromState(state)

	// Amount extraction — only when incentive keywords are present on the page.
	format, amount := ParseAmountContextual(pageText)
	if format == "narrative" {
		e.ForEach("p, li, td, h2, h3, strong", func(_ int, el *colly.HTMLElement) {
			if format != "narrative" {
				return
			}
			f, a := ParseAmountContextual(el.Text)
			if f != "narrative" {
				format = f
				amount = a
			}
		})
	}

	// Hub-page guard: 3+ distinct monetary values → listing page, not a single incentive.
	if format != "narrative" && countDistinctAmounts(pageText) >= 3 {
		format = "narrative"
		amount = nil
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
	customerType := extractCustomerTypeWithBody(pageURL+" "+programName, pageText)
	startDate := extractStartDate(pageText)
	endDate := extractEndDate(pageText)

	// Contact info.
	contactPhone := extractPhone(pageText)
	contactEmail := extractEmail(pageText)

	// Category and segment inference.
	categories := inferCategories(pageURL + " " + strings.ToLower(programName) + " " + strings.ToLower(pageText[:min(len(pageText), 2000)]))
	segments := inferSegments(pageURL+" "+programName, pageText)

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
	if imageURL != "" {
		inc.ImageURL = models.PtrString(imageURL)
	}
	inc.IncentiveFormat = models.PtrString(format)
	inc.ImplementingSector = models.PtrString("Utility")
	inc.ApplicationProcess = models.PtrString(xcelDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.SourceURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.Segment = segments
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
	s.CollyBase.ProxyURL = s.ProxyURL
	return s.CollyBase.NewHTTPClient(30 * time.Second)
}

func (s *XcelEnergyScraper) newCollector(domain string) *colly.Collector {
	s.CollyBase.AllowedDomain = domain
	s.CollyBase.Parallelism = 2
	s.CollyBase.Delay = 600 * time.Millisecond
	s.CollyBase.Logger = s.Logger
	s.CollyBase.ProxyURL = s.ProxyURL
	return s.CollyBase.NewCollector()
}
