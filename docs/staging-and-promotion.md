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
    implementing_sector = excluded.implementing_sector,
    stg_program_hash = excluded.stg_program_hash,
    updated_at = excluded.updated_at
-- stg_promotion_status, stg_promoted_at, stg_rebate_id are intentionally excluded
```

**Key properties:**
- `stg_source_id` is a deterministic UUID v5 — re-scraping the same program always hits the same row
- Data columns (including `implementing_sector`) are refreshed on re-scrape so stale data is kept current
- `stg_promotion_status` is **never overwritten** by a normal re-scrape — a previously promoted or skipped row stays that way

### Force refresh

When `--force-refresh` (or `FORCE_REFRESH=true`) is set, the service calls `db.ResetToPending()` immediately after each upsert batch. This resets `stg_promotion_status = 'pending'` and clears `stg_promoted_at` for every row that was just written, so the promoter will re-process them on its next run. The promoter upsert is idempotent, so no duplicate live rows are created.

```bash
npm run refresh                    # all sources
npm run refresh:dsireusa           # single source
FORCE_REFRESH=true RUN_ONCE=true go run ./cmd/scraper   # via Go directly
```

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

Promotion is handled by **`cmd/promoter`** in this service (run via `npm run scraper:promote`).

```bash
npm run scraper:promote             # promote pending rows → rebates
npm run scraper:promote:dry         # preview — no writes
```

### What Promotion Does

`db.Promote()` (`db/promoter.go`) runs an 8-phase pipeline for each batch of `stg_promotion_status = 'pending'` rows:

1. **Fetch** all pending rows from `scraper.rebates_staging`.

2. **Group by `stg_program_hash`**, sorted by source priority (`PROMOTER_SOURCE_PRIORITY`, default: `rewiring_america > dsireusa > energy_star`). Multiple scrape rows for the same program are merged into one canonical record — the highest-priority source wins for scalar fields; arrays are unioned.

3. **Build `LiveRebate` structs** — includes `implementing_sector` and the `portfolio` array derived from `category_tag`.

4. **Batch-upsert into `public.rebates`** (`ON CONFLICT (id)`) — data columns refreshed; `status` is excluded from the update so admin-approved programs stay approved.

5. **Resolve hash → ID** map from phase 3 data (no extra DB query needed).

6. **Bulk-upsert `zipcodes`, `rebate_zipcodes`, `rebate_zipcode_sources`** — one row per `(rebate, zip)` pair, idempotent.

7. **Sync `rebate_categories`** — for each promoted rebate, deletes stale category links and re-inserts join rows by matching `category_tag` values against `public.categories.name`. Unknown tags are silently skipped. This step is non-fatal.

8. **Mark staging rows as `promoted`:**
   ```sql
   UPDATE scraper.rebates_staging
   SET stg_promotion_status = 'promoted',
       stg_promoted_at       = NOW(),
       stg_rebate_id         = '<rebate-uuid>'
   WHERE id IN (...)
     AND stg_promotion_status = 'pending';
   ```

### Deduplication

Two staging rows with different `stg_source_id` values but the same `stg_program_hash` (same normalized `program_name + utility_company`) merge into **one `rebates` row**. All ZIP codes from every merged row are collected and written to `rebate_zipcodes`. Each merged staging row's `stg_rebate_id` points to the same live rebate.

This is intentional — the same Consumers Energy heat pump program offered in ZIP 48201 and ZIP 48202 should be one rebate with two zipcodes, not two separate programs.

### Dry-run

Always run `npm run scraper:promote:dry` before the first promotion of a new scrape batch to verify what would be written:

```
[promote] pending rows: 47
  · [dsireusa] "Heat Pump Rebate"
  · [rewiring_america] "Solar Incentive"
  · MERGE (2 sources) hash=3a7f9c1d…
      dsireusa             "Weatherization Assistance"
      rewiring_america     "Weatherization Assistance"
  ...
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
