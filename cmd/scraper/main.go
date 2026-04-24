// Command scraper fetches incentive data from DSIRE USA, Rewiring America, and
// Energy Star, then stages it in the rebates_staging PostgreSQL table.
//
// It never writes directly to the live rebates table.  Run the companion
// `cmd/promoter` after inspection to move approved rows into production.
//
// Usage:
//
//	# Run all scrapers once and exit
//	RUN_ONCE=true ./scraper
//
//	# Run only one specific scraper once and exit
//	RUN_ONCE=true SOURCE=dsireusa ./scraper
//	RUN_ONCE=true ./scraper --source energy_star
//
//	# Run one scraper on a schedule
//	SOURCE=rewiring_america ./scraper
//
//	# Scheduled (all scrapers, default: every 6 hours)
//	./scraper
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/incenva/rebate-scraper/config"
	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/logutil"
	"github.com/incenva/rebate-scraper/internal/zipdata"
	"github.com/incenva/rebate-scraper/models"
	"github.com/incenva/rebate-scraper/scrapers"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

func main() {
	// ── CLI flags ─────────────────────────────────────────────────────────────
	// --source can also be set via the SOURCE env var (env takes precedence).
	sourceFlag := flag.String("source", "", "run only this scraper (dsireusa | rewiring_america | energy_star)")
	flag.Parse()

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		panic("failed to load config: " + err.Error())
	}

	// Env var SOURCE wins over --source flag; fall back to flag if env not set.
	source := cfg.Source
	if source == "" {
		source = *sourceFlag
	}
	source = strings.TrimSpace(strings.ToLower(source))

	// ── Logger ────────────────────────────────────────────────────────────────
	logger := logutil.New(cfg.LogLevel, cfg.LogFormat)
	defer logger.Sync() //nolint:errcheck
	logger.Info("scraper service starting",
		zap.String("log_level", cfg.LogLevel),
		zap.String("log_format", cfg.LogFormat),
		zap.Bool("run_once", cfg.RunOnce),
		zap.String("source_filter", source),
	)

	// ── Database (GORM) ───────────────────────────────────────────────────────
	database, err := db.Connect(cfg.DatabaseURL, cfg.LogLevel)
	if err != nil {
		logger.Fatal("db connect failed", zap.Error(err))
	}
	defer database.Close() //nolint:errcheck

	if err := database.Ping(); err != nil {
		logger.Fatal("db ping failed", zap.Error(err))
	}
	logger.Info("database connected and staging table migrated")

	// ── ZIP data (state → ZIPs lookup) ───────────────────────────────────────
	// Load once, shared across all scrapers that need ZIP coverage per state.
	// Non-fatal — scrapers still run without it; ZipCodes field will be empty.
	stateZIPs, zipErr := zipdata.LoadPath(cfg.ZipCSVPath)
	if zipErr != nil {
		logger.Warn("uszips.csv not loaded — ZipCodes field will be empty",
			zap.Error(zipErr),
		)
	} else {
		logger.Info("uszips.csv loaded",
			zap.Int("states", len(stateZIPs)),
		)
	}

	// ── Registry ──────────────────────────────────────────────────────────────
	reg := scrapers.NewRegistry()

	reg.Register(&scrapers.DSIREScraper{
		BaseURL:        cfg.DSIREBaseURL,
		ScraperVersion: cfg.ScraperVersion,
		PageDelay:      cfg.PageDelay,
		StateZIPs:      stateZIPs,
		Logger:         logger,
	})

	reg.Register(&scrapers.RewiringAmericaScraper{
		BaseURL:        cfg.RewiringAmericaBaseURL,
		APIKey:         cfg.RewiringAmericaAPIKey,
		ScraperVersion: cfg.ScraperVersion,
		Logger:         logger,
	})

	reg.Register(&scrapers.EnergyStarScraper{
		BaseURL:        cfg.EnergyStarAPIBaseURL,
		PageDelay:      cfg.PageDelay,
		MaxConcurrency: cfg.MaxConcurrency,
		ScraperVersion: cfg.ScraperVersion,
		StateZIPs:      stateZIPs,
		Logger:         logger,
	})

	// ── Validate --source if provided ─────────────────────────────────────────
	if source != "" {
		if reg.Get(source) == nil {
			logger.Fatal("unknown scraper name",
				zap.String("source", source),
				zap.Strings("available", reg.Names()),
			)
		}
		logger.Info("single-source mode", zap.String("source", source))
	}

	// ── Core run function ─────────────────────────────────────────────────────
	runScrapers := func() {
		runStarted := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()

		var items []models.Incentive

		if source != "" {
			// Single scraper
			s := reg.Get(source)
			t0 := time.Now()
			logger.Info("scraper starting", zap.String("source", s.Name()))
			result, err := s.Scrape(ctx)
			if err != nil {
				logger.Error("scraper failed",
					zap.String("source", s.Name()),
					zap.Error(err),
					zap.Duration("elapsed", time.Since(t0)),
				)
				return
			}
			logger.Info("scraper finished",
				zap.String("source", s.Name()),
				zap.Int("count", len(result)),
				zap.Duration("elapsed", time.Since(t0)),
			)
			items = append(items, result...)
		} else {
			// All scrapers
			logger.Info("scrape run starting (all sources)")
			items = scrapers.RunAll(ctx, reg, logger)
		}

		logger.Info("scrape run finished",
			zap.Int("total_items", len(items)),
			zap.Duration("scrape_elapsed", time.Since(runStarted)),
		)

		if len(items) == 0 {
			logger.Warn("no items scraped — staging table unchanged")
			return
		}

		dbStarted := time.Now()
		// Scraped rows always go to rebates_staging only — never directly to rebates.
		upsertResult, err := db.UpsertToStaging(database, items)
		if err != nil {
			logger.Error("staging upsert failed", zap.Error(err))
			return
		}

		pending, _ := db.PendingCount(database)
		logger.Info("staging upsert complete",
			zap.String("table", "rebates_staging"),
			zap.Int("upserted", upsertResult.Upserted),
			zap.Int64("pending_total", pending),
			zap.Duration("db_elapsed", time.Since(dbStarted)),
			zap.Duration("total_elapsed", time.Since(runStarted)),
		)
	}

	// ── One-shot mode ─────────────────────────────────────────────────────────
	if cfg.RunOnce {
		runScrapers()
		logger.Info("RUN_ONCE=true — exiting after single run")
		os.Exit(0)
	}

	// ── Scheduled mode ────────────────────────────────────────────────────────
	c := cron.New(cron.WithLogger(zapCronLogger{logger}))

	if _, err := c.AddFunc(cfg.ScraperInterval, runScrapers); err != nil {
		logger.Fatal("invalid SCRAPER_INTERVAL",
			zap.String("interval", cfg.ScraperInterval),
			zap.Error(err),
		)
	}

	c.Start()
	if source != "" {
		logger.Info("scraper scheduled (single source)",
			zap.String("source", source),
			zap.String("interval", cfg.ScraperInterval),
		)
	} else {
		logger.Info("scraper scheduled (all sources)",
			zap.String("interval", cfg.ScraperInterval),
		)
	}

	// Run immediately on startup so we don't wait for the first cron tick.
	go runScrapers()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutdown signal received — stopping cron")
	ctx := c.Stop()
	<-ctx.Done()
	logger.Info("cron stopped cleanly")
}

// zapCronLogger adapts *zap.Logger to the robfig/cron logger interface.
type zapCronLogger struct{ z *zap.Logger }

func (l zapCronLogger) Info(msg string, keysAndValues ...interface{}) {
	l.z.Sugar().Infow(msg, keysAndValues...)
}
func (l zapCronLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	l.z.Sugar().Errorw(msg, append(keysAndValues, "error", err)...)
}

// printHelp is called when an invalid --source is given.
func printHelp(reg *scrapers.Registry) {
	fmt.Fprintf(os.Stderr, "Available scrapers: %s\n", strings.Join(reg.Names(), ", "))
}
