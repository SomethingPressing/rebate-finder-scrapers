# Xcel Energy (`xcel_energy`)

**Source:** [xcelenergy.com](https://www.xcelenergy.com) — Xcel Energy (multi-state utility)

**Approach:** Single corporate sitemap crawl + Colly HTML scraping. PDF URLs are routed to the PDF extraction path.

**States covered:** CO, MN, WI, ND, SD, NM — state is auto-detected from page text, not from URL subdomains.

---

## Approach

1. Fetches `https://www.xcelenergy.com/staticfiles/xe-responsive/assets/sitemap.xml` and applies `xcelFilterCfg` with `MinPathSegments: 3` hub-page detection.
2. Falls back to hardcoded seed URLs under `https://www.xcelenergy.com/programs_and_rebates/` if sitemap fails.
3. For each URL: PDF links → `ExtractPDFPages` + `ExtractIncentiveFromPDFText` (state/ZIP left blank — multi-state); HTML links → Colly `extractPage()`.
4. State is extracted from page text via `xcelStateFromText()`; ZIP and service territory are derived from detected state.

---

## Hub Page Detection

URLs with fewer than 3 path segments are rejected as category/hub pages:
- `/programs_and_rebates/equipment_rebates` — depth 2 → **excluded**
- `/programs_and_rebates/equipment_rebates/lighting_efficiency` — depth 3 → **included**

---

## URL Filter

**Exclusions (checked first — any match rejects the URL):**
Corporate: `/company/`, `/about_us/`, `/investor_relations/`, `/careers/`, `/media_room/`, `/news_releases/`, `/press_releases/`
Infrastructure: `/rates_and_regulations/`, `/filings/`, `/outages_and_emergencies/`, `/billing_and_payment/`, `/power_plants/`
Patterns: `_tool`, `_finder`, `_calculator`, `_advisor`, `/ways_to_save`, `_sign_up`, `_faq`, `_how_it_works`, `_case_study`, `/my_account`

**Inclusions (must match at least one):**
`rebate`, `rebates`, `incentive`, `reward`, `savings`, `efficient`, `upgrade`, `heat_pump`, `heat-pump`, `hvac`, `appliance`, `thermostat`, `solar`, `electric_vehicle`, `battery_storage`, `assistance`, `low_income`, `demand_response`, `peak_reward`, `saver`, `lighting`, `insulation`, `programs_and_rebates`, `program`

---

## Seed URLs (Fallback)

```
https://www.xcelenergy.com/programs_and_rebates/residential_programs_and_rebates
https://www.xcelenergy.com/programs_and_rebates/business_programs_and_rebates
```

---

## ID Generation

`DeterministicID("xcel_energy", pageURL)` — stable per page URL.

---

## State Detection (`xcelStateFromText`)

| Text found on page | `State` | `ZipCode` | `ServiceTerritory` |
|-------------------|---------|-----------|-------------------|
| "Colorado" | `"CO"` | `"80202"` | `"Xcel Energy Colorado Service Area"` |
| "Minnesota" | `"MN"` | `"55401"` | `"Xcel Energy Minnesota Service Area"` |
| "Wisconsin" | `"WI"` | `"53202"` | `"Xcel Energy Wisconsin Service Area"` |
| "New Mexico" | `"NM"` | `"87102"` | `"Xcel Energy New Mexico Service Area"` |
| "North Dakota" | `"ND"` | `"58501"` | `"Xcel Energy North Dakota Service Area"` |
| "South Dakota" | `"SD"` | `"57501"` | `"Xcel Energy South Dakota Service Area"` |
| *(no match)* | `""` | `""` | `"Xcel Energy Service Area"` |

---

## Fields Mapped

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("xcel_energy", pageURL)` |
| `Source` | `"xcel_energy"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of suffix |
| `UtilityCompany` | `"Xcel Energy"` (hardcoded) |
| `State` | Auto-detected via `xcelStateFromText()` |
| `ZipCode` | Derived from detected state via `xcelZIPFromState()` |
| `ServiceTerritory` | Derived from detected state via `xcelTerritoryFromState()` |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text, `<p>`, `<li>`, `<td>`, `<strong>` |
| `ApplicationURL` | First `<a href>` with "apply", "application", "submit", or "enroll" |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official Xcel Energy program website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL + title — see [shared.md](shared.md#category-inference) |
| `ContractorRequired` | `true` if licensed/approved contractor language found |
| `EnergyAuditRequired` | `true` if energy audit language found |
| `CustomerType` | `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` |
| `StartDate` | Date after "effective", "starting", "as of" keywords |
| `EndDate` | Date after "expires", "through", "deadline" keywords |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Xcel Energy")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `maximum_amount`, `image_url`, `rate_tiers`

---

## Rate Limiting

600 ms delay between requests, parallelism = 2.

---

## Configuration

No required env vars (uses hardcoded corporate sitemap URL).
