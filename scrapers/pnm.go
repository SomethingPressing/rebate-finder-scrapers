// Utility
// pnm.go — PNM (Public Service Company of New Mexico) rebate scraper.
//
// Discovers rebate pages via the PNM sitemap, then visits each page and
// extracts structured incentive data using HTML selectors and regex.
//
// URL filtering mirrors the two-pass (exclusion-first, then inclusion) logic
// from the SmythOS rf-crawler-pnm-srp-coned-xcel-peninsul LLM prompt.
// PNM uses a sitemap index structure and some child sitemaps return HTML
// "Access Denied" pages — FetchSitemapURLs handles this gracefully.
//
// Source defaults:
//   - Source:           "pnm"
//   - State:            NM
//   - UtilityCompany:   "PNM"
//   - ServiceTerritory: "PNM Service Area"
//   - ZipCode:          "87102"  (Albuquerque — largest NM city)
package scrapers

import (
	"context"
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
	pnmSitemapURL   = "https://www.pnm.com/sitemap.xml"
	pnmState        = "NM"
	pnmUtility      = "PNM"
	pnmTerritory    = "PNM Service Area"
	pnmZIP          = "87102"
	pnmSourceName   = "pnm"
	pnmDefaultApply = "Visit the official PNM program website to learn about eligibility requirements and submit your application."
)

// pnmFilterCfg mirrors the two-pass URL decision logic from the SmythOS
// PNM crawler LLM prompt.  PNM is more inclusive than Xcel/ConEd — when in
// doubt, include.
var pnmFilterCfg = FilterConfig{
	// ── Exclusions (checked first) ─────────────────────────────────────────
	ExcludeKeywords: []string{
		// Corporate / company info
		"/about-pnm",
		"/about-us",
		"/corporate",
		"/investor",
		"/news",
		"/newsroom",
		"/press-release",
		"/careers",
		"/jobs",

		// Legal / regulatory
		"/regulatory",
		"/regulation",
		"/filings",
		"/tariffs",
		"/legal",
		"/terms",
		"/privacy",

		// Account management
		"/login",
		"/sign-in",
		"/my-account",

		// Notice / announcement pages (informational, not rebate programs)
		"/notice-",
		"/notice/",
		"/customer-notice",
		"/bill-insert",
		"/insert",

		// Operational / non-customer
		"/outages",
		"/outage-map",
		"/safety",
		"/storm",
		"/emergency",
		"/start-service",
		"/stop-service",
		"/move",
		"/pay-bill",
		"/payment-options",
		"/customer-service",

		// Content / media
		"/documents",
		"/media",
		"/multimedia",
		"/education",
		"/schools",
		"/community",

		// Infrastructure
		"/infrastructure",
		"/grid",
		"/generation",
		"/power-plants",
		"/transmission",
	},

	// ── Inclusions ─────────────────────────────────────────────────────────
	// At least one must match after exclusion check passes.
	// PNM prompt says "be inclusive" — include anything that helps customers
	// save money or get a rebate.
	IncludeKeywords: []string{
		// Main savings hub and rebates
		"save-money-and-energy",
		"save-money",
		"save-energy",
		"/save",
		"rebate",
		"incentive",
		"savings",
		"discount",

		// Energy efficiency programs
		"energy-efficiency",
		"checkup",
		"home-energy-checkup",
		"weatherization",
		"energy-audit",
		"quick-saver",

		// Equipment programs
		"appliance-recycling",
		"refrigerator-recycling",
		"smart-thermostat",
		"heat-pump",
		"water-heater",
		"evaporative-cooler",
		"swamp-cooler",
		"pool-pump",
		"lighting",

		// Solar & renewable
		"solar",
		"pnmskyblue",
		"sky-blue",
		"renewable-energy",
		"green-energy",
		"net-metering",

		// EV programs
		"/ev",
		"electric-vehicle",
		"ev-tax-credit",
		"charging",
		"ev-rates",

		// Financial assistance
		"goodneighborfund",
		"good-neighbor-fund",
		"assistance",
		"liheap",
		"low-income",
		"help-paying-bill",
		"energy-assistance",
		"payment-plan",
		"payment-arrangement",
		"budget-billing",

		// Rate programs with savings
		"time-of-use",
		"/tou",
		"demand-response",
		"peak-",
		"off-peak",
		"rate-options",
	},
}

// pnmSeedURLs are well-known PNM rebate pages used as fallback.
func pnmSeedURLs() []string {
	return []string{
		"https://www.pnm.com/save-money-and-energy",
		"https://www.pnm.com/residential-rebates",
		"https://www.pnm.com/checkup",
		"https://www.pnm.com/goodneighborfund",
		"https://www.pnm.com/pnmskyblue",
		"https://www.pnm.com/residential-energy-efficiency",
		"https://www.pnm.com/electric-vehicles",
		"https://www.pnm.com/appliance-recycling",
		"https://pnm.clearesult.com/",
	}
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// PNMScraper discovers and scrapes rebate programs from pnm.com.
type PNMScraper struct {
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

// pnmExtractCfg is the shared goquery extraction config for PNM.
var pnmExtractCfg = PageExtractConfig{
	Source:         pnmSourceName,
	UtilityCompany: pnmUtility,
	State:          pnmState,
	ZipCode:        pnmZIP,
	Territory:      pnmTerritory,
	DefaultApply:   pnmDefaultApply,
	BaseURL:        "https://www.pnm.com",
	// PNM's CMS injects nav-link text into the h1 on some pages ("Navigation …").
	// "navigation" is added here so ExtractPageGoquery (used by the evaluator)
	// rejects those pages the same way extractPage does via TrimPrefix.
	SkipPhrases: append(append([]string{}, DefaultSkipPhrases...), "navigation"),
}

// Name implements Scraper.
func (s *PNMScraper) Name() string { return pnmSourceName }

// Scrape implements Scraper.
func (s *PNMScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	client := s.httpClient()

	// Lazy browser — only started if a permission error is encountered.
	getBF, cleanup := lazyBrowser(s.Logger)
	defer cleanup()

	// Step 1: discover rebate URLs from sitemap.
	// PNM uses a sitemap index; some child sitemaps return "Access Denied" HTML
	// which FetchSitemapURLs silently skips.  Permission errors trigger the
	// headless-browser fallback automatically.
	allURLs, err := sitemapWithFallback(ctx, client, pnmSitemapURL, getBF, s.Logger, "pnm")
	var urls []string
	if err != nil || len(allURLs) == 0 {
		if err != nil {
			s.Logger.Warn("pnm: sitemap fetch failed, using seed URLs", zap.Error(err))
		}
		urls = pnmSeedURLs()
	} else {
		urls = FilterSitemapURLs(allURLs, pnmFilterCfg)
		s.Logger.Info("pnm: sitemap discovery",
			zap.Int("sitemap_total", len(allURLs)),
			zap.Int("passed_filter", len(urls)),
		)
		if len(urls) == 0 {
			urls = pnmSeedURLs()
		}
	}

	if s.Limit > 0 && len(urls) > s.Limit {
		urls = urls[:s.Limit]
	}
	s.Logger.Info("pnm: scraping URLs", zap.Int("count", len(urls)))

	seen := make(map[string]bool)
	var all []models.Incentive

	pdfOpts := PDFIncentiveOpts{
		Source:         pnmSourceName,
		ScraperVersion: s.ScraperVersion,
		UtilityCompany: pnmUtility,
		State:          pnmState,
		ZipCode:        pnmZIP,
		Territory:      pnmTerritory,
		DefaultApply:   pnmDefaultApply,
	}
	extractCfg := pnmExtractCfg
	extractCfg.ScraperVersion = s.ScraperVersion
	extractCfg.CategoryInferrer = s.CategoryInferrer
	extractCfg.SegmentInferrer = s.SegmentInferrer

	// Step 2: Colly-based HTML scraping with automatic 403-fallback.
	c := s.newCollector("www.pnm.com", "pnm.clearesult.com")
	permBlocked := trackPermissionErrors(c)

	c.OnHTML("html", func(e *colly.HTMLElement) {
		pageURL := e.Request.URL.String()

		// Discover child program pages linked from hub pages that may not be
		// in the sitemap (e.g. specific rebate pages nested under a hub).
		childLinks := pnmChildProgramLinks(e, pageURL)

		for _, child := range childLinks {
			_ = c.Visit(child)
		}

		// Hub guard: 2+ child links and no incentive amount → this is a
		// category/landing page; the children carry the real data.
		if len(childLinks) >= 2 {
			_, amt := ParseAmountContextual(e.Text)
			if amt == nil {
				s.Logger.Info("pnm: hub page skipped, children queued",
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
		s.Logger.Info("pnm: program found",
			zap.String("name", inc.ProgramName),
			zap.Strings("categories", inc.CategoryTag),
			zap.Int("total_so_far", len(all)),
		)
	})

	total := len(urls)
	bar := NewProgressBar(total, "pnm")
	for i, u := range urls {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		s.Logger.Info("pnm: visiting URL",
			zap.Int("i", i+1),
			zap.Int("total", total),
			zap.String("url", u),
		)
		if IsPDFURL(u) {
			text, err := ExtractPDFPages(u, nil)
			if err != nil {
				s.Logger.Warn("pnm: pdf extract failed", zap.String("url", u), zap.Error(err))
				continue
			}
			inc := ExtractIncentiveFromPDFText(text, u, pdfOpts)
			if inc != nil && !seen[inc.ID] {
				seen[inc.ID] = true
				all = append(all, *inc)
				s.Logger.Info("pnm: program found (pdf)",
					zap.String("name", inc.ProgramName),
					zap.Int("total_so_far", len(all)),
				)
			}
			continue
		}
		if err := c.Visit(u); err != nil {
			s.Logger.Warn("pnm: visit failed",
				zap.String("url", u), zap.Error(err))
		}
		bar.Add(1) //nolint:errcheck
	}
	bar.Finish() //nolint:errcheck

	// Step 3: retry any permission-blocked pages with the headless browser.
	retryBlockedWithBrowser(ctx, *permBlocked, getBF, extractCfg, seen, &all, s.Logger, "pnm")

	s.Logger.Info("pnm: scrape complete", zap.Int("programs", len(all)))
	return all, nil
}

// extractPage extracts a single Incentive from a PNM rebate page.
func (s *PNMScraper) extractPage(e *colly.HTMLElement, pageURL string) *models.Incentive {
	// PNM injects two hide-accessible h1 elements before the visible one:
	//   <h1 class="hide-accessible">Navigation</h1>
	//   <h1 class="hide-accessible">PNM Good Neighbor Fund</h1>
	//   <h1 style="...">PNM Good Neighbor Fund: ...</h1>  ← real title
	// Skip any h1 with class "hide-accessible" and use the first visible one.
	programName := ""
	e.DOM.Find("h1").Each(func(_ int, sel *goquery.Selection) {
		if programName != "" {
			return
		}
		if strings.Contains(sel.AttrOr("class", ""), "hide-accessible") {
			return
		}
		if text := strings.Join(strings.Fields(sel.Text()), " "); len(text) >= 5 {
			programName = text
		}
	})
	if programName == "" {
		// Fallback: <title> tag (strip site suffix).
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

	// Strip a subtitle following " : " or " — " (e.g. "Fund: Supporting Our Communities")
	// to keep the stored program name concise and matchable.
	if idx := strings.Index(programName, ": "); idx > 10 {
		programName = strings.TrimSpace(programName[:idx])
	}

	titleLower := strings.ToLower(programName)
	for _, p := range pnmExtractCfg.SkipPhrases {
		if strings.Contains(titleLower, p) {
			return nil
		}
	}

	description := CollyDescriptionMarkdown(e, programName, 1000)

	// Guard: JS-rendered pages where Colly only captured a copyright footer.
	if isFooterOnlyDescription(description) {
		return nil
	}

	imageURL := CollyImageURL(e, "https://www.pnm.com")

	// Strip nav/header/footer before extracting page text so navigation items
	// (e.g. "Save Money" hub labels) don't pollute category/amount inference.
	contentDOM := e.DOM.Clone()
	contentDOM.Find("nav, header, footer").Remove()
	pageText := contentDOM.Text()

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

	// Financial-assistance and bill-insert pages contain stray numbers (grant
	// totals, phone extensions, eligibility thresholds) that are not the
	// program's own incentive amount — override to narrative to avoid false positives.
	pageURLLower := strings.ToLower(pageURL)
	if strings.Contains(pageURLLower, "financial-assistance") ||
		strings.Contains(pageURLLower, "goodneighborfund") ||
		strings.Contains(pageURLLower, "good-neighbor-fund") ||
		strings.Contains(pageURLLower, "bill-insert") ||
		strings.Contains(pageURLLower, "insert") ||
		strings.Contains(pageURLLower, "tax-rebate-resources") {
		format = "narrative"
		amount = nil
	}

	// Detect "up to" maximum amount — scan description first (most reliable),
	// then fall back to full page text.
	var maxAmount *float64
	if format == "dollar_amount" {
		for _, src := range []string{description, pageText} {
			_, upToAmt := ParseAmount(src)
			if upToAmt != nil && amount != nil && *upToAmt > *amount {
				maxAmount = upToAmt
				break
			}
		}
	}
	if maxAmount == nil && format == "narrative" {
		if _, upToAmt := ParseAmount(description); upToAmt != nil {
			maxAmount = upToAmt
		}
	}

	// Application URL — skip Liferay portal login redirects and generic
	// account portals that match "apply" keywords spuriously.
	applicationURL := ""
	e.ForEach("a[href]", func(_ int, el *colly.HTMLElement) {
		if applicationURL != "" {
			return
		}
		href := el.Attr("href")
		hrefLower := strings.ToLower(href)
		if strings.Contains(hrefLower, "/c/portal/login") ||
			strings.Contains(hrefLower, "/my-account") ||
			strings.Contains(hrefLower, "/dashboard") {
			return
		}
		text := strings.ToLower(el.Text + " " + href)
		if strings.Contains(text, "apply") || strings.Contains(text, "application") ||
			strings.Contains(text, "submit") || strings.Contains(text, "enroll") {
			if strings.HasPrefix(href, "http") {
				applicationURL = href
			} else if strings.HasPrefix(href, "/") {
				applicationURL = "https://www.pnm.com" + href
			}
		}
	})

	// Refine generic hub-page titles using the application URL path segment.
	if applicationURL != "" && isGenericHubTitle(programName) {
		if refined := titleFromURLSlug(applicationURL); refined != "" {
			programName = refined
		}
	}

	// ── Boolean / structured field extraction (from html_helpers.go) ────────
	contractorRequired := extractContractorRequired(pageText)
	energyAuditRequired := extractEnergyAuditRequired(pageText + " " + description)
	customerType := extractCustomerTypeWithBody(pageURL+" "+programName, pageText)
	startDate := extractStartDate(pageText)
	endDate := extractEndDate(pageText)

	// Contact info.
	contactPhone := extractPhone(pageText)
	contactEmail := extractEmail(pageText)

	// Infer category from URL + title + description only — using the full body
	// risks over-categorization when hub pages mention many related programs.
	inferText := pageURL + " " + strings.ToLower(programName) + " " + strings.ToLower(description)
	categories := inferCategories(inferText)
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

	// Income-qualified assistance programs (Good Neighbor Fund, LIHEAP, etc.)
	// are always residential — default customer_type when keyword extraction
	// finds nothing and the category is already determined.
	if customerType == "" {
		for _, cat := range categories {
			if strings.EqualFold(cat, "Income Qualified") {
				customerType = "Residential"
				break
			}
		}
	}

	if format == "" {
		format = "narrative"
	}

	id := models.DeterministicID(pnmSourceName, pageURL)

	inc := models.NewIncentive(pnmSourceName, s.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = pnmUtility
	inc.State = models.PtrString(pnmState)
	inc.ZipCode = models.PtrString(pnmZIP)
	inc.ServiceTerritory = models.PtrString(pnmTerritory)
	inc.IncentiveDescription = models.PtrString(description)
	if imageURL != "" {
		inc.ImageURL = models.PtrString(imageURL)
	}
	inc.IncentiveFormat = models.PtrString(format)
	inc.ImplementingSector = models.PtrString("Utility")
	inc.ApplicationProcess = models.PtrString(pnmDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.SourceURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.Segment = segments
	inc.ProgramHash = models.ComputeProgramHash(programName, pnmUtility)

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

	return &inc
}

// pnmChildProgramLinks returns URLs of program pages directly linked from e
// that are sub-paths of pageURL. These may not appear in the PNM sitemap
// (PNM often lists only hub/landing pages there) but contain rebate details.
func pnmChildProgramLinks(e *colly.HTMLElement, pageURL string) []string {
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
		case strings.HasPrefix(href, "https://www.pnm.com"):
			full = href
		case strings.HasPrefix(href, "/"):
			full = "https://www.pnm.com" + href
		default:
			return
		}
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
		// Direct child only — one level deeper, no further slashes.
		if !strings.HasPrefix(full, base+"/") {
			return
		}
		remainder := full[len(base)+1:]
		if strings.Contains(remainder, "/") {
			return
		}
		// Must pass the URL filter.
		if len(FilterSitemapURLs([]string{full}, pnmFilterCfg)) == 0 {
			return
		}
		seen[full] = true
		links = append(links, full)
	})
	return links
}

func (s *PNMScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	s.CollyBase.ProxyURL = s.ProxyURL
	return s.CollyBase.NewHTTPClient(30 * time.Second)
}

func (s *PNMScraper) newCollector(_ ...string) *colly.Collector {
	s.CollyBase.Parallelism = 2
	s.CollyBase.Delay = 600 * time.Millisecond
	s.CollyBase.Logger = s.Logger
	s.CollyBase.ProxyURL = s.ProxyURL
	// Domain list is ignored — all domains are allowed globally so that
	// redirects to partner portals (e.g. pnm.clearesult.com) are followed.
	return s.CollyBase.NewCollector()
}
