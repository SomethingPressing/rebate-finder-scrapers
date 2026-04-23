# Implementation Plan: Energy Star API Scraper

**Source file:** `rf-energystar-mobyqj8mqnc.smyth`
**Status:** Planning

---

## Overview

Implement a Go scraper for the [Energy Star Rebate Finder API](https://www.energystar.gov/productfinder/api/imp_rebate_results/search). Unlike web scrapers, this is a structured REST API that returns paginated JSON. The scraper takes a ZIP code as input, fetches all pages of results for that ZIP code, parses the nested JSON structure (the `incentivedata` field is a stringified JSON blob), and maps each record to the shared `models.Incentive` schema.

The SmythOS agent this plan is based on uses a three-phase approach:
1. **Pagination probe**: Fetch page 0 to discover `resultsCount` and `pageSize`
2. **Full fetch**: Iterate all pages (0 to `ceil(resultsCount/pageSize) - 1`) in parallel
3. **LLM transformation**: Map each parsed record to the output schema

The Go implementation replaces the LLM transformation step with deterministic field mapping logic derived from the LLM prompt's mapping tables.

---

## API Reference

### Endpoint

```
GET https://www.energystar.gov/productfinder/api/imp_rebate_results/search
```

### Query Parameters

| Parameter | Type | Notes |
|-----------|------|-------|
| `zip_code_filter` | string | Required — ZIP code to query |
| `page_number` | int | 0-indexed page number |
| `sort_by` | string | `utility` (fixed) |
| `sort_direction` | string | `asc` (fixed) |
| `scrollTo` | int | `0` (fixed) |
| `lastpage` | int | `0` (fixed) |
| `search_text` | string | `""` (empty) |
| `product_general_isopen` | int | `0` (fixed) |

### Example URL
```
https://www.energystar.gov/productfinder/api/imp_rebate_results/search?scrollTo=0&sort_by=utility&sort_direction=asc&page_number=0&lastpage=0&zip_code_filter=90001&search_text=&product_general_isopen=0
```

### Response Shape

```json
{
  "resultsCount": 142,
  "pageSize": 20,
  "results": [
    {
      "incentive_id": "3563",
      "publishedincentiveid": "...",
      "utility": "Southern California Edison",
      "zip_code": "90001",
      "available_nationwide": "Yes",
      "partner_category": "...",
      "product_category": "Commercial Food Service",
      "product_general": "Commercial Steam Cookers",
      "product": "All",
      "incentiveamount": "1500",
      "incentive_start_date": "1672556400000",
      "incentive_end_date": "1767243600000",
      "incentivedata": "{\"serviceterritory\":{...},\"incentivetype\":{...},\"incentiveamount\":\"1500\",\"programwebaddress\":\"...\", ...}"
    }
  ]
}
```

> **Key detail:** `incentivedata` is a **stringified JSON string**, not a nested object. It must be `json.Unmarshal`-ed separately.

---

## Architecture

```
Input: zip_code (string)

Phase 1 — Pagination probe
  GET /search?zip_code_filter={zip}&page_number=0
  → resultsCount, pageSize
  → compute totalPages = ceil(resultsCount / pageSize)

Phase 2 — Full fetch (concurrent with rate limit)
  for page in [0 .. totalPages-1]:
    GET /search?zip_code_filter={zip}&page_number={page}
    → []RawResult

Phase 3 — Parse + map
  for each RawResult:
    json.Unmarshal(result.IncentiveData) → incentiveData struct
    MapToIncentive(result, incentiveData, zipCode) → models.Incentive

Phase 4 — Stage
  db.UpsertToStaging(incentives)
```

---

## New Files

```
scrapers/
└── energy_star.go          ← Energy Star API scraper

models/
└── energy_star_types.go    ← Raw API response structs
```

---

## Data Structures

### `models/energy_star_types.go`

```go
// EnergyStarSearchResponse is the top-level API response.
type EnergyStarSearchResponse struct {
    ResultsCount int                    `json:"resultsCount"`
    PageSize     int                    `json:"pageSize"`
    Results      []EnergyStarRawResult  `json:"results"`
}

// EnergyStarRawResult is one row from the results array.
// incentivedata is a stringified JSON blob that must be parsed separately.
type EnergyStarRawResult struct {
    IncentiveID         string `json:"incentive_id"`
    PublishedIncentiveID string `json:"publishedincentiveid"`
    Utility             string `json:"utility"`
    ZipCode             string `json:"zip_code"`
    AvailableNationwide string `json:"available_nationwide"` // "Yes" / "No"
    ProductCategory     string `json:"product_category"`
    ProductGeneral      string `json:"product_general"`
    Product             string `json:"product"` // subcategory override
    IncentiveAmount     string `json:"incentiveamount"`
    IncentiveStartDate  string `json:"incentive_start_date"` // Unix ms timestamp
    IncentiveEndDate    string `json:"incentive_end_date"`   // Unix ms timestamp
    IncentiveData       string `json:"incentivedata"`        // stringified JSON
}

// EnergyStarIncentiveData is the parsed form of the incentivedata field.
type EnergyStarIncentiveData struct {
    ServiceTerritory      *ESTServiceTerritory      `json:"serviceterritory"`
    IncentiveType         *ESTNamedEntity           `json:"incentivetype"`
    IncentiveAmount       string                     `json:"incentiveamount"`
    IncentiveMarketSector *ESTNamedEntity           `json:"incentivemarketsector"`
    IncentiveBuildingSector *ESTNamedEntity         `json:"incentivebuildingsector"`
    IncentiveRecipient    *ESTNamedEntity           `json:"incentiverecipient"`
    IncomeQualification   *ESTNamedEntity           `json:"incomequalification"`
    EnergyAuditRequired   string                    `json:"energyauditrequired"` // "Y"/"N"
    DeliveryMechanics     json.RawMessage           `json:"incentivedeliverymechanics"`
    ProgramWebAddress     string                    `json:"programwebaddress"`
    ContactEmail          string                    `json:"contactemail"`
    ContactPhone          string                    `json:"contactphonenumber"`
    IncentiveStatus       *ESTIncentiveStatus       `json:"incentivestatus"`
    StartDate             string                    `json:"incentivestartedate"` // Unix ms
    EndDate               string                    `json:"incentiveenddate"`   // Unix ms
    ProductSubcategory    *ESTProductSubcategory    `json:"incentiveproductsubcategory"`
    WebsiteVisibility     *ESTNamedEntity           `json:"websitevisibility"`
    IncentiveComments     json.RawMessage           `json:"incentivecomments"`
}

type ESTServiceTerritory struct {
    Name      string `json:"serviceterritoryname"`
    StateCode string `json:"serviceterritorystatecode"`
    Type      *ESTNamedEntity `json:"serviceterritorytype"`
    Desc      string `json:"serviceterritorydesc"`
}

type ESTIncentiveStatus struct {
    Name         string          `json:"incentivestatusname"`
    ActiveStatus *ESTNamedEntity `json:"incentiveactivestatus"`
}

type ESTProductSubcategory struct {
    Name     string `json:"incentiveproductsubcategoryname"`
    Override string `json:"incentiveproductsubcategoryoverride"`
    General  *ESTProductGeneral `json:"incentiveproductgeneral"`
}

type ESTProductGeneral struct {
    Name string `json:"incentiveproductgeneralname"`
}

type ESTNamedEntity struct {
    Name string `json:"incentivetypename,omitempty"`     // incentivetype
    // other entities reuse similar name fields; use json.RawMessage + helper if needed
}

type ESTDeliveryMechanic struct {
    Name string `json:"incentivedeliverymechanicsname"`
    Type *ESTNamedEntity `json:"incentivetype"`
}
```

---

## Field Mapping

| Energy Star API field | `models.Incentive` field | Transformation |
|-----------------------|--------------------------|----------------|
| `"Energy Star"` (fixed) | `Source` | Constant |
| `zip_code` (input param) | `ZipCode` | Direct copy |
| `serviceTerritoryStateCode` | `State` | Direct copy (2-letter) |
| `utility` | `UtilityCompany` | Direct copy |
| `serviceTerritoryName` / `serviceTerritoryDesc` | `ServiceTerritory` | Name first, fallback to Desc |
| `available_nationwide == "Yes"` | `AvailableNationwide` | String → bool |
| `"[utility] [productGeneralName] Rebate"` | `ProgramName` | Constructed |
| `product_category` | `ProgramCategory` | Direct copy |
| `incentivetype.name` | `ProgramType` | Direct copy |
| `incentivemarketsector.name` | `CustomerType` | Direct copy |
| `product_category` | `ProductCategory` | Direct copy |
| `product` / `subcategoryOverride` | `Items` | Skip if "All" |
| `incentiveamount` | `IncentiveAmount` / `MaximumAmount` | Parsed (see Amount Parsing) |
| incentive format | `IncentiveFormat` | Derived from amount string |
| Generated | `IncentiveDescription` | `"[type]: [amount] on [productGeneralName]"` |
| `incentiverecipient.name` | `Recipient` | Direct copy |
| `incomequalification.name` | `LowIncomeEligible` / `ModerateIncomeEligible` / `HighIncomeEligible` | Mapped (see Income Mapping) |
| `energyauditrequired` | `EnergyAuditRequired` | `"Y"` → true, else false |
| `incentivebuildingsector.name` | `BuildingType` | Direct copy |
| `deliverymechanics` | `ApplicationProcess` | Parsed mechanic name → text |
| `deliverymechanics` | `InstantRebateAvailable` | true if mechanic mentions "retailers" or "instant" |
| `programwebaddress` | `ProgramURL` | Direct copy |
| `programwebaddress` | `SourcePage` | Same as ProgramURL |
| `contactemail` | `ContactEmail` | Direct copy |
| `contactphonenumber` | `ContactPhone` | Direct copy |
| `incentive_start_date` (Unix ms) | `StartDate` | Parse to `time.Time`, format YYYY-MM-DD |
| `incentive_end_date` (Unix ms) | `EndDate` | Parse to `time.Time`, format YYYY-MM-DD |
| `incentivestatus.activestate.name == "ACTIVE"` | `CurrentlyActive` | Bool |
| `DeterministicID("energy_star", incentive_id)` | `ID` | UUID v5 |
| `ComputeProgramHash(ProgramName, UtilityCompany)` | `ProgramHash` | SHA-256 |

---

## Amount Parsing Rules

The `incentiveamount` field can contain several formats. Parse deterministically:

| Input string | `IncentiveFormat` | `IncentiveAmount` | `MaximumAmount` |
|--------------|-------------------|--------------------|-----------------|
| `"1500"` or `"$1500"` | `dollar_amount` | `1500.00` | — |
| `"$100 - $500"` | `dollar_amount` | `100.00` | `500.00` |
| `"Up to $1000"` | `dollar_amount` | — | `1000.00` |
| `"30%"` | `percent` | — | — (`PercentValue: 30.00`) |
| `"Varies"` / empty | `narrative` | — | — |

Use a `parseIncentiveAmount(s string)` helper that tries these patterns in order with `regexp`.

---

## Income Qualification Mapping

| `incomequalification.name` | Fields set |
|----------------------------|------------|
| `"General"` | `HighIncomeEligible: true` |
| `"Low-Income"` | `LowIncomeEligible: true` |
| `"Moderate-Income"` | `ModerateIncomeEligible: true` |
| `"Low-to-Moderate Income"` | `LowIncomeEligible: true`, `ModerateIncomeEligible: true` |

---

## Delivery Mechanics → Application Process

Parse the JSON array in `deliverymechanics` (each element has a `incentivedeliverymechanicsname` string):

| Mechanic name contains | `ApplicationProcess` text | `InstantRebateAvailable` |
|------------------------|--------------------------|--------------------------|
| `"retailers"` | `"Purchase through participating retailers to receive instant rebate at point of sale. Visit program website for list of participating retailers."` | `true` |
| `"rebate application"` | `"Submit rebate application after purchase. Visit program website for application form and requirements."` | `false` |
| *(default)* | `"Visit the program website for application details and requirements."` | `false` |

---

## ID Generation

```go
// Deterministic ID keyed on source + Energy Star's own incentive_id
inc.ID = models.DeterministicID("energy_star", result.IncentiveID)

// Program hash for deduplication across sources
inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)
```

---

## Configuration

Add to `config/config.go`:

```go
EnergyStarAPIBaseURL string // ENERGY_STAR_API_BASE_URL
EnergyStarZipCodes   []string // ENERGY_STAR_ZIP_CODES (comma-separated)
EnergyStarPageDelay  time.Duration // ENERGY_STAR_PAGE_DELAY_MS
EnergyStarMaxConc    int          // ENERGY_STAR_MAX_CONCURRENCY (default: 3)
```

Add to `.env.example`:

```env
ENERGY_STAR_API_BASE_URL=https://www.energystar.gov
# Comma-separated ZIP codes to query (use representative ZIPs per utility territory)
ENERGY_STAR_ZIP_CODES=90001,10001,85001,87101,94102
ENERGY_STAR_PAGE_DELAY_MS=500
ENERGY_STAR_MAX_CONCURRENCY=3
```

> **Note on ZIP codes:** The Energy Star API returns results scoped to a utility's service territory for a given ZIP. To get broad national coverage, query a representative ZIP per major utility territory. The seed list above covers SoCal, NYC, Phoenix, Albuquerque, and SF Bay Area.

---

## Registration

Add to `cmd/scraper/main.go`:

```go
reg.Register(scrapers.NewEnergyStarScraper(
    cfg.EnergyStarAPIBaseURL,
    cfg.EnergyStarZipCodes,
    cfg.EnergyStarPageDelay,
    cfg.EnergyStarMaxConc,
    log,
))
```

---

## Implementation Steps

### Step 1 — Data structures
1. Create `models/energy_star_types.go` with all raw API structs
2. Add `Source = "Energy Star"` to the source enum constants if not already present

### Step 2 — HTTP client helper
1. In `scrapers/energy_star.go`, implement `fetchPage(ctx, zipCode, pageNum) (*EnergyStarSearchResponse, error)`
2. Respect `pageDelay` between requests; use `context.WithTimeout` (30s per request)

### Step 3 — Pagination probe
1. Call `fetchPage(ctx, zip, 0)` to get `resultsCount` and `pageSize`
2. Compute `totalPages = int(math.Ceil(float64(resultsCount) / float64(pageSize)))`
3. Log: `"Energy Star: zip=%s pages=%d total=%d"` 

### Step 4 — Full fetch (bounded concurrency)
1. Use `errgroup` + semaphore channel of size `maxConcurrency`
2. Collect all `EnergyStarRawResult` slices into a single flat slice

### Step 5 — Parse `incentivedata`
1. For each result, attempt `json.Unmarshal([]byte(result.IncentiveData), &incentiveData)`
2. Log + skip on parse error (don't crash the whole run)

### Step 6 — Map to `models.Incentive`
1. Implement `mapEnergyStarRecord(result, incentiveData, zipCode) models.Incentive`
2. Follow the field mapping table above
3. Call `parseIncentiveAmount(s)` for amount fields
4. Set `ID` and `ProgramHash`

### Step 7 — Register + test
1. Add env vars, register scraper in `main.go`
2. Test run: `SOURCE=energy_star RUN_ONCE=true LOG_FORMAT=console go run ./cmd/scraper`
3. Verify staging rows: `SELECT COUNT(*), source FROM rebates_staging WHERE source='Energy Star' GROUP BY source`

### Step 8 — Multi-ZIP run
1. The scraper's `Scrape()` method loops over all configured ZIP codes and merges results
2. Deduplicate by `incentive_id` before staging (same incentive can appear for multiple ZIPs)

---

## Implementation Checklist

- [ ] `models/energy_star_types.go` — all raw API structs defined
- [ ] `scrapers/energy_star.go` — `EnergyStarScraper` implements `Scraper` interface
- [ ] Pagination probe correctly computes `totalPages`
- [ ] Concurrent page fetch with bounded concurrency
- [ ] `incentivedata` stringified JSON parsed correctly (including null/invalid cases)
- [ ] `parseIncentiveAmount` handles all 5 input patterns
- [ ] Income qualification mapping applied
- [ ] Delivery mechanics parsed → `ApplicationProcess` + `InstantRebateAvailable`
- [ ] Unix millisecond timestamps converted to `YYYY-MM-DD`
- [ ] `DeterministicID` keyed on `incentive_id`
- [ ] Multi-ZIP deduplication before staging
- [ ] `config/config.go` — 4 new env vars added
- [ ] `.env.example` — new vars documented
- [ ] `cmd/scraper/main.go` — scraper registered
- [ ] Verified in `rebates_staging` with `source = 'Energy Star'`
- [ ] `docs/scrapers.md` updated with Energy Star entry

---

## Open Questions / Risks

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| `incentivedata` field is null or malformed for some records | Medium | Wrap parse in a recover; log + skip the record |
| API rate limiting / IP blocking on high-volume ZIP queries | Medium | Use `pageDelay` (default 500ms); limit `maxConcurrency` to 3 |
| Same incentive returned for multiple ZIP codes | High (by design) | Deduplicate by `incentive_id` before staging |
| API changes field names in `incentivedata` blob | Low | Struct uses `omitempty`; log unmapped fields in debug mode |
| Energy Star returns 0 results for some ZIPs | Normal | Handle `resultsCount == 0` gracefully (return empty slice, not error) |
| Large result sets (500+ records per ZIP) | Low | Pagination handles it; just ensure `totalPages` calc is correct |
