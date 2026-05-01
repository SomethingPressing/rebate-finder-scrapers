# Energy Star (`energy_star`)

**Source:** [energystar.gov/about/federal_tax_credits](https://www.energystar.gov/about/federal_tax_credits)

**Approach:** HTML scraping via [Colly](https://go-colly.org/). Targets card containers on the federal tax credits page.

---

## Geographic Coverage — How States & ZIPs Are Determined

Energy Star federal tax credit programs are **available nationwide** — there is no ZIP or state filtering during the scrape itself. The scraper visits a single URL and extracts all cards from the page.

**State on each record:** The API response for some programs includes a `service_territory.state_code` field. When present, `State` is set from that value. Federal-only programs leave `State` empty.

**ZIP population for storage (`ZipCodes` field):** When `StateZIPs` is loaded from `data/uszips.csv`, every ZIP for the program's state is stored in the `ZipCodes` array (same mechanism as DSIRE). For truly nationwide programs with no state, `ZipCodes` is left empty and `AvailableNationwide = true` indicates universal coverage.

---

## Selectors

```
.views-row                          → card container
.views-field-title                  → program name
.field-name-field-description       → description
.field-name-field-incentive         → amount (parsed for $, %)
.field-name-field-credit-type       → credit type / category tag
a[href]                             → program URL
```

Fallback: if `.views-row` yields nothing, tries `article` tags.

---

## ID Generation

`DeterministicID("energy_star", strings.ToLower(programName))` — keyed on normalized title.

---

## Fields Mapped

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("energy_star", normalized title)` |
| `Source` | `"energy_star"` (hardcoded) |
| `ProgramName` | `.views-field-title` text |
| `UtilityCompany` | `"U.S. Department of Energy"` (hardcoded) |
| `Administrator` | `"IRS / DOE"` (hardcoded) |
| `IncentiveDescription` | `.field-name-field-description` text |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | Amount extracted from `.field-name-field-incentive` |
| `CategoryTag` | `.field-name-field-credit-type` text |
| `ProgramURL` | `a[href]` on the card |
| `AvailableNationwide` | `true` (hardcoded) |
| `Segment` | `["Residential"]` (hardcoded) |
| `Portfolio` | `["Federal"]` (hardcoded) |
| `ScraperVersion` | From config |

**Fields NOT populated:** `state`, `zip_code`, `service_territory`, `contractor_required`, `energy_audit_required`, `start_date`, `end_date`

---

## Running

```bash
pnpm run:energy_star
```

Or directly via Go:

```bash
SOURCE=energy_star RUN_ONCE=true go run ./cmd/scraper
```

---

## Rate Limiting

500 ms delay between requests (Colly default).

---

## Configuration

```env
ENERGY_STAR_BASE_URL=https://www.energystar.gov   # override for testing
```
