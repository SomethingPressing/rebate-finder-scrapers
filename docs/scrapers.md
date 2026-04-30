# Scrapers Reference

## Overview

All scrapers implement the `scrapers.Scraper` interface:

```go
type Scraper interface {
    Name()  string                          // stable identifier, e.g. "dsireusa"
    Scrape(ctx context.Context) ([]models.Incentive, error)
}
```

Scrapers are registered in `cmd/scraper/main.go` and run sequentially by `scrapers.RunAll()`.

---

## DSIRE USA (`dsireusa`)

**Source:** [programs.dsireusa.org](https://programs.dsireusa.org) — Database of State Incentives for Renewables & Efficiency

**API:** Public REST API, no key required.

**Approach:**
1. Queries the DSIRE v1 programs endpoint with one representative ZIP per US state (51 ZIPs total).
2. Fetches all matching programs in a single page (`length=-1`) per ZIP.
3. Deduplicates by DSIRE program ID — the same program appearing in multiple states is only stored once.
4. Strips HTML from `summary` fields.

**Endpoint:**
```
GET https://programs.dsireusa.org/api/v1/programs
    ?zipcode[]={zip}
    &category[]=1
    &draw=1
    &start=0
    &length=-1
```

**ID generation:** `DeterministicID("dsireusa", dsireProgram.ID)` — UUID v5 keyed on the DSIRE integer program ID.

**Fields mapped:**
- `program_name` ← `program.ProgramName`
- `incentive_description` ← `program.Summary` (HTML stripped)
- `state` ← first state abbreviation in `program.States`
- `category_tag` ← `program.Categories`
- `segment` ← mapped from `program.SectorTypes`
- `administrator` ← `program.Administrator`
- `start_date`, `end_date` ← `program.StartDate`, `program.EndDate`
- `source` = `"dsireusa"`

**Rate limiting:** No explicit delay (DSIRE API is paged).

**Configuration:**
```env
DSIREUSA_BASE_URL=https://programs.dsireusa.org   # override for testing
```

---

## Rewiring America (`rewiring_america`)

**Source:** [rewiringamerica.org](https://www.rewiringamerica.org) — IRA incentive calculator

**API:** Requires a free API key from [rewiringamerica.org/api](https://www.rewiringamerica.org/api).

**Approach:**
1. Queries the IRA calculator endpoint with representative ZIPs for 51 major US cities.
2. Fixed household profile: homeowner, joint filing, $80,000 income, 4-person household.
3. Deduplicates by program+technology combination.

**Endpoint:**
```
GET https://api.rewiringamerica.org/api/v1/calculator
    ?zip={zip}
    &owner_status=homeowner
    &tax_filing=joint
    &household_income=80000
    &household_size=4
    &utility=
```

**ID generation:** `DeterministicID("rewiring_america", programName+"|"+technology)` — stable across re-scrapes for the same program/technology pair.

**Fields mapped:**
- `program_name` ← program name + technology type
- `incentive_amount` ← item amount
- `maximum_amount` ← item max_amount
- `incentive_format` ← derived from payment_method and unit
- `source` = `"rewiring_america"`
- `available_nationwide` = `true` (IRA programs are federal)
- `segment` ← `["Residential"]`
- `portfolio` ← `["Federal"]`

**Rate limiting:** 200 ms delay between ZIP requests.

**Configuration:**
```env
REWIRING_AMERICA_API_KEY=your_key_here          # required
REWIRING_AMERICA_BASE_URL=https://api.rewiringamerica.org   # override for testing
```

---

## Energy Star (`energy_star`)

**Source:** [energystar.gov/about/federal_tax_credits](https://www.energystar.gov/about/federal_tax_credits)

**Approach:** HTML scraping via [Colly](https://go-colly.org/). Targets card containers on the tax credits page.

**Selectors:**
```
.views-row                          → card container
.views-field-title                  → program name
.field-name-field-description       → description
.field-name-field-incentive         → amount (parsed for $, %)
.field-name-field-credit-type       → credit type / category tag
a[href]                             → program URL
```

Fallback: if `.views-row` yields nothing, tries `article` tags.

**ID generation:** `DeterministicID("energy_star", strings.ToLower(programName))` — keyed on normalized title.

**Fixed values on all rows:**
- `utility_company` = `"U.S. Department of Energy"`
- `administrator` = `"IRS / DOE"`
- `available_nationwide` = `true`
- `segment` = `["Residential"]`
- `portfolio` = `["Federal"]`
- `source` = `"energy_star"`

**Rate limiting:** 500 ms delay between requests (Colly default).

**Configuration:**
```env
ENERGY_STAR_BASE_URL=https://www.energystar.gov   # override for testing
```

---

## Con Edison (`con_edison`)

**Source:** [coned.com](https://www.coned.com) — Consolidated Edison Company of New York

**Approach:** Two-phase sitemap crawl + Colly HTML scraping.

1. Fetches `https://www.coned.com/sitemap.xml` and filters `<loc>` entries by rebate-related keywords.
2. Falls back to hardcoded seed URLs if the sitemap is unavailable or returns no matches.
3. Visits each URL with Colly and extracts program data using HTML selectors and regex.

**URL filter keywords:** `rebate`, `incentive`, `save-money`, `saving`, `efficiency`, `weatheriz`, `weather-ready`, `heat-pump`, `electric-vehicle`, `solar`, `smart-thermostat`, `energy-star`, `financial-assist`

**Seed URLs (fallback):**
```
https://www.coned.com/en/save-money/rebates-incentives-tax-credits-for-homes
https://www.coned.com/en/save-money/rebates-incentives-for-businesses
https://www.coned.com/en/save-money/weatherization
https://www.coned.com/en/save-money/heat-pumps
https://www.coned.com/en/save-money/electric-vehicles
https://www.coned.com/en/save-money/smart-usage-rewards
```

**ID generation:** `DeterministicID("con_edison", pageURL)` — stable per page URL.

**Fields populated:**

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("con_edison", pageURL)` |
| `Source` | `"con_edison"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of " \| Con Edison" suffix |
| `UtilityCompany` | `"Con Edison"` (hardcoded) |
| `State` | `"NY"` (hardcoded) |
| `ZipCode` | `"10001"` (Manhattan — representative NY ZIP) |
| `ServiceTerritory` | `"Con Edison Service Territory"` (hardcoded) |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed from page text via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text |
| `MaximumAmount` | Amount from "up to $X" patterns if larger than `IncentiveAmount` |
| `ApplicationURL` | First `<a href>` with text/href containing "apply", "application", "enroll", or "sign up" |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official Con Edison program website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL path and title keywords — see [Category Inference](#category-inference) |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Con Edison")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `customer_type`, `start_date`, `end_date`, `contractor_required`, `energy_audit_required`, `image_url`, `rate_tiers`

**Rate limiting:** 600 ms delay between requests, parallelism = 2.

**Configuration:** No required env vars (uses hardcoded base URL).

---

## PNM (`pnm`)

**Source:** [pnm.com](https://www.pnm.com) — Public Service Company of New Mexico

**Approach:** Two-phase sitemap crawl + Colly HTML scraping across `pnm.com` and `pnm.clearesult.com` (third-party rebate portal).

1. Fetches `https://www.pnm.com/sitemap.xml` (may be a sitemap index with nested sitemaps) and filters by keywords.
2. Falls back to hardcoded seed URLs if unavailable.
3. Visits each URL; also follows clearesult portal links for `application_url`.

**URL filter keywords:** `rebate`, `incentive`, `saving`, `efficiency`, `cool-rebate`, `heat-pump`, `thermostat`, `appliance`, `solar`, `ev-charger`, `electric-vehicle`, `low-income`, `weatheriz`, `lighting`

**Seed URLs (fallback):**
```
https://www.pnm.com/residential-rebates
https://www.pnm.com/business-rebates
https://www.pnm.com/cool-rebate
https://www.pnm.com/residential-energy-efficiency
https://www.pnm.com/electric-vehicles
https://www.pnm.com/low-income-programs
https://pnm.clearesult.com/
```

**ID generation:** `DeterministicID("pnm", pageURL)` — stable per page URL.

**Fields populated:**

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("pnm", pageURL)` |
| `Source` | `"pnm"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of suffix |
| `UtilityCompany` | `"PNM"` (hardcoded) |
| `State` | `"NM"` (hardcoded) |
| `ZipCode` | `"87102"` (Albuquerque — largest NM city) |
| `ServiceTerritory` | `"PNM Service Area"` (hardcoded) |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text, `<p>`, `<li>`, `<td>`, `<strong>` |
| `ApplicationURL` | First `<a href>` with text/href containing "apply", "application", "submit", or "enroll"; clearesult.com links are preferred |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official PNM program website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL path and title keywords — see [Category Inference](#category-inference) |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "PNM")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `customer_type`, `maximum_amount`, `start_date`, `end_date`, `contractor_required`, `energy_audit_required`, `image_url`, `rate_tiers`

**Rate limiting:** 600 ms delay between requests, parallelism = 2.

**Configuration:** No required env vars (uses hardcoded base URLs).

---

## Xcel Energy (`xcel_energy`)

**Source:** [xcelenergy.com](https://www.xcelenergy.com) — Xcel Energy (multi-state utility)

**Approach:** Per-state two-phase sitemap crawl + Colly HTML scraping. Iterates three state subdomains in sequence.

**States covered:**

| Subdomain | State | Service Territory | Representative ZIP |
|-----------|-------|-------------------|-------------------|
| `co.my.xcelenergy.com` | CO | Xcel Energy Colorado Service Area | `80202` (Denver) |
| `mn.my.xcelenergy.com` | MN | Xcel Energy Minnesota Service Area | `55401` (Minneapolis) |
| `wi.my.xcelenergy.com` | WI | Xcel Energy Wisconsin Service Area | `53202` (Milwaukee) |

For each state:
1. Fetches `https://{state}.my.xcelenergy.com/sitemap.xml` and filters by keywords.
2. Falls back to seed paths under `https://{state}.my.xcelenergy.com/s/` if sitemap fails.
3. Visits each page, infers the service territory from page content when possible (overrides the default).

**URL filter keywords:** `rebate`, `incentive`, `saving`, `efficiency`, `heat-pump`, `thermostat`, `electric-vehicle`, `ev-charger`, `solar`, `weatheriz`, `appliance`, `lighting`, `demand-response`, `energy-saving`, `rate-option`, `bill-credit`

**Seed URL paths per state (fallback):**
```
/s/energy-saving-programs
/s/rebates-incentives
/s/residential-rebates
/s/business-rebates
/s/electric-vehicles
/s/renewable-energy
```

**ID generation:** `DeterministicID("xcel_energy", pageURL)` — stable per page URL.

**Fields populated:**

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("xcel_energy", pageURL)` |
| `Source` | `"xcel_energy"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of suffix |
| `UtilityCompany` | `"Xcel Energy"` (hardcoded) |
| `State` | From state config — `"CO"`, `"MN"`, or `"WI"` |
| `ZipCode` | From state config — `"80202"`, `"55401"`, or `"53202"` |
| `ServiceTerritory` | From state config; overridden if page text mentions a specific state (e.g. "Colorado") |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text, `<p>`, `<li>`, `<td>`, `<strong>` |
| `ApplicationURL` | First `<a href>` with "apply", "application", "submit", or "enroll" in text/href |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official Xcel Energy program website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL path and title keywords — see [Category Inference](#category-inference) |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Xcel Energy")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `customer_type`, `maximum_amount`, `start_date`, `end_date`, `contractor_required`, `energy_audit_required`, `image_url`, `rate_tiers`

**Rate limiting:** 600 ms delay between requests, parallelism = 2 per state domain.

**Configuration:** No required env vars. To restrict which states are scraped, modify `XcelEnergyScraper.States` field in `cmd/scraper/main.go`.

---

## Category Inference

All three utility scrapers (`con_edison`, `pnm`, `xcel_energy`) use the same shared `inferCategories()` function in `scrapers/html_helpers.go`. It scans the page URL path and title text for keywords and returns matching category tags.

| Keyword(s) detected | Category tag |
|--------------------|-------------|
| heat pump water heater | `Water Heating` |
| water heater | `Water Heating` |
| heat pump | `HVAC` |
| hvac, air condition, cooling, heating, furnace, boiler | `HVAC` |
| insulation, weatheriz, weather-ready, window, door | `Weatherization` |
| solar, photovoltaic, pv system | `Solar` |
| electric vehicle, ev charger, charging station | `Electric Vehicles` |
| smart thermostat, thermostat | `Smart Thermostat` |
| lighting, led | `Lighting` |
| appliance, refrigerator, washer, dryer, dishwasher | `Appliances` |
| battery, storage | `Battery Storage` |
| demand response, time-of-use | `Demand Response` |
| low income, affordable | `Income Qualified` |
| *(no match)* | `Energy Efficiency` |

Multiple categories can be returned for a single page (e.g., a heat pump water heater page gets both `HVAC` and `Water Heating`).

---

## Shared HTML Helpers (`scrapers/html_helpers.go`)

All utility HTML scrapers share these extraction helpers:

| Function | Description |
|----------|-------------|
| `extractPhone(text)` | Returns the first US phone number found in the text using regex `(?:\+1[\s.-]?)?(?:\(?\d{3}\)?[\s.-]?\d{3}[\s.-]?\d{4})` |
| `extractEmail(text)` | Returns the first email address found using regex `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}` |
| `inferCategories(text)` | Returns `[]string` of category tags inferred from keyword matching against the combined URL + title text |

---

## Shared Sitemap Parser (`scrapers/sitemap.go`)

All utility scrapers use `FetchSitemapURLs()` and `FilterSitemapURLs()`:

| Function | Description |
|----------|-------------|
| `FetchSitemapURLs(ctx, client, url)` | Fetches a sitemap and returns all `<loc>` entries. Handles both `<sitemapindex>` (recursively fetches child sitemaps up to 3 levels deep) and `<urlset>` (leaf) formats. Returns empty slice on error — scrapers fall back to seed URLs. |
| `FilterSitemapURLs(urls, keywords)` | Returns only those URLs whose full URL string contains at least one of the provided keywords (case-insensitive). |

---

## Running a Single Scraper

```bash
# Direct (Go)
SOURCE=dsireusa         RUN_ONCE=true go run ./cmd/scraper
SOURCE=rewiring_america RUN_ONCE=true go run ./cmd/scraper
SOURCE=energy_star      RUN_ONCE=true go run ./cmd/scraper
SOURCE=con_edison       RUN_ONCE=true go run ./cmd/scraper
SOURCE=pnm              RUN_ONCE=true go run ./cmd/scraper
SOURCE=xcel_energy      RUN_ONCE=true go run ./cmd/scraper

# Makefile shortcuts
make scrape           # all sources
make scrape-coned     # Con Edison only
make scrape-pnm       # PNM only
make scrape-xcel      # Xcel Energy (CO/MN/WI) only

# pnpm helpers
pnpm run:dsireusa
pnpm run:rewiring_america
pnpm run:energy_star
pnpm run:con_edison
pnpm run:pnm
pnpm run:xcel_energy
```

---

## Amount Parsing (`scrapers/colly_template.go`)

The `ParseAmount(s)` helper parses human-readable incentive strings into `(incentiveFormat, *float64)`:

| Input | Format | Amount |
|-------|--------|--------|
| `"$600"` | `dollar_amount` | `600.0` |
| `"$0.10/kWh"` | `per_unit` | `0.10` |
| `"30%"` | `percent` | `30.0` |
| `"Up to $2,000"` | `dollar_amount` | `2000.0` |
| `"See terms"` | `narrative` | `nil` |

---

## Adding a New Scraper

See [adding-a-scraper.md](adding-a-scraper.md) for a step-by-step guide.
