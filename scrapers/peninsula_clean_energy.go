// peninsula_clean_energy.go — Peninsula Clean Energy (PCE) rebate scraper.
//
// Peninsula Clean Energy is the Community Choice Aggregator (CCA) for San
// Mateo County, CA.  Rebate pages are spread across four sitemaps:
//
//   - https://www.peninsulacleanenergy.com/page-sitemap.xml
//   - https://www.peninsulacleanenergy.com/post-sitemap.xml
//   - https://www.peninsulacleanenergy.com/news-releases-sitemap.xml
//   - https://www.peninsulacleanenergy.com/articles-sitemap1.xml
//
// URL filtering mirrors the exclusion-first two-pass logic from the SmythOS
// rf-crawler-pnm-srp-coned-xcel-peninsul LLM prompt for PCE.
//
// Source defaults:
//   - Source:           "peninsula_clean_energy"
//   - State:            CA
//   - UtilityCompany:   "Peninsula Clean Energy"
//   - ServiceTerritory: "San Mateo County and Los Banos"
//   - ZipCode:          "94025"  (Menlo Park — PCE headquarters ZIP)
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
	pceState         = "CA"
	pceUtility       = "Peninsula Clean Energy"
	pceTerritory     = "San Mateo County and Los Banos"
	pceZIP           = "94025"
	pceSourceName    = "peninsula_clean_energy"
	pceDefaultApply  = "Visit the official Peninsula Clean Energy website to learn about eligibility requirements and submit your application."
	pceDomain        = "www.peninsulacleanenergy.com"
	pceBaseURL       = "https://www.peninsulacleanenergy.com"
)

// pceSitemapURLs lists all four sitemaps PCE publishes.
var pceSitemapURLs = []string{
	pceBaseURL + "/page-sitemap.xml",
	pceBaseURL + "/post-sitemap.xml",
	pceBaseURL + "/news-releases-sitemap.xml",
	pceBaseURL + "/articles-sitemap1.xml",
}

// pceFilterCfg mirrors the PCE URL decision logic from the SmythOS crawler
// LLM prompt. Exclusion-first: corporate/admin patterns are checked before
// any inclusion keyword. Blog posts (post-sitemap, news-releases, articles)
// are only included if they explicitly contain "rebate" or "incentive".
var pceFilterCfg = FilterConfig{
	// ── Exclusions (checked first) ─────────────────────────────────────────
	ExcludeKeywords: []string{
		// Corporate / governance
		"/about-us/",
		"/about/",
		"/careers/",
		"/board-of-directors/",
		"/regulatory-filings/",
		"/annual-reports/",
		"/strategic-plan/",
		"/financial/",
		"/board-meetings/",
		"/advisory-committee/",
		"/staff/",
		"/leadership/",

		// Contact / generic
		"/contact-us/",
		"/contact/",
		"/faq/",
		"/sitemap",
		"/privacy",
		"/terms",

		// Case studies / qualifications / technical docs (not programs)
		"/case-studies/",
		"/case-study/",
		"/qualifications/",
		"/design-guidance-",
		"/installation-guidelines/",
		"/technical-resources/",
		"/spec-sheets/",

		// Solar billing / net metering (informational, not rebate programs)
		"/solar-rates/",
		"/solar-billing-plan/",
		"/net-energy-metering/",
		"/nem/",

		// Non-English / locale variants
		"/es/",
		"/zh-tw/",
		"/zh/",
		"/fl/",
		"/tl/",

		// Procurement / business operations
		"/procurement/",
		"/rfp/",
		"/supplier/",
		"/vendor/",
		"/bidding/",

		// Events / press
		"/events/",
		"/press-releases/",
		"/media/",
		"/newsletter/",
		"/community-updates/",
	},

	// ── Inclusions ─────────────────────────────────────────────────────────
	// At least one must match after exclusion check passes.
	// The key hubs are /rebates-offers/ and /rebates-offers-business/ — every
	// subpage is included. Blog/news posts are only included if they explicitly
	// mention rebate or incentive.
	IncludeKeywords: []string{
		// Primary rebate hubs (residential and business)
		"/rebates-offers/",
		"/rebates-offers-business/",

		// Home upgrade services hub
		"/home-upgrade-services/",
		"/home-upgrades/",

		// Public organization programs
		"/public-organization/",
		"/multifamily/",
		"/affordable-housing/",

		// Financing hub
		"/financing/",
		"/clean-energy-financing/",
		"/pace/",
		"/hero/",

		// Generic rebate / incentive keywords (for blog posts)
		"rebate",
		"incentive",
		"discount",
		"credit",
		"savings",
		"assistance",
		"income-qualified",
		"low-income",
		"affordable",

		// Equipment programs
		"heat-pump",
		"hvac",
		"water-heater",
		"appliance",
		"thermostat",
		"weatheriz",
		"insulation",
		"lighting",
		"efficiency",
		"upgrade",
		"electrification",

		// Clean energy
		"solar",
		"battery",
		"storage",
		"ev",
		"electric-vehicle",
		"charging",
		"renewable",

		// EV
		"ev-charging",
		"car-charging",
	},
}

// pceSeedURLs are well-known PCE rebate pages used as fallback.
func pceSeedURLs() []string {
	return []string{
		pceBaseURL + "/residential/rebates-offers/",
		pceBaseURL + "/business/rebates-offers-business/",
		pceBaseURL + "/residential/home-upgrade-services/",
		pceBaseURL + "/residential/financing/",
		pceBaseURL + "/public-organization/",
		pceBaseURL + "/multifamily/",
	}
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// PeninsulaCleanEnergyScraper discovers and scrapes rebate programs from
// peninsulacleanenergy.com.
type PeninsulaCleanEnergyScraper struct {
	CollyBase
	ScraperVersion string
	Logger         *zap.Logger
	HTTPClient     *http.Client
}

// Name implements Scraper.
func (s *PeninsulaCleanEnergyScraper) Name() string { return pceSourceName }

// Scrape implements Scraper.
func (s *PeninsulaCleanEnergyScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	client := s.httpClient()

	// Step 1: discover rebate URLs from all four sitemaps.
	var allURLs []string
	for si, sitemapURL := range pceSitemapURLs {
		fetched, err := FetchSitemapURLs(ctx, client, sitemapURL)
		if err != nil {
			s.Logger.Warn("peninsula_clean_energy: sitemap fetch failed",
				zap.String("sitemap", sitemapURL), zap.Error(err))
			continue
		}
		s.Logger.Info("peninsula_clean_energy: sitemap fetched",
			zap.Int("sitemap_index", si+1),
			zap.Int("sitemap_total", len(pceSitemapURLs)),
			zap.String("sitemap", sitemapURL),
			zap.Int("urls_found", len(fetched)),
		)
		allURLs = append(allURLs, fetched...)
	}

	var urls []string
	if len(allURLs) == 0 {
		s.Logger.Warn("peninsula_clean_energy: no URLs from sitemaps, using seed URLs")
		urls = pceSeedURLs()
	} else {
		urls = FilterSitemapURLs(allURLs, pceFilterCfg)
		s.Logger.Info("peninsula_clean_energy: sitemap discovery",
			zap.Int("sitemap_total", len(allURLs)),
			zap.Int("passed_filter", len(urls)),
		)
		if len(urls) == 0 {
			s.Logger.Warn("peninsula_clean_energy: no URLs passed filter, using seed URLs")
			urls = pceSeedURLs()
		}
	}

	s.Logger.Info("peninsula_clean_energy: scraping URLs", zap.Int("count", len(urls)))

	// Step 2: visit each page and extract incentive data.
	seen := make(map[string]bool)
	var all []models.Incentive

	pdfOpts := PDFIncentiveOpts{
		Source:         pceSourceName,
		ScraperVersion: s.ScraperVersion,
		UtilityCompany: pceUtility,
		State:          pceState,
		ZipCode:        pceZIP,
		Territory:      pceTerritory,
		DefaultApply:   pceDefaultApply,
	}

	c := s.newCollector(pceDomain)

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
		s.Logger.Info("peninsula_clean_energy: program found",
			zap.String("name", inc.ProgramName),
			zap.Strings("categories", inc.CategoryTag),
			zap.Int("total_so_far", len(all)),
		)
	})

	total := len(urls)
	bar := NewProgressBar(total, "peninsula_clean_energy")
	for i, u := range urls {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		s.Logger.Info("peninsula_clean_energy: visiting URL",
			zap.Int("i", i+1),
			zap.Int("total", total),
			zap.String("url", u),
		)
		if IsPDFURL(u) {
			text, err := ExtractPDFPages(u, nil)
			if err != nil {
				s.Logger.Warn("peninsula_clean_energy: pdf extract failed", zap.String("url", u), zap.Error(err))
				continue
			}
			inc := ExtractIncentiveFromPDFText(text, u, pdfOpts)
			if inc != nil && !seen[inc.ID] {
				seen[inc.ID] = true
				all = append(all, *inc)
				s.Logger.Info("peninsula_clean_energy: program found (pdf)",
					zap.String("name", inc.ProgramName),
					zap.Int("total_so_far", len(all)),
				)
			}
			continue
		}
		if err := c.Visit(u); err != nil {
			s.Logger.Warn("peninsula_clean_energy: visit failed",
				zap.String("url", u), zap.Error(err))
		}
		bar.Add(1) //nolint:errcheck
	}
	bar.Finish() //nolint:errcheck

	s.Logger.Info("peninsula_clean_energy: scrape complete", zap.Int("programs", len(all)))
	return all, nil
}

// extractPage extracts a single Incentive from a PCE rebate page.
// Returns nil if the page doesn't look like a meaningful incentive program.
func (s *PeninsulaCleanEnergyScraper) extractPage(e *colly.HTMLElement, pageURL string) *models.Incentive {
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
		// Strip " – Peninsula Clean Energy" suffix.
		if idx := strings.Index(programName, " – "); idx > 0 {
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
				applicationURL = pceBaseURL + href
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
	id := models.DeterministicID(pceSourceName, pageURL)

	if format == "" {
		format = "narrative"
	}

	inc := models.NewIncentive(pceSourceName, s.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = pceUtility
	inc.State = models.PtrString(pceState)
	inc.ZipCode = models.PtrString(pceZIP)
	inc.ServiceTerritory = models.PtrString(pceTerritory)
	inc.IncentiveDescription = models.PtrString(description)
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(pceDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, pceUtility)

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

func (s *PeninsulaCleanEnergyScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *PeninsulaCleanEnergyScraper) newCollector(domain string) *colly.Collector {
	s.CollyBase.AllowedDomain = domain
	s.CollyBase.Parallelism = 2
	s.CollyBase.Delay = 600 * time.Millisecond
	s.CollyBase.Logger = s.Logger
	return s.CollyBase.NewCollector()
}
