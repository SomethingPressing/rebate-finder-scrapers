# PNM (`pnm`)

**Source:** [pnm.com](https://www.pnm.com) — Public Service Company of New Mexico

**Approach:** Two-phase sitemap crawl + Colly HTML scraping across `pnm.com` and `pnm.clearesult.com` (third-party rebate portal). PDF URLs are routed to the PDF extraction path.

---

## Approach

1. Fetches `https://www.pnm.com/sitemap.xml` — may be a sitemap index with nested sitemaps. Child sitemaps returning HTML "Access Denied" are silently skipped.
2. Applies `pnmFilterCfg` (`FilterConfig`). Falls back to hardcoded seed URLs if unavailable or no matches.
3. For each URL: PDF links → `ExtractPDFPages` + `ExtractIncentiveFromPDFText`; HTML links → Colly `extractPage()`.
4. `pnm.clearesult.com` is an allowed domain so clearesult portal links are followed.

---

## URL Filter

**Exclusions (checked first — any match rejects the URL):**
`/about-pnm`, `/about-us`, `/corporate`, `/investor`, `/news`, `/newsroom`, `/press-release`, `/careers`, `/jobs`, `/regulatory`, `/regulation`, `/filings`, `/tariffs`, `/legal`, `/terms`, `/privacy`, `/login`, `/sign-in`, `/my-account`, `/outages`, `/outage-map`, `/safety`, `/storm`, `/emergency`, `/start-service`, `/stop-service`, `/move`, `/pay-bill`, `/payment-options`, `/customer-service`, `/documents`, `/media`, `/multimedia`, `/education`, `/schools`, `/community`, `/infrastructure`, `/grid`, `/generation`, `/power-plants`, `/transmission`

**Inclusions (must match at least one):**
`save-money-and-energy`, `save-money`, `save-energy`, `/save`, `rebate`, `incentive`, `savings`, `discount`, `energy-efficiency`, `checkup`, `home-energy-checkup`, `weatherization`, `energy-audit`, `quick-saver`, `appliance-recycling`, `refrigerator-recycling`, `smart-thermostat`, `heat-pump`, `water-heater`, `evaporative-cooler`, `swamp-cooler`, `pool-pump`, `lighting`, `solar`, `pnmskyblue`, `sky-blue`, `renewable-energy`, `green-energy`, `net-metering`, `/ev`, `electric-vehicle`, `ev-tax-credit`, `charging`, `ev-rates`, `goodneighborfund`, `good-neighbor-fund`, `assistance`, `liheap`, `low-income`, `help-paying-bill`, `energy-assistance`, `payment-plan`, `payment-arrangement`, `budget-billing`, `time-of-use`, `/tou`, `demand-response`, `peak-`, `off-peak`, `rate-options`

---

## Seed URLs (Fallback)

```
https://www.pnm.com/save-money-and-energy
https://www.pnm.com/residential-rebates
https://www.pnm.com/checkup
https://www.pnm.com/goodneighborfund
https://www.pnm.com/pnmskyblue
https://www.pnm.com/residential-energy-efficiency
https://www.pnm.com/electric-vehicles
https://www.pnm.com/appliance-recycling
https://pnm.clearesult.com/
```

---

## ID Generation

`DeterministicID("pnm", pageURL)` — stable per page URL.

---

## Fields Mapped

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
| `ApplicationURL` | First `<a href>` with "apply", "application", "submit", or "enroll"; clearesult.com links preferred |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official PNM program website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL + title — see [shared.md](shared.md#category-inference) |
| `ContractorRequired` | `true` if licensed/approved contractor language found |
| `EnergyAuditRequired` | `true` if energy audit language found |
| `CustomerType` | `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` |
| `StartDate` | Date after "effective", "starting", "as of" keywords |
| `EndDate` | Date after "expires", "through", "deadline" keywords |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "PNM")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `maximum_amount`, `image_url`, `rate_tiers`

---

## Rate Limiting

600 ms delay between requests, parallelism = 2.

---

## Configuration

No required env vars (uses hardcoded base URLs).
