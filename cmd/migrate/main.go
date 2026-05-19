// cmd/migrate — runs named data migrations that require coordinated access to
// both the scraper staging database and all tenant live databases.
//
// Each migration is a named, idempotent operation that cleans stale data,
// re-scrapes the affected source, and promotes the fresh rows.
//
// Usage:
//
//	go run ./cmd/migrate <migration-name> [flags]
//	go run ./cmd/migrate 001_energy_star_name_fix
//	go run ./cmd/migrate 001_energy_star_name_fix --dry-run
//
// Available migrations:
//
//	001_energy_star_name_fix  — Removes the fabricated " Rebate" suffix from all
//	                        Energy Star program names.  Deletes Energy Star rows
//	                        from every tenant live DB and from staging, then
//	                        re-scrapes and re-promotes so names match what the
//	                        Energy Star website displays.
//
// Env vars (same as cmd/scraper):
//
//	DATABASE_URL      — staging PostgreSQL DSN
//	SCRAPER_DB_SCHEMA — staging schema (default: scraper)
//	TENANTS_FILE      — path to tenants.json (default: config/tenants.json)
//	OPENAI_API_KEY    — optional; enables smart category inference
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/incenva/rebate-scraper/config"
	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/categoryinfer"
	"github.com/incenva/rebate-scraper/internal/llm"
	"github.com/incenva/rebate-scraper/internal/logutil"
	"github.com/incenva/rebate-scraper/internal/zipdata"
	"github.com/incenva/rebate-scraper/scrapers"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	dry := flag.Bool("dry-run", false, "print what would be done without making changes")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: migrate <migration-name> [--dry-run]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Available migrations:")
		fmt.Fprintln(os.Stderr, "  001_energy_star_name_fix  — clean + re-scrape + re-promote Energy Star with corrected program names")
		os.Exit(1)
	}

	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	logger := logutil.New(cfg.LogLevel, cfg.LogFormat)
	defer logger.Sync() //nolint:errcheck

	tenants, err := config.LoadTenants(cfg.TenantsFile)
	if err != nil {
		log.Fatalf("load tenants: %v", err)
	}

	switch flag.Arg(0) {
	case "001_energy_star_name_fix":
		if err := runEnergyStarNameFix(cfg, tenants, logger, *dry); err != nil {
			log.Fatalf("migration failed: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown migration %q\n", flag.Arg(0))
		os.Exit(1)
	}
}

// runEnergyStarNameFix deletes all Energy Star rows from every tenant live DB
// and from the scraper staging DB, then re-scrapes and re-promotes so program
// names match the Energy Star website (no fabricated " Rebate" suffix).
func runEnergyStarNameFix(cfg *config.Config, tenants []config.TenantConfig, logger *zap.Logger, dry bool) error {
	const source = "energy_star"

	// ── Connect to staging DB ─────────────────────────────────────────────────
	stagingDB, err := db.Connect(cfg.DatabaseURL, cfg.LogLevel, cfg.ScraperDBSchema)
	if err != nil {
		return fmt.Errorf("connect staging db: %w", err)
	}
	defer stagingDB.Close() //nolint:errcheck

	// ── Connect to every tenant live DB ──────────────────────────────────────
	type liveConn struct {
		label string
		d     *db.DB
		dsn   string
	}
	var liveDBs []liveConn

	if len(tenants) == 0 {
		// Single-tenant: live DB uses the same DSN, public schema.
		// Use ConnectTenantDB — no scraper schema setup or pre-migrations.
		d, err := db.ConnectTenantDB(cfg.DatabaseURL, cfg.LogLevel)
		if err != nil {
			return fmt.Errorf("connect live db (single-tenant): %w", err)
		}
		defer d.Close() //nolint:errcheck
		liveDBs = append(liveDBs, liveConn{"default", d, cfg.DatabaseURL})
	} else {
		for _, t := range tenants {
			dsn := t.DBUrl()
			if dsn == "" {
				logger.Warn("tenant has no DB URL — skipping", zap.String("tenant", t.ID))
				continue
			}
			// ConnectTenantDB skips scraper pre-migrations — tenant DBs only
			// have public.rebates and related tables, not rebates_staging.
			d, err := db.ConnectTenantDB(dsn, cfg.LogLevel)
			if err != nil {
				return fmt.Errorf("connect live db for tenant %q: %w", t.ID, err)
			}
			defer d.Close() //nolint:errcheck
			liveDBs = append(liveDBs, liveConn{t.ID, d, dsn})
			logger.Info("connected to tenant live DB", zap.String("tenant", t.ID))
		}
	}

	// ── Step 1: delete from every tenant live DB ──────────────────────────────
	for _, conn := range liveDBs {
		var n int64
		conn.d.GORM().Raw(`SELECT COUNT(*) FROM rebates WHERE source = ?`, source).Scan(&n)

		if dry {
			logger.Info("[dry-run] would delete from live DB",
				zap.String("db", conn.label), zap.Int64("rows", n))
			continue
		}

		res := conn.d.GORM().Exec(`DELETE FROM rebates WHERE source = ?`, source)
		if res.Error != nil {
			return fmt.Errorf("delete from live DB %q: %w", conn.label, res.Error)
		}
		logger.Info("deleted from live DB",
			zap.String("db", conn.label), zap.Int64("rows", res.RowsAffected))
	}

	// ── Step 2: delete from staging ───────────────────────────────────────────
	schema := cfg.ScraperDBSchema
	var stagingCount int64
	stagingDB.GORM().Raw(
		`SELECT COUNT(*) FROM `+schema+`.rebates_staging WHERE source = ?`, source,
	).Scan(&stagingCount)

	if dry {
		logger.Info("[dry-run] would delete from staging", zap.Int64("rows", stagingCount))
		logger.Info("[dry-run] done — no changes made")
		return nil
	}

	res := stagingDB.GORM().Exec(
		`DELETE FROM `+schema+`.rebates_staging WHERE source = ?`, source,
	)
	if res.Error != nil {
		return fmt.Errorf("delete from staging: %w", res.Error)
	}
	logger.Info("deleted from staging", zap.Int64("rows", res.RowsAffected))

	// ── Step 3: re-scrape Energy Star ─────────────────────────────────────────
	logger.Info("re-scraping energy_star …")

	stateZIPs, zipErr := zipdata.LoadPath(cfg.ZipCSVPath)
	if zipErr != nil {
		logger.Warn("uszips.csv not loaded — ZipCodes will be empty", zap.Error(zipErr))
	}

	var catInferrer *categoryinfer.CategoryInferrer
	if cfg.OpenAIKey != "" {
		catInferrer = categoryinfer.New(llm.NewClient(cfg.OpenAIKey), 0)
	}

	scraper := &scrapers.EnergyStarScraper{
		BaseURL:          cfg.EnergyStarAPIBaseURL,
		PageDelay:        cfg.PageDelay,
		MaxConcurrency:   cfg.MaxConcurrency,
		ScraperVersion:   cfg.ScraperVersion,
		StateZIPs:        stateZIPs,
		CategoryInferrer: catInferrer,
		Logger:           logger,
	}

	incentives, err := scraper.Scrape(context.Background())
	if err != nil {
		return fmt.Errorf("scrape: %w", err)
	}
	logger.Info("scrape complete", zap.Int("incentives", len(incentives)))

	// Tag each incentive with the tenant IDs whose location filter matches.
	// This populates rebate_tenant_status so PromoteTenant can find the rows.
	if len(tenants) > 0 {
		for i := range incentives {
			for _, t := range tenants {
				if t.MatchesIncentive(
					incentives[i].State,
					&incentives[i].UtilityCompany,
					incentives[i].ServiceTerritory,
					incentives[i].AvailableNationwide,
					incentives[i].ZipCodes,
				) {
					incentives[i].TenantIDs = append(incentives[i].TenantIDs, t.ID)
				}
			}
		}
	}

	upsertResult, err := db.UpsertToStaging(stagingDB, incentives, false)
	if err != nil {
		return fmt.Errorf("upsert to staging: %w", err)
	}
	logger.Info("upserted to staging", zap.Int("rows", upsertResult.Upserted))

	// ── Step 4: promote to every tenant live DB ───────────────────────────────
	logger.Info("promoting to live …")

	priority := cfg.PromoterSourcePriority
	opts := db.PromoteOptions{SourcePriority: priority}

	for _, conn := range liveDBs {
		var result *db.PromoteResult
		if len(tenants) == 0 {
			result, err = db.Promote(conn.d, opts)
		} else {
			// Find the matching tenant for source-filtering during promotion.
			var tenant config.TenantConfig
			for _, t := range tenants {
				if t.ID == conn.label {
					tenant = t
					break
				}
			}
			result, err = db.PromoteTenant(stagingDB, conn.d, tenant.ID, opts)
		}
		if err != nil {
			return fmt.Errorf("promote to %q: %w", conn.label, err)
		}
		logger.Info("promoted",
			zap.String("db", conn.label),
			zap.Int("staging_rows", result.StagingRows),
			zap.Int("promoted", result.Promoted),
			zap.Int("merged", result.Merged),
			zap.Int("failed", result.Failed),
		)
	}

	logger.Info("migration complete ✓")
	return nil
}
