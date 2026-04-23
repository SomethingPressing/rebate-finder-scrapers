# Implementation Plan: Rewiring America API Scraper

**Source file:** `rf-rewiringamerica-mobyqauw8nc.smyth`
**Status:** Planning

---

## Overview

Implement an enhanced Go scraper for the [Rewiring America Calculator API](https://api.rewiringamerica.org/api/v1/calculator). A `rewiring_america` scraper may already exist in this codebase — this plan documents the **full field mapping** derived from the SmythOS agent, which is significantly more detailed than a basic implementation.

The SmythOS agent uses a two-phase approach:
1. **API fetch**: Call the calculator endpoint with ZIP code + household parameters to get a list of available incentives
2. **LLM transformation**: Convert each raw API incentive into the standardized 55-field schema

The Go implementation replaces the LLM transformation with deterministic field mapping derived from the LLM prompt's mapping tables, authority-type rules, payment-method mapping, items mapping, and amount parsing logic.

---

## API Reference

### Endpoint

```
GET https://api.rewiringamerica.org/api/v1/calculator
```

### Query Parameters

| Parameter | Type | Default | Notes |
|-----------|------|---------|-------|
| `zip` | string | `"90001"` | ZIP code to query |
| `owner_status` | string | `"homeowner"` | `"homeowner"` or `"renter"` |
| `household_income` | int | `80000` | Annual household income in USD |
| `household_size` | int | `2` | Number of people in household |

### Authentication

```
Authorization: Bearer {REWIRING_AMERICA_API_KEY}
```

### Example Request

```
GET https://api.rewiringamerica.org/api/v1/calculator?zip=90001&owner_status=homeowner&household_income=80000&household_size=2
Authorization: Bearer <key>
```

### Response Shape

```json
{
  "incentives": [
    {
      "authority_type": "federal",
      "program": "Federal Alternative Fuel Vehicle Refueling Property Credit (30C)",
      "payment_methods": ["tax_credit"],
      "items": ["electric_vehicle_charger"],
      "amount": {
        "type": "dollar_amount",
        "number": 1000,
        "representative": null
      },
      "owner_status": ["homeowner", "renter"],
      "start_date": "2023",
      "end_date": "2026-06-30",
      "short_description": "Tax credit (up to $1,000) for EV chargers...",
      "program_url": "https://www.irs.gov/...",
      "more_info_url": "https://homes.rewiringamerica.org/..."
    }
  ],
  "coverage": {
    "state": "CA",
    "utility": "Southern California Edison"
  },
  "location": {
    "state": "CA"
  },
  "is_under_80_ami": true,
  "is_under_150_ami": true,
  "is_over_150_ami": false
}
```

---

## Architecture

```
Input: zip_code, household_size, household_income, owner_status

Phase 1 — API call
  GET /api/v1/calculator?zip={zip}&owner_status={owner_status}&...
  Authorization: Bearer {API_KEY}
  → response: incentives[] + coverage + location + AMI flags

Phase 2 — Extract shared context
  sharedCtx = {coverageState, coverageUtility, locationState, amiUnder80, amiUnder150, amiOver150}

Phase 3 — Map each incentive
  for each incentive:
    MapToIncentive(incentive, sharedCtx, zipCode) → models.Incentive

Phase 4 — Stage
  db.UpsertToStaging(incentives)
```

---

## New Files

```
scrapers/
└── rewiring_america.go          ← Rewiring America API scraper (new or enhanced)

models/
└── rewiring_america_types.go    ← Raw API response structs (new or update existing)
```

> **If `rewiring_america.go` already exists:** Replace the LLM-based transformation with the deterministic field mapping in this plan. Keep the HTTP fetch logic unchanged.

---

## Data Structures

### `models/rewiring_america_types.go`

```go
// RewiringAmericaResponse is the top-level API response.
type RewiringAmericaResponse struct {
    Incentives    []RAIncentive    `json:"incentives"`
    Coverage      RACoverage       `json:"coverage"`
    Location      RALocation       `json:"location"`
    IsUnder80AMI  bool             `json:"is_under_80_ami"`
    IsUnder150AMI bool             `json:"is_under_150_ami"`
    IsOver150AMI  bool             `json:"is_over_150_ami"`
}

type RAIncentive struct {
    AuthorityType  string        `json:"authority_type"`  // "federal", "state", "utility", "county", "city", "other"
    Program        string        `json:"program"`
    PaymentMethods []string      `json:"payment_methods"` // "tax_credit", "rebate", "pos_rebate", etc.
    Items          []string      `json:"items"`           // "heat_pump", "electric_vehicle", etc.
    Amount         RAAmount      `json:"amount"`
    OwnerStatus    []string      `json:"owner_status"`    // "homeowner", "renter"
    StartDate      string        `json:"start_date"`      // "2023", "2024-01-01", etc.
    EndDate        string        `json:"end_date"`
    ShortDescription string      `json:"short_description"`
    ProgramURL     string        `json:"program_url"`
    MoreInfoURL    string        `json:"more_info_url"`
}

type RAAmount struct {
    Type           string  `json:"type"`           // "dollar_amount", "percent", "dollars_per_unit"
    Number         float64 `json:"number"`
    Representative *float64 `json:"representative"` // min/representative value if different from max
}

type RACoverage struct {
    State   string `json:"state"`
    Utility string `json:"utility"`
}

type RALocation struct {
    State string `json:"state"`
}

// RASharedContext is extracted from the response-level fields and attached to each incentive.
type RASharedContext struct {
    CoverageState   string
    CoverageUtility string
    LocationState   string
    AMIUnder80      bool
    AMIUnder150     bool
    AMIOver150      bool
}
```

---

## Field Mapping

| Rewiring America field | `models.Incentive` field | Transformation |
|------------------------|--------------------------|----------------|
| `"Rewiring America"` (fixed) | `Source` | Constant |
| `zip_code` (input param) | `ZipCode` | Direct copy |
| `locationState` or `coverageState` | `State` | `locationState` first; omit for `"federal"` |
| `authorityType` | `AvailableNationwide` | `"federal"` → `true`, else `false` |
| `coverageUtility` / `authorityType` | `UtilityCompany` | See Authority Type Rules |
| `authorityType` | `ServiceTerritory` | See Authority Type Rules |
| `program` | `ProgramName` | Direct copy |
| `paymentMethods[]` | `ProgramType` | Mapped + joined (see Payment Methods) |
| `authorityType` | `ProgramLevel` | `"federal"` → `"Federal"`, etc. |
| `items[]` | `Items` | Mapped to readable names + joined (see Items Mapping) |
| `items[]` | `ProductCategory` | Derive dominant category from items (see Items Mapping) |
| `amount.type` + `amount.number` | `IncentiveFormat`, amount fields | See Amount Parsing |
| `amount.representative` | `IncentiveAmount` (min/rep value) | Used when `representative != nil` |
| `shortDescription` | `IncentiveDescription` | Direct copy |
| `ownerStatus[]` | `Recipient` | Join: `"Homeowner, Renter"` |
| `amiUnder80` | `LowIncomeEligible` | Direct copy (bool) |
| `amiUnder150` | `ModerateIncomeEligible` | Direct copy (bool) |
| `amiOver150` | `HighIncomeEligible` | Direct copy (bool) |
| `paymentMethods[]` | `ApplicationProcess` | Generated (see Application Process) |
| `startDate` | `StartDate` | Normalized to YYYY-MM-DD (see Date Parsing) |
| `endDate` | `EndDate` | Normalized to YYYY-MM-DD |
| (computed) | `CurrentlyActive` | `now >= startDate && (endDate == "" || now <= endDate)` |
| `programUrl` | `ProgramURL` | Direct copy |
| `moreInfoUrl` | `SourcePage` | Direct copy |
| `moreInfoUrl` | `AdditionalInformation` | Direct copy |
| `DeterministicID("rewiring_america", program+authorityType)` | `ID` | UUID v5 |
| `ComputeProgramHash(ProgramName, UtilityCompany)` | `ProgramHash` | SHA-256 |

---

## Authority Type Rules

The `authority_type` field controls several derived fields:

| `authority_type` | `State` | `UtilityCompany` | `ServiceTerritory` | `AvailableNationwide` | `ProgramLevel` |
|------------------|---------|-----------------|--------------------|-----------------------|----------------|
| `"federal"` | *(omit)* | `"Federal Government"` | `"Nationwide"` | `true` | `"Federal"` |
| `"state"` | 2-letter from `locationState`/`coverageState` | Full state name (see State Names) | `"[State] Statewide"` | `false` | `"State"` |
| `"utility"` | 2-letter from `locationState` | `coverageUtility` | `"[coverageUtility] Service Area"` | `false` | `"Utility"` |
| `"county"` | 2-letter from `locationState` | `coverageUtility` or county name | County service area | `false` | `"Local"` |
| `"city"` | 2-letter from `locationState` | `coverageUtility` or city name | City service area | `false` | `"Local"` |
| `"other"` | 2-letter from `locationState` | `coverageUtility` or `"Other"` | Service area | `false` | `"Other"` |

### State Code → Full State Name (for `utility_company` when `authority_type == "state"`)

```go
var stateNames = map[string]string{
    "AL": "Alabama", "AK": "Alaska", "AZ": "Arizona", "AR": "Arkansas",
    "CA": "California", "CO": "Colorado", "CT": "Connecticut", "DE": "Delaware",
    "DC": "District of Columbia", "FL": "Florida", "GA": "Georgia", "HI": "Hawaii",
    "ID": "Idaho", "IL": "Illinois", "IN": "Indiana", "IA": "Iowa",
    "KS": "Kansas", "KY": "Kentucky", "LA": "Louisiana", "ME": "Maine",
    "MD": "Maryland", "MA": "Massachusetts", "MI": "Michigan", "MN": "Minnesota",
    "MS": "Mississippi", "MO": "Missouri", "MT": "Montana", "NE": "Nebraska",
    "NV": "Nevada", "NH": "New Hampshire", "NJ": "New Jersey", "NM": "New Mexico",
    "NY": "New York", "NC": "North Carolina", "ND": "North Dakota", "OH": "Ohio",
    "OK": "Oklahoma", "OR": "Oregon", "PA": "Pennsylvania", "RI": "Rhode Island",
    "SC": "South Carolina", "SD": "South Dakota", "TN": "Tennessee", "TX": "Texas",
    "UT": "Utah", "VT": "Vermont", "VA": "Virginia", "WA": "Washington",
    "WV": "West Virginia", "WI": "Wisconsin", "WY": "Wyoming",
}
```

---

## Payment Methods Mapping

Convert the `payment_methods` array to a comma-separated `ProgramType` string:

| API value | Output text |
|-----------|-------------|
| `"tax_credit"` | `"Tax Credit"` |
| `"rebate"` | `"Rebate"` |
| `"pos_rebate"` | `"Point of Sale Rebate"` |
| `"account_credit"` | `"Account Credit"` |
| `"assistance_program"` | `"Assistance Program"` |
| `"performance_rebate"` | `"Performance Rebate"` |

```go
func mapPaymentMethods(methods []string) string // returns comma-joined readable names
```

---

## Items Mapping

Convert the `items` array to readable names and derive the product category:

| API item key | Readable name | Product category |
|-------------|---------------|-----------------|
| `"electric_vehicle_charger"` | `"Electric Vehicle Charger"` | `"Electric Vehicles"` |
| `"heat_pump"` | `"Heat Pump"` | `"HVAC"` |
| `"heat_pump_water_heater"` | `"Heat Pump Water Heater"` | `"Water Heaters"` |
| `"electric_panel"` | `"Electric Panel"` | `"Electrical"` |
| `"electric_wiring"` | `"Electric Wiring"` | `"Electrical"` |
| `"weatherization"` | `"Weatherization"` | `"Weatherization"` |
| `"insulation"` | `"Insulation"` | `"Weatherization"` |
| `"air_sealing"` | `"Air Sealing"` | `"Weatherization"` |
| `"rooftop_solar"` | `"Rooftop Solar"` | `"Solar"` |
| `"battery_storage"` | `"Battery Storage"` | `"Solar"` |
| `"electric_stove"` | `"Electric Stove"` | `"Appliances"` |
| `"heat_pump_clothes_dryer"` | `"Heat Pump Clothes Dryer"` | `"Appliances"` |
| `"electric_vehicle"` | `"Electric Vehicle"` | `"Electric Vehicles"` |
| `"ebike"` | `"E-Bike"` | `"Electric Vehicles"` |
| `"geothermal"` | `"Geothermal Heat Pump"` | `"HVAC"` |
| `"ductless_heat_pump"` | `"Ductless Heat Pump"` | `"HVAC"` |
| `"central_air_conditioner"` | `"Central Air Conditioner"` | `"HVAC"` |

**Multiple items:** Join readable names with `", "` for the `Items` field. For `ProductCategory`, use the most common category among the items (count occurrences; break ties by first appearance).

```go
func mapItems(items []string) (readableNames string, productCategory string)
```

---

## Amount Parsing Rules

| `amount.type` | `amount.number` | `amount.representative` | Output fields |
|---------------|-----------------|------------------------|---------------|
| `"dollar_amount"` | `1000` | `nil` | `MaximumAmount: 1000.00`, `IncentiveFormat: "dollar_amount"` |
| `"dollar_amount"` | `7500` | `&2000` | `IncentiveAmount: 2000.00`, `MaximumAmount: 7500.00`, `IncentiveFormat: "dollar_amount"` |
| `"percent"` | `30` | `nil` | `PercentValue: 30.00`, `IncentiveFormat: "percent"` |
| `"dollars_per_unit"` | `50` | `nil` | `PerUnitAmount: 50.00`, `UnitType: "unit"`, `IncentiveFormat: "per_unit"` |
| *(missing/zero)* | — | — | `IncentiveFormat: "narrative"` |

> **Note:** For `dollar_amount`, Rewiring America's `number` field is typically the **maximum** available. `representative` (when present) is the typical/minimum value.

---

## Date Parsing Rules

Normalize Rewiring America's flexible date formats to `YYYY-MM-DD`:

| Input | Output |
|-------|--------|
| `"2023"` | `"2023-01-01"` |
| `"2026-06-30"` | `"2026-06-30"` |
| `"2024-12"` | `"2024-12-01"` |
| `""` / `null` | *(omit field)* |

```go
func normalizeRADate(s string) string
```

---

## Application Process Generation

Generate `ApplicationProcess` deterministically from `payment_methods`:

| Contains | Generated text |
|----------|---------------|
| `"tax_credit"` | `"Claim when filing federal taxes using the relevant IRS form. Consult a tax professional for guidance."` |
| `"pos_rebate"` | `"Discount applied at point of sale through participating retailers or contractors."` |
| `"rebate"` (no pos) | `"Apply through the program website. Check eligibility requirements before purchasing equipment."` |
| `"account_credit"` | `"Contact your utility to enroll. Credits will be applied to your account statement."` |
| `"assistance_program"` | `"Apply through the program website. Income verification may be required."` |
| *(default)* | `"Visit the program website for application details and requirements."` |

If `payment_methods` contains multiple methods, generate text for the most specific method (priority: `pos_rebate` > `tax_credit` > `rebate` > others).

---

## Multi-Parameter Sweep

The API is parameterized by household income and owner status, which affects which AMI-flagged programs appear. To get comprehensive coverage:

```go
// Default sweep: query each ZIP with both income profiles and owner statuses
type SweepParams struct {
    ZipCode         string
    HouseholdSize   int
    HouseholdIncome int
    OwnerStatus     string  // "homeowner" or "renter"
}

// Example sweep combinations per ZIP:
// {zip, 2, 30000, "homeowner"}  → captures low-income programs
// {zip, 2, 80000, "homeowner"}  → captures general programs
// {zip, 2, 80000, "renter"}     → captures renter programs
```

Deduplicate by `program + authority_type` combination before staging. Different income levels may return the same program with different AMI flags — the last (highest-income) version wins to set `HighIncomeEligible: true` if the program appears for all income levels.

---

## ID Generation

```go
// Deterministic ID keyed on source + program name + authority type
// (Rewiring America has no stable numeric ID per incentive)
inc.ID = models.DeterministicID("rewiring_america", inc.ProgramName+"|"+authorityType)

// Program hash for cross-source deduplication
inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)
```

---

## Configuration

Add to (or confirm in) `config/config.go`:

```go
RewiringAmericaAPIKey      string        // REWIRING_AMERICA_API_KEY
RewiringAmericaAPIBaseURL  string        // REWIRING_AMERICA_API_BASE_URL
RewiringAmericaZipCodes    []string      // REWIRING_AMERICA_ZIP_CODES
RewiringAmericaHouseholdSize int         // REWIRING_AMERICA_HOUSEHOLD_SIZE
RewiringAmericaIncomes     []int         // REWIRING_AMERICA_INCOMES (comma-separated)
RewiringAmericaOwnerStatuses []string    // REWIRING_AMERICA_OWNER_STATUSES
RewiringAmericaRequestDelay time.Duration // REWIRING_AMERICA_REQUEST_DELAY_MS
```

Add to `.env.example`:

```env
REWIRING_AMERICA_API_KEY=your_api_key_here
REWIRING_AMERICA_API_BASE_URL=https://api.rewiringamerica.org
# Representative ZIPs: SoCal, NYC, Phoenix, Albuquerque, SF Bay Area, Denver
REWIRING_AMERICA_ZIP_CODES=90001,10001,85001,87101,94102,80202
REWIRING_AMERICA_HOUSEHOLD_SIZE=2
# Two income profiles for AMI coverage
REWIRING_AMERICA_INCOMES=30000,80000
# Both owner statuses for full coverage
REWIRING_AMERICA_OWNER_STATUSES=homeowner,renter
REWIRING_AMERICA_REQUEST_DELAY_MS=200
```

---

## Registration

Add to `cmd/scraper/main.go`:

```go
reg.Register(scrapers.NewRewiringAmericaScraper(
    cfg.RewiringAmericaAPIKey,
    cfg.RewiringAmericaAPIBaseURL,
    cfg.RewiringAmericaZipCodes,
    cfg.RewiringAmericaHouseholdSize,
    cfg.RewiringAmericaIncomes,
    cfg.RewiringAmericaOwnerStatuses,
    cfg.RewiringAmericaRequestDelay,
    log,
))
```

---

## Implementation Steps

### Step 1 — Data structures
1. Create/update `models/rewiring_america_types.go` with all API response structs
2. Add `RASharedContext` struct to carry response-level fields (coverage, location, AMI flags)

### Step 2 — HTTP client
1. In `scrapers/rewiring_america.go`, implement `fetchCalculator(ctx, params SweepParams) (*RewiringAmericaResponse, error)`
2. Set `Authorization: Bearer {API_KEY}` header
3. Handle HTTP errors: 401 (bad key), 422 (invalid ZIP), 429 (rate limit)

### Step 3 — Mapper helpers
1. Implement `mapPaymentMethods(methods []string) string`
2. Implement `mapItems(items []string) (readable, category string)`
3. Implement `normalizeRADate(s string) string`
4. Implement `applyAuthorityTypeRules(inc *RAIncentive, ctx RASharedContext) fields`
5. Implement `parseRAAmount(amt RAAmount) amountFields`
6. Implement `generateApplicationProcess(methods []string) string`

### Step 4 — Main mapper
1. Implement `mapRAIncentive(inc RAIncentive, ctx RASharedContext, zipCode string) models.Incentive`
2. Follow field mapping table exactly
3. Set `ID` and `ProgramHash`

### Step 5 — Multi-parameter sweep
1. The `Scrape()` method iterates all configured `(zip, income, ownerStatus)` combinations
2. Deduplicate by `ProgramName + AuthorityType` before staging
3. Merge AMI flags across income profiles (if same program appears at all income levels, all three booleans may be true)

### Step 6 — Register + test
1. Add/confirm env vars, register scraper in `main.go`
2. Test run: `SOURCE=rewiring_america RUN_ONCE=true LOG_FORMAT=console go run ./cmd/scraper`
3. Verify staging rows:
   ```sql
   SELECT COUNT(*), program_level FROM rebates_staging
   WHERE source='Rewiring America'
   GROUP BY program_level;
   ```
   Expect: `Federal`, `State`, `Utility` rows

### Step 7 — Verify authority type rules
1. Check that federal programs have `state = NULL`, `available_nationwide = true`
2. Check that state programs have `state` set and `service_territory = "[State] Statewide"`
3. Check that utility programs have `service_territory = "[utility] Service Area"`

---

## Implementation Checklist

- [ ] `models/rewiring_america_types.go` — all API structs + `RASharedContext`
- [ ] `scrapers/rewiring_america.go` — `RewiringAmericaScraper` implements `Scraper` interface
- [ ] `fetchCalculator` handles auth header + error codes (401, 422, 429)
- [ ] `mapPaymentMethods` — all 6 payment method mappings
- [ ] `mapItems` — all 17 item mappings + dominant category logic
- [ ] `normalizeRADate` — all 4 input formats
- [ ] Authority type rules applied correctly for all 6 authority types
- [ ] Amount parsing: dollar_amount, percent, dollars_per_unit, narrative
- [ ] `representative` value used as `IncentiveAmount` when present
- [ ] `application_process` generated from payment methods (priority order)
- [ ] `currently_active` computed from dates
- [ ] Multi-parameter sweep with deduplication
- [ ] AMI flags merged across income profiles
- [ ] `DeterministicID` keyed on program + authority type
- [ ] `config/config.go` — all env vars added/confirmed
- [ ] `.env.example` — all vars documented
- [ ] `cmd/scraper/main.go` — scraper registered
- [ ] Federal programs verified: `state = NULL`, `available_nationwide = true`
- [ ] State programs verified: `state` set, correct `service_territory`
- [ ] Utility programs verified: `utility_company = coverageUtility`
- [ ] Verified in `rebates_staging` with `source = 'Rewiring America'`
- [ ] `docs/scrapers.md` updated with Rewiring America entry

---

## Open Questions / Risks

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| API key expires or rate limits are hit during multi-ZIP sweep | Medium | Add `requestDelay`; handle 429 with exponential backoff |
| `coverageUtility` is null for some utility programs | Medium | Fall back to `"[State] Utility"` + log warning |
| Same program returned with conflicting `authority_type` for different ZIPs | Low | Dedup by `program + authority_type`; last write wins |
| Rewiring America API adds new `authority_type` values | Low | Default unmapped types to `ProgramLevel: "Other"`; log unknown values |
| `items` array contains unknown item keys | Low | Log unknown keys; fall back to `ProductCategory: "Other"` |
| `amount.type = "dollars_per_unit"` but no unit type context | Low | Default `UnitType: "unit"` since the API doesn't specify units beyond the amount |
| Household income sweep quadruples API calls | Known | 6 ZIPs × 2 incomes × 2 owner statuses = 24 calls max; manageable |
