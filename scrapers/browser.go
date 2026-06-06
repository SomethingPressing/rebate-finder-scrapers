// browser.go — headless Chromium page fetcher using go-rod.
//
// Use this instead of Colly for sites protected by Cloudflare (or similar
// WAFs) that reject requests based on TLS fingerprint, IP reputation, or
// JavaScript challenges.  A real Chrome process presents the correct TLS
// fingerprint and executes JS challenges natively.
//
// Rod auto-downloads Chromium to ~/.cache/rod/browser/ on first use (~150 MB,
// cached permanently) so no manual installation is required.
//
// Typical Cloudflare challenge flow:
//  1. Navigate → CF returns a "Just a moment…" JS-challenge page.
//  2. Chrome executes the challenge (~1-3 s) and CF redirects.
//  3. Target page loads normally.
//
// BrowserFetcher handles this by waiting for the load event, then sleeping
// an extra configurable delay and checking for a CF interstitial fingerprint
// before returning the HTML.
package scrapers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"go.uber.org/zap"
)

// newBrowserFetcher is a convenience alias used by all HTML scrapers.
// Launches headless Chromium with safe server defaults (NoSandbox=true).
func newBrowserFetcher(logger *zap.Logger) (*BrowserFetcher, error) {
	return NewBrowserFetcher(logger, BrowserFetcherOpts{NoSandbox: true})
}

// BrowserFetcherOpts configures a BrowserFetcher.
type BrowserFetcherOpts struct {
	// PageTimeout is the maximum time allowed for a single page load.
	// Default: 30 s.
	PageTimeout time.Duration
	// ExtraDelay is a sleep after WaitLoad to let CF-challenge redirects fully
	// settle before reading the HTML.  Default: 2 s.
	ExtraDelay time.Duration
	// CFRetryDelay is an additional wait if a CF interstitial is still detected
	// after ExtraDelay.  Default: 8 s.
	CFRetryDelay time.Duration
	// NoSandbox disables the Chromium sandbox — required in Docker/container
	// environments.  Defaults to true (safe for most server deployments).
	NoSandbox bool
}

// BrowserFetcher holds a single headless Chromium instance that is reused
// across all page fetches in one scraper run.  Create once per Scrape() call,
// then Close() after all URLs are processed.
type BrowserFetcher struct {
	browser      *rod.Browser
	pageTimeout  time.Duration
	extraDelay   time.Duration
	cfRetryDelay time.Duration
	logger       *zap.Logger
}

// NewBrowserFetcher launches a headless Chromium instance.  Rod downloads
// Chromium automatically if it is not already cached.
func NewBrowserFetcher(logger *zap.Logger, opts BrowserFetcherOpts) (*BrowserFetcher, error) {
	if opts.PageTimeout <= 0 {
		opts.PageTimeout = 30 * time.Second
	}
	if opts.ExtraDelay <= 0 {
		opts.ExtraDelay = 2 * time.Second
	}
	if opts.CFRetryDelay <= 0 {
		opts.CFRetryDelay = 8 * time.Second
	}

	l := launcher.New().
		Headless(true).
		// Realistic viewport — some WAFs inspect viewport headers.
		Set("window-size", "1280,900")
	if opts.NoSandbox {
		l = l.NoSandbox(true)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("browser: launch chromium: %w", err)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("browser: connect to chromium: %w", err)
	}

	if logger != nil {
		logger.Info("headless browser started",
			zap.Duration("page_timeout", opts.PageTimeout),
			zap.Duration("extra_delay", opts.ExtraDelay),
		)
	}

	return &BrowserFetcher{
		browser:      browser,
		pageTimeout:  opts.PageTimeout,
		extraDelay:   opts.ExtraDelay,
		cfRetryDelay: opts.CFRetryDelay,
		logger:       logger,
	}, nil
}

// FetchHTML opens a new browser tab, navigates to u, waits for the page to
// load (including any Cloudflare JS-challenge redirects), and returns the
// full rendered HTML.  The tab is closed before returning.
func (f *BrowserFetcher) FetchHTML(ctx context.Context, u string) (string, error) {
	// Open a new tab (blank page).
	page, err := f.browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return "", fmt.Errorf("browser: create tab: %w", err)
	}
	defer func() { _ = page.Close() }()

	// Apply per-page timeout.
	page = page.Timeout(f.pageTimeout)

	// Navigate.
	if err := page.Navigate(u); err != nil {
		return "", fmt.Errorf("browser: navigate %s: %w", u, err)
	}

	// Wait for the DOM load event.  Non-fatal on timeout — we'll try reading
	// whatever HTML is available.
	if err := page.WaitLoad(); err != nil && f.logger != nil {
		f.logger.Warn("browser: WaitLoad timed out — reading partial HTML",
			zap.String("url", u), zap.Error(err))
	}

	// Extra delay lets CF JS-challenge redirects complete before we read HTML.
	if err := sleepCtx(ctx, f.extraDelay); err != nil {
		return "", err
	}

	// Read initial HTML.
	html, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("browser: get html %s: %w", u, err)
	}

	// If we're still on a CF interstitial, wait longer for the challenge.
	if isCFChallengePage(html) {
		if f.logger != nil {
			f.logger.Debug("browser: Cloudflare challenge page detected, waiting",
				zap.String("url", u),
				zap.Duration("wait", f.cfRetryDelay),
			)
		}
		if err := sleepCtx(ctx, f.cfRetryDelay); err != nil {
			return "", err
		}
		html, err = page.HTML()
		if err != nil {
			return "", fmt.Errorf("browser: get html after cf wait %s: %w", u, err)
		}
	}

	return html, nil
}

// FetchHTMLWithStateSelect behaves like FetchHTML but, if the rendered page
// contains the phrase "select a service area", it tries to click an element
// whose visible text matches stateText (e.g. "Colorado") before reading the
// final HTML.  This bypasses region-selector interstitials on Salesforce
// Experience Cloud portals such as my.xcelenergy.com.
//
// If stateText is empty, or the element is not found, behaviour is identical
// to FetchHTML.
func (f *BrowserFetcher) FetchHTMLWithStateSelect(ctx context.Context, u string, stateText string) (string, error) {
	page, err := f.browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return "", fmt.Errorf("browser: create tab: %w", err)
	}
	defer func() { _ = page.Close() }()

	page = page.Timeout(f.pageTimeout)

	if err := page.Navigate(u); err != nil {
		return "", fmt.Errorf("browser: navigate %s: %w", u, err)
	}
	if err := page.WaitLoad(); err != nil && f.logger != nil {
		f.logger.Warn("browser: WaitLoad timed out — reading partial HTML",
			zap.String("url", u), zap.Error(err))
	}
	if err := sleepCtx(ctx, f.extraDelay); err != nil {
		return "", err
	}

	html, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("browser: get html %s: %w", u, err)
	}

	if isCFChallengePage(html) {
		if f.logger != nil {
			f.logger.Debug("browser: Cloudflare challenge detected, waiting",
				zap.String("url", u), zap.Duration("wait", f.cfRetryDelay))
		}
		if err := sleepCtx(ctx, f.cfRetryDelay); err != nil {
			return "", err
		}
		html, err = page.HTML()
		if err != nil {
			return "", fmt.Errorf("browser: get html after cf wait %s: %w", u, err)
		}
	}

	// Region-selector interstitial — try clicking the requested state via JS.
	// ElementR-based clicking can match the wrong element (e.g. a paragraph that
	// says "serving Colorado and Minnesota").  JavaScript evaluation finds the
	// FIRST element whose trimmed textContent exactly equals stateText and clicks
	// it, which is more precise for Salesforce Lightning Web Components.
	if stateText != "" && strings.Contains(strings.ToLower(html), "select a service area") {
		if f.logger != nil {
			f.logger.Debug("browser: region selector detected, clicking state via JS",
				zap.String("url", u), zap.String("state", stateText))
		}
		jsResult, evalErr := page.Eval(fmt.Sprintf(`() => {
			const tags = ['a','button','[role="button"]','li'];
			for (const tag of tags) {
				for (const el of document.querySelectorAll(tag)) {
					if (el.textContent.trim() === %q) {
						el.click();
						return 'clicked:' + tag;
					}
				}
			}
			return 'not-found';
		}`, stateText))
		if evalErr != nil {
			if f.logger != nil {
				f.logger.Debug("browser: JS eval failed for state click",
					zap.String("url", u), zap.Error(evalErr))
			}
		} else {
			result := jsResult.Value.Str()
			if f.logger != nil {
				f.logger.Debug("browser: state click result",
					zap.String("url", u), zap.String("result", result))
			}
			if strings.HasPrefix(result, "clicked:") {
				// Wait for Salesforce to re-render after the state selection.
				if err := sleepCtx(ctx, 4*time.Second); err != nil {
					return "", err
				}
				_ = page.WaitLoad()
				html, err = page.HTML()
				if err != nil {
					return "", fmt.Errorf("browser: get html after state select %s: %w", u, err)
				}
			} else if f.logger != nil {
				f.logger.Debug("browser: state selector element not found via JS",
					zap.String("url", u), zap.String("state", stateText))
			}
		}
	}

	return html, nil
}

// FetchXML navigates to a URL (typically a sitemap) and returns the raw
// document text content using JavaScript fetch() with the CF cookies already
// set from the navigation.  This works around Chrome's XML-viewer mode which
// wraps the content in HTML, making it unparseable as XML directly.
//
// The flow is:
//  1. Navigate to the URL (solves any CF JS challenge, sets CF cookies).
//  2. Call window.fetch(url, {credentials:'include'}) in JS to get raw text.
//  3. Return the raw bytes for XML parsing.
func (f *BrowserFetcher) FetchXML(ctx context.Context, u string) ([]byte, error) {
	page, err := f.browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("browser: create tab for xml: %w", err)
	}
	defer func() { _ = page.Close() }()

	page = page.Timeout(f.pageTimeout)

	// Navigate so CF cookies are set.
	if err := page.Navigate(u); err != nil {
		return nil, fmt.Errorf("browser: navigate xml %s: %w", u, err)
	}
	if err := page.WaitLoad(); err != nil && f.logger != nil {
		f.logger.Warn("browser: WaitLoad timeout on xml page",
			zap.String("url", u), zap.Error(err))
	}

	// Extra delay for CF challenge redirect.
	if err := sleepCtx(ctx, f.extraDelay); err != nil {
		return nil, err
	}

	// Use fetch() with CF cookies included to get the raw XML text.
	result, err := page.Eval(
		`(url) => fetch(url, {credentials: 'include'}).then(r => r.text())`, u,
	)
	if err != nil {
		return nil, fmt.Errorf("browser: fetch xml %s: %w", u, err)
	}

	return []byte(result.Value.Str()), nil
}

// Close shuts down the Chromium process.  Always call this when done.
func (f *BrowserFetcher) Close() {
	if f.browser != nil {
		_ = f.browser.Close()
	}
}

// isCFChallengePage returns true when the HTML looks like an active Cloudflare
// interstitial (JS-challenge) rather than the real target page.
func isCFChallengePage(html string) bool {
	lower := strings.ToLower(html)
	return strings.Contains(lower, "just a moment") ||
		strings.Contains(lower, "cf-challenge-running") ||
		strings.Contains(lower, "challenge-platform") ||
		strings.Contains(lower, "checking your browser")
}

// sleepCtx sleeps for d, returning ctx.Err() if the context is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
