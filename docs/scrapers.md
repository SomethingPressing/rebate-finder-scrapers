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
- `state` ← `program.StateObj.Abbreviation`
- `category_tag` ← extracted from `ParameterSets[].Technologies`
- `segment` ← extracted from `ParameterSets[].Sectors`
- `customer_type` ← joined sector names from `ParameterSets`
- `portfolio` ← program level derived from `SectorObj.Name` (Federal/State/Utility/Local)
- `product_category` ← top technology category from `ParameterSets`
- `administrator` ← `program.Administrator`
- `start_date`, `end_date` ← `program.StartDate`, `program.EndDate`
- `status` ← `"active"` when `program.Published == "Yes"`, otherwise default `"draft"`
- `program_hash` ← `ComputeProgramHash(ProgramName, UtilityCompany)`
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
- `program_name` ← `"[Authority Name] — [program]"`
- `utility_company` ← `authorityName` resolved from top-level `authorities` map
- `incentive_amount` ← item `amount.number`
- `maximum_amount` ← item `amount.maximum` (when > 0)
- `percent_value` ← item `amount.number * 100` when `amount.type == "percent"`
- `incentive_format` ← derived from `amount.type` (`dollar_amount`, `percent`, `dollars_per_unit`)
- `start_date`, `end_date` ← item `start_date`, `end_date`
- `service_territory` ← `"Nationwide"` (federal) / `"[Authority] Statewide"` (state) / `"[Authority] Service Area"` (utility/city/county)
- `portfolio` ← `["Federal"]` / `["State"]` / `["Utility"]` / `["Local"]` from `authority_type`
- `segment` ← `item.owner_status` (e.g. `["homeowner", "renter"]`)
- `category_tag` ← human-readable labels from `item.items` (product-type strings)
- `product_category` ← `raProductCategory(items[0])` — maps first item key to category tag
- `available_nationwide` ← `true` when `authority_type == "federal"`, else `false`
- `source` = `"rewiring_america"`

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

1. Fetches `https://www.coned.com/sitemap.xml` and applies `conEdisonFilterCfg` (`FilterConfig`) — exclusions checked first, then inclusion keywords.
2. Falls back to hardcoded seed URLs (specific program sub-pages) if the sitemap is unavailable or returns no matches.
3. Visits each URL with Colly and extracts program data using HTML selectors and regex helpers.

**URL filter — exclusions (checked first):** `/using-distributed-generation`, `/shop-for-energy-service`, `/our-energy-vision`, `/where-we-are-going`, `/my-account`, `/login`, `/sign-in`, `/about-us`, `/careers`, `/media-center`, `/news`, `/investor`, `/safety`, `/outages`, `/grid`, `/transmission`, `/tariff`, `/fault-current`, `/contact-us`, `/terms-of-use`, `/privacy`, `/search`

**URL filter — inclusions (must match one):** `rebate`, `incentive`, `save-money`, `saving`, `credit`, `reward`, `assistance`, `payment-plans-assistance`, `heat-pump`, `electric-vehicle`, `solar`, `financing`, `smart-usage`, `demand-response`, `weatherization`, `insulation`, `efficiency`, `low-income`, `income-eligible`, `find-incentive`, `incentive-viewer`

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
| `ContractorRequired` | `extractContractorRequired(pageText)` — `true` if licensed/approved contractor language found |
| `EnergyAuditRequired` | `extractEnergyAuditRequired(pageText)` — `true` if energy audit language found |
| `CustomerType` | `extractCustomerType(url + title)` — `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` |
| `StartDate` | `extractStartDate(pageText)` — date after "effective", "starting", "as of" keywords |
| `EndDate` | `extractEndDate(pageText)` — date after "expires", "through", "deadline" keywords |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Con Edison")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `image_url`, `rate_tiers`

**Rate limiting:** 600 ms delay between requests, parallelism = 2.

**Configuration:** No required env vars (uses hardcoded base URL).

---

## PNM (`pnm`)

**Source:** [pnm.com](https://www.pnm.com) — Public Service Company of New Mexico

**Approach:** Two-phase sitemap crawl + Colly HTML scraping across `pnm.com` and `pnm.clearesult.com` (third-party rebate portal).

1. Fetches `https://www.pnm.com/sitemap.xml` (may be a sitemap index with nested sitemaps; child sitemaps returning HTML "Access Denied" are silently skipped) and applies `pnmFilterCfg` (`FilterConfig`).
2. Falls back to hardcoded seed URLs if unavailable.
3. Visits each URL; `pnm.clearesult.com` is an allowed domain for Colly so clearesult portal links are followed for `application_url`.

**URL filter — exclusions (checked first):** `/about-pnm`, `/corporate`, `/investor`, `/news`, `/careers`, `/jobs`, `/regulatory`, `/filings`, `/tariffs`, `/legal`, `/terms`, `/privacy`, `/login`, `/sign-in`, `/my-account`, `/outages`, `/safety`, `/storm`, `/emergency`, `/start-service`, `/stop-service`, `/pay-bill`, `/documents`, `/media`, `/education`, `/community`, `/infrastructure`, `/grid`, `/generation`, `/power-plants`, `/transmission`

**URL filter — inclusions (must match one):** `save-money-and-energy`, `rebate`, `incentive`, `checkup`, `energy-efficiency`, `weatherization`, `appliance-recycling`, `solar`, `pnmskyblue`, `/ev`, `electric-vehicle`, `goodneighborfund`, `assistance`, `liheap`, `low-income`, `time-of-use`, `demand-response`, `quick-saver`, `heat-pump`, `thermostat`, `lighting`

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
| `ContractorRequired` | `extractContractorRequired(pageText)` |
| `EnergyAuditRequired` | `extractEnergyAuditRequired(pageText)` |
| `CustomerType` | `extractCustomerType(url + title)` |
| `StartDate` | `extractStartDate(pageText)` |
| `EndDate` | `extractEndDate(pageText)` |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "PNM")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `maximum_amount`, `image_url`, `rate_tiers`

**Rate limiting:** 600 ms delay between requests, parallelism = 2.

**Configuration:** No required env vars (uses hardcoded base URLs).

---

## Xcel Energy (`xcel_energy`)

**Source:** [xcelenergy.com](https://www.xcelenergy.com) — Xcel Energy (multi-state utility)

**Approach:** Single corporate sitemap crawl + Colly HTML scraping. All rebate pages live on the main `xcelenergy.com` domain.

**Sitemap:** `https://www.xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml`

**States covered:** CO, MN, WI, ND, SD, NM — state is auto-detected from page text rather than URL subdomain.

1. Fetches the static corporate XML sitemap and applies `xcelFilterCfg` (`FilterConfig`) with extensive exclusion-first logic and `MinPathSegments: 3` hub-page detection.
2. Falls back to hardcoded seed URLs under `https://www.xcelenergy.com/programs_and_rebates/` if sitemap fails.
3. Visits each page; state is extracted from page text via `xcelStateFromText()` (matches "Colorado" → `"CO"`, "Minnesota" → `"MN"`, etc.). Service territory and representative ZIP are derived from detected state.

**URL filter — hub page detection:** URLs with fewer than 3 path segments are rejected as category/hub pages (e.g. `/programs_and_rebates/equipment_rebates` depth=2 → excluded; `/programs_and_rebates/equipment_rebates/lighting_efficiency` depth=3 → included).

**URL filter — exclusions (checked first):** Corporate (`/company/`, `/about_us/`, `/investor_relations/`, `/careers/`, `/media_room/`, `/news_releases/`, etc.), infrastructure (`/rates_and_regulations/`, `/filings/`, `/outages_and_emergencies/`, `/billing_and_payment/`, `/power_plants/`, etc.), pattern exclusions (`_tool`, `_finder`, `_calculator`, `_advisor`, `/ways_to_save`, `_sign_up`, `_faq`, `_how_it_works`, `_case_study`, `/my_account`, etc.)

**URL filter — inclusions (must match one):** `rebate`, `rebates`, `incentive`, `reward`, `savings`, `efficient`, `upgrade`, `heat_pump`, `heat-pump`, `hvac`, `appliance`, `thermostat`, `solar`, `electric_vehicle`, `battery_storage`, `assistance`, `low_income`, `demand_response`, `peak_reward`, `saver`, `lighting`, `insulation`, `programs_and_rebates`, `program`

**ID generation:** `DeterministicID("xcel_energy", pageURL)` — stable per page URL.

**Fields populated:**

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("xcel_energy", pageURL)` |
| `Source` | `"xcel_energy"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of suffix |
| `UtilityCompany` | `"Xcel Energy"` (hardcoded) |
| `State` | Auto-detected from page text via `xcelStateFromText()` — e.g. "Colorado" → `"CO"`, "Minnesota" → `"MN"` |
| `ZipCode` | Derived from detected state via `xcelZIPFromState()` — e.g. CO→`80202`, MN→`55401`, WI→`53202`, NM→`87102`, ND→`58501`, SD→`57501` |
| `ServiceTerritory` | Derived from detected state via `xcelTerritoryFromState()` — e.g. `"Xcel Energy Colorado Service Area"` |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text, `<p>`, `<li>`, `<td>`, `<strong>` |
| `ApplicationURL` | First `<a href>` with "apply", "application", "submit", or "enroll" in text/href |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official Xcel Energy program website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL path and title keywords — see [Category Inference](#category-inference) |
| `ContractorRequired` | `extractContractorRequired(pageText)` |
| `EnergyAuditRequired` | `extractEnergyAuditRequired(pageText)` |
| `CustomerType` | `extractCustomerType(url + title)` |
| `StartDate` | `extractStartDate(pageText)` |
| `EndDate` | `extractEndDate(pageText)` |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Xcel Energy")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `maximum_amount`, `image_url`, `rate_tiers`

**Rate limiting:** 600 ms delay between requests, parallelism = 2.

**Configuration:** No required env vars (uses hardcoded corporate sitemap URL).

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

| Function | Returns | Description |
|----------|---------|-------------|
| `extractPhone(text)` | `string` | First US phone number found using regex `(?:\+1[\s.-]?)?(?:\(?\d{3}\)?[\s.-]?\d{3}[\s.-]?\d{4})` |
| `extractEmail(text)` | `string` | First email address found using regex `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}` |
| `extractContractorRequired(text)` | `*bool` | `true` if text matches "licensed contractor", "approved contractor", "trade ally", "contractor required", "participating contractor", or "must be installed/completed by" |
| `extractEnergyAuditRequired(text)` | `*bool` | `true` if text matches "energy audit required", "home assessment required", "home energy assessment", "home energy checkup required", or "pre-inspection required" |
| `extractCurrentlyActive(text)` | `*bool` | `false` if text signals program ended ("expired", "program ended", "no longer available", "funding exhausted", "waitlist"); `true` otherwise |
| `extractLowIncomeEligible(text)` | `*bool` | `true` if text mentions income-qualified eligibility (CARE, FERA, LIHEAP, "low-income", "income-qualified", AMI percentage) |
| `extractCustomerType(urlAndTitle)` | `string` | `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` based on URL and title keywords |
| `extractRecipient(text)` | `string` | `"Homeowner"`, `"Renter"`, `"Landlord"`, `"Small Business Owner"`, or `"Business Owner"` |
| `extractStartDate(text)` | `string` | Date string following "effective", "starting", "beginning", "as of", or "from" keywords |
| `extractEndDate(text)` | `string` | Date string following "ends", "expires", "through", "until", "deadline", "valid through", or "offer ends" keywords |
| `inferCategories(text)` | `[]string` | Category tags inferred from 50+ keyword rules against the combined URL + title text; returns `["Energy Efficiency"]` when no keywords match |

---

## Shared Sitemap Parser (`scrapers/sitemap.go`)

All utility scrapers use `FetchSitemapURLs()` and `FilterSitemapURLs()`:

| Function | Description |
|----------|-------------|
| `FetchSitemapURLs(ctx, client, url)` | Fetches a sitemap and returns all `<loc>` entries. Handles both `<sitemapindex>` (recursively fetches child sitemaps up to 3 levels deep) and `<urlset>` (leaf) formats. Silently skips child sitemaps that return HTML error pages. Returns empty slice on error — scrapers fall back to seed URLs. |
| `FilterSitemapURLs(urls, cfg FilterConfig)` | Applies a `FilterConfig` to a URL list. Exclusion keywords are checked first (any match → reject). Then path depth is checked via `MinPathSegments` (URLs with fewer segments → reject). Finally, at least one inclusion keyword must match. |

```go
// FilterConfig holds the two-pass URL filtering configuration.
type FilterConfig struct {
    ExcludeKeywords []string  // checked first — any match rejects the URL
    IncludeKeywords []string  // at least one must match after exclusions pass
    MinPathSegments int       // URLs with fewer path segments are rejected (hub detection)
}
```

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
