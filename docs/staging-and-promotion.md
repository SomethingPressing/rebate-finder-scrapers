# Staging and Promotion

## Overview

Scraped incentives never go directly to the live `rebates` table. They always pass through `rebates_staging` first, where they can be reviewed and optionally edited before being promoted.

```
Scraper run
    │
    ▼
rebates_staging   (stg_promotion_status = 'pending')
    │
    ├── [review / skip bad rows]
    │
    ▼
pnpm scraper:promote   (consumer app)
    │
    ▼
rebates   (live table — visible to end users)
```

---

## Staging Phase

Every scraper run calls `db.UpsertToStaging()`, which does:

```sql
INSERT INTO rebates_staging (stg_source_id, program_name, ...)
VALUES (...)
ON CONFLICT (stg_source_id) DO UPDATE
SET program_name = excluded.program_name,
    ...
    stg_program_hash = excluded.stg_program_hash,
    updated_at = excluded.updated_at
-- stg_promotion_status, stg_promoted_at, stg_rebate_id are intentionally excluded
```

**Key properties:**
- `stg_source_id` is a deterministic UUID v5 — re-scraping the same program always hits the same row
- Data columns are refreshed on re-scrape so stale data is kept current
- `stg_promotion_status` is **never overwritten** — a previously promoted or skipped row stays that way even after a re-scrape

---

## Review Phase (optional)

After a scrape run you can inspect staging rows before promoting:

```sql
-- How many rows are waiting?
SELECT stg_promotion_status, COUNT(*)
FROM rebates_staging
WHERE deleted_at IS NULL
GROUP BY 1;

-- Preview pending rows
SELECT stg_source_id, program_name, utility_company, state, incentive_format, incentive_amount
FROM rebates_staging
WHERE stg_promotion_status = 'pending'
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 30;

-- Skip a row you don't want promoted (e.g. bad data)
UPDATE rebates_staging
SET stg_promotion_status = 'skipped'
WHERE stg_source_id = '<uuid>';

-- Reset a row back to pending
UPDATE rebates_staging
SET stg_promotion_status = 'pending',
    stg_promoted_at       = NULL,
    stg_rebate_id         = NULL
WHERE stg_source_id = '<uuid>';
```

---

## Promotion Phase

Promotion is handled by **`prisma/scripts/promote-staging.ts`** in the consumer app — not this service.

```bash
cd ../rebate-finder

pnpm scraper:promote:dry            # preview — no writes
pnpm scraper:promote                # promote pending rows → rebates
pnpm scraper:promote:supabase       # promote + push to Supabase rebate_source_raw
pnpm scraper:promote:supabase:dry   # dry-run the Supabase push
```

### What Promotion Does

For each `stg_promotion_status = 'pending'` row:

1. **Resolves `program_hash`** — uses `stg_program_hash` from the row; falls back to computing it on-the-fly for older rows that pre-date the column.

2. **Upserts into `rebates`** keyed on `program_hash`:
   - **INSERT (new program):** status = `draft`, processed = false, is_featured = false
   - **UPDATE (existing program):** all data columns refreshed; `status` is excluded so admin-approved programs stay approved

3. **Creates `Zipcode` + `RebateZipcode`** if the staging row has a `zip_code`:
   - Upserts `zipcodes` row (idempotent)
   - Upserts `rebate_zipcodes` join (many-to-many between `rebates` and `zipcodes`)

4. **Marks the staging row as promoted:**
   ```sql
   UPDATE rebates_staging
   SET stg_promotion_status = 'promoted',
       stg_promoted_at       = NOW(),
       stg_rebate_id         = '<rebate-uuid>'
   WHERE id = <staging_row_id>
     AND stg_promotion_status = 'pending';
   ```

### Deduplication

Two staging rows with different `stg_source_id` values but the same `program_hash` (same program name + utility company, different zip codes) merge into **one `rebates` row** with multiple `zipcodes` entries. The second staging row's `stg_rebate_id` points to the same live rebate as the first.

This is intentional — the same Consumers Energy heat pump program offered in ZIP 48201 and ZIP 48202 should be one rebate with two zipcodes, not two separate programs.

### Dry-run

Always run with `DRY_RUN=true` (or `pnpm scraper:promote:dry`) before the first promotion of a new scrape batch to verify what would be written:

```
[promote-staging] pending rows: 47
[promote-staging] DRY_RUN=true — no writes. Preview:
  · stg_source_id=abc123  program="Heat Pump Rebate"  source=dsireusa
  · stg_source_id=def456  program="Solar Incentive"   source=rewiring_america
  ...
[promote-staging] DRY_RUN — done, nothing written.
```

---

## Promotion Status Reference

| Status | Meaning |
|--------|---------|
| `pending` | Not yet promoted. Will be picked up by `pnpm scraper:promote`. |
| `promoted` | Successfully upserted into `rebates`. `stg_rebate_id` + `stg_promoted_at` are set. |
| `skipped` | Deliberately excluded from promotion. Will not be picked up unless manually reset to `pending`. |

---

## Tracking a Row End-to-End

```sql
-- 1. Find a staging row
SELECT * FROM rebates_staging WHERE stg_source_id = '<uuid>';

-- 2. Find the live rebate it was promoted into
SELECT r.*
FROM rebates r
JOIN rebates_staging s ON r.id = s.stg_rebate_id
WHERE s.stg_source_id = '<uuid>';

-- 3. Find all staging rows that promoted into the same rebate
--    (shows all ZIP codes that were merged)
SELECT s.stg_source_id, s.zip_code, s.source, s.stg_promoted_at
FROM rebates_staging s
WHERE s.stg_rebate_id = '<rebate-uuid>'
ORDER BY s.stg_promoted_at;

-- 4. See all ZIP codes now attached to the live rebate
SELECT z.code
FROM zipcodes z
JOIN rebate_zipcodes rz ON rz."zipcodeCode" = z.code
WHERE rz."rebateId" = '<rebate-uuid>';
```
