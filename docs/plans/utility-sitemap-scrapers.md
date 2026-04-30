# Implementation Plan: Utility Sitemap Scrapers

**Source file:** `rf-crawler-pnm-srp-coned-xcel-peninsul-mobyct44nw.smyth`  
**Scraper source file:** `rf-scraper-pnm-srp-coned-xcel-peninsul-mobyqg49ph.smyth`  
**Status:** ✅ Complete — 95% (5/5 scrapers implemented; staging verification pending)  
**Estimated Complexity:** Large

---

## Overview

Five new Go scrapers for utility companies whose rebate/incentive pages are discovered dynamically via XML sitemaps. Unlike the existing API-based scrapers (DSIRE, Rewiring America), these utilities don't expose structured APIs — their program data lives on marketing web pages.

The SmythOS agent uses a two-phase approach:
1. **Sitemap crawl → URL discovery**: Fetch the utility's XML sitemap(s), then use an LLM to identify which URLs describe rebate/incentive programs.
2. **Page scraping**: Fetch each discovered URL and extract program data.

The Go implementation replaces the LLM-based URL filter with a deterministic keyword/pattern filter (`FilterConfig`) derived from the same decision framework the LLM prompts encode.

---

## Utilities to Implement

| Scraper | Source ID | State(s) | Status | Sitemap URL |
|---------|-----------|----------|--------|-------------|
| Con Edison | `con_edison` | NY | ✅ Complete | `https://www.coned.com/sitemap.xml` |
| PNM | `pnm` | NM | ✅ Complete | `https://www.pnm.com/sitemap.xml` (index) |
| Xcel Energy | `xcel_energy` | CO, MN, WI, ND, SD, NM | ✅ Complete | `https://www.xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml` |
| SRP (Salt River Project) | `srp` | AZ | ✅ Complete | `https://www.srpnet.com/sitemap.xml` |
| Peninsula Clean Energy | `peninsula_clean_energy` | CA | ✅ Complete | Four sitemaps (page, post, news-releases, articles) |

---

## Architecture

```
For each utility:

  1. Sitemap Fetch
     └─ FetchSitemapURLs(ctx, client, sitemapURL)
     └─ Handles both <sitemapindex> (recursive, depth ≤ 3) and <urlset> (leaf)
     └─ Skips HTML error pages ("Access Denied", <!DOCTYPE html>)

  2. URL Filter (FilterSitemapURLs)
     └─ Exclusion check FIRST (any match → reject)
     └─ Path-depth check (hub page detection via MinPathSegments)
     └─ Inclusion check (must match ≥1 keyword)

  3. Page Scrape (Colly)
     └─ Visit each candidate URL
     └─ Extract: program name, description, amount, apply URL, etc.
     └─ Boolean fields via html_helpers: contractor_required, energy_audit_required,
        customer_type, start_date, end_date
     └─ Categories via inferCategories(url + title)
     └─ Build models.Incentive

  4. Stage
     └─ db.UpsertToStaging(incentives)
```

---

## Implemented Files

```
scrapers/
├── sitemap.go                  ← FetchSitemapURLs + FilterSitemapURLs(FilterConfig)
├── html_helpers.go             ← extractPhone, extractEmail, inferCategories (50+ keywords),
│                                  extractContractorRequired, extractEnergyAuditRequired,
│                                  extractCurrentlyActive, extractLowIncomeEligible,
│                                  extractCustomerType, extractRecipient,
│                                  extractStartDate, extractEndDate
├── con_edison.go               ← Con Edison scraper (conEdisonFilterCfg)
├── pnm.go                      ← PNM scraper (pnmFilterCfg)
├── xcel_energy.go              ← Xcel Energy scraper (xcelFilterCfg, MinPathSegments=3)
├── srp.go                      ← SRP scraper (srpFilterCfg)
└── peninsula_clean_energy.go   ← PCE scraper (pceFilterCfg, 4 sitemaps)
```

---

## Shared Infrastructure

### `scrapers/sitemap.go`

```go
// FilterConfig holds the two-pass URL filtering configuration.
type FilterConfig struct {
    ExcludeKeywords []string  // checked FIRST — any match → reject
    IncludeKeywords []string  // at least one must match after exclusions pass
    MinPathSegments int       // reject URLs with fewer path segments (hub detection)
}

// FetchSitemapURLs fetches a sitemap URL and returns all <loc> entries.
// Recursively resolves sitemap index references up to 3 levels.
// Skips child sitemaps that return HTML error pages.
func FetchSitemapURLs(ctx, client, sitemapURL) ([]string, error)

// FilterSitemapURLs applies a FilterConfig to a list of URLs.
// Exclusions are checked first; acceptance requires ≥1 inclusion match.
func FilterSitemapURLs(urls []string, cfg FilterConfig) []string
```

### `scrapers/html_helpers.go`

All extraction functions used by the three utility scrapers:

| Function | Returns | Pattern |
|----------|---------|---------|
| `extractPhone(text)` | `string` | US phone regex |
| `extractEmail(text)` | `string` | Email regex |
| `extractContractorRequired(text)` | `*bool` | "licensed contractor", "trade ally" |
| `extractEnergyAuditRequired(text)` | `*bool` | "energy audit required", "home assessment" |
| `extractCurrentlyActive(text)` | `*bool` | "expired", "program ended", "no longer available" |
| `extractLowIncomeEligible(text)` | `*bool` | CARE, FERA, LIHEAP, "low-income", "income-qualified" |
| `extractCustomerType(urlAndTitle)` | `string` | "Residential" / "Commercial" / "" |
| `extractRecipient(text)` | `string` | "Homeowner" / "Renter" / "Business Owner" / … |
| `extractStartDate(text)` | `string` | "effective", "starting", "as of" + date |
| `extractEndDate(text)` | `string` | "expires", "through", "deadline" + date |
| `inferCategories(text)` | `[]string` | 50+ keyword→category rules |

---

## Per-Scraper Specifications

---

### 1. Con Edison ✅

**Source ID:** `con_edison`  
**Utility Company:** `"Con Edison"`  
**State:** `NY` | **ZIP:** `10001`  
**Sitemap:** `https://www.coned.com/sitemap.xml`

**URL Filter (`conEdisonFilterCfg`):**

*Exclusions (checked first):*
- Con Edison-specific: `/using-distributed-generation`, `/shop-for-energy-service`, `/our-energy-vision`, `/where-we-are-going`
- Account/auth: `/my-account`, `/login`, `/sign-in`
- Corporate: `/about-us`, `/careers`, `/media-center`, `/news`, `/investor`, `/safety`, `/outages`
- Infrastructure: `/grid`, `/transmission`, `/tariff`, `/fault-current`
- Support: `/contact-us`, `/terms-of-use`, `/privacy`, `/search`

*Inclusions (must match at least one):*
- `rebate`, `incentive`, `save-money`, `saving`, `credit`, `reward`, `assistance`, `payment-plans-assistance`, `heat-pump`, `electric-vehicle`, `solar`, `financing`, `smart-usage`, `demand-response`, `weatherization`, `insulation`, `efficiency`, `low-income`, `income-eligible`, `find-incentive`, `incentive-viewer`

**Fields extracted per page:**
- Program name (h1 → title), description (meta → first paragraph)
- Amount (ParseAmount), max amount, application URL (apply/enroll links)
- ContractorRequired, EnergyAuditRequired, CustomerType, StartDate, EndDate
- Phone, email, category tags

---

### 2. PNM ✅

**Source ID:** `pnm`  
**Utility Company:** `"PNM"`  
**State:** `NM` | **ZIP:** `87102`  
**Sitemap:** `https://www.pnm.com/sitemap.xml` (sitemap index — child sitemaps may return "Access Denied")

**URL Filter (`pnmFilterCfg`):**

*Exclusions (checked first):*
- Corporate: `/about-pnm`, `/corporate`, `/investor`, `/news`, `/careers`, `/jobs`
- Legal: `/regulatory`, `/filings`, `/tariffs`, `/legal`, `/terms`, `/privacy`
- Account: `/login`, `/sign-in`, `/my-account`
- Operational: `/outages`, `/safety`, `/storm`, `/emergency`, `/start-service`, `/stop-service`, `/pay-bill`
- Content: `/documents`, `/media`, `/education`, `/community`
- Infrastructure: `/infrastructure`, `/grid`, `/generation`, `/power-plants`, `/transmission`

*Inclusions (be inclusive per LLM prompt):*
- `save-money-and-energy`, `rebate`, `incentive`, `checkup`, `energy-efficiency`, `weatherization`, `appliance-recycling`, `solar`, `pnmskyblue`, `/ev`, `electric-vehicle`, `goodneighborfund`, `assistance`, `liheap`, `low-income`, `time-of-use`, `demand-response`, `quick-saver`, `heat-pump`, `thermostat`, `lighting`

**Fields extracted per page:** Same as Con Edison above.

**Note:** PNM uses `pnm.clearesult.com` for some rebate applications; Colly allows this domain.

---

### 3. Xcel Energy ✅

**Source ID:** `xcel_energy`  
**Utility Company:** `"Xcel Energy"`  
**State:** extracted from page text (CO/MN/WI/ND/SD/NM)  
**Sitemap:** `https://www.xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml`

**URL Filter (`xcelFilterCfg`):**

*Absolute corporate exclusions (checked first):*
- `/company/`, `/about_us/`, `/investor_relations/`, `/board_of_directors/`, `/leadership/`, `/media_room/`, `/news_releases/`, `/careers/`, `/corporate_governance/`, `/corporate_responsibility`

*Infrastructure exclusions:*
- `/rates_and_regulations/`, `/filings/`, `/rate_cases/`, `/outages_and_emergencies/`, `/storm_center/`, `/customer_support/`, `/contact_us`, `/billing_and_payment/`, `/power_plants/`, `/trade_partners/`, `/suppliers/`, etc.

*Pattern exclusions (non-program pages):*
- `_tool`, `_finder`, `_calculator`, `_advisor`, `/ways_to_save`, `/energy_saving_tips`, `_sign_up`, `_enrollment`, `_faq`, `_how_it_works`, `_case_study`, `/my_account`, etc.

*Hub page detection:*
- `MinPathSegments: 3` — URLs with fewer than 3 path segments are hub/category pages and excluded
- ❌ `/programs_and_rebates/equipment_rebates` (depth 2 — hub)
- ✅ `/programs_and_rebates/equipment_rebates/lighting_efficiency` (depth 3 — specific program)

*Inclusions:*
- `rebate`, `incentive`, `reward`, `savings`, `efficient`, `upgrade`, `heat_pump`, `appliance`, `thermostat`, `solar`, `electric_vehicle`, `battery_storage`, `assistance`, `low_income`, `demand_response`, `peak_reward`, `saver`, `lighting`, `insulation`, `programs_and_rebates`

**Fields extracted per page:** Same as Con Edison + state auto-detected from page text.

---

### 4. SRP ✅

**Source ID:** `srp`  
**Utility Company:** `"Salt River Project"`  
**State:** `AZ` | **ZIP:** `85001`  
**Sitemap:** `https://www.srpnet.com/sitemap.xml`  
**File:** `scrapers/srp.go`

**URL Filter (`srpFilterCfg`):**

*Exclusions (checked first):*
- Business/trade: `/doing-business/`, `/trade-ally/`, `/trade-allies/`
- Corporate: `/about/`, `/about-srp/`, `/careers/`, `/governance/`, `/leadership/`, `/news/`, `/investor/`
- Account/auth: `/account/`, `/my-account/`, `/login/`
- Contact/support: `/contact-us`, `/customer-service/`
- Infrastructure: `/grid-water-management/`, `/water-`, `/irrigation/`, `/transmission/`, `/outages/`, `/tariff/`
- Content patterns: `-workshop`, `-audit`, `-assessment`, `-faq`, `/savings-tools`, `/diy-`, `/how-to-`, `/tips/`, `/blog/`

*Inclusions (must match at least one):*
- `/rebates/`, `/rebate/`, `/incentive/`, `/energy-savings-rebates/`, `/financial-assistance`, `assistance`, `economy`, `discount`, `demand-response`, `heat-pump`, `thermostat`, `solar`, `battery`, `electric-vehicle`, `efficiency`, `upgrade`, `save`, `credit`, `saver`, `time-of-use`, `peak`

**Fields extracted per page:** Same as Con Edison above.

---

### 5. Peninsula Clean Energy ✅

**Source ID:** `peninsula_clean_energy`  
**Utility Company:** `"Peninsula Clean Energy"`  
**State:** `CA` | **ZIP:** `94025` | **Territory:** `"San Mateo County and Los Banos"`  
**Sitemaps (4):**
```
https://www.peninsulacleanenergy.com/page-sitemap.xml
https://www.peninsulacleanenergy.com/post-sitemap.xml
https://www.peninsulacleanenergy.com/news-releases-sitemap.xml
https://www.peninsulacleanenergy.com/articles-sitemap1.xml
```
**File:** `scrapers/peninsula_clean_energy.go`

**URL Filter (`pceFilterCfg`):**

*Exclusions (checked first):*
- Corporate/governance: `/about-us/`, `/careers/`, `/board-of-directors/`, `/regulatory-filings/`, `/staff/`, `/leadership/`
- Contact/generic: `/contact-us/`, `/faq/`, `/sitemap`, `/privacy`, `/terms`
- Technical docs (non-program): `/case-studies/`, `/qualifications/`, `/design-guidance-`, `/installation-guidelines/`
- Solar billing/NEM (informational only): `/solar-rates/`, `/solar-billing-plan/`, `/net-energy-metering/`
- Non-English locale variants: `/es/`, `/zh-tw/`, `/zh/`, `/fl/`, `/tl/`
- Procurement/events: `/procurement/`, `/rfp/`, `/events/`, `/press-releases/`

*Inclusions (must match at least one):*
- Primary hubs: `/rebates-offers/`, `/rebates-offers-business/`, `/home-upgrade-services/`, `/public-organization/`, `/multifamily/`, `/financing/`
- Generic rebate keywords (for blog/news URLs): `rebate`, `incentive`, `discount`, `credit`, `savings`, `assistance`, `heat-pump`, `electrification`, `solar`, `battery`, `ev`, `electric-vehicle`, `efficiency`, `upgrade`

**Note:** The `Scrape()` method iterates over all four sitemaps and aggregates URLs before filtering. Each sitemap is fetched independently; failures are logged and skipped.

---

## Field Mapping (All Utility Scrapers)

| `models.Incentive` field | Source |
|--------------------------|--------|
| `ID` | `DeterministicID(sourceID, pageURL)` |
| `ProgramName` | Page `h1` / `<title>` stripped of site name |
| `UtilityCompany` | Hardcoded per scraper |
| `State` | Hardcoded (Con Edison/PNM) or detected from text (Xcel) |
| `ZipCode` | Hardcoded representative ZIP per state |
| `ServiceTerritory` | Hardcoded per scraper |
| `IncentiveDescription` | `meta[name=description]` → first `<p>` |
| `IncentiveFormat` | `ParseAmount()` |
| `IncentiveAmount` | `ParseAmount()` |
| `MaximumAmount` | `ParseAmount()` "up to" pattern |
| `ApplicationURL` | First link with "apply"/"enroll"/"application" |
| `ProgramURL` | The page URL itself |
| `ContactPhone` | `extractPhone(pageText)` |
| `ContactEmail` | `extractEmail(pageText)` |
| `CategoryTag` | `inferCategories(url + title)` (50+ rules) |
| `ContractorRequired` | `extractContractorRequired(pageText)` |
| `EnergyAuditRequired` | `extractEnergyAuditRequired(pageText)` |
| `CustomerType` | `extractCustomerType(url + title)` |
| `StartDate` | `extractStartDate(pageText)` |
| `EndDate` | `extractEndDate(pageText)` |
| `AvailableNationwide` | `false` (utility programs are regional) |
| `ProgramHash` | `ComputeProgramHash(ProgramName, UtilityCompany)` |
| `Source` | Hardcoded source ID per scraper |
| `ScraperVersion` | From config |

---

## Other Scrapers — Field Gap Review

Reviewed all 5 SmythOS agents vs. existing Go scrapers. Gaps and fixes:

### Rewiring America (`rewiring_america.go`) — Fixed ✅
| Gap | Fix Applied |
|-----|-------------|
| `ServiceTerritory` not set | Added: "Nationwide" / "[Authority] Statewide" / "[Authority] Service Area" based on `authorityType` |
| `ProductCategory` not set | Added: `raProductCategory(items[0])` maps item keys to category tags |
| `Portfolio` not reflecting program level | Fixed: "Federal" / "State" / "Utility" / "Local" from `authorityType` |
| `Segment` was storing paymentMethods | Fixed: `Segment = ownerStatus` (homeowner, renter), paymentMethods → Portfolio fallback |

### DSIRE (`dsireusa.go`) — Fixed ✅
| Gap | Fix Applied |
|-----|-------------|
| `Published` field ignored | Added: `published == "Yes"` → `Status = "active"` |
| `ProgramHash` not set | Added: `ComputeProgramHash(ProgramName, UtilityCompany)` |

### Energy Star (`energy_star.go`) — OK ✅
| Observation | Notes |
|-------------|-------|
| `program_type` from `incentiveType` | Model has no `ProgramType` field; stored in `CategoryTag` |
| `contractor_required` hardcoded `false` | Acceptable — API doesn't provide this |
| `recipient` stored in `Administrator` | Model has no `Recipient` field |
| `building_type` stored in `Portfolio` | Model has no `BuildingType` field |
| Low/moderate/high income flags | Model has no boolean fields; encoded in `CategoryTag` |

---

## Rate Limiting

| Scraper | Inter-request delay | Notes |
|---------|-------------------|-------|
| Con Edison | 600ms | `CollyBase.Delay` |
| PNM | 600ms | `CollyBase.Delay` |
| Xcel Energy | 600ms | `CollyBase.Delay` |
| SRP | 600ms | `CollyBase.Delay` |
| Peninsula Clean Energy | 600ms | `CollyBase.Delay` |

---

## Registration in `cmd/scraper/main.go`

```go
reg.Register(&scrapers.ConEdisonScraper{...})
reg.Register(&scrapers.PNMScraper{...})
reg.Register(&scrapers.XcelEnergyScraper{...})
reg.Register(&scrapers.SRPScraper{...})
reg.Register(&scrapers.PeninsulaCleanEnergyScraper{...})
```

---

## Implementation Checklist

### Shared Infrastructure
- [x] `scrapers/sitemap.go` — `FetchSitemapURLs` (index + urlset, 3-level recursion, HTML error detection) + `FilterSitemapURLs(FilterConfig)` with exclusion-first logic + `pathDepth` hub detection
- [x] `scrapers/html_helpers.go` — all extraction helpers (phone, email, categories, contractor_required, energy_audit_required, currently_active, low_income_eligible, customer_type, recipient, start_date, end_date)

### Utility Scrapers (3 implemented)
- [x] `scrapers/con_edison.go` — proper `FilterConfig` with ConEd-specific exclusions/inclusions; all html_helpers fields populated
- [x] `scrapers/pnm.go` — proper `FilterConfig`; clearesult.com domain allowed; all html_helpers fields populated
- [x] `scrapers/xcel_energy.go` — correct sitemap URL `xcelenergy.com/staticfiles/...`; absolute corporate exclusions + pattern exclusions; `MinPathSegments=3`; state auto-detected from page text
- [x] `scrapers/srp.go` — SRP scraper; srpFilterCfg with AZ-specific exclusions; all html_helpers fields populated
- [x] `scrapers/peninsula_clean_energy.go` — PCE scraper; pceFilterCfg; iterates all 4 sitemaps; San Mateo County / Los Banos territory

### Existing Scraper Field Fixes
- [x] `scrapers/rewiring_america.go` — ServiceTerritory, ProductCategory, Portfolio level, Segment fixed
- [x] `scrapers/dsireusa.go` — Published → Status("active"), ProgramHash added

### Tooling
- [x] `cmd/scraper/main.go` — all five utility scrapers registered (con_edison, pnm, xcel_energy, srp, peninsula_clean_energy)
- [x] `Makefile` — `scrape-coned`, `scrape-pnm`, `scrape-xcel`, `scrape-srp`, `scrape-pce` targets
- [x] `package.json` — `run:con_edison`, `run:pnm`, `run:xcel_energy`, `run:srp`, `run:peninsula_clean_energy` scripts
- [x] `scripts/run.mjs` — all five utility scrapers whitelisted
- [x] `docs/scrapers.md` — field-by-field documentation for all scrapers

### Verification (pending first run)
- [ ] Con Edison rows in `rebates_staging` with `source = 'con_edison'`
- [ ] PNM rows in `rebates_staging` with `source = 'pnm'`
- [ ] Xcel Energy rows in `rebates_staging` with `source = 'xcel_energy'`

---

## Spec Alignment

**Source specs:** SmythOS agent files `rf-crawler-pnm-srp-coned-xcel-peninsul-mobyct44nw.smyth` and `rf-scraper-pnm-srp-coned-xcel-peninsul-mobyqg49ph.smyth`.

**Divergences from SmythOS spec:**
- SmythOS uses an LLM to classify sitemap URLs. Go implementation replaces this with a deterministic `FilterConfig` (exclusion-first keyword list). The decision framework is derived from the same rules the LLM prompts encode.
- SmythOS visits listing pages then follows internal links to sub-program pages ("two-phase enrichment"). Go implementation currently only visits URLs discovered directly from the sitemap. See the **Two-Phase Enrichment** section below.
- Xcel Energy: SmythOS used per-state subdomains (`co.my.xcelenergy.com`, `mn.my.xcelenergy.com`, etc.). Go implementation uses the single corporate sitemap (`xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml`) with state auto-detected from page text.

---

## Implementation Order

For each new utility scraper, follow this order:

1. Define `FilterConfig` — exclusion list, inclusion list, `MinPathSegments`
2. Implement `extractPage()` — Colly selectors + html_helper calls
3. Implement `Scrape()` — sitemap fetch → filter → colly visit → upsert
4. Add fallback seed URLs
5. Register in `cmd/scraper/main.go`
6. Add Makefile target (`scrape-<name>`)
7. Add `pnpm` script in `package.json` and whitelist in `scripts/run.mjs`
8. Verify staging rows: `SELECT count(*) FROM rebates_staging WHERE source = '<name>'`

---

## Test Plan

**Unit tests (pending):**
- `html_helpers_test.go` — table-driven tests for each extraction function:
  - `extractStartDate` / `extractEndDate` — happy path (various date formats), no match, edge cases
  - `extractContractorRequired` / `extractEnergyAuditRequired` — true/false/nil cases
  - `extractCustomerType` — residential/commercial/mixed/empty inputs
  - `inferCategories` — keyword coverage, multi-match, default fallback

**Integration tests (pending):**
- `FilterSitemapURLs` with realistic URL sets for each utility — verify exclusion-first logic and `MinPathSegments` filtering
- `FetchSitemapURLs` against a test HTTP server returning known sitemap XML — verify recursion and HTML error-page skipping

**Manual verification:**
- Run each scraper with `RUN_ONCE=true SOURCE=<name> go run ./cmd/scraper`
- Check `SELECT count(*), state FROM rebates_staging WHERE source='<name>' GROUP BY state`
- Spot-check 5–10 rows for field completeness (program_name, incentive_amount, category_tag, contractor_required, start_date)
- Confirm `program_hash` is non-null on all rows

---

## Open Questions / Risks

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| Con Edison uses anti-scraping (Cloudflare) | High | Realistic User-Agent + 600ms delay; fallback to seed URLs |
| PNM child sitemaps return "Access Denied" HTML | Known/handled | `FetchSitemapURLs` silently skips non-XML pages |
| Xcel sitemap is large (1000s of URLs) | Known/handled | Strict FilterConfig + MinPathSegments=3 reduces to specific programs only |
| Xcel state detection from page text | Medium | Pattern matching on "Colorado"/"Minnesota"/etc.; falls back to no state if undetected |
| SRP uses JavaScript-rendered content | Low | SmythOS uses `javascriptRendering: false` — static HTML should work |
| PCE blog/news URLs need content-based filtering | Medium | Post-scrape content check required; out of scope for now |

---

## Two-Phase Enrichment (Deferred)

The SmythOS scraper LLM visits `program_url` links found *within* listing pages to enrich the initial record (e.g., a hub page lists 5 programs, each linking to their own page). The Go implementation currently only visits URLs discovered from the sitemap.

**Future work:** After scraping a page, follow any internal links to sub-program pages and create separate `models.Incentive` records — matching the SmythOS "separate records for separate incentives" golden rule (tiered programs should generate one record each).
