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

	"github.com/gocolly/colly/v2"
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
	HTTPClient     *http.Client
}

// Name implements Scraper.
func (s *PNMScraper) Name() string { return pnmSourceName }

// Scrape implements Scraper.
func (s *PNMScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	client := s.httpClient()

	// Step 1: discover rebate URLs from sitemap.
	// PNM uses a sitemap index; some child sitemaps return "Access Denied" HTML
	// which FetchSitemapURLs silently skips.
	allURLs, err := FetchSitemapURLs(ctx, client, pnmSitemapURL)
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

	s.Logger.Info("pnm: scraping URLs", zap.Int("count", len(urls)))

	// Step 2: split PDF vs HTML URLs, then scrape each.
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

	c := s.newCollector("www.pnm.com", "pnm.clearesult.com")

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
				s.Logger.Warn("pnm: pdf extract failed", zap.String("url", u), zap.Error(err))
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
			s.Logger.Warn("pnm: visit failed",
				zap.String("url", u), zap.Error(err))
		}
	}

	s.Logger.Info("pnm: scrape complete", zap.Int("programs", len(all)))
	return all, nil
}

// extractPage extracts a single Incentive from a PNM rebate page.
func (s *PNMScraper) extractPage(e *colly.HTMLElement, pageURL string) *models.Incentive {
	// Page title.
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

	// Full page text for all regex extractions.
	pageText := e.Text

	// Amount extraction — PNM often shows "Save $X" or "$X rebate".
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
				applicationURL = "https://www.pnm.com" + href
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
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(pnmDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, pnmUtility)

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

func (s *PNMScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *PNMScraper) newCollector(domains ...string) *colly.Collector {
	if len(domains) > 0 {
		s.CollyBase.AllowedDomain = domains[0]
	}
	s.CollyBase.Parallelism = 2
	s.CollyBase.Delay = 600 * time.Millisecond
	s.CollyBase.Logger = s.Logger

	c := s.CollyBase.NewCollector()
	// Allow additional domains (clearesult portal).
	for _, d := range domains[1:] {
		c.AllowedDomains = append(c.AllowedDomains, d)
	}
	return c
}
