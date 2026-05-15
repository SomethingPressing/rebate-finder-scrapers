// Command promoter moves pending rows from scraper.rebates_staging into the
// live public.rebates table.
//
// # Modes
//
// Single-tenant (no active tenants in TENANTS_FILE):
//   Behaves exactly as before — connects to DATABASE_URL and promotes all
//   pending rows into that database's public.rebates table.
//
// Multi-tenant (active tenants found in TENANTS_FILE):
//   Connects to DATABASE_URL as the shared staging DB.
//   For each active tenant:
//     1. Reads pending staging rows tagged for that tenant.
//     2. Connects to the tenant's dedicated database (TENANT_<ID>_DB_URL).
//     3. Upserts into that database's public.rebates, public.zipcodes, etc.
//     4. Marks the tenant status rows as promoted in staging.
//   One tenant failure does not stop promotion for the remaining tenants.
//
// # Environment variables
//
//	DATABASE_URL             — PostgreSQL connection string for the staging DB (required)
//	TENANTS_FILE             — path to tenants.json (default: config/tenants.json)
//	SCRAPER_DB_SCHEMA        — schema that holds rebates_staging (default: scraper)
//	PROMOTER_SOURCE_PRIORITY — comma-separated scraper names, highest first
//
// # Usage
//
//	go run ./cmd/promoter
//	go run ./cmd/promoter --dry-run
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/config"
	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/logutil"
	"go.uber.org/zap"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "preview what would be promoted without writing anything")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "promoter: config load: %v\n", err)
		os.Exit(1)
	}

	logger := logutil.New(cfg.LogLevel, cfg.LogFormat)
	defer logger.Sync() //nolint:errcheck

	priority := cfg.PromoterSourcePriority
	if len(priority) == 0 {
		priority = db.DefaultSourcePriority
	}

	opts := db.PromoteOptions{
		DryRun:         *dryRun,
		SourcePriority: priority,
	}

	// ── Load tenants ──────────────────────────────────────────────────────────
	tenants, err := config.LoadTenants(cfg.TenantsFile)
	if err != nil {
		logger.Fatal("load tenants failed", zap.String("file", cfg.TenantsFile), zap.Error(err))
	}

	// ── Single-tenant mode ────────────────────────────────────────────────────
	// Also use single-tenant mode when every tenant points to the same DB as
	// DATABASE_URL — this is a single-database deployment that uses tenants.json
	// only for scraper configuration, not for separate per-tenant databases.
	// Single-tenant mode promotes ALL pending rows regardless of tenant tags,
	// which is the correct behaviour for a shared database.
	allSameDB := true
	for _, t := range tenants {
		if t.DBUrl() != cfg.DatabaseURL {
			allSameDB = false
			break
		}
	}
	if len(tenants) == 0 || allSameDB {
		runSingleTenant(cfg, opts, logger, *dryRun, priority)
		return
	}

	// ── Multi-tenant mode (separate per-tenant databases) ─────────────────────
	ids := make([]string, len(tenants))
	for i, t := range tenants {
		ids[i] = t.ID
	}
	logger.Info("promoter starting (multi-tenant)",
		zap.Strings("tenants", ids),
		zap.String("source_priority", strings.Join(priority, " > ")),
		zap.Bool("dry_run", *dryRun),
	)

	stagingDB, err := db.Connect(cfg.DatabaseURL, cfg.LogLevel, cfg.ScraperDBSchema)
	if err != nil {
		logger.Fatal("staging db connect failed", zap.Error(err))
	}
	defer stagingDB.Close() //nolint:errcheck

	if err := stagingDB.Ping(); err != nil {
		logger.Fatal("staging db ping failed", zap.Error(err))
	}

	if *dryRun {
		fmt.Println("[promoter] DRY RUN — no writes will be made.")
	}

	var totalPromoted, totalFailed int

	for _, tenant := range tenants {
		tenantURL := tenant.DBUrl()
		if tenantURL == "" {
			logger.Warn("tenant DB URL not set — skipping",
				zap.String("tenant", tenant.ID),
				zap.String("env_var", tenant.DBURLEnv),
			)
			continue
		}

		tenantDB, err := db.ConnectTenantDB(tenantURL, cfg.LogLevel)
		if err != nil {
			logger.Error("tenant db connect failed — skipping",
				zap.String("tenant", tenant.ID),
				zap.Error(err),
			)
			continue
		}

		start := time.Now()
		result, err := db.PromoteTenant(stagingDB, tenantDB, tenant.ID, opts)
		elapsed := time.Since(start)

		tenantDB.Close() //nolint:errcheck

		if err != nil {
			logger.Error("promote tenant failed",
				zap.String("tenant", tenant.ID),
				zap.Error(err),
				zap.Duration("elapsed", elapsed),
			)
			totalFailed++
			continue
		}

		if result.StagingRows == 0 {
			logger.Info("tenant up to date — nothing to promote",
				zap.String("tenant", tenant.ID),
			)
			continue
		}

		logger.Info("tenant promoted",
			zap.String("tenant", tenant.ID),
			zap.Int("staging_rows", result.StagingRows),
			zap.Int("programs", result.Programs),
			zap.Int("promoted", result.Promoted),
			zap.Int("merged", result.Merged),
			zap.Int("zips_written", result.ZipsWritten),
			zap.Duration("elapsed", elapsed),
		)
		totalPromoted += result.Promoted
	}

	fmt.Printf("\n[promoter] done — %d tenant(s), %d row(s) promoted, %d tenant(s) failed\n",
		len(tenants), totalPromoted, totalFailed)

	if totalFailed > 0 {
		os.Exit(1)
	}
}

// runSingleTenant runs the original single-DB promotion pipeline.
// Used when no active tenants are configured (backward compat with PM2 deployments).
func runSingleTenant(cfg *config.Config, opts db.PromoteOptions, logger *zap.Logger, dryRun bool, priority []string) {
	database, err := db.Connect(cfg.DatabaseURL, cfg.LogLevel, cfg.ScraperDBSchema)
	if err != nil {
		logger.Fatal("db connect failed", zap.Error(err))
	}
	defer database.Close() //nolint:errcheck

	if err := database.Ping(); err != nil {
		logger.Fatal("db ping failed", zap.Error(err))
	}

	logger.Info("promoter starting (single-tenant)",
		zap.String("schema", cfg.ScraperDBSchema),
		zap.String("source_priority", strings.Join(priority, " > ")),
		zap.Bool("dry_run", dryRun),
	)

	schema := cfg.ScraperDBSchema
	var exists int64
	if err := database.GORM().Raw(
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = 'rebates_staging'`,
		schema,
	).Scan(&exists).Error; err != nil || exists == 0 {
		logger.Warn("staging table not found — run the scraper first", zap.String("schema", schema))
		os.Exit(0)
	}

	if dryRun {
		fmt.Println("[promoter] DRY RUN — no writes will be made.")
	}

	start := time.Now()
	result, err := db.Promote(database, opts)
	elapsed := time.Since(start)

	if err != nil {
		logger.Fatal("promoter run failed", zap.Error(err), zap.Duration("elapsed", elapsed))
	}

	if dryRun {
		fmt.Printf("\n[promoter] DRY RUN complete — %d program(s) would be promoted (%d cross-source merges).\n",
			result.Programs, result.Merged)
		return
	}

	logger.Info("promoter complete",
		zap.Int("staging_rows", result.StagingRows),
		zap.Int("programs", result.Programs),
		zap.Int("promoted", result.Promoted),
		zap.Int("merged", result.Merged),
		zap.Int("failed", result.Failed),
		zap.Int("zips_written", result.ZipsWritten),
		zap.Int("links_written", result.LinksWritten),
		zap.Duration("elapsed", elapsed),
	)

	fmt.Printf("\n[promoter] done — %d staging row(s) → %d program(s)"+
		" (%d merge(s), %d zip(s), %d link(s), %d failed) in %s\n",
		result.Promoted, result.Programs,
		result.Merged, result.ZipsWritten, result.LinksWritten,
		result.Failed, elapsed.Round(time.Millisecond),
	)

	if result.Failed > 0 {
		os.Exit(1)
	}
}
