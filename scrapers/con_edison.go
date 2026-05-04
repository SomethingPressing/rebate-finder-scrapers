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
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
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
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-tax-credits-for-residential-customers",
		"https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-for-businesses",
		"https://www.coned.com/en/save-money/weatherization",
		"https://www.coned.com/en/save-money/heat-pumps",
		"https://www.coned.com/en/our-energy-future/electric-vehicles/power-ready-program",
		"https://www.coned.com/en/save-money/smart-usage-rewards",
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

	// Step 2: Colly-based HTML scraping with automatic 403-fallback.
	c := s.newCollector("www.coned.com")

	// Track any URLs blocked with a permission error — retried via browser below.
	permBlocked := trackPermissionErrors(c)

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
	skipPhrases := []string{"page not found", "404", "error", "home", "login", "site map"}
	for _, p := range skipPhrases {
		if strings.Contains(titleLower, p) {
			return nil
		}
	}

	// Extract meta description or first meaningful paragraph as description.
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
	// Truncate long descriptions.
	if len(description) > 500 {
		description = description[:497] + "..."
	}

	// Full page text for all regex extractions.
	pageText := e.Text

	// Extract dollar amounts.
	format, amount := ParseAmount(pageText)
	if format == "narrative" {
		// Also scan individual text nodes for amounts.
		e.ForEach("p, li, td, h2, h3", func(_ int, el *colly.HTMLElement) {
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

	// Detect "up to" maximum amount.
	var maxAmount *float64
	if format == "dollar_amount" {
		_, upToAmt := ParseAmount(pageText)
		if upToAmt != nil && amount != nil && *upToAmt > *amount {
			maxAmount = upToAmt
		}
	}

	// Extract application URL (first link containing "apply" or "application").
	applicationURL := ""
	e.ForEach("a[href]", func(_ int, el *colly.HTMLElement) {
		if applicationURL != "" {
			return
		}
		href := el.Attr("href")
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
	energyAuditRequired := extractEnergyAuditRequired(pageText)
	customerType := extractCustomerType(pageURL + " " + programName)
	startDate := extractStartDate(pageText)
	endDate := extractEndDate(pageText)

	// Contact info.
	contactPhone := extractPhone(pageText)
	contactEmail := extractEmail(pageText)

	// Infer category from URL and title.
	categories := inferCategories(pageURL + " " + strings.ToLower(programName))

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
	inc.Portfolio = []string{"Utility"}
	inc.State = models.PtrString(conEdisonState)
	inc.ZipCode = models.PtrString(conEdisonZIP)
	inc.ServiceTerritory = models.PtrString(conEdisonTerritory)
	inc.IncentiveDescription = models.PtrString(description)
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(conEdisonDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, conEdisonUtility)

	if amount != nil {
		inc.IncentiveAmount = amount
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

	return &inc
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
