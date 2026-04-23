# Database Reference

The scraper service manages two tables. Both are created by GORM `AutoMigrate` on first startup.

---

## `rebates_staging`

Staging area for all scraped incentives. Scrapers only write here — never to the live `rebates` table.

### Columns

| Column | Type | Notes |
|--------|------|-------|
| `id` | `bigserial` PK | Auto-increment integer (GORM default) |
| `created_at` | `timestamptz` | Set on insert |
| `updated_at` | `timestamptz` | Updated on every upsert |
| `deleted_at` | `timestamptz` | GORM soft-delete (NULL = active) |
| `stg_source_id` | `text` UNIQUE NOT NULL | Deterministic UUID v5 — stable upsert key |
| `stg_promotion_status` | `text` DEFAULT `'pending'` | `pending` / `promoted` / `skipped` |
| `stg_promoted_at` | `timestamptz` | Set when promoted; NULL while pending/skipped |
| `stg_rebate_id` | `text` | UUID of the live `rebates` row after promotion |
| `stg_program_hash` | `text` | SHA-256 of `normalize(program_name\|utility_company)` |
| `program_name` | `text` NOT NULL | Program name |
| `utility_company` | `text` NOT NULL | Utility / program administrator |
| `incentive_description` | `text` | Full description |
| `incentive_amount` | `decimal` | Dollar amount (dollar_amount type) |
| `maximum_amount` | `decimal` | Cap for percent / per-unit types |
| `percent_value` | `decimal` | Percentage for percent type |
| `per_unit_amount` | `decimal` | Rate per unit for per_unit type |
| `incentive_format` | `text` | `dollar_amount`, `percent`, `per_unit`, `tiered`, etc. |
| `unit_type` | `text` | Unit for per_unit: `watt`, `kwh`, `ton`, etc. |
| `state` | `text` | Two-letter state code |
| `zip_code` | `text` | ZIP code for this scrape hit (single value per row) |
| `service_territory` | `text` | Utility service territory description |
| `available_nationwide` | `boolean` | True for federal programs |
| `category_tag` | `text[]` | Category labels |
| `segment` | `text[]` | `Residential`, `Commercial`, `Industrial`, etc. |
| `portfolio` | `text[]` | `Federal`, `State`, `Utility`, etc. |
| `customer_type` | `text` | Target customer |
| `product_category` | `text` | Product/equipment category |
| `administrator` | `text` | Program administrator name |
| `source` | `text` NOT NULL | Scraper identifier (`dsireusa`, `rewiring_america`, etc.) |
| `start_date` | `text` | Program start date (string — varies by source) |
| `end_date` | `text` | Program end date |
| `while_funds_last` | `boolean` | True if program is limited-duration |
| `application_url` | `text` | Application form URL |
| `application_process` | `text` | Description of how to apply |
| `program_url` | `text` | Program landing page |
| `contact_email` | `text` | Contact email |
| `contact_phone` | `text` | Contact phone |
| `image_url` | `text` | Primary image |
| `image_urls` | `text[]` | Additional images |
| `contractor_required` | `boolean` | Whether a contractor is required |
| `energy_audit_required` | `boolean` | Whether an energy audit is required |
| `rate_tiers` | `jsonb` | Array of `{id, description, amount, unit}` for tiered programs |
| `scraper_version` | `text` | Scraper version string at time of scrape |

### Indexes

| Name | Columns | Type |
|------|---------|------|
| `idx_rebates_staging_stg_source_id` | `stg_source_id` | UNIQUE — upsert key |
| `idx_rebates_staging_rebate_id` | `stg_rebate_id` | B-tree — reverse lookup |
| `idx_rebates_staging_promotion_status` | `stg_promotion_status` | B-tree — filter pending |
| `idx_rebates_staging_deleted_at` | `deleted_at` | B-tree — GORM soft-delete |

### `rate_tiers` JSONB shape

```json
[
  {
    "id":          "HV101a",
    "description": "Split AC, < 5.4 Tons, Min 14.3 SEER2",
    "amount":      30.0,
    "unit":        "$/Ton"
  }
]
```

---

## `pdf_scrape_raw`

Audit trail for PDF extractions. Stores the raw text extracted from each PDF page range so you can verify what the PDF says vs. what was staged.

Only populated when `--save-supabase` is passed to `cmd/pdf-scraper`.

### Columns

| Column | Type | Notes |
|--------|------|-------|
| `id` | `bigserial` PK | Auto-increment |
| `created_at` | `timestamptz` | |
| `updated_at` | `timestamptz` | |
| `deleted_at` | `timestamptz` | GORM soft-delete |
| `source` | `text` NOT NULL | Source identifier, e.g. `consumers_energy_pdf` |
| `measure_key` | `text` NOT NULL | Measure identifier, e.g. `hvac_air_conditioning` |
| `pdf_type` | `text` NOT NULL | `catalog` or `application` |
| `pages` | `text` NOT NULL | Human-readable page range, e.g. `p.50` |
| `file_path` | `text` NOT NULL | Absolute path to the PDF at scrape time |
| `raw_text` | `text` NOT NULL | Plain text extracted from the page range |
| `scraped_at` | `timestamptz` NOT NULL | When the extraction ran |

### Unique index

`idx_pdf_raw_unique` on `(source, measure_key, pdf_type)` — one row per measure per PDF per source.

---

## Useful Queries

```sql
-- Count by promotion status
SELECT stg_promotion_status, COUNT(*)
FROM rebates_staging
GROUP BY 1;

-- Preview pending rows
SELECT stg_source_id, program_name, utility_company, state, zip_code,
       incentive_format, incentive_amount, stg_program_hash
FROM rebates_staging
WHERE stg_promotion_status = 'pending'
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 20;

-- Find all staging rows for a live rebate
SELECT s.*
FROM rebates_staging s
WHERE s.stg_rebate_id = '<rebate-uuid>'
ORDER BY s.created_at DESC;

-- See which ZIP codes a program was scraped for
SELECT stg_source_id, zip_code, stg_promotion_status
FROM rebates_staging
WHERE program_name ILIKE '%heat pump%'
ORDER BY zip_code;

-- Manually skip a bad row
UPDATE rebates_staging
SET stg_promotion_status = 'skipped'
WHERE stg_source_id = '<uuid>';

-- Reset a row back to pending
UPDATE rebates_staging
SET stg_promotion_status = 'pending',
    stg_promoted_at       = NULL,
    stg_rebate_id         = NULL
WHERE stg_source_id = '<uuid>';

-- Audit: what did the PDF say for a measure?
SELECT measure_key, pdf_type, pages, raw_text
FROM pdf_scrape_raw
WHERE source = 'consumers_energy_pdf'
ORDER BY measure_key, pdf_type;
```
