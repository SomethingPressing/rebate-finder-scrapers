# Rewiring America (`rewiring_america`)

**Source:** [rewiringamerica.org](https://www.rewiringamerica.org) — IRA incentive calculator

**API:** Requires a free API key from [rewiringamerica.org/api](https://www.rewiringamerica.org/api).

---

## Approach

1. Queries the IRA calculator endpoint with representative ZIPs for 51 major US cities.
2. Sweeps each ZIP across 3 household profiles (homeowner/low-income, homeowner/mid-income, renter/mid-income) to maximize program coverage.
3. Worker pool processes ZIP × profile pairs concurrently.
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

## Rate Limiting

200 ms delay between ZIP requests.

---

## Configuration

```env
REWIRING_AMERICA_API_KEY=your_key_here                      # required
REWIRING_AMERICA_BASE_URL=https://api.rewiringamerica.org   # override for testing
```
