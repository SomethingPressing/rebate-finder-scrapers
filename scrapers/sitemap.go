// sitemap.go — shared sitemap XML parser and URL filter used by utility HTML scrapers.
//
// Handles both sitemap index files (<sitemapindex>) and URL-set files (<urlset>).
// Recursively resolves nested sitemap index references up to maxDepth levels deep.
//
// FilterConfig mirrors the two-pass (exclusion-first, then inclusion) logic used
// in the SmythOS crawler LLM prompts for each utility.  Exclusions are checked
// first; any match rejects the URL immediately before the inclusion check runs.
package scrapers

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const sitemapMaxDepth = 3

// sitemapURLSet is the <urlset> root element of a leaf sitemap file.
type sitemapURLSet struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

// sitemapIndex is the <sitemapindex> root element of a sitemap index file.
type sitemapIndex struct {
	XMLName  xml.Name `xml:"sitemapindex"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// FetchSitemapURLs fetches a sitemap URL and returns all <loc> entries.
// Recursively resolves sitemap index references up to sitemapMaxDepth levels.
// Skips child sitemaps that return HTML error pages (Access Denied, 404, etc.).
func FetchSitemapURLs(ctx context.Context, client *http.Client, sitemapURL string) ([]string, error) {
	return fetchSitemapURLsDepth(ctx, client, sitemapURL, 0)
}

func fetchSitemapURLsDepth(ctx context.Context, client *http.Client, u string, depth int) ([]string, error) {
	if depth >= sitemapMaxDepth {
		return nil, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("sitemap: build request: %w", err)
	}
	// Use a realistic browser UA — some sites (e.g. SRP) return 403 for known bot UAs.
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	cl := client
	if cl == nil {
		cl = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sitemap: fetch %s: %w", u, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitemap: HTTP %d for %s", resp.StatusCode, u)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // max 10 MB
	if err != nil {
		return nil, fmt.Errorf("sitemap: read body: %w", err)
	}

	// Skip HTML error pages (PNM child sitemaps sometimes return "Access Denied" HTML).
	bodyStr := string(body[:min(512, len(body))])
	if strings.Contains(bodyStr, "<!DOCTYPE html") || strings.Contains(bodyStr, "Access Denied") {
		return nil, fmt.Errorf("sitemap: HTML error page returned for %s", u)
	}

	// Try parsing as a sitemap index first.
	var idx sitemapIndex
	if err := xml.Unmarshal(body, &idx); err == nil && len(idx.Sitemaps) > 0 {
		var all []string
		for _, sm := range idx.Sitemaps {
			if sm.Loc == "" {
				continue
			}
			sub, _ := fetchSitemapURLsDepth(ctx, client, sm.Loc, depth+1)
			all = append(all, sub...)
		}
		return all, nil
	}

	// Try parsing as a URL set.
	var urlset sitemapURLSet
	if err := xml.Unmarshal(body, &urlset); err != nil {
		return nil, fmt.Errorf("sitemap: parse %s: %w", u, err)
	}

	out := make([]string, 0, len(urlset.URLs))
	for _, entry := range urlset.URLs {
		if entry.Loc != "" {
			out = append(out, entry.Loc)
		}
	}
	return out, nil
}

// ── URL filtering ─────────────────────────────────────────────────────────────

// FilterConfig mirrors the two-pass filtering logic from the SmythOS crawler
// LLM prompts: exclusions are checked first (any match → reject), then
// inclusions (at least one match required to accept).
type FilterConfig struct {
	// ExcludeKeywords are checked first.  A URL containing ANY of these substrings
	// (case-insensitive) is rejected immediately, even if it also matches an
	// include keyword.
	ExcludeKeywords []string
	// IncludeKeywords: a URL must contain AT LEAST ONE of these (case-insensitive)
	// to be accepted, after passing the exclusion check.
	IncludeKeywords []string
	// MinPathSegments rejects URLs whose path has fewer segments than this value.
	// Used for Xcel Energy hub-page detection (generic category pages have short
	// paths, specific program pages have deeper paths).
	MinPathSegments int
}

// FilterSitemapURLs applies a FilterConfig to a list of URLs.
// Exclusions are checked first; a URL is accepted only if it passes all
// exclusions AND matches at least one include keyword.
func FilterSitemapURLs(urls []string, cfg FilterConfig) []string {
	var out []string
	for _, u := range urls {
		lower := strings.ToLower(u)

		// Step 1 — exclusion check (any match → skip).
		excluded := false
		for _, ex := range cfg.ExcludeKeywords {
			if strings.Contains(lower, strings.ToLower(ex)) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}

		// Step 2 — path depth check (hub page detection).
		if cfg.MinPathSegments > 0 {
			depth := pathDepth(u)
			if depth < cfg.MinPathSegments {
				continue
			}
		}

		// Step 3 — inclusion check (must match at least one).
		if len(cfg.IncludeKeywords) > 0 {
			matched := false
			for _, inc := range cfg.IncludeKeywords {
				if strings.Contains(lower, strings.ToLower(inc)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		out = append(out, u)
	}
	return out
}

// pathDepth counts the number of non-empty path segments in a URL.
// IsPDFURL returns true when the URL path ends with ".pdf" (case-insensitive).
// Used by utility scrapers to route PDF links to the text-extraction path
// instead of the Colly HTML-scraping path.
func IsPDFURL(u string) bool {
	lower := strings.ToLower(u)
	// Strip query string before checking extension.
	if i := strings.Index(lower, "?"); i >= 0 {
		lower = lower[:i]
	}
	return strings.HasSuffix(lower, ".pdf")
}

// "https://example.com/a/b/c" → 3
func pathDepth(u string) int {
	// Strip scheme + host.
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[i:]
	} else {
		return 0
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	count := 0
	for _, p := range parts {
		if p != "" {
			count++
		}
	}
	return count
}
