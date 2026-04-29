package db

import (
	"fmt"

	"github.com/incenva/rebate-scraper/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB wraps a GORM *gorm.DB so callers import db.DB rather than gorm types.
type DB struct {
	gorm   *gorm.DB
	schema string // PostgreSQL schema that owns the Go-side tables (e.g. "scraper")
}

// Schema returns the PostgreSQL schema name used for Go-owned tables.
func (d *DB) Schema() string { return d.schema }

// Connect opens a GORM connection pool against the given PostgreSQL DSN,
// runs AutoMigrate to create / update the scraper tables, and returns a
// ready-to-use *DB.
//
// scraperSchema is the PostgreSQL schema that owns all Go-side tables
// (rebates_staging, pdf_scrape_raw).  Pass cfg.ScraperDBSchema here; the
// value comes from SCRAPER_DB_SCHEMA in .env (default: "scraper").
//
// logLevel controls SQL query logging:
//   - "silent" → no SQL output
//   - "warn"   → slow queries only
//   - "info"   → all queries (noisy; good for debugging)
//
// Any other value (or empty string) defaults to "warn".
func Connect(dsn, logLevel, scraperSchema string) (*DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("db: DATABASE_URL is required")
	}
	if scraperSchema == "" {
		scraperSchema = "scraper"
	}
	dsn = sanitizePostgresDSN(dsn)

	// Set the package-level variable so TableName() methods on all models
	// return the correct schema-qualified table name.
	models.ScraperSchema = scraperSchema

	lvl := logger.Warn
	switch logLevel {
	case "silent":
		lvl = logger.Silent
	case "info":
		lvl = logger.Info
	}

	gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(lvl),
	})
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	// Ensure the scraper schema exists before AutoMigrate tries to create tables
	// inside it.  This is idempotent (IF NOT EXISTS) so safe to run every time.
	if err := gormDB.Exec("CREATE SCHEMA IF NOT EXISTS " + scraperSchema).Error; err != nil {
		return nil, fmt.Errorf("db: create scraper schema %q: %w", scraperSchema, err)
	}

	// Auto-migrate first — creates the tables if they don't exist yet.
	// Must run before pre-migrations because pre-migrations ALTER existing tables
	// and will fail with "relation does not exist" on a fresh database.
	if err := gormDB.AutoMigrate(&models.StagedRebate{}, &models.PDFScrapeRaw{}); err != nil {
		return nil, fmt.Errorf("db: automigrate: %w", err)
	}

	// Run hand-written migrations after AutoMigrate has ensured the tables exist.
	// These handle changes AutoMigrate cannot do safely (e.g. adding a NOT NULL
	// column to a table that already has rows).
	if err := runPreMigrations(gormDB); err != nil {
		return nil, fmt.Errorf("db: pre-migrations: %w", err)
	}

	return &DB{gorm: gormDB, schema: scraperSchema}, nil
}

// GORM returns the underlying *gorm.DB for callers that need raw access.
func (d *DB) GORM() *gorm.DB { return d.gorm }

// Close releases the underlying database connection pool.
func (d *DB) Close() error {
	sqlDB, err := d.gorm.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping checks that the database is reachable.
func (d *DB) Ping() error {
	sqlDB, err := d.gorm.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}
