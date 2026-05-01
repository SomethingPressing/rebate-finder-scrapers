// srp.go — Salt River Project (SRP) rebate and incentive scraper.
//
// Discovers rebate pages via the SRP sitemap, then visits each page
// and extracts structured incentive data using HTML selectors and regex.
//
// URL filtering mirrors the exclusion-first two-pass logic from the SmythOS
// rf-crawler-pnm-srp-coned-xcel-peninsul LLM prompt for SRP.
//
// Source defaults:
//   - Source:           "srp"
//   - State:            AZ
//   - UtilityCompany:   "Salt River Project"
//   - ServiceTerritory: "SRP Service Area"
//   - ZipCode:          "85001"  (Phoenix — representative AZ ZIP)
package scrapers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── Constants ────────────────────────────────────────────────────────────────

const (
	srpSitemapURL    = "https://www.srpnet.com/sitemap.xml"
	srpState         = "AZ"
	srpUtility       = "Salt River Project"
	srpTerritory     = "SRP Service Area"
	srpZIP           = "85001"
	srpSourceName    = "srp"
	srpDefaultApply  = "Visit the official Salt River Project website to learn about eligibility requirements and submit your application."
)

// srpFilterCfg mirrors the SRP URL decision logic from the SmythOS crawler
// LLM prompt. Exclusion-first: corporate/infrastructure patterns are checked
// before any inclusion keyword.
var srpFilterCfg = FilterConfig{
	// ── Exclusions (checked first) ─────────────────────────────────────────
	ExcludeKeywords: []string{
		// Business / trade
		"/doing-business/",
		"/trade-ally/",
		"/trade-allies/",

		// Corporate / company info
		"/about/",
		"/about-srp/",
		"/careers/",
		"/governance/",
		"/leadership/",
		"/board/",
		"/media/",
		"/news/",
		"/newsroom/",
		"/press/",
		"/investor/",
		"/environment/",
		"/community/",

		// Account / login
		"/account/",
		"/my-account/",
		"/login/",
		"/sign-in/",
		"/register/",

		// Contact / support
		"/contact-us",
		"/contact/",
		"/customer-service/",
		"/support/",

		// Infrastructure / operations
		"/grid-water-management/",
		"/water/",
		"/water-",
		"/irrigation/",
		"/transmission/",
		"/generation/",
		"/outages/",
		"/emergency/",
		"/safety/",
		"/tariff/",
		"/rate-schedule/",

		// Non-program content patterns
		"-workshop",
		"-audit",
		"-assessment",
		"-faq",
		"/faq",
		"/savings-tools",
		"/diy-",
		"/how-to-",
		"/tips/",
		"/blog/",
		"/glossary/",
		"/sitemap",
		"/privacy",
		"/terms",
		"/supplier/",
		"/vendor/",
	},

	// ── Inclusions ─────────────────────────────────────────────────────────
	// At least one must match after exclusion check passes.
	IncludeKeywords: []string{
		// Core rebate / incentive paths
		"/rebates/",
		"/rebate/",
		"/incentive/",
		"/incentives/",
		"/energy-savings-rebates/",
		"/energy-savings/",

		// Financial assistance
		"/financial-assistance",
		"/bill-assistance/",
		"/limited-income/",
		"/income-qualified/",
		"assistance",
		"affordable",
		"liheap",
		"budget-billing",

		// Savings programs
		"economy",
		"discount",
		"savings",
		"save",
		"credit",
		"refund",
		"cashback",
		"reward",
		"free",

		// Demand response / rate programs
		"demand-response",
		"time-of-use",
		"time-of-day",
		"peak",
		"demand-side",
		"load-management",
		"saver",

		// Equipment
		"heat-pump",
		"hvac",
		"thermostat",
		"appliance",
		"water-heater",
		"lighting",
		"weatheriz",
		"insulation",
		"efficiency",
		"upgrade",

		// Clean energy
		"solar",
		"battery",
		"storage",
		"electric-vehicle",
		"ev-charging",
		"ev/",
		"renewable",
	},
}

// srpSeedURLs are well-known SRP rebate pages used as fallback.
func srpSeedURLs() []string {
	return []string{
		"https://www.srpnet.com/energy-savings-rebates/home/rebates",
		"https://www.srpnet.com/energy-savings-rebates/business/rebates",
		"https://www.srpnet.com/customer-service/billing-payment/financial-assistance",
		"https://www.srpnet.com/energy-savings-rebates/home/electric-vehicles",
		"https://www.srpnet.com/energy-savings-rebates/home/solar",
		"https://www.srpnet.com/rates-and-power-quality/residential/time-of-use",
		"https://www.srpnet.com/rates-and-power-quality/residential/demand-management",
	}
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// SRPScraper discovers and scrapes rebate programs from srpnet.com.
type SRPScraper struct {
	CollyBase
	ScraperVersion string
	Logger         *zap.Logger
	HTTPClient     *http.Client // optional override for tests

	// ProxyURL routes sitemap fetches and Colly visits through a proxy.
	// Required when the target CDN (Cloudflare) blocks the server's IP range.
	// Format: "http://user:pass@host:port" or "socks5://host:port".
	// Env var: SCRAPER_PROXY_URL
	ProxyURL string

	// UseHeadlessBrowser, when true, uses a headless Chromium instance (via
	// go-rod) instead of Colly for page visits.  This bypasses Cloudflare's
	// TLS-fingerprint and JS-challenge protections that block data-center IPs.
	// Rod downloads Chromium automatically on first use (~150 MB, permanently
	// cached at ~/.cache/rod/browser/).
	// Env var: USE_HEADLESS_BROWSER=true
	UseHeadlessBrowser bool
}

// Name implements Scraper.
func (s *SRPScraper) Name() string { return srpSourceName }

// Scrape implements Scraper.
func (s *SRPScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	client := s.httpClient()

	// Step 1: discover rebate URLs from sitemap (fallback to seeds on error).
	allURLs, err := FetchSitemapURLs(ctx, client, srpSitemapURL)
	var urls []string
	if err != nil || len(allURLs) == 0 {
		if err != nil {
			s.Logger.Warn("srp: sitemap fetch failed, using seed URLs", zap.Error(err))
		}
		urls = srpSeedURLs()
	} else {
		urls = FilterSitemapURLs(allURLs, srpFilterCfg)
		s.Logger.Info("srp: sitemap discovery",
			zap.Int("sitemap_total", len(allURLs)),
			zap.Int("passed_filter", len(urls)),
		)
		if len(urls) == 0 {
			s.Logger.Warn("srp: no URLs passed filter, using seed URLs")
			urls = srpSeedURLs()
		}
	}

	s.Logger.Info("srp: scraping URLs", zap.Int("count", len(urls)))

	// Step 2: visit each page and extract incentive data.
	seen := make(map[string]bool)
	var all []models.Incentive

	pdfOpts := PDFIncentiveOpts{
		Source:         srpSourceName,
		ScraperVersion: s.ScraperVersion,
		UtilityCompany: srpUtility,
		State:          srpState,
		ZipCode:        srpZIP,
		Territory:      srpTerritory,
		DefaultApply:   srpDefaultApply,
	}

	total := len(urls)
	bar := NewProgressBar(total, "srp")

	// ── Headless browser path ────────────────────────────────────────────────
	// When UseHeadlessBrowser=true, skip Colly and use a real Chrome process.
	// This bypasses Cloudflare IP/TLS blocks that affect direct HTTP clients.
	if s.UseHeadlessBrowser {
		bf, err := NewBrowserFetcher(s.Logger, BrowserFetcherOpts{NoSandbox: true})
		if err != nil {
			s.Logger.Error("srp: headless browser init failed — falling back to Colly",
				zap.Error(err))
			// fall through to Colly path below
		} else {
			defer bf.Close()
			for i, u := range urls {
				select {
				case <-ctx.Done():
					bar.Finish() //nolint:errcheck
					return all, ctx.Err()
				default:
				}
				s.Logger.Info("srp: visiting URL (browser)",
					zap.Int("i", i+1),
					zap.Int("total", total),
					zap.String("url", u),
				)

				if IsPDFURL(u) {
					text, pdfErr := ExtractPDFPages(u, nil)
					if pdfErr != nil {
						s.Logger.Warn("srp: pdf extract failed", zap.String("url", u), zap.Error(pdfErr))
						bar.Add(1) //nolint:errcheck
						continue
					}
					inc := ExtractIncentiveFromPDFText(text, u, pdfOpts)
					if inc != nil && !seen[inc.ID] {
						seen[inc.ID] = true
						all = append(all, *inc)
						s.Logger.Info("srp: program found (pdf)",
							zap.String("name", inc.ProgramName),
							zap.Int("total_so_far", len(all)),
						)
					}
					bar.Add(1) //nolint:errcheck
					continue
				}

				html, fetchErr := bf.FetchHTML(ctx, u)
				if fetchErr != nil {
					s.Logger.Warn("srp: browser fetch failed",
						zap.String("url", u), zap.Error(fetchErr))
					bar.Add(1) //nolint:errcheck
					continue
				}

				doc, parseErr := goquery.NewDocumentFromReader(strings.NewReader(html))
				if parseErr != nil {
					s.Logger.Warn("srp: goquery parse failed",
						zap.String("url", u), zap.Error(parseErr))
					bar.Add(1) //nolint:errcheck
					continue
				}

				inc := s.extractPageDoc(doc, u)
				if inc != nil && !seen[inc.ID] {
					seen[inc.ID] = true
					all = append(all, *inc)
					s.Logger.Info("srp: program found (browser)",
						zap.String("name", inc.ProgramName),
						zap.Strings("categories", inc.CategoryTag),
						zap.Int("total_so_far", len(all)),
					)
				}
				bar.Add(1) //nolint:errcheck
			}
			bar.Finish() //nolint:errcheck
			s.Logger.Info("srp: scrape complete", zap.Int("programs", len(all)))
			return all, nil
		}
	}

	// ── Colly path (default) ─────────────────────────────────────────────────
	c := s.newCollector("www.srpnet.com")

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
		s.Logger.Info("srp: program found",
			zap.String("name", inc.ProgramName),
			zap.Strings("categories", inc.CategoryTag),
			zap.Int("total_so_far", len(all)),
		)
	})

	for i, u := range urls {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		s.Logger.Info("srp: visiting URL",
			zap.Int("i", i+1),
			zap.Int("total", total),
			zap.String("url", u),
		)
		if IsPDFURL(u) {
			text, err := ExtractPDFPages(u, nil)
			if err != nil {
				s.Logger.Warn("srp: pdf extract failed", zap.String("url", u), zap.Error(err))
				continue
			}
			inc := ExtractIncentiveFromPDFText(text, u, pdfOpts)
			if inc != nil && !seen[inc.ID] {
				seen[inc.ID] = true
				all = append(all, *inc)
				s.Logger.Info("srp: program found (pdf)",
					zap.String("name", inc.ProgramName),
					zap.Int("total_so_far", len(all)),
				)
			}
			continue
		}
		if err := c.Visit(u); err != nil {
			s.Logger.Warn("srp: visit failed",
				zap.String("url", u), zap.Error(err))
		}
		bar.Add(1) //nolint:errcheck
	}
	bar.Finish() //nolint:errcheck

	s.Logger.Info("srp: scrape complete", zap.Int("programs", len(all)))
	return all, nil
}

// extractPage extracts a single Incentive from an SRP rebate page.
// Returns nil if the page doesn't look like a meaningful incentive program.
func (s *SRPScraper) extractPage(e *colly.HTMLElement, pageURL string) *models.Incentive {
	// Extract page title (h1 first, then <title>).
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
	if len(description) > 500 {
		description = description[:497] + "..."
	}

	// Full page text for regex extractions.
	pageText := e.Text

	// Extract dollar amounts.
	format, amount := ParseAmount(pageText)
	if format == "narrative" {
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
				applicationURL = "https://www.srpnet.com" + href
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
	id := models.DeterministicID(srpSourceName, pageURL)

	if format == "" {
		format = "narrative"
	}

	inc := models.NewIncentive(srpSourceName, s.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = srpUtility
	inc.State = models.PtrString(srpState)
	inc.ZipCode = models.PtrString(srpZIP)
	inc.ServiceTerritory = models.PtrString(srpTerritory)
	inc.IncentiveDescription = models.PtrString(description)
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(srpDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, srpUtility)

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

// extractPageDoc is the goquery-based counterpart of extractPage.
// It is called from the headless-browser path where the HTML has been fetched
// by Rod and parsed into a *goquery.Document instead of a colly.HTMLElement.
// The extraction logic is identical — only the HTML access API differs.
func (s *SRPScraper) extractPageDoc(doc *goquery.Document, pageURL string) *models.Incentive {
	// ── Program name ──────────────────────────────────────────────────────────
	programName := strings.TrimSpace(doc.Find("h1").First().Text())
	if programName == "" {
		programName = strings.TrimSpace(doc.Find("title").First().Text())
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

	// Skip navigation / generic pages.
	titleLower := strings.ToLower(programName)
	for _, p := range []string{"page not found", "404", "error", "home", "login", "site map"} {
		if strings.Contains(titleLower, p) {
			return nil
		}
	}

	// ── Description ───────────────────────────────────────────────────────────
	description, _ := doc.Find(`meta[name="description"]`).Attr("content")
	description = strings.TrimSpace(description)
	if description == "" {
		doc.Find("p").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
			t := strings.TrimSpace(sel.Text())
			if len(t) > 40 {
				description = t
				return false // stop after first match
			}
			return true
		})
	}
	if description == "" {
		description = programName
	}
	if len(description) > 500 {
		description = description[:497] + "..."
	}

	// ── Full page text (for regex helpers) ───────────────────────────────────
	pageText := doc.Find("html").Text()

	// ── Amount parsing ────────────────────────────────────────────────────────
	format, amount := ParseAmount(pageText)
	if format == "narrative" {
		doc.Find("p, li, td, h2, h3").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
			f, a := ParseAmount(sel.Text())
			if f != "narrative" {
				format = f
				amount = a
				return false
			}
			return true
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

	// ── Application URL ───────────────────────────────────────────────────────
	applicationURL := ""
	doc.Find("a[href]").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		href, _ := sel.Attr("href")
		text := strings.ToLower(sel.Text() + href)
		if strings.Contains(text, "apply") || strings.Contains(text, "application") ||
			strings.Contains(text, "enroll") || strings.Contains(text, "sign up") {
			if strings.HasPrefix(href, "http") {
				applicationURL = href
				return false
			} else if strings.HasPrefix(href, "/") {
				applicationURL = "https://www.srpnet.com" + href
				return false
			}
		}
		return true
	})

	// ── Structured field extraction (string-based helpers from html_helpers.go) ─
	contractorRequired := extractContractorRequired(pageText)
	energyAuditRequired := extractEnergyAuditRequired(pageText)
	customerType := extractCustomerType(pageURL + " " + programName)
	startDate := extractStartDate(pageText)
	endDate := extractEndDate(pageText)
	contactPhone := extractPhone(pageText)
	contactEmail := extractEmail(pageText)
	categories := inferCategories(pageURL + " " + titleLower)

	id := models.DeterministicID(srpSourceName, pageURL)
	if format == "" {
		format = "narrative"
	}

	inc := models.NewIncentive(srpSourceName, s.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = srpUtility
	inc.State = models.PtrString(srpState)
	inc.ZipCode = models.PtrString(srpZIP)
	inc.ServiceTerritory = models.PtrString(srpTerritory)
	inc.IncentiveDescription = models.PtrString(description)
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(srpDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, srpUtility)

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

func (s *SRPScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	s.CollyBase.ProxyURL = s.ProxyURL
	return s.CollyBase.NewHTTPClient(30 * time.Second)
}

func (s *SRPScraper) newCollector(domain string) *colly.Collector {
	s.CollyBase.AllowedDomain = domain
	s.CollyBase.Parallelism = 2
	s.CollyBase.Delay = 600 * time.Millisecond
	s.CollyBase.Logger = s.Logger
	s.CollyBase.ProxyURL = s.ProxyURL
	return s.CollyBase.NewCollector()
}
