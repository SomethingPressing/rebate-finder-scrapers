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
	gorm *gorm.DB
}

// Connect opens a GORM connection pool against the given PostgreSQL DSN,
// runs AutoMigrate to create / update the rebates_staging table, and returns
// a ready-to-use *DB.
//
// logLevel controls SQL query logging:
//   - "silent" → no SQL output
//   - "warn"   → slow queries only
//   - "info"   → all queries (noisy; good for debugging)
//
// Any other value (or empty string) defaults to "warn".
func Connect(dsn, logLevel string) (*DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("db: DATABASE_URL is required")
	}
	dsn = sanitizePostgresDSN(dsn)

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

	// Run hand-written migrations that AutoMigrate cannot handle (e.g. adding a
	// NOT NULL column to a table that already has rows).
	if err := runPreMigrations(gormDB); err != nil {
		return nil, fmt.Errorf("db: pre-migrations: %w", err)
	}

	// Auto-migrate creates or updates managed tables.
	// It is safe to run on every startup — GORM only adds missing columns.
	if err := gormDB.AutoMigrate(&models.StagedRebate{}, &models.PDFScrapeRaw{}); err != nil {
		return nil, fmt.Errorf("db: automigrate: %w", err)
	}

	return &DB{gorm: gormDB}, nil
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
