# Implementation Plan: Utility Sitemap Scrapers

**Source file:** `rf-crawler-pnm-srp-coned-xcel-peninsul-mobyct44nw.smyth`
**Status:** Partially implemented — Con Edison, PNM, Xcel Energy complete; SRP and Peninsula Clean Energy pending

---

## Overview

Implement five new Go scrapers for utility companies whose rebate/incentive pages are discovered dynamically via XML sitemaps. Unlike the existing API-based scrapers (DSIRE, Rewiring America), these utilities don't expose structured APIs — their program data lives on marketing web pages.

The SmythOS agent this plan is based on uses a two-phase approach:
1. **Sitemap crawl → URL discovery**: Fetch the utility's XML sitemap(s), then use an LLM to identify which URLs describe rebate/incentive programs.
2. **Page scraping**: Fetch each discovered URL and extract program data.

The Go implementation replaces the LLM-based URL filter with a deterministic keyword/pattern filter derived from the same decision framework the LLM prompts encode.

---

## Utilities to Implement

| Scraper | Source ID | State(s) | Utility Type | Sitemap URL |
|---------|-----------|----------|--------------|-------------|
| SRP (Salt River Project) | `srp` | AZ | Electric utility | `https://www.srpnet.com/sitemap.xml` |
| Xcel Energy | `xcel_energy` | CO, MN, TX, NM, WI, SD | Electric/Gas utility | `https://www.xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml` |
| Con Edison | `con_edison` | NY, NJ | Electric/Gas utility | `https://www.coned.com/sitemap_coned_en.xml` |
| PNM (Public Service Co. of NM) | `pnm` | NM | Electric utility | `https://www.pnm.com/sitemap.xml` (sitemap index) |
| Peninsula Clean Energy | `peninsula_clean_energy` | CA (San Mateo County) | Community Choice Aggregator (CCA) | Three sitemaps (see below) |

---

## Architecture

```
For each utility:

  1. Sitemap Fetch
     └─ HTTP GET sitemap URL(s)
     └─ Parse XML: handle both <sitemapindex> and <urlset>
     └─ For sitemapindex: recursively fetch child sitemaps

  2. URL Filter
     └─ Apply include/exclude keyword rules
     └─ Return candidate rebate/incentive page URLs

  3. Page Scrape (Colly)
     └─ Visit each candidate URL
     └─ Extract: program name, description, amount, apply URL, etc.
     └─ Build models.Incentive

  4. Stage
     └─ db.UpsertToStaging(incentives)
```

---

## New Files

```
scrapers/
├── sitemap_parser.go          ← shared: fetch + parse XML sitemaps (index + urlset)
├── url_filter.go              ← shared: keyword-based URL include/exclude logic
├── srp.go                     ← SRP scraper
├── xcel_energy.go             ← Xcel Energy scraper
├── con_edison.go              ← Con Edison scraper
├── pnm.go                     ← PNM scraper
└── peninsula_clean_energy.go  ← Peninsula Clean Energy scraper
```

---

## Shared Infrastructure

### `scrapers/sitemap_parser.go`

```go
// FetchSitemapURLs fetches one or more sitemap URLs and returns all <loc> values.
// Handles both <sitemapindex> (recursive) and <urlset> (leaf) formats.
func FetchSitemapURLs(ctx context.Context, sitemapURLs []string) ([]string, error)
```

**Logic:**
- HTTP GET each sitemap URL with a 30-second timeout
- Parse XML: detect whether root element is `<sitemapindex>` or `<urlset>`
- For `<sitemapindex>`: extract nested `<sitemap><loc>` values and recursively fetch them (depth limit: 3)
- For `<urlset>`: extract `<url><loc>` values directly
- Skip child sitemap entries that return HTTP errors or non-XML content (HTML error pages)
- Return deduplicated flat list of all page URLs

### `scrapers/url_filter.go`

```go
type URLFilterConfig struct {
    IncludeKeywords []string   // any match → candidate
    ExcludeKeywords []string   // any match → reject (checked first)
    IncludePrefixes []string   // path starts with → candidate
    ExcludePrefixes []string   // path starts with → reject (checked first)
}

// FilterRebateURLs applies include/exclude rules to a list of URLs.
// Exclusions are checked first; any exclusion match rejects the URL.
// A URL must match at least one include keyword or prefix to be accepted.
func FilterRebateURLs(urls []string, cfg URLFilterConfig) []string
```

**Shared exclusion keywords** (apply to all utilities):
```
careers, investor, governance, board, leadership, media-room, press-release,
newsroom, about-us, contact-us, terms-of-use, privacy-policy, sitemap,
contractor, trade-ally, trade-partner, supplier, vendor, wholesale,
grid, transmission, substation, power-plant, generation, tariff, rate-schedule,
billing, payment, start-service, stop-service, account, login, search,
faq (standalone path segment), outage, outages, safety, job-opening
```

**Shared inclusion keywords:**
```
rebate, incentive, savings, discount, credit, refund, cashback, reward,
assistance, income-qualified, income-eligible, low-income, limited-income,
weatherization, heat-pump, heat pump, solar, ev-charging, electric-vehicle,
appliance, hvac, insulation, thermostat, water-heater, lighting, battery-storage,
demand-response, time-of-use, peak-rewards, energy-efficiency, save-money,
save-energy, financial-assistance, program, upgrade
```

---

## Per-Scraper Specifications

---

### 1. SRP (Salt River Project)

**Source ID:** `srp`
**Utility Company:** `"Salt River Project"`
**State:** `"AZ"`
**Segment:** `["Residential", "Commercial"]`
**Portfolio:** `["Utility"]`

**Sitemap:**
```
https://www.srpnet.com/sitemap.xml
```
Single flat sitemap (no sitemap index).

**Additional include prefixes for SRP:**
```
/energy-savings-rebates/
/customer-service/residential-electric/assistance
/customer-service/residential-electric/economy
/customer-service/residential-electric/discount
/customer-service/residential-electric/weatherization
/price-plans/
```

**Additional exclude prefixes for SRP:**
```
/doing-business/
/about-srp/
/irrigation/
/water-
/grid-water-management/
/improvement-projects/
/account/
```

**Page scraping selectors (Colly):**
- Program name: `h1`, `h2.page-title`, `.program-title`
- Description: `.program-description`, `.content-body p`
- Incentive amount: text containing `$`, `%`, `/kWh`, `/ton` patterns
- Apply URL: `a[href*="apply"], a[href*="rebate-form"], a[href*="enroll"]`
- Category tags: breadcrumb segments or `<meta name="keywords">`

**ID generation:**
```go
inc.ID = models.DeterministicID("srp", pageURL)
```

**Config:**
```env
SRP_BASE_URL=https://www.srpnet.com
```

---

### 2. Xcel Energy

**Source ID:** `xcel_energy`
**Utility Company:** `"Xcel Energy"`
**State:** `""` (multi-state; derive from page content if possible, else leave empty)
**Segment:** `["Residential", "Commercial"]`
**Portfolio:** `["Utility"]`

**Sitemap:**
```
https://www.xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml
```
Flat sitemap, large (may contain thousands of URLs).

**Additional include prefixes for Xcel:**
```
/programs_and_rebates/
/savings_and_rebates/
/energy_efficiency/
/renewable_energy/
/electric_vehicles/
/business/programs/
/residential/programs/
```

**Additional exclude prefixes for Xcel:**
```
/company/
/regulatory/
/investor/
/careers/
/newsroom/
/contact_us/
/my_account/
/outage/
/tariff/
```

**Page scraping selectors:**
- Program name: `h1.page-title`, `.program-header h1`, `.rebate-title`
- Description: `.program-description`, `.content-area p`
- Amount: text nodes near `$`, `%`, `up to`, `per unit`
- Apply URL: `a[href*="rebate"], a[href*="apply"], a[href*="enroll"]`

**ID generation:**
```go
inc.ID = models.DeterministicID("xcel_energy", pageURL)
```

**Config:**
```env
XCEL_ENERGY_BASE_URL=https://www.xcelenergy.com
```

---

### 3. Con Edison

**Source ID:** `con_edison`
**Utility Company:** `"Con Edison"`
**State:** `"NY"`
**Segment:** `["Residential", "Commercial"]`
**Portfolio:** `["Utility"]`

**Sitemap:**
```
https://www.coned.com/sitemap_coned_en.xml
```
Single flat sitemap.

**Additional include prefixes for ConEd:**
```
/en/save-energy/
/en/rebates/
/en/incentives/
/en/discounts/
/en/clean-energy/
/en/electric-vehicles/
/en/energy-efficiency/
/en/financial-assistance/
/en/for-my-home/
/en/for-my-business/
```

**Additional exclude prefixes for ConEd:**
```
/en/about/
/en/careers/
/en/media/
/en/contact/
/en/help/billing/
/en/outages/
/en/services/
/en/construction/
/en/safety/
```

**Page scraping selectors:**
- Program name: `h1.hero__title`, `.program-name`, `h1`
- Description: `.program-description p`, `.content__body p`
- Amount: text near `$`, `%`, `up to`, `per appliance`
- Apply URL: `a[href*="apply"]`, `a[href*="rebate"]`, `a.cta-button`

**ID generation:**
```go
inc.ID = models.DeterministicID("con_edison", pageURL)
```

**Config:**
```env
CONED_BASE_URL=https://www.coned.com
```

---

### 4. PNM (Public Service Company of New Mexico)

**Source ID:** `pnm`
**Utility Company:** `"PNM - Public Service Company of New Mexico"`
**State:** `"NM"`
**Segment:** `["Residential", "Commercial"]`
**Portfolio:** `["Utility"]`

**Sitemap:**
```
https://www.pnm.com/sitemap.xml
```

> ⚠️ **PNM uses a sitemap index.** The root sitemap contains `<sitemapindex>` with multiple child sitemap `<loc>` entries. The sitemap parser must recursively fetch each child and aggregate all `<url><loc>` values.

**PNM sitemap quirk:** Some child sitemap URLs return HTML "Access Denied" pages instead of XML. The parser must detect this (check for `<!DOCTYPE html>` in response body) and skip those entries gracefully.

**Additional include path segments / keywords for PNM:**
```
/save-money-and-energy
/rebates
/residential-rebates
/business-rebates
/bizrebates
/incentives
/savings
/save-
/checkup
/home-energy-checkup
/energy-efficiency
/weatherization
/appliance-recycling
/solar
/electric-vehicle
/ev-charging
/good-neighbor
/liheap
/income-qualified
/demand-response
/quick-saver
/time-of-use
```

**Additional exclude keywords for PNM:**
```
/careers/
/about-pnm/
/investor/
/news/
/contact/
/outage/
/account/
/tariff/
/rate-schedule/
/regulatory/
/environmental/
```

**Page scraping selectors:**
- Program name: `h1.page-title`, `.hero-title`, `h1`
- Description: `.field-items p`, `.content-main p`
- Amount: text containing `$`, `%`, `up to`, `per item`
- Apply URL: `a[href*="clearesult.com"]`, `a[href*="apply"]`, `a[href*="rebate"]`

> **Note:** PNM uses a third-party rebate portal at `pnm.clearesult.com` for some applications. Store these as `application_url` on the incentive.

**ID generation:**
```go
inc.ID = models.DeterministicID("pnm", pageURL)
```

**Config:**
```env
PNM_BASE_URL=https://www.pnm.com
```

---

### 5. Peninsula Clean Energy (PCE)

**Source ID:** `peninsula_clean_energy`
**Utility Company:** `"Peninsula Clean Energy"`
**State:** `"CA"`
**Segment:** `["Residential", "Commercial"]`
**Portfolio:** `["Utility", "State"]`

> PCE is a **Community Choice Aggregator (CCA)** serving San Mateo County and the City of Los Banos, California. It works alongside PG&E for billing/delivery. It offers its own rebate programs on top of utility programs.

**Sitemaps (three — fetch all):**
```
https://www.peninsulacleanenergy.com/post-sitemap.xml
https://www.peninsulacleanenergy.com/page-sitemap.xml
https://www.peninsulacleanenergy.com/news-sitemap.xml
```

**Additional include prefixes for PCE:**
```
/rebates-offers/
/rebates-offers-business/
/home-upgrade-services/
/electric-vehicles/
/e-bike/
/programs/
/financing/
/solar/
/battery/
/heat-pump/
/water-heater/
/income-qualified/
/savings-toolkit/
/rebate-aggregator/
/public-organizations/
/demand-response/
/flexibility/
```

**Blog/news inclusion rule:** Include blog/news URLs **only if** the URL path or scraped page title contains explicit dollar amounts (e.g., `$500`) or the words `rebate`, `incentive`, or `program`. Skip general informational posts.

**Additional exclude prefixes for PCE:**
```
/about/
/careers/
/press/
/blog/      ← default exclude; override by content check above
/contact/
/governance/
/board/
/team/
/news/      ← default exclude; override by content check above
/events/
/subscribe/
```

**Page scraping selectors:**
- Program name: `h1.entry-title`, `.program-title`, `h1`
- Description: `.entry-content p`, `.program-body p`
- Amount: text containing `$`, `%`, `up to`, `rebate amount`
- Apply URL: `a[href*="apply"]`, `a[href*="rebate"]`, `.cta a`
- Category tags: WordPress post categories/tags from `<meta>` or breadcrumbs

**ID generation:**
```go
inc.ID = models.DeterministicID("peninsula_clean_energy", pageURL)
```

**Config:**
```env
PENINSULA_CLEAN_ENERGY_BASE_URL=https://www.peninsulacleanenergy.com
```

---

## Field Mapping (All Utility Scrapers)

| `models.Incentive` field | Source |
|--------------------------|--------|
| `ID` | `DeterministicID(sourceID, pageURL)` |
| `ProgramName` | Page `h1` / title element |
| `UtilityCompany` | Hardcoded per scraper |
| `IncentiveDescription` | Page description text (first 2–3 paragraphs) |
| `IncentiveAmount` | Parsed from dollar text on page |
| `IncentiveFormat` | Derived from amount text (dollar_amount / percent / per_unit) |
| `MaximumAmount` | Parsed from "up to $X" patterns |
| `State` | Hardcoded per scraper (empty for Xcel — multi-state) |
| `ApplicationURL` | Apply/enroll CTA link on page |
| `ProgramURL` | The page URL itself |
| `Segment` | Inferred from page path (`/residential` vs `/business`) |
| `Portfolio` | `["Utility"]` for all; PCE also `["State"]` |
| `Source` | Hardcoded source ID per scraper |
| `ProgramHash` | `ComputeProgramHash(ProgramName, UtilityCompany)` |
| `ScraperVersion` | From config |

---

## Rate Limiting

| Scraper | Inter-request delay | Notes |
|---------|-------------------|-------|
| SRP | 1s | Standard HTML pages |
| Xcel Energy | 1s | Standard HTML pages |
| Con Edison | 1.5s | Anti-scraping protection flagged in SmythOS |
| PNM | 1s | Some pages behind Akamai |
| Peninsula Clean Energy | 500ms | WordPress site, well-behaved |

Use Colly's `LimitRule` with `RandomDelay` (`0.5 × delay`) to avoid detection.

---

## Configuration Summary

Add to `config/config.go`:

```go
SRPBaseURL                     string   // SRP_BASE_URL
XcelEnergyBaseURL              string   // XCEL_ENERGY_BASE_URL
ConEdisonBaseURL               string   // CONED_BASE_URL
PNMBaseURL                     string   // PNM_BASE_URL
PeninsulaCleanEnergyBaseURL    string   // PENINSULA_CLEAN_ENERGY_BASE_URL
```

Add to `.env.example`:

```env
SRP_BASE_URL=https://www.srpnet.com
XCEL_ENERGY_BASE_URL=https://www.xcelenergy.com
CONED_BASE_URL=https://www.coned.com
PNM_BASE_URL=https://www.pnm.com
PENINSULA_CLEAN_ENERGY_BASE_URL=https://www.peninsulacleanenergy.com
```

---

## Registration in `cmd/scraper/main.go`

```go
reg.Register(scrapers.NewSRPScraper(cfg.SRPBaseURL, log))
reg.Register(scrapers.NewXcelEnergyScraper(cfg.XcelEnergyBaseURL, log))
reg.Register(scrapers.NewConEdisonScraper(cfg.ConEdisonBaseURL, log))
reg.Register(scrapers.NewPNMScraper(cfg.PNMBaseURL, log))
reg.Register(scrapers.NewPeninsulaCleanEnergyScraper(cfg.PeninsulaCleanEnergyBaseURL, log))
```

---

## Implementation Steps

### Step 1 — Shared infrastructure
1. Create `scrapers/sitemap_parser.go` with `FetchSitemapURLs(ctx, urls)` — handles both `<sitemapindex>` (recursive, depth-limited) and `<urlset>` formats; skips HTML error responses
2. Create `scrapers/url_filter.go` with `URLFilterConfig` struct and `FilterRebateURLs(urls, cfg)` — exclusions checked first, then inclusions

### Step 2 — SRP scraper
1. Create `scrapers/srp.go` implementing `Scraper` interface
2. Phase 1: call `FetchSitemapURLs` → `FilterRebateURLs` with SRP config
3. Phase 2: Colly-visit each URL, extract fields, build `models.Incentive`
4. Test: `SOURCE=srp RUN_ONCE=true LOG_FORMAT=console go run ./cmd/scraper`

### Step 3 — Xcel Energy scraper
1. Create `scrapers/xcel_energy.go`
2. Same two-phase pattern; note: large sitemap, apply stricter filtering
3. Test: `SOURCE=xcel_energy RUN_ONCE=true ...`

### Step 4 — Con Edison scraper
1. Create `scrapers/con_edison.go`
2. Enable Colly anti-scraping protection (`antiScrapingProtection: true` in SmythOS implies Cloudflare or similar); may need `chromedp` or a backoff strategy
3. Test: `SOURCE=con_edison RUN_ONCE=true ...`

### Step 5 — PNM scraper
1. Create `scrapers/pnm.go`
2. Handle sitemap index recursion; skip `Access Denied` responses from child sitemaps
3. Test: `SOURCE=pnm RUN_ONCE=true ...`

### Step 6 — Peninsula Clean Energy scraper
1. Create `scrapers/peninsula_clean_energy.go`
2. Fetch all three sitemaps; merge URL lists before filtering
3. Apply extra blog/news content-check rule (include only if dollar amounts present)
4. Test: `SOURCE=peninsula_clean_energy RUN_ONCE=true ...`

### Step 7 — Config + registration
1. Add 5 env vars to `config/config.go` and `.env.example`
2. Register all 5 scrapers in `cmd/scraper/main.go`

### Step 8 — Verify staging rows
```sql
SELECT source, COUNT(*), MIN(created_at)
FROM rebates_staging
WHERE source IN ('srp','xcel_energy','con_edison','pnm','peninsula_clean_energy')
GROUP BY source;
```

---

## Implementation Checklist

- [x] `scrapers/sitemap.go` — `FetchSitemapURLs` (index + urlset, 3-level recursion) + `FilterSitemapURLs`
- [x] `scrapers/html_helpers.go` — `extractPhone`, `extractEmail`, `inferCategories` (30+ keyword→category rules)
- [ ] `scrapers/srp.go` — SRP scraper *(pending)*
- [x] `scrapers/xcel_energy.go` — Xcel Energy scraper registered (CO, MN, WI)
- [x] `scrapers/con_edison.go` — Con Edison scraper registered (NY)
- [x] `scrapers/pnm.go` — PNM scraper registered (NM, clearesult portal support)
- [ ] `scrapers/peninsula_clean_energy.go` — PCE scraper *(pending)*
- [ ] `config/config.go` — env vars for utility base URLs *(not yet added — scrapers use hardcoded defaults)*
- [x] `cmd/scraper/main.go` — con_edison, pnm, xcel_energy registered
- [x] `Makefile` — `scrape-coned`, `scrape-pnm`, `scrape-xcel` targets added
- [x] `package.json` — `run:con_edison`, `run:pnm`, `run:xcel_energy` tasks added
- [x] `scripts/run.mjs` — new source names whitelisted
- [ ] All scrapers verified in `rebates_staging` with correct `source` values *(pending first run)*
- [x] `docs/scrapers.md` updated with full field-by-field documentation for all 3 scrapers

---

## Open Questions / Risks

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| Con Edison uses anti-scraping (Cloudflare, JS rendering) | High | Use Colly with realistic User-Agent + random delay; if blocked, flag for chromedp or Playwright-based approach |
| PNM child sitemaps return 403/HTML errors | Medium | Already handled in sitemap parser — skip non-XML gracefully |
| Xcel sitemap is very large (1000+ URLs, most irrelevant) | Medium | Strict filtering; log filter stats (total → kept ratio) |
| PCE blog posts change frequently | Low | URL-based deterministic IDs + upsert means re-scraping updates rather than duplicates |
| Page HTML structure changes after implementation | Medium | Colly selectors should be broad (h1, p); log `program_name == ""` rows so empty scrapes are visible |
| SRP uses JavaScript-rendered content | Low | SmythOS uses `javascriptRendering: false` — static HTML should work with Colly |
