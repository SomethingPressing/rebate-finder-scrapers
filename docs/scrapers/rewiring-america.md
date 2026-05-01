# Rewiring America (`rewiring_america`)

**Source:** [rewiringamerica.org](https://www.rewiringamerica.org) — IRA incentive calculator

**API:** Requires a free API key from [rewiringamerica.org/api](https://www.rewiringamerica.org/api).

---

## Geographic Coverage — How ZIPs Are Determined

The Rewiring America API is ZIP-based — each request must include a ZIP code, and the API returns only incentives available for that ZIP's utility territory. The scraper uses a **three-tier ZIP selection strategy**, evaluated in priority order:

| Priority | Source | When used |
|----------|--------|-----------|
| 1 | `s.ZIPs` (explicit override) | Tests or CLI `--zips` flag |
| 2 | `data/uszips.csv` via `zipdata.Sample(stateZIPs, 0)` | Normal production run — all ZIPs for all 50 states + DC |
| 3 | Built-in `representativeZIPs` (52 cities) | Fallback when CSV is missing |

**Production run (priority 2):** `data/uszips.csv` contains every deliverable US ZIP code. `zipdata.Sample(stateZIPs, 0)` (n=0 = no limit) returns all ZIPs across all 51 state/DC buckets, sorted by descending population within each state. This gives complete national coverage.

**Built-in fallback list (priority 3):** 52 ZIPs covering one major city per state/DC, used when the CSV is not present:

```
10001 NY  90012 CA  60601 IL  77001 TX  85001 AZ  19103 PA  78201 TX
92101 CA  75201 TX  95101 CA  78701 TX  79901 TX  32202 FL  76102 TX
43215 OH  28202 NC  94102 CA  46204 IN  98101 WA  80202 CO  37201 TN
73102 OK  21202 MD  27601 NC  40202 KY  53202 WI  87102 NM  85701 AZ
93721 CA  95814 CA  20001 DC  02108 MA  45202 OH  39501 MS  72201 AR
70112 LA  55401 MN  67202 KS  66102 KS  68508 NE  57501 SD  58501 ND
59601 MT  83702 ID  84101 UT  82001 WY  80903 CO  89501 NV  96813 HI
99501 AK  33101 FL  30301 GA
```

**Why multiple ZIPs matter:** Utility-level incentives (e.g. Xcel Energy heat pump rebates) only appear in the API response for ZIPs within that utility's territory. A single city ZIP would miss rural utilities and cooperative territories in the same state.

**Sweep profiles:** Each ZIP is queried 3× with different household profiles to surface programs that are only available to low-income households or renters:

| Profile | `owner_status` | `household_income` |
|---------|----------------|-------------------|
| Low-income homeowner | `homeowner` | `30000` |
| Mid-income homeowner | `homeowner` | `80000` |
| Mid-income renter | `renter` | `80000` |

---

## Approach

1. Loads ZIP list from `data/uszips.csv` (all US ZIPs, sorted by population per state). Falls back to 52 built-in representative ZIPs if CSV missing.
2. Builds a task list of ZIP × profile pairs (up to `total_zips × 3` requests).
3. Worker pool (default concurrency = 3) processes all tasks in parallel.
4. Deduplicates by program + technology combination via `DeterministicID`.

---

## Endpoint

```
GET https://api.rewiringamerica.org/api/v1/calculator
    ?zip={zip}
    &owner_status={owner_status}     // homeowner | renter
    &tax_filing=joint
    &household_income={income}       // e.g. 30000, 80000
    &household_size=4
    &utility=
```

### Sweep Profiles

| Profile | `owner_status` | `household_income` |
|---------|----------------|-------------------|
| Low-income homeowner | `homeowner` | `30000` |
| Mid-income homeowner | `homeowner` | `80000` |
| Mid-income renter | `renter` | `80000` |

---

## ID Generation

`DeterministicID("rewiring_america", programName+"|"+technology)` — stable across re-scrapes for the same program/technology pair.

---

## Fields Mapped

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ProgramName` | `"[Authority Name] — [program]"` |
| `UtilityCompany` | `authorityName` resolved from top-level `authorities` map |
| `IncentiveAmount` | `item.amount.representative` (normalized) |
| `MaximumAmount` | `item.amount.maximum` (when > 0) |
| `PercentValue` | `item.amount.number * 100` when `amount.type == "percent"` |
| `IncentiveFormat` | Derived from `amount.type` — `dollar_amount`, `percent`, `dollars_per_unit` |
| `StartDate` | `item.start_date` (normalized via `normalizeRADate`) |
| `EndDate` | `item.end_date` (normalized via `normalizeRADate`) |
| `ServiceTerritory` | `"Nationwide"` (federal) / `"[Authority] Statewide"` (state) / `"[Authority] Service Area"` (utility/city/county) |
| `Portfolio` | `["Federal"]` / `["State"]` / `["Utility"]` / `["Local"]` from `authority_type` |
| `Segment` | `item.owner_status` (e.g. `["homeowner", "renter"]`) |
| `CategoryTag` | Human-readable labels from `item.items` via `raItemHuman` |
| `ProductCategory` | `raProductCategory(items[0])` — maps first item key to category tag |
| `CustomerType` | `raMapPaymentMethods(item.payment_methods)` — e.g. `"Tax Credit"`, `"Point of Sale Rebate"` |
| `ApplicationProcess` | `raGenerateApplicationProcess(item.payment_methods)` — priority-ordered instructions |
| `Status` | `"active"` when program is currently live, based on start/end dates |
| `AvailableNationwide` | `true` when `authority_type == "federal"`, else `false` |
| `Source` | `"rewiring_america"` (hardcoded) |

---

## Date Normalization (`normalizeRADate`)

| API value | Stored as |
|-----------|-----------|
| `"2023"` | `"2023-01-01"` |
| `"2024-12"` | `"2024-12-01"` |
| `"2024-03-15"` | `"2024-03-15"` (unchanged) |

---

## Running

```bash
pnpm run:rewiring_america
```

Or directly via Go:

```bash
SOURCE=rewiring_america RUN_ONCE=true go run ./cmd/scraper
```

---

## Rate Limiting

200 ms delay between ZIP requests.

---

## Configuration

```env
REWIRING_AMERICA_API_KEY=your_key_here                      # required
REWIRING_AMERICA_BASE_URL=https://api.rewiringamerica.org   # override for testing
```
