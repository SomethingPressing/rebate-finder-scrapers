package db

import (
	"github.com/incenva/rebate-scraper/models"
	"gorm.io/gorm"
)

// runPreMigrations executes hand-written SQL migrations that GORM AutoMigrate
// cannot handle safely on its own — typically adding NOT NULL columns to tables
// that already contain rows.
//
// Each migration is idempotent: it checks whether the change is needed before
// applying it, so re-running Connect never fails on an already-migrated schema.
func runPreMigrations(db *gorm.DB) error {
	// Move tables from public → the configured scraper schema first (one-time,
	// idempotent).  Must run before the other migrations so subsequent queries
	// target the correct schema.
	if err := migrateToScraperSchema(db); err != nil {
		return err
	}
	if err := migrateStagingSourceID(db); err != nil {
		return err
	}
	if err := migrateLegacySourceIDNullable(db); err != nil {
		return err
	}
	return nil
}

// migrateToScraperSchema moves rebates_staging and pdf_scrape_raw from the
// public schema to the configured scraper schema if they still live in public.
//
// This is the one-time migration that implements the "separate schema"
// architecture: Prisma owns public.*, Go/GORM owns <ScraperSchema>.*.
// After this runs, `prisma db push` never sees these tables and will never
// attempt to drop them.
func migrateToScraperSchema(db *gorm.DB) error {
	schema := models.ScraperSchema
	for _, table := range []string{"rebates_staging", "pdf_scrape_raw"} {
		var count int64
		if err := db.Raw(`
			SELECT COUNT(*)
			FROM   information_schema.tables
			WHERE  table_schema = 'public'
			AND    table_name   = ?
		`, table).Scan(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			continue // already moved or never existed in public
		}
		if err := db.Exec(`ALTER TABLE public.` + table + ` SET SCHEMA ` + schema).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateLegacySourceIDNullable makes the old source_id column (pre-stg_ rename)
// nullable so that GORM inserts — which only populate stg_source_id — don't fail.
// On a fresh database this column never exists, so the migration is a no-op.
func migrateLegacySourceIDNullable(db *gorm.DB) error {
	schema := models.ScraperSchema
	var count int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM   information_schema.columns
		WHERE  table_schema = ?
		AND    table_name   = 'rebates_staging'
		AND    column_name  = 'source_id'
	`, schema).Scan(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil // column doesn't exist — nothing to do
	}
	return db.Exec(`ALTER TABLE ` + schema + `.rebates_staging ALTER COLUMN source_id DROP NOT NULL`).Error
}

// migrateStagingSourceID adds the stg_source_id column to rebates_staging when
// it doesn't yet exist.
//
// AutoMigrate would emit `ALTER TABLE ... ADD "stg_source_id" text NOT NULL`
// which fails if the table already has rows (existing rows would be NULL).
// This migration instead:
//  1. Adds the column as nullable.
//  2. Backfills existing rows with gen_random_uuid() so no row is NULL.
//  3. Adds the NOT NULL constraint.
//
// After this runs, AutoMigrate sees the column and skips it.
func migrateStagingSourceID(db *gorm.DB) error {
	schema := models.ScraperSchema
	tbl := schema + ".rebates_staging"

	// Check whether the column already exists.
	var count int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM   information_schema.columns
		WHERE  table_schema = ?
		AND    table_name   = 'rebates_staging'
		AND    column_name  = 'stg_source_id'
	`, schema).Scan(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		// Column exists — ensure the unique index on stg_source_id is present.
		// Use a distinct name so it doesn't collide with the legacy source_id index.
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS "idx_rebates_staging_stg_source_id"
			ON ` + tbl + ` (stg_source_id)
		`).Error
	}

	// Step 1: add as nullable so existing rows are not immediately rejected.
	if err := db.Exec(`ALTER TABLE ` + tbl + ` ADD COLUMN stg_source_id text`).Error; err != nil {
		return err
	}

	// Step 2: backfill NULL rows with a random UUID so the NOT NULL constraint
	// can be applied.  New scraper-produced rows will always carry a deterministic
	// UUID from models.DeterministicID; the random ones here are just placeholders
	// for legacy staging rows that pre-date this column.
	if err := db.Exec(`
		UPDATE ` + tbl + `
		SET    stg_source_id = gen_random_uuid()::text
		WHERE  stg_source_id IS NULL
	`).Error; err != nil {
		return err
	}

	// Step 3: add NOT NULL constraint now that every row has a value.
	if err := db.Exec(`ALTER TABLE ` + tbl + ` ALTER COLUMN stg_source_id SET NOT NULL`).Error; err != nil {
		return err
	}

	// Step 4: create the unique index that ON CONFLICT (stg_source_id) requires.
	if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS "idx_rebates_staging_stg_source_id"
		ON ` + tbl + ` (stg_source_id)
	`).Error; err != nil {
		return err
	}

	return nil
}
