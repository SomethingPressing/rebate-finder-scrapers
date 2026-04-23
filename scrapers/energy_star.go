package scrapers

import (
	"context"
	"fmt"
	"strings"

	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// EnergyStarScraper scrapes federal tax credit information from
// https://www.energystar.gov/about/federal_tax_credits using Colly.
//
// Energy Star does not expose a public API for its tax credit pages, so we
// scrape the HTML.  Each tax credit card on the page becomes one Incentive.
type EnergyStarScraper struct {
	CollyBase
	BaseURL        string
	ScraperVersion string
}

// Name implements Scraper.
func (s *EnergyStarScraper) Name() string { return "energy_star" }

// Scrape implements Scraper.
func (s *EnergyStarScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
	c := s.NewCollector()

	var results []models.Incentive
	var scrapeErr error

	// ── Card-level selectors ──────────────────────────────────────────────────
	// Energy Star renders each tax credit as a <div class="views-row"> block.
	// Inside each block:
	//   .views-field-title           → program name
	//   .field-name-field-description → description text
	//   .field-name-field-incentive  → incentive amount / percentage
	//   .field-name-field-credit-type → credit type (e.g. "Tax Credit")
	//   a[href]                       → program URL (first link in block)

	c.OnHTML(".views-row", func(e *colly.HTMLElement) {
		title := strings.TrimSpace(e.ChildText(".views-field-title"))
		if title == "" {
			// Fallback: try h3 / h2 headings inside the card.
			title = strings.TrimSpace(e.ChildText("h3"))
		}
		if title == "" {
			title = strings.TrimSpace(e.ChildText("h2"))
		}
		if title == "" {
			return // skip cards without a title
		}

		desc := strings.TrimSpace(
			e.ChildText(".field-name-field-description"),
		)
		amountRaw := strings.TrimSpace(
			e.ChildText(".field-name-field-incentive"),
		)
		creditType := strings.TrimSpace(
			e.ChildText(".field-name-field-credit-type"),
		)
		programURL := e.ChildAttr("a", "href")
		if programURL != "" && !strings.HasPrefix(programURL, "http") {
			programURL = "https://www.energystar.gov" + programURL
		}

		inc := models.NewIncentive(s.Name(), s.ScraperVersion)
		// Deterministic ID from program title (stable across re-scrapes)
		inc.ID = models.DeterministicID(s.Name(), strings.ToLower(title))

		inc.ProgramName = title
		inc.UtilityCompany = "U.S. Department of Energy"
		inc.Administrator = models.PtrString("IRS / DOE")
		inc.AvailableNationwide = models.PtrBool(true)

		if desc != "" {
			inc.IncentiveDescription = models.PtrString(desc)
		}

		// Parse amount
		if amountRaw != "" {
			format, amount := ParseAmount(amountRaw)
			inc.IncentiveFormat = models.PtrString(format)
			switch format {
			case "dollar_amount":
				inc.IncentiveAmount = amount
			case "percent":
				inc.PercentValue = amount
			case "per_unit":
				inc.PerUnitAmount = amount
			}
		}

		if creditType != "" {
			inc.CategoryTag = []string{creditType}
		} else {
			inc.CategoryTag = []string{"Federal Tax Credit"}
		}

		inc.Segment = []string{"Residential"}
		inc.Portfolio = []string{"Federal"}

		if programURL != "" {
			inc.ProgramURL = models.PtrString(programURL)
			inc.ApplicationURL = models.PtrString(programURL)
		}

		results = append(results, inc)

		s.Logger.Debug("energy_star: scraped card",
			zap.String("title", title),
			zap.String("amount", amountRaw),
		)
	})

	// ── Fallback: broader selector if views-row yields nothing ───────────────
	// Some Energy Star pages restructure the DOM.  We add a secondary pass
	// that targets plain article / section cards with a heading + paragraph.
	c.OnHTML("article", func(e *colly.HTMLElement) {
		// Only fire if the primary selector found nothing.
		if len(results) > 0 {
			return
		}
		title := strings.TrimSpace(e.ChildText("h2, h3, h4"))
		if title == "" {
			return
		}
		desc := strings.TrimSpace(e.ChildText("p"))
		programURL := e.ChildAttr("a", "href")
		if programURL != "" && !strings.HasPrefix(programURL, "http") {
			programURL = "https://www.energystar.gov" + programURL
		}

		inc := models.NewIncentive(s.Name(), s.ScraperVersion)
		inc.ID = models.DeterministicID(s.Name(), strings.ToLower(title))
		inc.ProgramName = title
		inc.UtilityCompany = "U.S. Department of Energy"
		inc.AvailableNationwide = models.PtrBool(true)
		inc.CategoryTag = []string{"Federal Tax Credit"}
		inc.Segment = []string{"Residential"}
		inc.Portfolio = []string{"Federal"}
		if desc != "" {
			inc.IncentiveDescription = models.PtrString(desc)
		}
		if programURL != "" {
			inc.ProgramURL = models.PtrString(programURL)
		}

		results = append(results, inc)
	})

	c.OnError(func(r *colly.Response, err error) {
		scrapeErr = fmt.Errorf("energy_star: %s: HTTP %d: %w",
			r.Request.URL, r.StatusCode, err)
	})

	s.Logger.Info("energy_star fetching page", zap.String("url", s.BaseURL))

	if err := c.Visit(s.BaseURL); err != nil {
		return nil, fmt.Errorf("energy_star: visit %s: %w", s.BaseURL, err)
	}

	c.Wait()

	if scrapeErr != nil {
		return results, scrapeErr
	}

	s.Logger.Info("energy_star scrape complete",
		zap.Int("count", len(results)),
		zap.String("url", s.BaseURL),
	)

	return results, nil
}
