-- =============================================================================
-- backfill-zipcode-sources.sql
--
-- One-time backfill: populates rebate_zipcode_sources from the already-promoted
-- staging rows in scraper.rebates_staging.
--
-- Context
-- ───────
-- The rebate_zipcode_sources table was added after the initial promotion runs.
-- All promoted staging rows still contain their original zip_code / zip_codes
-- and source fields, so we can reconstruct the per-source attribution retroactively.
--
-- Run once (idempotent — ON CONFLICT DO NOTHING):
--   psql "$DATABASE_URL" -f scripts/backfill-zipcode-sources.sql
--
-- Or via pnpm:
--   pnpm backfill:zipcode-sources
-- =============================================================================

BEGIN;

INSERT INTO rebate_zipcode_sources
    ("rebateId", "zipcodeCode", source, "stagingSourceId")

SELECT DISTINCT
    s.stg_rebate_id          AS "rebateId",
    z.zip                    AS "zipcodeCode",
    s.source,
    s.stg_source_id          AS "stagingSourceId"

FROM scraper.rebates_staging s

-- Expand both the single zip_code column and the zip_codes array into rows.
CROSS JOIN LATERAL (
    SELECT s.zip_code AS zip
    WHERE  s.zip_code IS NOT NULL AND s.zip_code <> ''

    UNION ALL

    SELECT unnest(s.zip_codes) AS zip
    WHERE  s.zip_codes IS NOT NULL AND array_length(s.zip_codes, 1) > 0
) z

-- Only include zips that actually made it into rebate_zipcodes
-- (guards against any zips that were filtered out during promotion).
JOIN rebate_zipcodes rz
  ON rz."rebateId"    = s.stg_rebate_id
 AND rz."zipcodeCode" = z.zip

WHERE s.stg_promotion_status = 'promoted'
  AND s.stg_rebate_id IS NOT NULL
  AND z.zip IS NOT NULL
  AND z.zip <> ''

ON CONFLICT ("rebateId", "zipcodeCode", source) DO NOTHING;

-- Report how many rows were inserted.
DO $$
DECLARE
  n BIGINT;
BEGIN
  SELECT COUNT(*) INTO n FROM rebate_zipcode_sources;
  RAISE NOTICE 'rebate_zipcode_sources now has % rows', n;
END $$;

COMMIT;
