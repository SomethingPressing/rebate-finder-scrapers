// rehydrate_html.go — RehydrateStream implementations for all HTML-based
// utility scrapers (Con Edison, PNM, Xcel Energy, SRP, Peninsula Clean Energy).
//
// Instead of re-crawling sitemaps to discover program pages, rehydrate reads
// the program_url / source_url values already stored in the staging DB and
// visits only those known pages.
package scrapers

import (
	"context"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── Con Edison ────────────────────────────────────────────────────────────────

func (s *ConEdisonScraper) RehydrateStream(ctx context.Context, records []RehydrateRecord, sink func([]models.Incentive)) error {
	urls := rehydrateURLList(records)
	s.Logger.Info("con_edison: rehydrating from staging", zap.Int("urls", len(urls)))

	getBF, cleanup := lazyBrowser(s.Logger)
	defer cleanup()

	seen := make(map[string]bool)
	extractCfg := conEdisonExtractCfg
	extractCfg.ScraperVersion = s.ScraperVersion

	c := s.newCollector("www.coned.com")
	permBlocked := trackPermissionErrors(c)
	c.OnHTML("html", func(e *colly.HTMLElement) {
		inc := s.extractPage(e, e.Request.URL.String())
		if inc == nil || seen[inc.ID] {
			return
		}
		seen[inc.ID] = true
		sink([]models.Incentive{*inc})
	})

	visitURLs(ctx, c, urls, s.Logger, "con_edison")

	var browserResults []models.Incentive
	retryBlockedWithBrowser(ctx, *permBlocked, getBF, extractCfg, seen, &browserResults, s.Logger, "con_edison")
	if len(browserResults) > 0 {
		sink(browserResults)
	}
	return nil
}

// ── PNM ───────────────────────────────────────────────────────────────────────

func (s *PNMScraper) RehydrateStream(ctx context.Context, records []RehydrateRecord, sink func([]models.Incentive)) error {
	urls := rehydrateURLList(records)
	s.Logger.Info("pnm: rehydrating from staging", zap.Int("urls", len(urls)))

	getBF, cleanup := lazyBrowser(s.Logger)
	defer cleanup()

	seen := make(map[string]bool)
	extractCfg := pnmExtractCfg
	extractCfg.ScraperVersion = s.ScraperVersion

	c := s.newCollector("www.pnm.com", "pnm.clearesult.com")
	permBlocked := trackPermissionErrors(c)
	c.OnHTML("html", func(e *colly.HTMLElement) {
		inc := s.extractPage(e, e.Request.URL.String())
		if inc == nil || seen[inc.ID] {
			return
		}
		seen[inc.ID] = true
		sink([]models.Incentive{*inc})
	})

	visitURLs(ctx, c, urls, s.Logger, "pnm")

	var browserResults []models.Incentive
	retryBlockedWithBrowser(ctx, *permBlocked, getBF, extractCfg, seen, &browserResults, s.Logger, "pnm")
	if len(browserResults) > 0 {
		sink(browserResults)
	}
	return nil
}

// ── Xcel Energy ───────────────────────────────────────────────────────────────

func (s *XcelEnergyScraper) RehydrateStream(ctx context.Context, records []RehydrateRecord, sink func([]models.Incentive)) error {
	urls := rehydrateURLList(records)
	s.Logger.Info("xcel_energy: rehydrating from staging", zap.Int("urls", len(urls)))

	getBF, cleanup := lazyBrowser(s.Logger)
	defer cleanup()

	seen := make(map[string]bool)
	extractCfg := xcelExtractCfg
	extractCfg.ScraperVersion = s.ScraperVersion

	c := s.newCollector("www.xcelenergy.com")
	permBlocked := trackPermissionErrors(c)
	c.OnHTML("html", func(e *colly.HTMLElement) {
		inc := s.extractPage(e, e.Request.URL.String())
		if inc == nil || seen[inc.ID] {
			return
		}
		seen[inc.ID] = true
		sink([]models.Incentive{*inc})
	})

	visitURLs(ctx, c, urls, s.Logger, "xcel_energy")

	var browserResults []models.Incentive
	retryBlockedWithBrowser(ctx, *permBlocked, getBF, extractCfg, seen, &browserResults, s.Logger, "xcel_energy")
	if len(browserResults) > 0 {
		sink(browserResults)
	}
	return nil
}

// ── SRP ───────────────────────────────────────────────────────────────────────
// SRP is always behind Cloudflare, so rehydrate uses the headless browser
// directly for every page (same as the normal Scrape path).

func (s *SRPScraper) RehydrateStream(ctx context.Context, records []RehydrateRecord, sink func([]models.Incentive)) error {
	urls := rehydrateURLList(records)
	s.Logger.Info("srp: rehydrating from staging", zap.Int("urls", len(urls)))

	bf, err := newBrowserFetcher(s.Logger)
	if err != nil {
		return err
	}
	defer bf.Close()

	extractCfg := srpExtractCfg
	extractCfg.ScraperVersion = s.ScraperVersion

	seen := make(map[string]bool)
	bar := NewProgressBar(len(urls), "srp")
	for i, u := range urls {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		html, fetchErr := bf.FetchHTML(ctx, u)
		if fetchErr != nil {
			s.Logger.Warn("srp: rehydrate fetch failed", zap.String("url", u), zap.Error(fetchErr))
			bar.Add(1) //nolint:errcheck
			continue
		}
		doc, parseErr := goquery.NewDocumentFromReader(strings.NewReader(html))
		if parseErr != nil {
			s.Logger.Warn("srp: rehydrate parse failed", zap.String("url", u), zap.Error(parseErr))
			bar.Add(1) //nolint:errcheck
			continue
		}
		inc := ExtractPageGoquery(doc, u, extractCfg)
		if inc != nil && !seen[inc.ID] {
			seen[inc.ID] = true
			sink([]models.Incentive{*inc})
			s.Logger.Info("srp: rehydrated", zap.String("name", inc.ProgramName), zap.Int("i", i+1))
		}
		bar.Add(1) //nolint:errcheck
		if s.CollyBase.Delay > 0 {
			time.Sleep(s.CollyBase.Delay)
		}
	}
	bar.Finish() //nolint:errcheck
	return nil
}

// ── Peninsula Clean Energy ────────────────────────────────────────────────────

func (s *PeninsulaCleanEnergyScraper) RehydrateStream(ctx context.Context, records []RehydrateRecord, sink func([]models.Incentive)) error {
	urls := rehydrateURLList(records)
	s.Logger.Info("peninsula_clean_energy: rehydrating from staging", zap.Int("urls", len(urls)))

	getBF, cleanup := lazyBrowser(s.Logger)
	defer cleanup()

	seen := make(map[string]bool)
	extractCfg := pceExtractCfg
	extractCfg.ScraperVersion = s.ScraperVersion

	c := s.newCollector("www.peninsulacleanenergy.com")
	permBlocked := trackPermissionErrors(c)
	c.OnHTML("html", func(e *colly.HTMLElement) {
		inc := s.extractPage(e, e.Request.URL.String())
		if inc == nil || seen[inc.ID] {
			return
		}
		seen[inc.ID] = true
		sink([]models.Incentive{*inc})
	})

	visitURLs(ctx, c, urls, s.Logger, "peninsula_clean_energy")

	var browserResults []models.Incentive
	retryBlockedWithBrowser(ctx, *permBlocked, getBF, extractCfg, seen, &browserResults, s.Logger, "peninsula_clean_energy")
	if len(browserResults) > 0 {
		sink(browserResults)
	}
	return nil
}

// ── Shared helpers ────────────────────────────────────────────────────────────

// rehydrateURLList extracts a deduplicated list of URLs from rehydrate records.
// source_url is preferred; falls back to program_url.
func rehydrateURLList(records []RehydrateRecord) []string {
	seen := make(map[string]struct{}, len(records))
	var out []string
	for _, r := range records {
		u := rehydrateURL(r)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; !ok {
			seen[u] = struct{}{}
			out = append(out, u)
		}
	}
	return out
}

// visitURLs drives a Colly collector through a list of URLs respecting
// context cancellation.
func visitURLs(ctx context.Context, c *colly.Collector, urls []string, logger *zap.Logger, source string) {
	bar := NewProgressBar(len(urls), source)
	for _, u := range urls {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := c.Visit(u); err != nil {
			logger.Warn(source+": rehydrate visit failed", zap.String("url", u), zap.Error(err))
		}
		bar.Add(1) //nolint:errcheck
	}
	bar.Finish() //nolint:errcheck
}
