# Implementation Plan: Utility Web Scraper (PNM, SRP, ConEd, Xcel, Peninsula Clean Energy)

**Source file:** `rf-scraper-pnm-srp-coned-xcel-peninsul-mobyqg49ph.smyth`
**Status:** Planning

---

## Overview

Implement a Go-based **two-stage web scraper** that extracts energy rebate programs from utility company web pages. Unlike the sitemap-based crawlers (which discover URLs via XML sitemaps), this scraper takes a **known page URL as input** and performs deep extraction followed by detail-page enrichment.

The SmythOS agent uses two LLM calls per page:
1. **Extraction**: Given the scraped HTML/markdown content, produce a JSON array of initial incentive records
2. **Enrichment**: For each incentive that has a `program_url`, scrape that detail page and merge richer data into the record

The Go implementation replaces both LLM calls with deterministic parsing logic, supplemented by structured selectors derived from what the LLM prompts encode. For cases where the content is too varied for deterministic extraction, an **optional LLM enrichment** pass is supported via the existing `llm` package.

---

## Supported Sources

| Source ENUM | State | Default Territory | Notes |
|------------|-------|-------------------|-------|
| `Peninsula Clean Energy` | `CA` | `San Mateo County and Los Banos` | CCA, not a utility |
| `PNM` | `NM` | `PNM Service Area` | Electric utility, NM |
| `Con Edison` | `NY` | `Con Edison Service Territory` | Electric/gas, NY/NJ |
| `Xcel Energy` | *(from content)* | *(from content)* | Multi-state: CO, MN, WI, TX, NM |
| `SRP` | `AZ` | `SRP Service Area` | Electric utility, AZ |

---

## Architecture

```
Input: URL (string)

Phase 1 — URL classification
  Detect if URL ends with ".pdf" or content-type is application/pdf
  → PDF path:  Download PDF bytes → extract text with pdftotext
  → HTML path: HTTP GET URL → parse HTML with goquery/Colly

Phase 2 — Extraction
  Given URL + scraped content → extract []PartialIncentive
  (deterministic selectors + fallback regex patterns)

Phase 3 — Detail enrichment (async, per incentive)
  for each incentive with non-empty ProgramURL:
    HTTP GET incentive.ProgramURL
    → extract additional fields
    → merge into incentive record

Phase 4 — Normalize + stage
  for each enriched incentive:
    ApplySourceDefaults(incentive)
    SetDeterministicID(incentive)
    db.UpsertToStaging(incentive)
```

---

## New Files

```
scrapers/
├── utility_web_scraper.go        ← Main scraper (implements Scraper interface)
├── utility_extractor.go          ← HTML extraction logic (selectors + regex)
├── utility_enricher.go           ← Detail-page enrichment logic
└── utility_source_defaults.go    ← Source-specific defaults table
```

---

## Phase 1: URL Classification and Fetching

### PDF Detection

```go
func IsPDFURL(rawURL string) bool {
    u, err := url.Parse(rawURL)
    if err != nil { return false }
    return strings.HasSuffix(strings.ToLower(u.Path), ".pdf")
}

// Also check Content-Type header from HEAD request
func IsPDFByContentType(ctx context.Context, rawURL string) (bool, error)
```

### PDF Text Extraction

Use `pdfcpu` (pure Go) or shell out to `pdftotext`:

```go
// ExtractPDFText extracts readable text from a PDF URL.
// Downloads to a temp file, runs pdftotext, returns combined text.
func ExtractPDFText(ctx context.Context, rawURL string) (string, error)
```

### HTML Fetching

Use Colly with:
- `antiScrapingProtection` equivalent: realistic User-Agent rotation
- `javascriptRendering`: Use `chromedp` for JS-heavy pages (SRP, ConEd)
- `autoScroll`: chromedp scroll helper for lazy-loaded content

```go
type FetchOptions struct {
    UseHeadlessBrowser bool          // true for SRP, ConEd
    Timeout            time.Duration
    UserAgent          string
}

func FetchPage(ctx context.Context, rawURL string, opts FetchOptions) (string, error)
```

---

## Phase 2: Extraction

### Source Detection

Identify the source from the URL domain or page content:

```go
func DetectSource(rawURL string, content string) string {
    // Check URL domain
    // "srpnet.com"              → "SRP"
    // "pnm.com" / "pnm.clearesult.com" → "PNM"
    // "coned.com"              → "Con Edison"
    // "xcelenergy.com"         → "Xcel Energy"
    // "peninsulacleanenergy.com" → "Peninsula Clean Energy"
}
```

### Extraction Selectors by Source

Each source has a set of CSS selectors and regex patterns:

#### SRP (`srpnet.com`)
```go
var SRPSelectors = ExtractorConfig{
    ProgramName:     []string{"h1", "h2.page-title", ".program-title"},
    Description:     []string{".program-description", ".content-body p", ".rebate-description"},
    Amount:          AmountPatterns, // shared regex
    ApplyURL:        []string{"a[href*='apply']", "a[href*='rebate-form']", "a[href*='enroll']"},
    ProgramLinks:    []string{"a[href*='/rebates/']", "a[href*='/energy-savings/']", ".rebate-card a"},
    Category:        []string{".breadcrumb li:last-child", "meta[name='keywords']"},
}
```

#### PNM (`pnm.com`)
```go
var PNMSelectors = ExtractorConfig{
    ProgramName:     []string{"h1.page-title", ".hero-title", "h1"},
    Description:     []string{".field-items p", ".content-main p", ".rebate-body p"},
    Amount:          AmountPatterns,
    ApplyURL:        []string{"a[href*='clearesult.com']", "a[href*='apply']", "a[href*='rebate']"},
    ProgramLinks:    []string{"a[href*='/rebates']", "a[href*='/save-']", ".rebate-link"},
    Category:        []string{".breadcrumb li:last-child"},
}
```

#### Con Edison (`coned.com`)
```go
var ConEdisonSelectors = ExtractorConfig{
    ProgramName:     []string{"h1.hero__title", ".program-name", "h1"},
    Description:     []string{".program-description p", ".content__body p", ".hero__description"},
    Amount:          AmountPatterns,
    ApplyURL:        []string{"a[href*='apply']", "a[href*='rebate']", "a.cta-button"},
    ProgramLinks:    []string{"a[href*='/en/save-energy/']", "a[href*='/en/rebates/']", ".program-card a"},
    Category:        []string{".breadcrumb li:last-child", ".program-category"},
}
```

#### Xcel Energy (`xcelenergy.com`)
```go
var XcelEnergySelectors = ExtractorConfig{
    ProgramName:     []string{"h1.page-title", ".program-header h1", ".rebate-title", "h1"},
    Description:     []string{".program-description", ".content-area p", ".rebate-body"},
    Amount:          AmountPatterns,
    ApplyURL:        []string{"a[href*='rebate']", "a[href*='apply']", "a[href*='enroll']"},
    ProgramLinks:    []string{"a[href*='/programs_and_rebates/']", "a[href*='/savings_and_rebates/']"},
    Category:        []string{".breadcrumb li:last-child"},
    StateExtractor:  extractXcelStateFromContent, // multi-state; parse from "Colorado customers", etc.
}
```

#### Peninsula Clean Energy (`peninsulacleanenergy.com`)
```go
var PCESelectors = ExtractorConfig{
    ProgramName:     []string{"h1.entry-title", ".program-title", "h1"},
    Description:     []string{".entry-content p", ".program-body p", ".rebate-description"},
    Amount:          AmountPatterns,
    ApplyURL:        []string{"a[href*='apply']", "a[href*='rebate']", ".cta a"},
    ProgramLinks:    []string{"a[href*='/rebates-offers/']", "a[href*='/programs/']"},
    Category:        []string{".entry-categories a", ".breadcrumb li:last-child"},
}
```

### Shared Amount Patterns

```go
var AmountPatterns = []AmountPattern{
    {Regexp: `\$(\d[\d,]*)\s*[-–to]+\s*\$(\d[\d,]*)`, Format: "dollar_amount", HasRange: true},
    {Regexp: `[Uu]p to \$(\d[\d,]*)`, Format: "dollar_amount", IsMax: true},
    {Regexp: `\$(\d[\d,]*)(?:\.\d{2})?(?:\s*/\s*(\w+))?`, Format: "dollar_amount"},
    {Regexp: `(\d+(?:\.\d+)?)\s*%`, Format: "percent"},
    {Regexp: `\$(\d+(?:\.\d+)?)\s*per\s+(\w+)`, Format: "per_unit"},
    {Regexp: `(\d+(?:\.\d+)?)\s*%\s*APR`, Format: "financing"},
    {Regexp: `0%\s*(?:APR|financing|interest)`, Format: "financing"},
}
```

### Tiered Rebates → Multiple Records

When a page describes multiple tiers (different amounts for different equipment types), split into separate records:

```go
// DetectTiers looks for patterns like rebate tables, bullet lists with amounts,
// or headings followed by dollar amounts. Returns one PartialIncentive per tier.
func DetectTiers(content, sourcePage, source string) []PartialIncentive
```

Naming convention for split records:
- `"Evaporative Cooler Rebate - Large Units"` (`maximum_amount: 400`)
- `"Evaporative Cooler Rebate - Window-Mounted"` (`maximum_amount: 200`)

---

## Phase 3: Detail-Page Enrichment

### When to enrich

Enrich if the extracted `PartialIncentive` has a non-empty `ProgramURL` **and** the `ProgramURL != SourcePage`. Skip enrichment if:
- `ProgramURL` is empty or the same as `SourcePage`
- The `ProgramURL` 404s or returns an error
- The enrichment HTTP request exceeds 30 seconds

### Enrichment merge rules

| Field behavior | Rule |
|---------------|------|
| Conflict between listing and detail page | Prefer detail page |
| Field missing in listing, found in detail | Add from detail |
| `source`, `source_page`, `program_url` | **Never change** |
| `incentive_amount` / `maximum_amount` | Update if detail page has more specific value |
| `eligibility_summary`, `special_requirements` | Merge if both have content |
| `notes` | Append detail-page caveats if new information |

---

## Phase 4: Source Defaults

Apply after extraction and enrichment, before staging:

```go
type SourceDefaults struct {
    Source          string
    State           string // "" means extract from content
    UtilityCompany  string
    ServiceTerritory string // "" means "[UtilityCompany] Service Area"
    ZipCodeDefault  string // state-capital ZIP if not found in content
}

var SourceDefaultsTable = map[string]SourceDefaults{
    "Peninsula Clean Energy": {State: "CA", UtilityCompany: "Peninsula Clean Energy", ServiceTerritory: "San Mateo County and Los Banos", ZipCodeDefault: "94402"},
    "PNM":                    {State: "NM", UtilityCompany: "PNM", ServiceTerritory: "PNM Service Area", ZipCodeDefault: "87101"},
    "Con Edison":             {State: "NY", UtilityCompany: "Con Edison", ServiceTerritory: "Con Edison Service Territory", ZipCodeDefault: "10001"},
    "Xcel Energy":            {State: "",   UtilityCompany: "Xcel Energy", ZipCodeDefault: "80202"}, // CO default
    "SRP":                    {State: "AZ", UtilityCompany: "SRP", ServiceTerritory: "SRP Service Area", ZipCodeDefault: "85001"},
}
```

> **Xcel state extraction**: Parse phrases like `"Colorado residential customers"`, `"Minnesota customers"`, URL subdomain (`co.my.xcelenergy.com` → `CO`), or page title.

---

## ID Generation

```go
// ID keyed on source + source page URL + program name hash
// (no stable API ID available; URL + name combination is most stable)
inc.ID = models.DeterministicID(source, sourcePage+"#"+normalizedProgramName)

// Program hash for cross-source deduplication
inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)
```

---

## Required Output Fields

Every staged incentive must have:

| Field | Validation |
|-------|-----------|
| `source` | Must be one of the 5 valid ENUM values |
| `program_name` | Non-empty |
| `utility_company` | From source defaults if not found |
| `service_territory` | From source defaults or `"[utility] Service Area"` |
| `incentive_description` | Non-empty (use first paragraph if no explicit description) |
| `incentive_format` | Must be valid ENUM |
| `application_process` | Default: `"Visit the official program website to learn about eligibility requirements and submit your application."` |
| `source_page` | The input URL |
| `program_url` OR `source_page` | At least one must be set |

---

## Configuration

Add to `config/config.go`:

```go
UtilityWebScraperHeadless bool          // UTILITY_WEB_SCRAPER_HEADLESS
UtilityWebScraperTimeout  time.Duration // UTILITY_WEB_SCRAPER_TIMEOUT_SEC
UtilityWebScraperDelay    time.Duration // UTILITY_WEB_SCRAPER_DELAY_MS
UtilityWebScraperURLs     []string      // UTILITY_WEB_SCRAPER_URLS (comma-separated seed URLs)
```

Add to `.env.example`:

```env
UTILITY_WEB_SCRAPER_HEADLESS=false
UTILITY_WEB_SCRAPER_TIMEOUT_SEC=30
UTILITY_WEB_SCRAPER_DELAY_MS=1000
# Seed URLs — one known rebate listing page per utility
UTILITY_WEB_SCRAPER_URLS=https://www.srpnet.com/energy-savings-rebates/home/rebates/residential-rebates,https://www.pnm.com/save-money-and-energy,https://www.coned.com/en/save-energy,https://co.my.xcelenergy.com/rebates,https://www.peninsulacleanenergy.com/rebates-offers/
```

---

## Registration

Add to `cmd/scraper/main.go`:

```go
reg.Register(scrapers.NewUtilityWebScraper(
    cfg.UtilityWebScraperURLs,
    cfg.UtilityWebScraperHeadless,
    cfg.UtilityWebScraperTimeout,
    cfg.UtilityWebScraperDelay,
    log,
))
```

---

## Implementation Steps

### Step 1 — Source detection + defaults table
1. Create `scrapers/utility_source_defaults.go` with `SourceDefaultsTable` and `DetectSource(url, content)`
2. Unit-test source detection against known URLs

### Step 2 — PDF detection + text extraction
1. Implement `IsPDFURL` and `IsPDFByContentType` in `scrapers/utility_web_scraper.go`
2. Implement `ExtractPDFText` using `pdfcpu` or `pdftotext`

### Step 3 — HTML fetching
1. Add `FetchPage(ctx, url, opts)` — standard Colly for simple pages, `chromedp` for SRP/ConEd
2. Headless mode controllable via config (`UtilityWebScraperHeadless`)

### Step 4 — Extraction
1. Create `scrapers/utility_extractor.go` with selector configs for all 5 sources
2. Implement `ExtractIncentives(url, content, source) []PartialIncentive`
3. Implement `parseIncentiveAmount` shared helper
4. Implement `DetectTiers` for tiered rebate splitting

### Step 5 — Enrichment
1. Create `scrapers/utility_enricher.go` with `EnrichIncentive(ctx, inc) models.Incentive`
2. Fetch `ProgramURL`, parse, merge according to merge rules
3. Respect rate limit delay between enrichment requests

### Step 6 — Normalize + stage
1. Apply `SourceDefaults`, validate required fields
2. Set `ID` and `ProgramHash`
3. Drop records that fail validation (log reason)

### Step 7 — Register + test
1. Add env vars, register scraper in `main.go`
2. Test each source individually:
   ```bash
   SOURCE=utility_web RUN_ONCE=true UTILITY_WEB_SCRAPER_URLS=https://www.srpnet.com/energy-savings-rebates/home/rebates/residential-rebates go run ./cmd/scraper
   ```
3. Verify staging rows per source:
   ```sql
   SELECT source, COUNT(*) FROM rebates_staging
   WHERE source IN ('SRP','PNM','Con Edison','Xcel Energy','Peninsula Clean Energy')
   GROUP BY source;
   ```

### Step 8 — PDF test
1. Find a known PDF URL from PNM or Con Edison (check their document libraries)
2. Test `IsPDFURL` + `ExtractPDFText` end-to-end

---

## Implementation Checklist

- [ ] `scrapers/utility_source_defaults.go` — defaults table + `DetectSource`
- [ ] `scrapers/utility_web_scraper.go` — `UtilityWebScraper` implements `Scraper` interface
- [ ] `scrapers/utility_extractor.go` — per-source selectors + amount regex + tier splitting
- [ ] `scrapers/utility_enricher.go` — detail-page fetch + field merge
- [ ] PDF detection + text extraction working
- [ ] Source-specific defaults applied after extraction
- [ ] `incentive_format` derived from amount patterns for all 5 formats
- [ ] Tier splitting creates separate records with name suffixes
- [ ] `program_url` vs `source_page` handled correctly
- [ ] Required fields validated before staging (drop + log invalid records)
- [ ] Xcel Energy multi-state detection
- [ ] `DeterministicID` keyed on source + URL + program name
- [ ] `config/config.go` — 4 new env vars
- [ ] `.env.example` — seed URLs for all 5 utilities
- [ ] `cmd/scraper/main.go` — scraper registered
- [ ] Verified in `rebates_staging` for all 5 sources
- [ ] `docs/scrapers.md` updated with utility web scraper entry

---

## Open Questions / Risks

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| SRP and ConEd use heavy JavaScript rendering | High | `chromedp` fallback in `FetchPage`; controllable via `UTILITY_WEB_SCRAPER_HEADLESS` |
| Page structure diverges significantly from selectors | High | Log fields that came back empty; use broad selectors (h1, p) as fallback |
| Tiered rebate detection false positives (splitting when it shouldn't) | Medium | Unit-test `DetectTiers` against real page samples before deploying |
| PNM uses third-party portal (clearesult.com) for some program pages | Medium | `application_url` field captures clearesult links; enricher should follow them |
| PDF extraction quality varies (scanned PDFs = no text) | Medium | Log OCR failure; skip record with note in `program_details` |
| Xcel Energy is multi-state — wrong state assigned | Medium | Extract state from URL subdomain, page title, or first paragraph mentioning state name |
| Enrichment stage doubles HTTP requests | Known | Async enrichment per incentive; bounded concurrency (max 5 concurrent enrichments) |
| ConEd anti-scraping (Cloudflare) | High | Realistic User-Agent, respect delays; if blocked, flag for Playwright-based approach |
