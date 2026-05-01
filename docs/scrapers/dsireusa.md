# DSIRE USA (`dsireusa`)

**Source:** [programs.dsireusa.org](https://programs.dsireusa.org) — Database of State Incentives for Renewables & Efficiency

**API:** Public REST API, no key required.

---

## Approach

1. Queries the DSIRE v1 programs endpoint with one representative ZIP per US state (51 ZIPs total).
2. Fetches all matching programs in a single page (`length=-1`) per ZIP.
3. Deduplicates by DSIRE program ID — the same program appearing in multiple states is only stored once.
4. Strips HTML from `summary` fields.

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

## Rate Limiting

No explicit delay (DSIRE API is paged).

---

## Configuration

```env
DSIREUSA_BASE_URL=https://programs.dsireusa.org   # override for testing
```
