# PDF Scraper — Consumers Energy

The `cmd/pdf-scraper` binary extracts structured incentive data from Consumers Energy's annual PDF publications and stages it in `rebates_staging`.

---

## Usage

```bash
# Minimal (paths from env vars)
go run ./cmd/pdf-scraper

# Explicit paths + human-readable output
LOG_FORMAT=console go run ./cmd/pdf-scraper \
  --catalog     /path/to/Consumers_Energy_Incentive_Catalog.pdf \
  --application /path/to/Incentive-Application.pdf

# Also save raw extracted PDF text to pdf_scrape_raw (audit trail)
go run ./cmd/pdf-scraper --save-supabase
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--catalog` | `$CONSUMERS_ENERGY_CATALOG_PDF` | Path to the Incentive Catalog PDF |
| `--application` | `$CONSUMERS_ENERGY_APPLICATION_PDF` | Path to the Incentive Application PDF |
| `--save-supabase` | `false` | Also upsert raw PDF text to `pdf_scrape_raw` for audit |

### File Path Resolution (highest priority first)

1. `--catalog` / `--application` CLI flags
2. `CONSUMERS_ENERGY_CATALOG_PDF` / `CONSUMERS_ENERGY_APPLICATION_PDF` env vars
3. Defaults from `.env` file

---

## Page Offset

Consumers Energy PDFs use a page numbering offset vs. the printed page numbers:

| PDF | PDF page | Printed page | Formula |
|-----|----------|--------------|---------|
| Catalog | 50 | 48 | PDF = printed + 2 |
| Application | 23 | 23 | PDF = printed (no offset) |

Always use **PDF page numbers** (what a PDF viewer shows) when specifying ranges.

---

## Current Measures (2026)

### 1. Unitary & Split Air Conditioning Systems (`hvac_air_conditioning`)

RTU and split (including heat pump) AC systems. Incentive by nameplate cooling capacity (tons).

**Page ranges:**
- Catalog: p.50 (efficiency requirements)
- Application: p.23 (rate table)

**Rate tiers extracted (10 tiers):**

| ID | Description | Rate |
|----|-------------|------|
| HV101a | Split AC, < 5.4 Tons, Min 14.3 SEER2 | $30/ton |
| HV101b | Split AC, ≥ 5.4 to < 11.25 Tons, Min 12.0 EER & 19.0 IEER | $40/ton |
| HV101c | Split AC, ≥ 11.25 to < 20 Tons | $40/ton |
| HV101d | Split AC, ≥ 20 to 63 Tons | $30/ton |
| HV101e | Split AC, > 63 Tons, Min 11.4 IEER | $30/ton |
| HV101f | Unitary RTU, < 5.4 Tons, Min 15.2 SEER2 | $30/ton |
| HV101g | Unitary RTU, ≥ 5.4 to < 11.25 Tons | $40/ton |
| HV101h | Unitary RTU, ≥ 11.25 to < 20 Tons | $40/ton |
| HV101i | Unitary RTU, ≥ 20 to 63 Tons | $30/ton |
| HV101j | Unitary RTU, > 63 Tons, Min 11.4 IEER | $30/ton |

**Stored as:** `incentive_format = "tiered"`, all tiers embedded in `rate_tiers` JSONB.

---

### 2. Interior Linear LED Tube Light Retrofits (`lighting_led_tubes`)

T8/T12/T5 fluorescent lamps replaced with LED tube lights (Type A, B, C, or Dual Mode). Incentive per lamp replaced or per watt reduced.

**Page ranges:**
- Catalog: p.13–14
- Application: p.10–11

**Rate tiers extracted (29 tiers):**

| ID Range | Description | Rate |
|----------|-------------|------|
| LT101–LT113 | Various T12/T8 → LED (Type A/B/DM), 2-ft, 3-ft, 4-ft, 8-ft | $1.00–$10.00/lamp |
| LT114–LT126 | Same as above but Type C LED tubes | $1.00–$10.00/lamp |
| LT207 | New fixture, High Bay ≥ 15-ft | $0.55/watt reduced |
| LT208 | New fixture, High Bay ≥ 15-ft (continuous operation) | $1.00/watt reduced |
| LT209 | New fixture, Low Bay < 15-ft | $0.30/watt reduced |

---

### 3. Discus or Scroll Compressors for Walk-In Coolers/Freezers (`refrigeration_compressors`)

High-efficiency semi-hermetic compressors. Incentive by nameplate cooling capacity (tons).

**Page ranges:**
- Catalog: p.85
- Application: p.36

**Rate tiers extracted (2 tiers):**

| ID | Description | Rate |
|----|-------------|------|
| RL101 | Discus Compressors | $20/ton |
| RL102 | Scroll Compressors | $40/ton |

---

## How It Works

```
1. Load config (DATABASE_URL is only strict requirement)
2. Resolve PDF file paths (CLI flags → env vars → .env defaults)
3. Validate both files exist
4. For each measure spec:
   a. Extract text from specified page ranges (ledongthuc/pdf)
   b. Rate tiers are hardcoded (hand-curated from the PDF tables)
   c. Convert to models.Incentive with RateTiers JSONB
5. Compute deterministic stg_source_id (UUID v5 keyed on measure key)
6. Compute stg_program_hash (SHA-256 of name|utility_company)
7. Upsert all 3 measures to rebates_staging
8. If --save-supabase: also upsert raw extracted text to pdf_scrape_raw
```

> **Note:** The rate tier data is hardcoded in `scrapers/consumers_energy.go`, not parsed dynamically from the PDF text. The PDF text is extracted for audit trail purposes only. This avoids fragile PDF text parsing while keeping a record of what the PDF says.

---

## Output Modes

### Console (human-readable)

```bash
LOG_FORMAT=console go run ./cmd/pdf-scraper ...
```

Outputs a formatted report to stdout:

```
════════════════════════════════════════════════════
Consumers Energy — Incentive Catalog Extraction
════════════════════════════════════════════════════

  Unitary (RTU) and Split Air Conditioning Systems
  ─────────────────────────────────────────────────
  HV101a  Split AC, < 5.4 Tons  →  $30/ton
  ...

Done.  3 measures logged.  Staged to rebates_staging.
```

### JSON (default — production)

Structured JSON lines via zap. One log entry per measure inserted.

---

## Audit Trail (`--save-supabase`)

When `--save-supabase` is passed, extracted PDF text is also saved to `pdf_scrape_raw`:

```sql
SELECT source, measure_key, pdf_type, pages, raw_text
FROM pdf_scrape_raw
WHERE source = 'consumers_energy_pdf';
```

This lets you compare what the PDF actually says against what was extracted, useful for verifying after a PDF update.

---

## Adding a New Measure

1. Open `scrapers/consumers_energy.go`
2. Find the `ceSpecs` slice (the list of `ceIncentiveSpec` structs)
3. Add a new entry:

```go
{
    MeasureKey:  "your_measure_key",           // stable, lowercase, underscore
    ProgramName: "Human-Readable Program Name",
    IDs:         []string{"XX101", "XX102"},   // IDs from the application PDF
    Category:    "Category Name",
    Description: "Full description for incentive_description field.",
    CatalogPages: []scrapers.PageRange{{Start: 42, End: 43}},
    ApplicationPages: []scrapers.PageRange{{Start: 18, End: 18}},
    Rates: []ceRate{
        {ID: "XX101", Description: "Product Type A", Amount: 50.0, Unit: "$/unit"},
        {ID: "XX102", Description: "Product Type B", Amount: 75.0, Unit: "$/unit"},
    },
},
```

4. Run the scraper to verify:

```bash
LOG_FORMAT=console go run ./cmd/pdf-scraper \
  --catalog /path/to/catalog.pdf \
  --application /path/to/application.pdf
```

---

## Updating After a New PDF Is Released

1. Download the new PDFs
2. Open them in a PDF viewer and note the **PDF page numbers** for each measure (not the printed page numbers)
3. Update the `CatalogPages` and `ApplicationPages` in the relevant `ceIncentiveSpec` entries in `scrapers/consumers_energy.go`
4. Update the `Rates` slice if incentive amounts changed
5. Re-run the scraper — `ON CONFLICT DO UPDATE` will refresh existing staging rows with new data
6. Run `pnpm scraper:promote` in the consumer app to push updates to live rebates
