// browser_fallback.go — shared helpers for automatic headless-browser fallback
// on HTTP 403 / permission errors in all Colly-based HTML scrapers.
//
// Pattern used in each scraper's Scrape():
//
//  1. getBF, cleanup := lazyBrowser(s.Logger)
//     defer cleanup()
//
//  2. Sitemap fetch with automatic fallback:
//     allURLs, err := sitemapWithFallback(ctx, client, url, getBF, s.Logger, "scraper_name")
//
//  3. Register a 403 tracker on the Colly collector:
//     permBlocked := trackPermissionErrors(c)
//
//  4. After the Colly URL loop:
//     retryBlockedWithBrowser(ctx, *permBlocked, getBF, extractCfg, seen, &all, s.Logger, "scraper_name")
package scrapers

import (
	"context"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// lazyBrowser returns:
//   - getBF: a factory that starts Chromium on first call and reuses it after.
//     Returns nil (with logged error) if the browser cannot start.
//   - cleanup: call via defer to shut the browser down when the scraper exits.
//
// The browser is only started if needed (first 403 hit), so scrapers that
// never hit a WAF pay zero overhead.
func lazyBrowser(logger *zap.Logger) (getBF func() *BrowserFetcher, cleanup func()) {
	var bf *BrowserFetcher
	getBF = func() *BrowserFetcher {
		if bf != nil {
			return bf
		}
		var err error
		bf, err = newBrowserFetcher(logger)
		if err != nil {
			if logger != nil {
				logger.Error("browser: failed to start headless Chromium", zap.Error(err))
			}
			return nil
		}
		return bf
	}
	cleanup = func() {
		if bf != nil {
			bf.Close()
		}
	}
	return getBF, cleanup
}

// sitemapWithFallback calls FetchSitemapURLs via the normal HTTP client.
// If that returns a permission error (HTTP 403/401/407), it retries via the
// headless browser.  getBF is only invoked on a permission error.
func sitemapWithFallback(
	ctx context.Context,
	client *http.Client,
	sitemapURL string,
	getBF func() *BrowserFetcher,
	logger *zap.Logger,
	logPrefix string,
) ([]string, error) {
	allURLs, err := FetchSitemapURLs(ctx, client, sitemapURL)
	if err == nil {
		return allURLs, nil
	}
	if !isPermissionError(err) {
		return nil, err
	}
	// HTTP was blocked — fall through to browser.
	if logger != nil {
		logger.Warn(logPrefix+": sitemap HTTP blocked, retrying with headless browser",
			zap.String("url", sitemapURL),
			zap.Error(err),
		)
	}
	bf := getBF()
	if bf == nil {
		return nil, err // browser init failed; return original error
	}
	return FetchSitemapURLsBrowser(ctx, bf, sitemapURL)
}

// trackPermissionErrors registers an OnError callback on c that appends any
// 403/401/407 response URLs to a slice.  Returns a pointer to that slice so
// the caller can read it after the Colly run.
//
// Note: Colly may call OnError from concurrent goroutines when Parallelism>1.
// The append is not mutex-protected here to stay consistent with the existing
// (unprotected) OnHTML callbacks in each scraper.  The window for a race is
// very small given the 600 ms inter-request delay.
func trackPermissionErrors(c *colly.Collector) *[]string {
	blocked := make([]string, 0)
	c.OnError(func(r *colly.Response, _ error) {
		if r.StatusCode == 403 || r.StatusCode == 401 || r.StatusCode == 407 {
			blocked = append(blocked, r.Request.URL.String())
		}
	})
	return &blocked
}

// retryBlockedWithBrowser fetches each URL in blocked via the headless browser,
// parses the HTML with goquery, runs ExtractPageGoquery, and appends any new
// Incentives to *all.  Skips URLs that are already in seen.
func retryBlockedWithBrowser(
	ctx context.Context,
	blocked []string,
	getBF func() *BrowserFetcher,
	extractCfg PageExtractConfig,
	seen map[string]bool,
	all *[]models.Incentive,
	logger *zap.Logger,
	logPrefix string,
) {
	if len(blocked) == 0 {
		return
	}
	bf := getBF()
	if bf == nil {
		return
	}

	if logger != nil {
		logger.Info(logPrefix+": retrying permission-blocked URLs with headless browser",
			zap.Int("count", len(blocked)))
	}

	bar := NewProgressBar(len(blocked), logPrefix)
	for _, u := range blocked {
		select {
		case <-ctx.Done():
			bar.Finish() //nolint:errcheck
			return
		default:
		}

		html, err := bf.FetchHTML(ctx, u)
		if err != nil {
			if logger != nil {
				logger.Warn(logPrefix+": browser fetch failed",
					zap.String("url", u), zap.Error(err))
			}
			bar.Add(1) //nolint:errcheck
			continue
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
		if err != nil {
			if logger != nil {
				logger.Warn(logPrefix+": parse failed after browser fetch",
					zap.String("url", u), zap.Error(err))
			}
			bar.Add(1) //nolint:errcheck
			continue
		}

		inc := ExtractPageGoquery(doc, u, extractCfg)
		if inc != nil && !seen[inc.ID] {
			seen[inc.ID] = true
			*all = append(*all, *inc)
			if logger != nil {
				logger.Info(logPrefix+": program found (browser fallback)",
					zap.String("name", inc.ProgramName),
					zap.Strings("categories", inc.CategoryTag),
					zap.Int("total_so_far", len(*all)),
				)
			}
		}
		bar.Add(1) //nolint:errcheck
	}
	bar.Finish() //nolint:errcheck
}
