// cmd/pdf-scraper — Consumers Energy 2026 incentive extractor.
//
// Reads two PDF files (Incentive Catalog + Incentive Application), extracts
// the relevant pages for three target measures, logs the results, and upserts
// each rate tier into rebates_staging.  Nothing is written to the live rebates
// table — run cmd/promoter after review to promote pending rows.
//
// Staging upserts are always performed; they are idempotent (keyed on a
// deterministic UUID per rate ID, e.g. "HV101a").  Re-running refreshes data
// without creating duplicates.
//
// # File inputs (highest priority first)
//
//  1. CLI flags:  --catalog <path>  --application <path>
//  2. Env vars:   CONSUMERS_ENERGY_CATALOG_PDF / CONSUMERS_ENERGY_APPLICATION_PDF
//  3. .env file defaults (scraper-service/.env)
//
// # Usage
//
//	# Standard run — logs + stages to rebates_staging:
//	go run ./cmd/pdf-scraper
//	go run ./cmd/pdf-scraper --catalog /path/to/catalog.pdf --application /path/to/app.pdf
//
//	# Also save raw PDF text to pdf_scrape_raw (audit trail):
//	go run ./cmd/pdf-scraper --save-supabase
//
//	# Human-readable output:
//	LOG_FORMAT=console go run ./cmd/pdf-scraper
//	LOG_FORMAT=console go run ./cmd/pdf-scraper --save-supabase
//
// From the monorepo root:
//
//	pnpm scraper:pdf:consumers
//	pnpm scraper:pdf:consumers -- --save-supabase
//	LOG_FORMAT=console pnpm scraper:pdf:consumers -- \
//	  --catalog ~/Downloads/Consumers_Energy_Incentive_Catalog_1.pdf \
//	  --application ~/Downloads/"Incentive-Application (1).pdf"
package main

import (
	"context"
	"flag"
	"os"

	"github.com/incenva/rebate-scraper/config"
	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/logutil"
	"github.com/incenva/rebate-scraper/scrapers"
	"go.uber.org/zap"
)

func main() {
	// ── CLI flags ─────────────────────────────────────────────────────────────
	catalogFlag := flag.String("catalog", "", "path to the Incentive Catalog PDF")
	appFlag := flag.String("application", "", "path to the Incentive Application PDF")
	saveSupabaseFlag := flag.Bool("save-supabase", false, "also upsert raw PDF text extracts into pdf_scrape_raw (audit trail)")
	flag.Parse()

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, _ := config.Load() // best-effort

	logLevel, logFormat, dbURL, scraperVersion, scraperSchema := "info", "json", "", "1.0", "scraper"
	if cfg != nil {
		if cfg.LogLevel != "" {
			logLevel = cfg.LogLevel
		}
		if cfg.LogFormat != "" {
			logFormat = cfg.LogFormat
		}
		if cfg.DatabaseURL != "" {
			dbURL = cfg.DatabaseURL
		}
		if cfg.ScraperVersion != "" {
			scraperVersion = cfg.ScraperVersion
		}
		if cfg.ScraperDBSchema != "" {
			scraperSchema = cfg.ScraperDBSchema
		}
	}

	log := logutil.New(logLevel, logFormat)
	defer log.Sync() //nolint:errcheck

	// ── Override PDF paths via flags (flags beat env vars) ────────────────────
	if *catalogFlag != "" {
		os.Setenv("CONSUMERS_ENERGY_CATALOG_PDF", *catalogFlag)
	}
	if *appFlag != "" {
		os.Setenv("CONSUMERS_ENERGY_APPLICATION_PDF", *appFlag)
	}

	// ── Database ──────────────────────────────────────────────────────────────
	if dbURL == "" {
		log.Fatal("pdf-scraper: DATABASE_URL is required — set it in scraper-service/.env or the environment")
	}
	database, err := db.Connect(dbURL, logLevel, scraperSchema)
	if err != nil {
		log.Fatal("pdf-scraper: db connect failed", zap.Error(err))
	}
	defer database.Close() //nolint:errcheck

	if err := database.Ping(); err != nil {
		log.Fatal("pdf-scraper: db ping failed", zap.Error(err))
	}
	log.Info("pdf-scraper: database connected")

	// ── Run ───────────────────────────────────────────────────────────────────
	ctx := context.Background()

	opts := scrapers.PDFScrapeOpts{
		SaveSupabase:        *saveSupabaseFlag,
		DB:             database,
		ScraperVersion: scraperVersion,
	}

	if err := scrapers.ScrapeConsumersEnergyPDFs(ctx, log, opts); err != nil {
		log.Error("pdf-scraper: fatal", zap.Error(err))
		os.Exit(1)
	}
}
