# DSIRE USA (`dsireusa`)

**Source:** [programs.dsireusa.org](https://programs.dsireusa.org) — Database of State Incentives for Renewables & Efficiency

**API:** Public REST API, no key required.

---

## Geographic Coverage — How States & ZIPs Are Determined

DSIRE is queried **per state**, not per ZIP. The scraper iterates over a hardcoded list of all 50 US states + DC:

```
AL AK AZ AR CA CO CT DE DC FL GA HI ID IL IN IA KS KY LA ME
MD MA MI MN MS MO MT NE NV NH NJ NM NY NC ND OH OK OR PA RI
SC SD TN TX UT VT VA WA WV WI WY
```

One API request is made per state (`?state[]={abbr}`). This guarantees every state's programs are discovered regardless of ZIP coverage.

**ZIP population for storage (`ZipCodes` field):** When `StateZIPs` is loaded from `data/uszips.csv`, every ZIP in the program's state is stored in the `ZipCodes` array on the incentive record. ZIPs are sorted by descending population so downstream systems get the most-populated ZIP first. This is purely for indexing — it does not affect which programs are fetched.

**Deduplication:** Federal and multi-state programs appear in multiple state responses. The scraper deduplicates by DSIRE program integer ID, so each program is stored only once.

---

## Approach

1. Queries the DSIRE v1 programs endpoint once per US state + DC (51 requests total).
2. Fetches all matching programs in a single page (`length=-1`) per state.
3. Deduplicates by DSIRE program ID — the same program appearing in multiple states is only stored once.
4. Strips HTML from `summary` fields.
5. Stores all ZIPs for the program's state in `ZipCodes` (from `data/uszips.csv`).

---

## Endpoint

```
GET https://programs.dsireusa.org/api/v1/programs
    ?zipcode[]={zip}
    &category[]=1
    &draw=1
    &start=0
    &length=-1
```

---

## ID Generation

`DeterministicID("dsireusa", dsireProgram.ID)` — UUID v5 keyed on the DSIRE integer program ID.

---

## Fields Mapped

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ProgramName` | `program.ProgramName` |
| `IncentiveDescription` | `program.Summary` (HTML stripped) |
| `State` | `program.StateObj.Abbreviation` |
| `CategoryTag` | Extracted from `ParameterSets[].Technologies` |
| `Segment` | Extracted from `ParameterSets[].Sectors` |
| `CustomerType` | Joined sector names from `ParameterSets` |
| `Portfolio` | Program level derived from `SectorObj.Name` — Federal / State / Utility / Local |
| `ProductCategory` | Top technology category from `ParameterSets` |
| `Administrator` | `program.Administrator` |
| `StartDate` | `program.StartDate` |
| `EndDate` | `program.EndDate` |
| `Status` | `"active"` when `program.Published == "Yes"`, otherwise default `"draft"` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, UtilityCompany)` |
| `Source` | `"dsireusa"` (hardcoded) |

---

## Running

```bash
pnpm run:dsireusa
```

Or directly via Go:

```bash
SOURCE=dsireusa RUN_ONCE=true go run ./cmd/scraper
```

---

## Rate Limiting

No explicit delay (DSIRE API is paged).

---

## Configuration

```env
DSIREUSA_BASE_URL=https://programs.dsireusa.org   # override for testing
```
