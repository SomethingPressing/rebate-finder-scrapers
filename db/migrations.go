package db

import "gorm.io/gorm"

// runPreMigrations executes hand-written SQL migrations that GORM AutoMigrate
// cannot handle safely on its own — typically adding NOT NULL columns to tables
// that already contain rows.
//
// Each migration is idempotent: it checks whether the change is needed before
// applying it, so re-running Connect never fails on an already-migrated schema.
func runPreMigrations(db *gorm.DB) error {
	if err := migrateStagingSourceID(db); err != nil {
		return err
	}
	if err := migrateLegacySourceIDNullable(db); err != nil {
		return err
	}
	return nil
}

// migrateLegacySourceIDNullable makes the old source_id column (pre-stg_ rename)
// nullable so that GORM inserts — which only populate stg_source_id — don't fail.
// This is idempotent: dropping NOT NULL on a nullable column is a no-op.
func migrateLegacySourceIDNullable(db *gorm.DB) error {
	return db.Exec(`
		ALTER TABLE rebates_staging ALTER COLUMN source_id DROP NOT NULL
	`).Error
}

// migrateStagingSourceID adds the stg_source_id column to rebates_staging when
// it doesn't yet exist.
//
// AutoMigrate would emit `ALTER TABLE ... ADD "stg_source_id" text NOT NULL`
// which fails if the table already has rows (existing rows would be NULL).
// This migration instead:
//   1. Adds the column as nullable.
//   2. Backfills existing rows with gen_random_uuid() so no row is NULL.
//   3. Adds the NOT NULL constraint.
//
// After this runs, AutoMigrate sees the column and skips it.
func migrateStagingSourceID(db *gorm.DB) error {
	// Check whether the column already exists.
	var count int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM   information_schema.columns
		WHERE  table_schema = CURRENT_SCHEMA()
		AND    table_name   = 'rebates_staging'
		AND    column_name  = 'stg_source_id'
	`).Scan(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		// Column exists — ensure the unique index on stg_source_id is present.
		// Use a distinct name (idx_rebates_staging_stg_source_id) so it doesn't
		// collide with idx_rebates_staging_source_id which belongs to a different
		// legacy column named source_id.
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS "idx_rebates_staging_stg_source_id"
			ON rebates_staging (stg_source_id)
		`).Error
	}

	// Step 1: add as nullable so existing rows are not immediately rejected.
	if err := db.Exec(`
		ALTER TABLE rebates_staging
		ADD COLUMN stg_source_id text
	`).Error; err != nil {
		return err
	}

	// Step 2: backfill NULL rows with a random UUID so the NOT NULL constraint
	// can be applied.  New scraper-produced rows will always carry a deterministic
	// UUID from models.DeterministicID; the random ones here are just placeholders
	// for legacy staging rows that pre-date this column.
	if err := db.Exec(`
		UPDATE rebates_staging
		SET    stg_source_id = gen_random_uuid()::text
		WHERE  stg_source_id IS NULL
	`).Error; err != nil {
		return err
	}

	// Step 3: add NOT NULL constraint now that every row has a value.
	if err := db.Exec(`
		ALTER TABLE rebates_staging
		ALTER COLUMN stg_source_id SET NOT NULL
	`).Error; err != nil {
		return err
	}

	// Step 4: create the unique index that ON CONFLICT (stg_source_id) requires.
	if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS "idx_rebates_staging_stg_source_id"
		ON rebates_staging (stg_source_id)
	`).Error; err != nil {
		return err
	}

	return nil
}
