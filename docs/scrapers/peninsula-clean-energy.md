# Peninsula Clean Energy (`peninsula_clean_energy`)

**Source:** [peninsulacleanenergy.com](https://www.peninsulacleanenergy.com) — Peninsula Clean Energy Authority (San Mateo County, CA)

**Approach:** Multi-sitemap crawl + Colly HTML scraping. PDF URLs are routed to the PDF extraction path.

---

## Approach

1. Fetches all four PCE sitemaps and aggregates their URLs before filtering:
   - `https://www.peninsulacleanenergy.com/page-sitemap.xml`
   - `https://www.peninsulacleanenergy.com/post-sitemap.xml`
   - `https://www.peninsulacleanenergy.com/news-releases-sitemap.xml`
   - `https://www.peninsulacleanenergy.com/articles-sitemap1.xml`
2. Applies `pceFilterCfg` — exclusions checked first, then inclusion keywords. Deduplicates URLs across sitemaps before visiting.
3. Falls back to hardcoded seed URLs if all sitemaps fail.
4. For each URL: PDF links → `ExtractPDFPages` + `ExtractIncentiveFromPDFText`; HTML links → Colly `extractPage()`.

---

## URL Filter

**Exclusions (checked first — any match rejects the URL):**
`/about-us/`, `/careers/`, `/board-of-directors/`, `/regulatory-filings/`, `/case-studies/`, `/design-guidance-`, `/solar-rates/`, `/solar-billing-plan/`, `/net-energy-metering/`, `/es/` (Spanish), `/zh-tw/` (Traditional Chinese), `/fl/` (Filpino)

**Inclusions (must match at least one):**
`/rebates-offers/`, `/rebates-offers-business/`, `/home-upgrade-services/`, `/public-organization/`, `/multifamily/`, `/financing/`, `rebate`, `incentive`, `heat-pump`, `electrification`, `ev`

---

## Seed URLs (Fallback)

```
https://www.peninsulacleanenergy.com/rebates-offers/
https://www.peninsulacleanenergy.com/rebates-offers-business/
https://www.peninsulacleanenergy.com/home-upgrade-services/
https://www.peninsulacleanenergy.com/multifamily/
https://www.peninsulacleanenergy.com/financing/
```

---

## ID Generation

`DeterministicID("peninsula_clean_energy", pageURL)` — stable per page URL.

---

## Fields Mapped

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("peninsula_clean_energy", pageURL)` |
| `Source` | `"peninsula_clean_energy"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of `" – Peninsula Clean Energy"` suffix |
| `UtilityCompany` | `"Peninsula Clean Energy"` (hardcoded) |
| `State` | `"CA"` (hardcoded) |
| `ZipCode` | `"94025"` (Menlo Park — representative San Mateo County ZIP) |
| `ServiceTerritory` | `"San Mateo County and Los Banos"` (hardcoded) |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text, `<p>`, `<li>`, `<td>`, `<strong>` |
| `ApplicationURL` | First `<a href>` with "apply", "application", "enroll", or "sign up" |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official Peninsula Clean Energy website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL + title — see [shared.md](shared.md#category-inference) |
| `ContractorRequired` | `true` if licensed/approved contractor language found |
| `EnergyAuditRequired` | `true` if energy audit language found |
| `CustomerType` | `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` |
| `StartDate` | Date after "effective", "starting", "as of" keywords |
| `EndDate` | Date after "expires", "through", "deadline" keywords |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Peninsula Clean Energy")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `maximum_amount`, `image_url`, `rate_tiers`

---

## Rate Limiting

600 ms delay between requests, parallelism = 2.

---

## Configuration

No required env vars (uses hardcoded base URLs).
