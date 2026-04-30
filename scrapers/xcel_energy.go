// xcel_energy.go — Xcel Energy multi-state rebate and incentive scraper.
//
// Xcel Energy operates in Colorado, Minnesota, Texas, and other states.
// This scraper targets the three main state portals from the SAS agent:
// CO, MN, and WI (Texas Xcel operates as Southwestern Public Service and
// has its own brand separate from rebate discovery here).
//
// URL pattern: https://{state}.my.xcelenergy.com/
//   - CO: https://co.my.xcelenergy.com/
//   - MN: https://mn.my.xcelenergy.com/
//   - WI: https://wi.my.xcelenergy.com/
//
// Source defaults:
//   - Source:           "xcel_energy"
//   - UtilityCompany:   "Xcel Energy"
//   - State / Territory: extracted from the URL subdomain or page content
package scrapers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	xcelUtility      = "Xcel Energy"
	xcelSourceName   = "xcel_energy"
	xcelDefaultApply = "Visit the official Xcel Energy program website to learn about eligibility requirements and submit your application."
)

// xcelStateConfig maps state subdomain → (state abbr, service territory, representative ZIP).
var xcelStateConfig = map[string]struct {
	State       string
	Territory   string
	RepZIP      string
	SitemapURL  string
}{
	"co": {
		State:      "CO",
		Territory:  "Xcel Energy Colorado Service Area",
		RepZIP:     "80202",
		SitemapURL: "https://co.my.xcelenergy.com/sitemap.xml",
	},
	"mn": {
		State:      "MN",
		Territory:  "Xcel Energy Minnesota Service Area",
		RepZIP:     "55401",
		SitemapURL: "https://mn.my.xcelenergy.com/sitemap.xml",
	},
	"wi": {
		State:      "WI",
		Territory:  "Xcel Energy Wisconsin Service Area",
		RepZIP:     "53202",
		SitemapURL: "https://wi.my.xcelenergy.com/sitemap.xml",
	},
}

// xcelRebateKeywords are URL substrings that signal rebate/incentive content.
var xcelRebateKeywords = []string{
	"rebate", "incentive", "saving", "efficiency", "heat-pump",
	"thermostat", "electric-vehicle", "ev-charger", "solar",
	"weatheriz", "appliance", "lighting", "demand-response",
	"energy-saving", "rate-option", "bill-credit",
}

// xcelSeedURLs returns fallback URLs for a given state subdomain.
func xcelSeedURLs(stateSub string) []string {
	base := fmt.Sprintf("https://%s.my.xcelenergy.com", stateSub)
	return []string{
		base + "/s/energy-saving-programs",
		base + "/s/rebates-incentives",
		base + "/s/residential-rebates",
		base + "/s/business-rebates",
		base + "/s/electric-vehicles",
		base + "/s/renewable-energy",
	}
}

// ── Scraper ───────────────────────────────────────────────────────────────────

// XcelEnergyScraper discovers and scrapes rebate programs from xcelenergy.com
// across Colorado, Minnesota, and Wisconsin.
type XcelEnergyScraper struct {
	CollyBase
	ScraperVersion string
	Logger         *zap.Logger
	HTTPClient     *http.Client
	// States restricts scraping to specific state subdomains.
	// Defaults to ["co", "mn", "wi"] when empty.
	States []string
}

// Name implements Scraper.
func (s *XcelEnergyScraper) Name() string { return xcelSourceName }

// Scrape implements Scraper.
func (s *XcelEnergyScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	states := s.States
	if len(states) == 0 {
		states = []string{"co", "mn", "wi"}
	}

	client := s.httpClient()
	seen := make(map[string]bool)
	var all []models.Incentive

	for _, stateSub := range states {
		cfg, ok := xcelStateConfig[stateSub]
		if !ok {
			s.Logger.Warn("xcel_energy: unknown state subdomain", zap.String("state", stateSub))
			continue
		}

		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}

		stateIncentives := s.scrapeState(ctx, client, stateSub, cfg.State, cfg.Territory, cfg.RepZIP, cfg.SitemapURL, seen)
		all = append(all, stateIncentives...)

		s.Logger.Info("xcel_energy: state complete",
			zap.String("state", stateSub),
			zap.Int("programs", len(stateIncentives)),
		)
	}

	s.Logger.Info("xcel_energy: scrape complete", zap.Int("total_programs", len(all)))
	return all, nil
}

// scrapeState handles one state subdomain.
func (s *XcelEnergyScraper) scrapeState(
	ctx context.Context,
	client *http.Client,
	stateSub, state, territory, repZIP, sitemapURL string,
	seen map[string]bool,
) []models.Incentive {
	domain := fmt.Sprintf("%s.my.xcelenergy.com", stateSub)

	allURLs, err := FetchSitemapURLs(ctx, client, sitemapURL)
	var urls []string
	if err != nil || len(allURLs) == 0 {
		if err != nil {
			s.Logger.Warn("xcel_energy: sitemap failed, using seeds",
				zap.String("state", stateSub), zap.Error(err))
		}
		urls = xcelSeedURLs(stateSub)
	} else {
		urls = FilterSitemapURLs(allURLs, xcelRebateKeywords)
		if len(urls) == 0 {
			urls = xcelSeedURLs(stateSub)
		}
	}

	s.Logger.Info("xcel_energy: scraping state",
		zap.String("state", stateSub),
		zap.Int("urls", len(urls)),
	)

	var stateIncentives []models.Incentive

	c := s.newStateCollector(domain)

	c.OnHTML("html", func(e *colly.HTMLElement) {
		pageURL := e.Request.URL.String()
		inc := s.extractPage(e, pageURL, state, territory, repZIP)
		if inc == nil {
			return
		}
		if seen[inc.ID] {
			return
		}
		seen[inc.ID] = true
		stateIncentives = append(stateIncentives, *inc)
	})

	for _, u := range urls {
		select {
		case <-ctx.Done():
			return stateIncentives
		default:
		}
		if err := c.Visit(u); err != nil {
			s.Logger.Warn("xcel_energy: visit failed",
				zap.String("url", u), zap.Error(err))
		}
	}

	return stateIncentives
}

// extractPage extracts a single Incentive from an Xcel Energy rebate page.
func (s *XcelEnergyScraper) extractPage(
	e *colly.HTMLElement,
	pageURL, state, territory, repZIP string,
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

	// Service territory — override if content mentions a specific state area.
	effectiveTerritory := territory
	if strings.Contains(strings.ToLower(e.Text), "colorado") {
		effectiveTerritory = "Xcel Energy Colorado Service Area"
	} else if strings.Contains(strings.ToLower(e.Text), "minnesota") {
		effectiveTerritory = "Xcel Energy Minnesota Service Area"
	} else if strings.Contains(strings.ToLower(e.Text), "wisconsin") {
		effectiveTerritory = "Xcel Energy Wisconsin Service Area"
	}

	// Amount extraction.
	pageText := e.Text
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
			}
		}
	})

	contactPhone := extractPhone(pageText)
	contactEmail := extractEmail(pageText)
	categories := inferCategories(pageURL + " " + strings.ToLower(programName))

	if format == "" {
		format = "narrative"
	}

	id := models.DeterministicID(xcelSourceName, pageURL)

	inc := models.NewIncentive(xcelSourceName, s.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = xcelUtility
	inc.State = models.PtrString(state)
	inc.ZipCode = models.PtrString(repZIP)
	inc.ServiceTerritory = models.PtrString(effectiveTerritory)
	inc.IncentiveDescription = models.PtrString(description)
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(xcelDefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, xcelUtility)

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

	return &inc
}

func (s *XcelEnergyScraper) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *XcelEnergyScraper) newStateCollector(domain string) *colly.Collector {
	s.CollyBase.AllowedDomain = domain
	s.CollyBase.Parallelism = 2
	s.CollyBase.Delay = 600 * time.Millisecond
	s.CollyBase.Logger = s.Logger
	return s.CollyBase.NewCollector()
}
