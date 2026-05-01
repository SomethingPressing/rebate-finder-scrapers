package scrapers

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"go.uber.org/zap"
)

// browserUA is a realistic Chrome user-agent string.
// Using a recognisable browser UA is necessary to avoid 403s from WAF/CDN
// protections on utility websites (e.g. SRP, Xcel) that block known bot UAs.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) " +
	"Chrome/124.0.0.0 Safari/537.36"

// browserHeaders are the additional HTTP headers sent with every Colly request
// to make the traffic profile match a real browser as closely as possible.
var browserHeaders = map[string]string{
	"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
	"Accept-Language":           "en-US,en;q=0.9",
	"Accept-Encoding":           "gzip, deflate, br",
	"Connection":                "keep-alive",
	"Upgrade-Insecure-Requests": "1",
	"Sec-Fetch-Dest":            "document",
	"Sec-Fetch-Mode":            "navigate",
	"Sec-Fetch-Site":            "none",
	"Sec-Fetch-User":            "?1",
}

// CollyBase provides a pre-configured Colly collector for HTML scraping.
// Embed this in utility-specific scrapers to avoid repeating boilerplate.
//
// Example:
//
//	type MyUtilityScraper struct {
//	    CollyBase
//	    scraperVersion string
//	}
//
//	func (s *MyUtilityScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
//	    c := s.NewCollector()
//	    c.OnHTML(".rebate-item", func(e *colly.HTMLElement) { ... })
//	    c.Visit("https://myutility.com/rebates")
//	    return results, nil
//	}
type CollyBase struct {
	AllowedDomain  string
	RequestTimeout time.Duration
	Parallelism    int
	Delay          time.Duration
	Logger         *zap.Logger

	// ProxyURL, when non-empty, routes all Colly requests and bare *http.Client
	// calls through this proxy.  Useful for bypassing CDN/WAF IP-range blocks
	// (e.g. Cloudflare blocks data-center IPs for SRP regardless of UA).
	// Accepted formats: "http://user:pass@host:port", "socks5://host:port".
	ProxyURL string
}

// NewCollector returns a *colly.Collector configured with the settings on CollyBase.
func (b *CollyBase) NewCollector() *colly.Collector {
	opts := []colly.CollectorOption{
		colly.UserAgent(browserUA),
	}
	if b.AllowedDomain != "" {
		opts = append(opts, colly.AllowedDomains(b.AllowedDomain))
	}

	c := colly.NewCollector(opts...)
	c.SetRequestTimeout(b.requestTimeout())

	parallelism := b.Parallelism
	if parallelism <= 0 {
		parallelism = 2
	}
	delay := b.Delay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}

	_ = c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: parallelism,
		Delay:       delay,
		RandomDelay: delay / 2, // adds up to 50% jitter to avoid rhythmic patterns
	})

	// Attach browser-like headers to every request.
	c.OnRequest(func(r *colly.Request) {
		for k, v := range browserHeaders {
			r.Headers.Set(k, v)
		}
	})

	if b.Logger != nil {
		c.OnError(func(r *colly.Response, err error) {
			b.Logger.Warn("colly request error",
				zap.String("url", r.Request.URL.String()),
				zap.Int("status", r.StatusCode),
				zap.Error(err),
			)
		})
	}

	// Route through proxy if configured (e.g. residential proxy to bypass
	// Cloudflare IP-range blocks that affect data-center egress IPs).
	if b.ProxyURL != "" {
		if err := c.SetProxy(b.ProxyURL); err != nil {
			if b.Logger != nil {
				b.Logger.Warn("colly: failed to set proxy",
					zap.String("proxy", b.ProxyURL),
					zap.Error(err),
				)
			}
		}
	}

	return c
}

// NewHTTPClient returns an *http.Client configured with the CollyBase proxy
// (if set) and the given timeout.  Use this for sitemap fetches and any other
// non-Colly HTTP calls that must share the same proxy as the collector.
func (b *CollyBase) NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// Clone the default transport so we inherit TLS settings, keep-alives, etc.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if b.ProxyURL != "" {
		if parsed, err := url.Parse(b.ProxyURL); err == nil {
			tr.Proxy = http.ProxyURL(parsed)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

func (b *CollyBase) requestTimeout() time.Duration {
	if b.RequestTimeout > 0 {
		return b.RequestTimeout
	}
	return 30 * time.Second
}

// ── Amount parsing helpers ────────────────────────────────────────────────────

var (
	reDollar  = regexp.MustCompile(`\$([0-9,]+(?:\.[0-9]+)?)`)
	rePercent = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*%`)
	rePerKwh  = regexp.MustCompile(`\$([0-9]+(?:\.[0-9]+)?)\s*/\s*(?:k[Ww][Hh]|kwh)`)
	reUpTo    = regexp.MustCompile(`(?i)up\s+to\s+\$([0-9,]+(?:\.[0-9]+)?)`)
)

// ParseAmount parses a human-readable incentive amount string into an
// incentive_format enum value and a numeric amount.
//
// Examples:
//
//	"$600"           → ("dollar_amount", 600.0)
//	"30% of cost"    → ("percent", 30.0)
//	"up to $2,000"   → ("dollar_amount", 2000.0)
//	"$0.10/kWh"      → ("per_unit", 0.10)
//	"varies"         → ("narrative", nil)
func ParseAmount(s string) (format string, amount *float64) {
	s = strings.TrimSpace(s)

	// Per-unit (kWh) — check before generic dollar
	if m := rePerKwh.FindStringSubmatch(s); len(m) == 2 {
		if f := parseCommaFloat(m[1]); f != 0 {
			return "per_unit", &f
		}
	}

	// Up to $X
	if m := reUpTo.FindStringSubmatch(s); len(m) == 2 {
		if f := parseCommaFloat(m[1]); f != 0 {
			return "dollar_amount", &f
		}
	}

	// Plain $X
	if m := reDollar.FindStringSubmatch(s); len(m) == 2 {
		if f := parseCommaFloat(m[1]); f != 0 {
			return "dollar_amount", &f
		}
	}

	// Percent
	if m := rePercent.FindStringSubmatch(s); len(m) == 2 {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil && f != 0 {
			return "percent", &f
		}
	}

	return "narrative", nil
}

func parseCommaFloat(s string) float64 {
	s = strings.ReplaceAll(s, ",", "")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}
