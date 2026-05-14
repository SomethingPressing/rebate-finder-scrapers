// Command scraper fetches incentive data from DSIRE USA, Rewiring America, and
// Energy Star, then stages it in the rebates_staging PostgreSQL table.
//
// It never writes directly to the live rebates table.  Run the companion
// `cmd/promoter` after inspection to move approved rows into production.
//
// Multi-tenant mode (when TENANTS_FILE is set and has active tenants):
//   - Scrapers run only for the union of tenant sources.
//   - Each incentive is tagged with the IDs of tenants whose location filters match.
//   - The promoter routes tagged rows to each tenant's dedicated database.
//
// Single-tenant mode (no TENANTS_FILE, or no active tenants):
//   - Behaves exactly as before: all scrapers run, all rows go to DATABASE_URL.
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
	sourceFlag := flag.String("source", "", "run only this scraper (dsireusa | rewiring_america | energy_star | con_edison | pnm | xcel_energy | srp | peninsula_clean_energy)")
	debugFlag  := flag.Bool("debug", false, "enable verbose per-item debug output (sets log level to debug)")
	flag.Parse()

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		panic("failed to load config: " + err.Error())
	}

	source := cfg.Source
	if source == "" {
		source = *sourceFlag
	}
	source = strings.TrimSpace(strings.ToLower(source))

	// --debug / DEBUG=true both force log level to debug.
	if *debugFlag || cfg.Debug {
		cfg.LogLevel = "debug"
		cfg.LogFormat = "console" // console format is far more readable for debug sessions
	}

	// ── Logger ────────────────────────────────────────────────────────────────
	logger := logutil.New(cfg.LogLevel, cfg.LogFormat)
	defer logger.Sync() //nolint:errcheck

	// ── Tenants ───────────────────────────────────────────────────────────────
	tenants, err := config.LoadTenants(cfg.TenantsFile)
	if err != nil {
		logger.Fatal("failed to load tenants", zap.String("file", cfg.TenantsFile), zap.Error(err))
	}
	multiTenant := len(tenants) > 0
	if multiTenant {
		ids := make([]string, len(tenants))
		for i, t := range tenants {
			ids[i] = t.ID
		}
		logger.Info("multi-tenant mode", zap.Strings("tenants", ids))
	} else {
		logger.Info("single-tenant mode")
	}

	proxyActive := cfg.ProxyURL != ""
	logger.Info("scraper service starting",
		zap.String("log_level", cfg.LogLevel),
		zap.Bool("run_once", cfg.RunOnce),
		zap.String("source_filter", source),
		zap.Bool("proxy_active", proxyActive),
		zap.Bool("multi_tenant", multiTenant),
	)

	// ── Database (staging DB) ─────────────────────────────────────────────────
	database, err := db.Connect(cfg.DatabaseURL, cfg.LogLevel, cfg.ScraperDBSchema)
	if err != nil {
		logger.Fatal("db connect failed", zap.Error(err))
	}
	defer database.Close() //nolint:errcheck

	if err := database.Ping(); err != nil {
		logger.Fatal("db ping failed", zap.Error(err))
	}
	logger.Info("database connected and staging table migrated")

	// ── ZIP data ──────────────────────────────────────────────────────────────
	stateZIPs, zipErr := zipdata.LoadPath(cfg.ZipCSVPath)
	if zipErr != nil {
		logger.Warn("uszips.csv not loaded — ZipCodes field will be empty", zap.Error(zipErr))
	} else {
		logger.Info("uszips.csv loaded", zap.Int("states", len(stateZIPs)))
	}

	// ── Scraper registry ──────────────────────────────────────────────────────
	reg := scrapers.NewRegistry()

	reg.Register(&scrapers.DSIREScraper{
		BaseURL: cfg.DSIREBaseURL, ScraperVersion: cfg.ScraperVersion,
		PageDelay: cfg.PageDelay, StateZIPs: stateZIPs, Logger: logger,
	})
	reg.Register(&scrapers.RewiringAmericaScraper{
		BaseURL: cfg.RewiringAmericaBaseURL, APIKey: cfg.RewiringAmericaAPIKey,
		ScraperVersion: cfg.ScraperVersion, StateZIPs: stateZIPs,
		Concurrency: cfg.RewiringAmericaConcurrency, Logger: logger,
	})
	reg.Register(&scrapers.EnergyStarScraper{
		BaseURL: cfg.EnergyStarAPIBaseURL, PageDelay: cfg.PageDelay,
		MaxConcurrency: cfg.MaxConcurrency, ScraperVersion: cfg.ScraperVersion,
		StateZIPs: stateZIPs, Logger: logger,
	})
	reg.Register(&scrapers.ConEdisonScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL})
	reg.Register(&scrapers.PNMScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL})
	reg.Register(&scrapers.XcelEnergyScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL})
	reg.Register(&scrapers.SRPScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL})
	reg.Register(&scrapers.PeninsulaCleanEnergyScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL})

	// ── Validate --source ─────────────────────────────────────────────────────
	if source != "" {
		if reg.Get(source) == nil {
			fmt.Fprintf(os.Stderr, "Unknown scraper %q. Available: %s\n", source, strings.Join(reg.Names(), ", "))
			os.Exit(1)
		}
		logger.Info("single-source mode", zap.String("source", source))
	}

	// ── Determine which scrapers to run ───────────────────────────────────────
	// In multi-tenant mode: run the union of sources across all tenants.
	// --source flag overrides and restricts to one scraper for all tenants.
	var activeScrapers []scrapers.Scraper
	if source != "" {
		activeScrapers = []scrapers.Scraper{reg.Get(source)}
	} else if multiTenant {
		if allowed := config.ActiveSources(tenants); allowed != nil {
			for _, name := range allowed {
				if s := reg.Get(name); s != nil {
					activeScrapers = append(activeScrapers, s)
				}
			}
			names := make([]string, len(activeScrapers))
			for i, s := range activeScrapers {
				names[i] = s.Name()
			}
			logger.Info("tenant-filtered scrapers", zap.Strings("sources", names))
		} else {
			activeScrapers = reg.All()
		}
	} else {
		activeScrapers = reg.All()
	}

	// ── Core run function ─────────────────────────────────────────────────────
	runScrapers := func() {
		runStarted := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()

		var totalUpserted int

		// flush is called immediately after each scraper finishes so rows are
		// persisted without waiting for the full run to complete.
		flush := func(source string, items []models.Incentive) {
			// Enforce max_incentives_per_source as a hard fetch limit:
			// find the smallest non-zero limit across all tenants and truncate
			// the items slice before staging. This keeps the staging table small
			// and makes test runs with a low limit finish quickly.
			if multiTenant {
				limit := 0
				for _, t := range tenants {
					if t.MaxIncentivesPerSource > 0 && (limit == 0 || t.MaxIncentivesPerSource < limit) {
						limit = t.MaxIncentivesPerSource
					}
				}
				if limit > 0 && len(items) > limit {
					logger.Debug("fetch limit applied",
						zap.String("source", source),
						zap.Int("fetched", len(items)),
						zap.Int("limit", limit),
					)
					items = items[:limit]
				}
			}

			// Tag incentives with matching tenant IDs.
			if multiTenant {
				// tenantCount tracks how many items each tenant has been tagged
				// for in this source batch, used to enforce max_incentives_per_source.
				tenantCount := make(map[string]int)
				tagged := 0
				for i := range items {
					for _, t := range tenants {
						if t.MaxIncentivesPerSource > 0 && tenantCount[t.ID] >= t.MaxIncentivesPerSource {
							continue
						}
						if t.MatchesIncentive(items[i].State, &items[i].UtilityCompany, items[i].ServiceTerritory, items[i].AvailableNationwide, items[i].ZipCodes) {
							items[i].TenantIDs = append(items[i].TenantIDs, t.ID)
							tenantCount[t.ID]++
						}
					}
					if len(items[i].TenantIDs) > 0 {
						tagged++
					}
				}
				logger.Info("tenant tagging complete",
					zap.String("source", source),
					zap.Int("items", len(items)),
					zap.Int("tagged", tagged),
				)
			}

			// Write to staging immediately.
			dbStarted := time.Now()
			result, err := db.UpsertToStaging(database, items)
			if err != nil {
				logger.Error("staging upsert failed",
					zap.String("source", source),
					zap.Error(err),
				)
				return
			}
			totalUpserted += result.Upserted
			logger.Info("staging upsert complete",
				zap.String("source", source),
				zap.Int("upserted", result.Upserted),
				zap.Duration("db_elapsed", time.Since(dbStarted)),
			)
		}

		scrapers.RunListFlush(ctx, activeScrapers, logger, flush)

		pending, _ := db.PendingCount(database)
		logger.Info("scrape run finished",
			zap.Int("total_upserted", totalUpserted),
			zap.Int64("pending_total", pending),
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
	logger.Info("scraper scheduled", zap.String("interval", cfg.ScraperInterval))

	go runScrapers()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutdown signal received — stopping cron")
	ctx := c.Stop()
	<-ctx.Done()
	logger.Info("cron stopped cleanly")
}

type zapCronLogger struct{ z *zap.Logger }

func (l zapCronLogger) Info(msg string, keysAndValues ...interface{}) {
	l.z.Sugar().Infow(msg, keysAndValues...)
}
func (l zapCronLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	l.z.Sugar().Errorw(msg, append(keysAndValues, "error", err)...)
}
