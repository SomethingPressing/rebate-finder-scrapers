# Con Edison (`con_edison`)

**Source:** [coned.com](https://www.coned.com) — Consolidated Edison Company of New York

**Approach:** Two-phase sitemap crawl + Colly HTML scraping. PDF URLs in the sitemap are routed to the PDF extraction path instead of Colly.

---

## Approach

1. Fetches `https://www.coned.com/sitemap_coned_en.xml` and applies `conEdisonFilterCfg` — exclusions checked first, then inclusion keywords.
2. Falls back to hardcoded seed URLs if the sitemap is unavailable or returns no matches.
3. For each URL: PDF links → `ExtractPDFPages` + `ExtractIncentiveFromPDFText`; HTML links → Colly `extractPage()`.

---

## URL Filter

**Exclusions (checked first — any match rejects the URL):**
`/using-distributed-generation`, `/shop-for-energy-service`, `/our-energy-vision`, `/where-we-are-going`, `/my-account`, `/login`, `/sign-in`, `/about-us`, `/about-con-edison`, `/careers`, `/media-center`, `/news`, `/press`, `/investor`, `/board`, `/governance`, `/leadership`, `/safety`, `/outages`, `/emergency`, `/grid`, `/transmission`, `/substation`, `/distribution`, `/tariff`, `/rate-schedule`, `/fault-current`, `/interconnection`, `/contact-us`, `/terms-of-use`, `/privacy`, `/search`, `/sitemap`, `/contractor-portal`, `/supplier`, `/vendor`

**Inclusions (must match at least one):**
`rebate`, `incentive`, `save-money`, `saving`, `savings`, `credit`, `refund`, `cashback`, `reward`, `discount`, `free-product`, `no-cost`, `zero-percent`, `payment-plans-assistance`, `help-paying`, `financial-assist`, `assistance`, `affordable`, `low-income`, `income-eligible`, `budget-billing`, `weatherization`, `insulation`, `heat-pump`, `geothermal`, `thermostat`, `appliance`, `water-heater`, `lighting`, `efficiency`, `upgrade`, `improvement`, `solar`, `renewable`, `electric-vehicle`, `ev-charging`, `battery`, `storage`, `smart-usage`, `demand-response`, `smart-energy-plan`, `time-of-use`, `peak-shaving`, `financing`, `find-incentive`, `incentive-viewer`, `program-finder`, `explore-clean-energy`, `financial-assistance-advisor`

---

## Seed URLs (Fallback)

```
https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-tax-credits-for-residential-customers
https://www.coned.com/en/save-money/rebates-incentives-tax-credits/rebates-incentives-for-businesses
https://www.coned.com/en/save-money/weatherization
https://www.coned.com/en/save-money/heat-pumps
https://www.coned.com/en/our-energy-future/electric-vehicles/power-ready-program
https://www.coned.com/en/save-money/smart-usage-rewards
https://www.coned.com/en/accounts-billing/payment-plans-assistance/help-paying-your-bill
```

---

## ID Generation

`DeterministicID("con_edison", pageURL)` — stable per page URL.

---

## Fields Mapped

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("con_edison", pageURL)` |
| `Source` | `"con_edison"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of `" \| Con Edison"` suffix |
| `UtilityCompany` | `"Con Edison"` (hardcoded) |
| `State` | `"NY"` (hardcoded) |
| `ZipCode` | `"10001"` (Manhattan — representative NY ZIP) |
| `ServiceTerritory` | `"Con Edison Service Territory"` (hardcoded) |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text |
| `MaximumAmount` | Amount from "up to $X" patterns if larger than `IncentiveAmount` |
| `ApplicationURL` | First `<a href>` containing "apply", "application", "enroll", or "sign up" |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official Con Edison program website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL + title — see [shared.md](shared.md#category-inference) |
| `ContractorRequired` | `true` if licensed/approved contractor language found |
| `EnergyAuditRequired` | `true` if energy audit language found |
| `CustomerType` | `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` |
| `StartDate` | Date after "effective", "starting", "as of" keywords |
| `EndDate` | Date after "expires", "through", "deadline" keywords |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Con Edison")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `image_url`, `rate_tiers`

---

## Rate Limiting

600 ms delay between requests, parallelism = 2.

---

## Configuration

No required env vars (uses hardcoded base URLs).
