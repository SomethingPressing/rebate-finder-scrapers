// Utility
// con_edison.go — Con Edison (coned.com) rebate and incentive scraper.
//
// Discovers rebate pages via the Con Edison sitemap, then visits each page
// and extracts structured incentive data using HTML selectors and regex.
//
// URL filtering mirrors the two-pass (exclusion-first, then inclusion) logic
// from the SmythOS rf-crawler-pnm-srp-coned-xcel-peninsul LLM prompt.
//
// Source defaults:
//   - Source:           "con_edison"
//   - State:            NY
//   - UtilityCompany:   "Con Edison"
//   - ServiceTerritory: "Con Edison Service Territory"
//   - ZipCode:          "10001"  (Manhattan — representative NY ZIP)
package scrapers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/internal/categoryinfer"
	"github.com/incenva/rebate-scraper/internal/segmentinfer"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── Constants ────────────────────────────────────────────────────────────────

const (
	conEdisonSitemapURL   = "https://www.coned.com/sitemap_coned_en.xml"
	conEdisonState        = "NY"
	conEdisonUtility      = "Con Edison"
	conEdisonTerritory    = "Con Edison Service Territory"
	conEdisonZIP          = "10001"
	conEdisonSourceName   = "con_edison"
	conEdisonDefaultApply = "Visit the official Con Edison program website to learn about eligibility requirements and submit your application."
)

// conEdisonFilterCfg mirrors the two-pass URL decision logic from the
// SmythOS Con Edison crawler LLM prompt.
// Exclusions are checked FIRST; a URL matching any exclusion is always rejected
// even if it also contains an inclusion keyword.
var conEdisonFilterCfg = FilterConfig{
	// ── Exclusions (checked first) ─────────────────────────────────────────
	// Con Edison-specific path exclusions and general corporate/support patterns.
	ExcludeKeywords: []string{
		// Con Edison-specific
		"/using-distributed-generation",
		"/shop-for-energy-service",
		"/our-energy-vision",
		"/where-we-are-going",

		// Account / login
		"/my-account",
		"/login",
		"/sign-in",

		// Corporate / admin
		"/about-us",
		"/about-con-edison",
		"/careers",
		"/media-center",
		"/news",
		"/press",
		"/investor",
		"/board",
		"/governance",
		"/leadership",
		"/safety",
		"/outages",
		"/emergency",

		// Infrastructure / regulatory (non-customer)
		"/grid",
		"/transmission",
		"/substation",
		"/distribution",
		"/tariff",
		"/rate-schedule",
		"/fault-current",
		"/interconnection",

		// Generic support (non-program)
		"/contact-us",
		"/terms-of-use",
		"/privacy",
		"/search",
		"/sitemap",
		"/contractor-portal",
		"/supplier",
		"/vendor",

		// Community affairs / PR updates — not customer rebate programs
		"/community-affairs",
		"/community-updates",
		"/community-affairs/community-updates",

		// RFP / procurement pages — not customer-facing incentives
		"/request-for-proposals",
		"/rfp",
		"/bulk-energy-storage",
		"/business-opportunities",

		// Archive / historical pages — programs no longer active
		"/archive",

		// FAQ / tips / tools sub-pages — not program pages; scrape the parent instead
		"/frequently-asked-questions",
		"/faqs",
		"/faq",
		"/tips-to-lower-your-bill",
		"/tips-library",
		"/tools-technical-guidelines",
		"/program-tools",
		"/technical-guidelines",

		// Contractor-facing resources — not customer-facing program pages
		"/contractor-resources",
		"/participating-contractor",
		"/smb-participating-contractor",

		// Telecom / business-partner portals — not customer rebate programs
		"/business-partners/",
		"/telecom-application-management",

		// Marketing / editorial content — not structured program pages
		"/success-stories",
		"/videos",
		"/contractor-training",
		"/eco-friendly-cars",
		"/liquefied-natural-gas",
		"/energy-savings-options",
		"/interest-form",
		"/financial-statement-form",

		// JS-rendered incentive viewer sub-pages (parent landing page is fine)
		"/clean-energy-incentives-viewer/",

		// ── Educational guides (not rebate program descriptions) ───────────────
		// All Con Edison guide pages are informational; the rebate is on the parent.
		"-guide",

		// ── FAQ sub-pages (informational, not program descriptions) ──────────
		// Extends the existing /faq exclusion to catch paths like
		// "demand-response-faq" and "uptime-report-rule-change-faq".
		"-faq",

		// ── Contractor-facing pages (not customer rebate programs) ────────────
		"/resources-for-contractors",
		"/become-a-",

		// ── Documentation / requirements pages ───────────────────────────────
		"/documentation",

		// ── Non-program utility and rate pages ────────────────────────────────
		"/best-electric-delivery-rate",
		"/electric-vehicles-and-your-bill",
		"/make-better-energychoices-with-green-button",
		"/upgrade-to-a-billing-interval-meter",
		"/smart-usage-rewards-form",

		// ── Informational / tool pages (not specific Con Edison programs) ─────
		"/third-party-incentives",
		"/find-clean-energy-incentives",
	},

	// ── Inclusions ─────────────────────────────────────────────────────────
	// At least one must match after exclusion check passes.
	IncludeKeywords: []string{
		// Direct financial benefit
		"rebate",
		"incentive",
		"save-money",
		"saving",
		"savings",
		"credit",
		"refund",
		"cashback",
		"reward",
		"discount",
		"free-product",
		"no-cost",
		"zero-percent",

		// Payment / financial assistance
		"payment-plans-assistance",
		"help-paying",
		"financial-assist",
		"assistance",
		"affordable",
		"low-income",
		"income-eligible",
		"budget-billing",

		// Energy efficiency / equipment
		"weatherization",
		"insulation",
		"heat-pump",
		"geothermal",
		"thermostat",
		"appliance",
		"water-heater",
		"lighting",
		"efficiency",
		"upgrade",
		"improvement",

		// Clean energy
		"solar",
		"renewable",
		"electric-vehicle",
		"ev-charging",
		"battery",
		"storage",

		// Smart / demand programs
		"smart-usage",
		"demand-response",
		"smart-energy-plan",
		"time-of-use",
		"peak-shaving",

		// Financing
		"financing",

		// Incentive tools/viewers
		"find-incentive",
		"incentive-viewer",
		"program-finder",
		"explore-clean-energy",
		"financial-assistance-advisor",
	},
}

// conEdisonSeedURLs are well-known rebate listing pages used as fallback when
// the sitemap is unavailable or returns no matching URLs.
func conEdisonSeedURLs() []string {
	return []string{
		// ── Residential ───────────────────────────────────────────────────────
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-tax-credits-for-residential-customers",
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-tax-credits-for-residential-customers/electric-heating-and-cooling-technology-for-renters-homeowners/save-on-a-central-air-source-heat-pump",
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-tax-credits-for-residential-customers/smart-usage-rewards",
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-tax-credits-for-residential-customers/energy-exchange",
		"https://www.coned.com/en/save-money/weatherization",
		// ── Commercial & Industrial ───────────────────────────────────────────
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-tax-credits-for-commercial-industrial-buildings-customers/save-with-energy-efficiency-upgrades",
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-tax-credits-for-commercial-industrial-buildings-customers/commercial-neighborhood-program",
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/small-business",
		// ── Multifamily ───────────────────────────────────────────────────────
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-for-multifamily-customers/market-rate-buildings",
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-for-multifamily-customers/affordable-buildings",
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-for-multifamily-customers/multifamily-neighborhood-program",
		// ── Electric Vehicles ─────────────────────────────────────────────────
		"https://www.coned.com/en/our-energy-future/electric-vehicles/power-ready-program",
		// ── Income Qualified ──────────────────────────────────────────────────
		"https://www.coned.com/en/accounts-billing/payment-plans-assistance/help-paying-your-bill",
	}
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// ConEdisonScraper discovers and scrapes rebate programs from coned.com.
// Set ProxyURL to route through a residential proxy if Cloudflare/WAF blocks
// the server's IP range (same pattern as SRPScraper).
type ConEdisonScraper struct {
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

// conEdisonExtractCfg is the shared goquery extraction config.
// ScraperVersion is filled in at scrape-time.
var conEdisonExtractCfg = PageExtractConfig{
	Source:         conEdisonSourceName,
	UtilityCompany: conEdisonUtility,
	State:          conEdisonState,
	ZipCode:        conEdisonZIP,
	Territory:      conEdisonTerritory,
	DefaultApply:   conEdisonDefaultApply,
	BaseURL:        "https://www.coned.com",
}

// Name implements Scraper.
func (s *ConEdisonScraper) Name() string { return conEdisonSourceName }

// Scrape implements Scraper.
func (s *ConEdisonScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	client := s.httpClient()

	// Lazy browser — only started if a permission error is encountered.
	getBF, cleanup := lazyBrowser(s.Logger)
	defer cleanup()

	// Step 1: discover rebate URLs from sitemap.
	// Automatically retries with the headless browser if the HTTP request
	// returns 403 / 401 / 407.
	allURLs, err := sitemapWithFallback(ctx, client, conEdisonSitemapURL, getBF, s.Logger, "con_edison")
	var urls []string
	if err != nil || len(allURLs) == 0 {
		if err != nil {
			s.Logger.Warn("con_edison: sitemap fetch failed, using seed URLs", zap.Error(err))
		}
		urls = conEdisonSeedURLs()
	} else {
		urls = FilterSitemapURLs(allURLs, conEdisonFilterCfg)
		s.Logger.Info("con_edison: sitemap discovery",
			zap.Int("sitemap_total", len(allURLs)),
			zap.Int("passed_filter", len(urls)),
		)
		if len(urls) == 0 {
			urls = conEdisonSeedURLs()
		}
	}

	if s.Limit > 0 && len(urls) > s.Limit {
		urls = urls[:s.Limit]
	}
	s.Logger.Info("con_edison: scraping URLs", zap.Int("count", len(urls)))

	seen := make(map[string]bool)
	var all []models.Incentive

	pdfOpts := PDFIncentiveOpts{
		Source:         conEdisonSourceName,
		ScraperVersion: s.ScraperVersion,
		UtilityCompany: conEdisonUtility,
		State:          conEdisonState,
		ZipCode:        conEdisonZIP,
		Territory:      conEdisonTerritory,
		DefaultApply:   conEdisonDefaultApply,
	}
	extractCfg := conEdisonExtractCfg
	extractCfg.ScraperVersion = s.ScraperVersion
	extractCfg.CategoryInferrer = s.CategoryInferrer
	extractCfg.SegmentInferrer = s.SegmentInferrer

	// Step 2: Colly-based HTML scraping with automatic 403-fallback.
	c := s.newCollector("www.coned.com")

	// Track any URLs blocked with a permission error — retried via browser below.
	permBlocked := trackPermissionErrors(c)

	c.OnHTML("html", func(e *colly.HTMLElement) {
		pageURL := e.Request.URL.String()

		// Discover child program pages linked from this page that may not be in
		// the sitemap (e.g. specific rebate pages nested under a hub landing page).
		childLinks := conEdisonChildProgramLinks(e, pageURL)
		for _, child := range childLinks {
			_ = c.Visit(child) // no-op if already queued/visited by Colly
		}

		// Hub guard: a page with 2+ child program links and no incentive amount
		// is a category/landing page — skip it; the children carry the real data.
		if len(childLinks) >= 2 {
			_, amt := ParseAmountContextual(e.Text)
			if amt == nil {
				s.Logger.Info("con_edison: hub page skipped, children queued",
					zap.String("url", pageURL),
					zap.Int("children", len(childLinks)),
				)
				return
			}
		}

		inc := s.extractPage(e, pageURL)
		if inc == nil {
			return
		}
		if seen[inc.ID] {
			return
		}
		seen[inc.ID] = true
		all = append(all, *inc)
		s.Logger.Info("con_edison: program found",
			zap.String("name", inc.ProgramName),
			zap.Strings("categories", inc.CategoryTag),
			zap.Int("total_so_far", len(all)),
		)
	})

	total := len(urls)
	bar := NewProgressBar(total, "con_edison")
	for i, u := range urls {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		s.Logger.Info("con_edison: visiting URL",
			zap.Int("i", i+1),
			zap.Int("total", total),
			zap.String("url", u),
		)
		if IsPDFURL(u) {
			text, err := ExtractPDFPages(u, nil)
			if err != nil {
				s.Logger.Warn("con_edison: pdf extract failed", zap.String("url", u), zap.Error(err))
				continue
			}
			inc := ExtractIncentiveFromPDFText(text, u, pdfOpts)
			if inc != nil && !seen[inc.ID] {
				seen[inc.ID] = true
				all = append(all, *inc)
				s.Logger.Info("con_edison: program found (pdf)",
					zap.String("name", inc.ProgramName),
					zap.Int("total_so_far", len(all)),
				)
			}
			continue
		}
		if err := c.Visit(u); err != nil {
			s.Logger.Warn("con_edison: visit failed",
				zap.String("url", u), zap.Error(err))
		}
		bar.Add(1) //nolint:errcheck
	}
	bar.Finish() //nolint:errcheck

	// Step 3: retry any permission-blocked pages with the headless browser.
	retryBlockedWithBrowser(ctx, *permBlocked, getBF, extractCfg, seen, &all, s.Logger, "con_edison")

	s.Logger.Info("con_edison: scrape complete", zap.Int("programs", len(all)))
	return all, nil
}

// extractPage extracts a single Incentive from a Con Edison rebate page.
// Returns nil if the page doesn't look like a meaningful incentive program.
func (s *ConEdisonScraper) extractPage(e *colly.HTMLElement, pageURL string) *models.Incentive {
	// Extract page title (h1 first, then <title>).
	programName := strings.TrimSpace(e.ChildText("h1"))
	if programName == "" {
		programName = strings.TrimSpace(e.ChildText("title"))
		// Strip " | Con Edison" or similar suffixes
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

	// Skip navigation/generic pages.
	titleLower := strings.ToLower(programName)
	for _, p := range DefaultSkipPhrases {
		if strings.Contains(titleLower, p) {
			return nil
		}
	}

	description := CollyDescriptionMarkdown(e, programName, 1000)

	// Guard: if the description is just a copyright footer (JS-rendered page
	// with no static content), skip this page entirely.
	if isFooterOnlyDescription(description) {
		return nil
	}

	imageURL := CollyImageURL(e, "https://www.coned.com")

	// Full page text for regex extractions.
	// Clone and strip nav/header/footer so that navigation items like
	// "Electric Heating & Cooling" don't pollute category inference.
	contentDOM := e.DOM.Clone()
	contentDOM.Find("nav, header, footer").Remove()
	pageText := contentDOM.Text()

	// Extract dollar amounts — only when incentive keywords are present on the page.
	format, amount := ParseAmountContextual(pageText)
	if format == "narrative" {
		// Also scan individual text nodes for amounts.
		e.ForEach("p, li, td, h2, h3", func(_ int, el *colly.HTMLElement) {
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

	// Rate/tariff pages and payment-assistance pages contain stray numbers
	// (rate schedule IDs, government benefit amounts) that are not Con Edison
	// incentive amounts. Override to narrative to avoid false positives.
	pageURLLower := strings.ToLower(pageURL)
	if strings.Contains(pageURLLower, "incentive-rate") ||
		strings.Contains(pageURLLower, "payment-plans-assistance") ||
		strings.Contains(pageURLLower, "help-paying") {
		format = "narrative"
		amount = nil
	}

	// Detect "up to" maximum amount.
	var maxAmount *float64
	if format == "dollar_amount" {
		_, upToAmt := ParseAmount(pageText)
		if upToAmt != nil && amount != nil && *upToAmt > *amount {
			maxAmount = upToAmt
		}
	}

	// Extract application URL — first link containing "apply"/"application"/etc.
	// Skip telecom/business-partner portal hrefs that match spuriously.
	applicationURL := ""
	e.ForEach("a[href]", func(_ int, el *colly.HTMLElement) {
		if applicationURL != "" {
			return
		}
		href := el.Attr("href")
		hrefLower := strings.ToLower(href)
		// Reject non-program URLs: account portals, generic dashboards, and
		// telecom/business-partner links that match enrollment keywords spuriously.
		if strings.Contains(hrefLower, "/business-partners/") ||
			strings.Contains(hrefLower, "telecom") ||
			strings.Contains(hrefLower, "/my-account") ||
			strings.Contains(hrefLower, "/dashboard") ||
			strings.Contains(hrefLower, "manage-my-account") ||
			strings.Contains(hrefLower, "sectionid=") {
			return
		}
		text := strings.ToLower(el.Text + href)
		if strings.Contains(text, "apply") || strings.Contains(text, "application") ||
			strings.Contains(text, "enroll") || strings.Contains(text, "sign up") {
			if strings.HasPrefix(href, "http") {
				applicationURL = href
			} else if strings.HasPrefix(href, "/") {
				applicationURL = "https://www.coned.com" + href
			}
		}
	})

	// ── Boolean / structured field extraction (from html_helpers.go) ────────
	contractorRequired := extractContractorRequired(pageText)
	energyAuditRequired := extractEnergyAuditRequired(pageText + " " + description)
	// Rate/economic-development pages mention "energy rebates" in eligibility
	// criteria but don't actually require an energy audit — clear false positives.
	if energyAuditRequired != nil && strings.Contains(strings.ToLower(pageURL), "economic-development") {
		energyAuditRequired = nil
	}
	customerType := extractCustomerTypeWithBody(pageURL+" "+programName, pageText)
	// Bill-assistance and payment-plans pages are always residential.
	if customerType == "" {
		lower := strings.ToLower(pageURL)
		if strings.Contains(lower, "payment-plans-assistance") || strings.Contains(lower, "help-paying") {
			customerType = "Residential"
		}
	}
	startDate := extractStartDate(pageText)
	endDate := extractEndDate(pageText)

	// Contact info.
	contactPhone := extractPhone(pageText)
	contactEmail := extractEmail(pageText)

	// Refine generic hub-page titles using the application URL path segment.
	// e.g. h1="Financial Assistance Programs" + applicationURL=".../energy-affordability-program"
	//      → programName="Energy Affordability Program"
	if applicationURL != "" && isGenericHubTitle(programName) {
		if refined := titleFromURLSlug(applicationURL); refined != "" {
			programName = refined
		}
	}

	// Infer category and segment from URL + title + body text.
	// categoryKeywords and segmentGroups include URL-path (hyphenated) variants
	// so passing the full URL is sufficient — no separate URL rule layer needed.
	inferText := pageURL + " " + strings.ToLower(programName) + " " + strings.ToLower(pageText[:min(len(pageText), 8000)])
	categories := inferCategories(inferText)
	// Rate/tariff pages have no incentive category — URL keywords like
	// "commercial-industrial" would otherwise tag them as Energy Efficiency.
	if strings.Contains(pageURLLower, "incentive-rate") {
		categories = nil
	}
	// AI fallback: when keyword inference finds nothing, use the embedding+LLM
	// inferrer (same pattern as ExtractPageGoquery). This handles program types
	// not yet covered by categoryKeywords without requiring taxonomy edits.
	if len(categories) == 0 && s.CategoryInferrer != nil {
		if tags, err := s.CategoryInferrer.Infer(programName); err == nil {
			categories = tags
		}
	}
	segments := inferSegments(pageURL+" "+programName, pageText)
	if len(segments) == 0 && s.SegmentInferrer != nil {
		if segs, err := s.SegmentInferrer.Infer(programName, description); err == nil {
			segments = segs
		}
	}

	// Build stable ID.
	id := models.DeterministicID(conEdisonSourceName, pageURL)

	// Determine incentive format — fall back to narrative if unknown.
	if format == "" {
		format = "narrative"
	}

	inc := models.NewIncentive(conEdisonSourceName, s.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = conEdisonUtility
	inc.State = models.PtrString(conEdisonState)
	inc.ZipCode = models.PtrString(conEdisonZIP)
	inc.ServiceTerritory = models.PtrString(conEdisonTerritory)
	inc.IncentiveDescription = models.PtrString(description)
	if imageURL != "" {
		inc.ImageURL = models.PtrString(imageURL)
	}
	inc.IncentiveFormat = models.PtrString(format)
	inc.ImplementingSector = models.PtrString("Utility")
	inc.ApplicationProcess = models.PtrString(conEdisonDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.SourceURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.Segment = segments
	inc.ProgramHash = models.ComputeProgramHash(programName, conEdisonUtility)

	if amount != nil {
		if format == "percent" {
			inc.PercentValue = amount
		} else {
			inc.IncentiveAmount = amount
		}
	}
	if maxAmount != nil {
		inc.MaximumAmount = maxAmount
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

	// Extract rate tiers from Con Edison's "Bill Breakdown by Time Period" tables
	// (e.g. Smart Energy Plan peak/off-peak cost comparison).
	// When tiers are present, override format to "tiered" so the UI renders them.
	if tiers := extractConEdisonRateTiers(e); len(tiers) > 0 {
		inc.RateTiers = tiers
		inc.IncentiveFormat = models.PtrString("tiered")
	}

	return &inc
}

// isFooterOnlyDescription returns true when the extracted description is just
// a copyright footer — a signal that the page body is JavaScript-rendered and
// Colly only captured static boilerplate.
// isGenericHubTitle returns true when the title is a section header rather
// than a specific program name — typically ends with "Programs", "Assistance",
// "Services", "Solutions", "Overview", or "Options".
// conEdisonChildProgramLinks returns URLs of program pages that are directly
// linked from e and are sub-paths of pageURL. These pages may not appear in
// the sitemap (Con Edison often lists only parent hub pages there) but contain
// the actual rebate details. The caller should queue them for scraping.
func conEdisonChildProgramLinks(e *colly.HTMLElement, pageURL string) []string {
	base := pageURL
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, "/")

	seen := make(map[string]bool)
	var links []string
	e.ForEach("a[href]", func(_ int, el *colly.HTMLElement) {
		href := el.Attr("href")
		var full string
		switch {
		case strings.HasPrefix(href, "https://www.coned.com"):
			full = href
		case strings.HasPrefix(href, "/"):
			full = "https://www.coned.com" + href
		default:
			return
		}
		// Normalise: strip query and fragment.
		if i := strings.Index(full, "?"); i >= 0 {
			full = full[:i]
		}
		if i := strings.Index(full, "#"); i >= 0 {
			full = full[:i]
		}
		full = strings.TrimRight(full, "/")

		if full == base || seen[full] {
			return
		}
		// Must be a direct sub-path (one level deeper than the current page).
		if !strings.HasPrefix(full, base+"/") {
			return
		}
		// No further slashes after the extra segment → direct child only.
		remainder := full[len(base)+1:]
		if strings.Contains(remainder, "/") {
			return
		}
		// Must pass the URL filter (exclusions checked first, then inclusions).
		if len(FilterSitemapURLs([]string{full}, conEdisonFilterCfg)) == 0 {
			return
		}
		seen[full] = true
		links = append(links, full)
	})
	return links
}

// extractConEdisonRateTiers reads the "Bill Breakdown by Time Period" data table
// that Con Edison renders on rate-comparison program pages (e.g. Smart Energy Plan).
//
// Table structure:
//
//	<tr class="data-table__row">
//	  <td>…<p class="data-table__column-text">SUMMER PEAK</p></td>   ← period label
//	  <td>…<p class="data-table__column-mobile">With SIMULTANEOUS Use: 4KW</p>
//	       <p class="data-table__column-text">$122.72</p></td>       ← usage + amount
//	  <td>…<p class="data-table__column-mobile">With STAGGERED Use: 3KW</p>
//	       <p class="data-table__column-text">$92.04</p></td>
//	</tr>
func extractConEdisonRateTiers(e *colly.HTMLElement) []models.RateTier {
	var tiers []models.RateTier
	e.ForEach("tr.data-table__row", func(_ int, row *colly.HTMLElement) {
		// First td → time-period label (Summer Peak, Off-Peak, etc.)
		period := strings.TrimSpace(
			row.DOM.Find("td.data-table__column").First().
				Find("p.data-table__column-text").Text(),
		)
		if period == "" {
			return
		}
		// Each subsequent td → usage scenario label + dollar amount
		row.ForEach("td.data-table__column", func(j int, cell *colly.HTMLElement) {
			if j == 0 {
				return // skip the period column
			}
			usage := strings.TrimSpace(cell.ChildText("p.data-table__column-mobile"))
			raw := strings.TrimSpace(cell.ChildText("p.data-table__column-text"))
			raw = strings.TrimPrefix(raw, "$")
			raw = strings.ReplaceAll(raw, ",", "")
			amt, err := strconv.ParseFloat(raw, 64)
			if err != nil || amt == 0 {
				return
			}
			desc := period
			if usage != "" {
				desc += " – " + usage
			}
			tiers = append(tiers, models.RateTier{
				ID:          conEdisonTierID(period, usage),
				Description: desc,
				Amount:      amt,
				Unit:        "dollar",
			})
		})
	})
	return tiers
}

// conEdisonTierID builds a stable lowercase ID from rate tier label parts.
func conEdisonTierID(parts ...string) string {
	raw := strings.ToLower(strings.Join(parts, "_"))
	raw = strings.NewReplacer(
		" ", "_", ":", "", "/", "_",
		"(", "", ")", "", "&", "_", ".", "",
	).Replace(raw)
	for strings.Contains(raw, "__") {
		raw = strings.ReplaceAll(raw, "__", "_")
	}
	return strings.Trim(raw, "_")
}

func isGenericHubTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	for _, suffix := range []string{
		"programs", "assistance", "services", "solutions",
		"overview", "options", "resources",
	} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// titleFromURLSlug converts the last meaningful path segment of a URL into a
// title-cased program name.
// e.g. ".../energy-affordability-program" → "Energy Affordability Program"
// Returns "" when the segment looks like an admin page (my-account, login, etc.)
func titleFromURLSlug(rawURL string) string {
	// Strip query string.
	if idx := strings.Index(rawURL, "?"); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	rawURL = strings.TrimRight(rawURL, "/")
	parts := strings.Split(rawURL, "/")
	skipSegments := map[string]bool{
		"my-account": true, "manage-my-account": true, "login": true,
		"sign-in": true, "enroll": true, "apply": true,
		"application": true, "dashboard": true, "account": true,
	}
	for i := len(parts) - 1; i >= 0; i-- {
		seg := parts[i]
		if len(seg) < 5 || skipSegments[seg] {
			continue
		}
		words := strings.Split(seg, "-")
		titled := make([]string, 0, len(words))
		for _, w := range words {
			if len(w) > 0 {
				titled = append(titled, strings.ToUpper(w[:1])+w[1:])
			}
		}
		return strings.Join(titled, " ")
	}
	return ""
}

func isFooterOnlyDescription(desc string) bool {
	if len(desc) == 0 {
		return true
	}
	lower := strings.ToLower(desc)
	footerPhrases := []string{
		"consolidated edison company of new york",
		"all rights reserved",
		"© 20",
		"copyright 20",
	}
	for _, p := range footerPhrases {
		if strings.Contains(lower, p) && len(desc) < 200 {
			return true
		}
	}
	return false
}


func (s *ConEdisonScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	s.CollyBase.ProxyURL = s.ProxyURL
	return s.CollyBase.NewHTTPClient(30 * time.Second)
}

func (s *ConEdisonScraper) newCollector(domain string) *colly.Collector {
	s.CollyBase.AllowedDomain = domain
	s.CollyBase.Parallelism = 2
	s.CollyBase.Delay = 600 * time.Millisecond
	s.CollyBase.Logger = s.Logger
	s.CollyBase.ProxyURL = s.ProxyURL
	return s.CollyBase.NewCollector()
}
