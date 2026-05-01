# SRP — Salt River Project (`srp`)

**Source:** [srpnet.com](https://www.srpnet.com) — Salt River Project (Arizona)

**Approach:** Sitemap crawl + Colly HTML scraping. PDF URLs are routed to the PDF extraction path.

---

## Approach

1. Fetches `https://www.srpnet.com/sitemap.xml` and applies `srpFilterCfg` — exclusions checked first, then inclusion keywords.
2. Falls back to hardcoded seed URLs if the sitemap is unavailable or returns no matches.
3. For each URL: PDF links → `ExtractPDFPages` + `ExtractIncentiveFromPDFText`; HTML links → Colly `extractPage()`.

---

## URL Filter

**Exclusions (checked first — any match rejects the URL):**
`/doing-business/`, `/trade-ally/`, `/about/`, `/careers/`, `/water-`, `/irrigation/`, `-workshop`, `-audit`, `-faq`, `/savings-tools`, `/diy-`, `/how-to-`

**Inclusions (must match at least one):**
`/rebates/`, `/rebate/`, `/incentive/`, `/energy-savings-rebates/`, `assistance`, `economy`, `discount`, `demand-response`, `heat-pump`, `solar`

---

## Seed URLs (Fallback)

```
https://www.srpnet.com/energy-savings-rebates/home
https://www.srpnet.com/energy-savings-rebates/business
https://www.srpnet.com/price-plans/home/time-of-use
https://www.srpnet.com/assistance
```

---

## ID Generation

`DeterministicID("srp", pageURL)` — stable per page URL.

---

## Fields Mapped

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("srp", pageURL)` |
| `Source` | `"srp"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of `" \| SRP"` suffix |
| `UtilityCompany` | `"Salt River Project"` (hardcoded) |
| `State` | `"AZ"` (hardcoded) |
| `ZipCode` | `"85001"` (Phoenix — representative AZ ZIP) |
| `ServiceTerritory` | `"SRP Service Area"` (hardcoded) |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text, `<p>`, `<li>`, `<td>`, `<strong>` |
| `ApplicationURL` | First `<a href>` with "apply", "application", "enroll", or "sign up" |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official Salt River Project website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL + title — see [shared.md](shared.md#category-inference) |
| `ContractorRequired` | `true` if licensed/approved contractor language found |
| `EnergyAuditRequired` | `true` if energy audit language found |
| `CustomerType` | `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` |
| `StartDate` | Date after "effective", "starting", "as of" keywords |
| `EndDate` | Date after "expires", "through", "deadline" keywords |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Salt River Project")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `maximum_amount`, `image_url`, `rate_tiers`

---

## Rate Limiting

600 ms delay between requests, parallelism = 2.

---

## Configuration

No required env vars (uses hardcoded base URLs).
