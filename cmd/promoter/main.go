// Command promoter moves pending rows from scraper.rebates_staging into the
// live public.rebates table using GORM — no hand-written SQL for data writes.
//
// # Strategy
//
//  1. Fetch all pending rows from scraper.rebates_staging.
//  2. Group by stg_program_hash; when the same program appears in multiple
//     scrapers, the highest-priority source wins for scalar fields and arrays
//     are unioned across all sources.
//  3. Batch-upsert into public.rebates (ON CONFLICT program_hash).
//     On INSERT:  status = "draft", processed = false, is_featured = false.
//     On UPDATE:  data columns only — status is NEVER overwritten so that
//                 admin-approved records keep their approval.
//  4. Bulk-upsert public.zipcodes + public.rebate_zipcodes.
//  5. Mark promoted staging rows as stg_promotion_status = "promoted".
//
// # Environment variables
//
//	DATABASE_URL                 — PostgreSQL connection string (required)
//	SCRAPER_DB_SCHEMA            — schema that holds rebates_staging (default: scraper)
//	PROMOTER_SOURCE_PRIORITY     — comma-separated scraper names, highest first
//	                               (default: rewiring_america,dsireusa,energy_star)
//
// # Usage
//
//	# Promote all pending rows
//	go run ./cmd/promoter
//
//	# Dry-run: preview without writing
//	go run ./cmd/promoter --dry-run
//
//	# From the monorepo root
//	pnpm scraper:promote
//	pnpm scraper:promote:dry
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

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "promoter: config load: %v\n", err)
		os.Exit(1)
	}

	// ── Logger ────────────────────────────────────────────────────────────────
	logger := logutil.New(cfg.LogLevel, cfg.LogFormat)
	defer logger.Sync() //nolint:errcheck

	// ── Database ──────────────────────────────────────────────────────────────
	database, err := db.Connect(cfg.DatabaseURL, cfg.LogLevel, cfg.ScraperDBSchema)
	if err != nil {
		logger.Fatal("promoter: db connect failed", zap.Error(err))
	}
	defer database.Close() //nolint:errcheck

	if err := database.Ping(); err != nil {
		logger.Fatal("promoter: db ping failed", zap.Error(err))
	}

	// ── Source priority ───────────────────────────────────────────────────────
	priority := cfg.PromoterSourcePriority
	if len(priority) == 0 {
		priority = db.DefaultSourcePriority
	}

	logger.Info("promoter starting",
		zap.String("schema", cfg.ScraperDBSchema),
		zap.String("source_priority", strings.Join(priority, " > ")),
		zap.Bool("dry_run", *dryRun),
	)

	// ── Guard: check staging table exists ────────────────────────────────────
	schema := cfg.ScraperDBSchema
	var exists int64
	if err := database.GORM().Raw(
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = 'rebates_staging'`,
		schema,
	).Scan(&exists).Error; err != nil || exists == 0 {
		logger.Warn("promoter: staging table not found — run the scraper first",
			zap.String("schema", schema),
		)
		os.Exit(0)
	}

	// ── Run ───────────────────────────────────────────────────────────────────
	if *dryRun {
		fmt.Println("[promoter] DRY RUN — no writes will be made.")
	}

	start := time.Now()
	result, err := db.Promote(database, db.PromoteOptions{
		DryRun:         *dryRun,
		SourcePriority: priority,
	})
	elapsed := time.Since(start)

	if err != nil {
		logger.Fatal("promoter: run failed",
			zap.Error(err),
			zap.Duration("elapsed", elapsed),
		)
	}

	if *dryRun {
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
