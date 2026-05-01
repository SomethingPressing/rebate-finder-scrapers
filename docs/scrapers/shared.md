# Shared Infrastructure

Utilities shared by all HTML-based scrapers (Con Edison, PNM, Xcel Energy, SRP, Peninsula Clean Energy).

---

## Sitemap Parser (`scrapers/sitemap.go`)

### `FetchSitemapURLs(ctx, client, url)`

Fetches a sitemap and returns all `<loc>` entries.

- Handles both `<sitemapindex>` (recursively fetches child sitemaps up to **3 levels** deep) and `<urlset>` (leaf) formats.
- Silently skips child sitemaps that return HTML error pages (`<!DOCTYPE html>`, `"Access Denied"`).
- Returns empty slice on error — scrapers fall back to seed URLs.

### `FilterSitemapURLs(urls, cfg FilterConfig)`

Applies a `FilterConfig` to a URL list using a three-pass decision:

1. **Exclusion check** (any match → reject — checked first)
2. **Path depth check** (`MinPathSegments` — hub page detection)
3. **Inclusion check** (at least one match required)

### `IsPDFURL(u string) bool`

Returns `true` when the URL path ends with `.pdf` (case-insensitive, strips query string before checking).

### `FilterConfig`

```go
type FilterConfig struct {
    ExcludeKeywords []string  // checked first — any match rejects the URL
    IncludeKeywords []string  // at least one must match after exclusions pass
    MinPathSegments int       // URLs with fewer path segments are rejected (hub detection)
}
```

---

## HTML Helpers (`scrapers/html_helpers.go`)

All extraction functions used by every utility HTML scraper:

| Function | Returns | Description |
|----------|---------|-------------|
| `extractPhone(text)` | `string` | First US phone number via regex `(?:\+1[\s.-]?)?(?:\(?\d{3}\)?[\s.-]?\d{3}[\s.-]?\d{4})` |
| `extractEmail(text)` | `string` | First email address via regex `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}` |
| `extractContractorRequired(text)` | `*bool` | `true` if text contains "licensed contractor", "approved contractor", "trade ally", "contractor required", "participating contractor", or "must be installed/completed by" |
| `extractEnergyAuditRequired(text)` | `*bool` | `true` if text contains "energy audit required", "home assessment required", "home energy assessment", "home energy checkup required", or "pre-inspection required" |
| `extractCurrentlyActive(text)` | `*bool` | `false` if text signals program ended ("expired", "program ended", "no longer available", "funding exhausted", "waitlist"); `true` otherwise |
| `extractLowIncomeEligible(text)` | `*bool` | `true` if text mentions income-qualified eligibility (CARE, FERA, LIHEAP, "low-income", "income-qualified", AMI %) |
| `extractCustomerType(urlAndTitle)` | `string` | `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` based on URL and title keywords |
| `extractRecipient(text)` | `string` | `"Homeowner"`, `"Renter"`, `"Landlord"`, `"Small Business Owner"`, or `"Business Owner"` |
| `extractStartDate(text)` | `string` | Date string following "effective", "starting", "beginning", "as of", or "from" keywords |
| `extractEndDate(text)` | `string` | Date string following "ends", "expires", "through", "until", "deadline", "valid through", or "offer ends" keywords |
| `inferCategories(text)` | `[]string` | Category tags from 50+ keyword rules; returns `["Energy Efficiency"]` when no keywords match |

---

## Category Inference

`inferCategories()` scans a combined URL + title string for keywords and returns matching category tags. Multiple categories can be returned for a single page.

| Keyword(s) detected | Category tag |
|--------------------|-------------|
| `heat pump water heater`, `hpwh` | `Water Heating` |
| `water heater` | `Water Heating` |
| `heat pump`, `mini-split`, `geothermal`, `hvac`, `air condition`, `cooling`, `heating`, `furnace`, `boiler` | `HVAC` |
| `evaporative cooler`, `swamp cooler` | `HVAC` |
| `insulation`, `weatheriz`, `weather-ready`, `window`, `door replacement`, `air seal` | `Weatherization` |
| `solar`, `photovoltaic`, `pv system` | `Solar` |
| `battery storage`, `battery` | `Battery Storage` |
| `electric vehicle`, `ev charger`, `ev-ready`, `ev rebate`, `charging station`, `e-bike`, `ebike` | `Electric Vehicles` |
| `smart thermostat`, `thermostat` | `Smart Thermostat` |
| `lighting`, `led` | `Lighting` |
| `appliance`, `refrigerator`, `washer`, `dryer`, `dishwasher` | `Appliances` |
| `pool pump` | `Appliances` |
| `demand response`, `time-of-use`, `peak reward`, `load management`, `smart usage` | `Demand Response` |
| `low income`, `income-qualified`, `income-eligible`, `liheap`, `good neighbor`, `care program`, `fera program` | `Income Qualified` |
| `financing`, `zero percent loan` | `Financing` |
| *(no match)* | `Energy Efficiency` |

---

## PDF Extraction (`scrapers/html_helpers.go`)

All utility scrapers detect PDF URLs with `IsPDFURL()` and extract them via `ExtractIncentiveFromPDFText` instead of Colly.

### `PDFIncentiveOpts`

```go
type PDFIncentiveOpts struct {
    Source         string  // e.g. "con_edison"
    ScraperVersion string
    UtilityCompany string  // e.g. "Con Edison"
    State          string  // 2-letter code; "" for multi-state scrapers (Xcel)
    ZipCode        string  // representative ZIP; "" if unknown
    Territory      string  // service territory label
    DefaultApply   string  // fallback application_process text
}
```

### `ExtractIncentiveFromPDFText(text, pageURL, opts)`

Builds a `models.Incentive` from raw PDF text:

1. **Program name** — first non-blank line (5–200 chars); falls back to filename from URL.
2. **Description** — first paragraph longer than 40 chars; truncated to 500 chars.
3. **Amount** — `ParseAmount(text)`.
4. **All helper fields** — phone, email, `extractContractorRequired`, `extractEnergyAuditRequired`, `extractCustomerType`, `extractStartDate`, `extractEndDate`.
5. **Categories** — `inferCategories(url + title + first 500 chars of text)`.

Returns `nil` if text is empty or the program name looks like an error page.

---

## Amount Parsing (`scrapers/colly_template.go`)

`ParseAmount(s)` parses human-readable incentive strings into `(incentiveFormat, *float64)`:

| Input | Format | Amount |
|-------|--------|--------|
| `"$600"` | `dollar_amount` | `600.0` |
| `"$0.10/kWh"` | `per_unit` | `0.10` |
| `"30%"` | `percent` | `30.0` |
| `"Up to $2,000"` | `dollar_amount` | `2000.0` |
| `"See terms"` | `narrative` | `nil` |
