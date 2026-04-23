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

## Running a Single Scraper

```bash
# Direct
SOURCE=dsireusa      RUN_ONCE=true go run ./cmd/scraper
SOURCE=rewiring_america RUN_ONCE=true go run ./cmd/scraper
SOURCE=energy_star   RUN_ONCE=true go run ./cmd/scraper

# npm helpers
npm run run:dsireusa
npm run run:rewiring_america
npm run run:energy_star
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
