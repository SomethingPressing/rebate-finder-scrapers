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
	sourceFlag         := flag.String("source", "", "run only this scraper (dsireusa | rewiring_america | energy_star | con_edison | pnm | xcel_energy | srp | peninsula_clean_energy)")
	debugFlag          := flag.Bool("debug", false, "enable verbose per-item debug output (sets log level to debug)")
	forceURLUpdateFlag := flag.Bool("force-url-update", false, "overwrite program_url and application_url on ALL matching staging rows regardless of promotion status (also set via FORCE_URL_UPDATE=true)")
	forceRefreshFlag   := flag.Bool("force-refresh", false, "re-scrape and reset promotion status to pending so the promoter re-pushes fresh data to live (also set via FORCE_REFRESH=true)")
	limitFlag          := flag.Int("limit", 0, "cap the number of programs fetched per source (0 = no limit); useful for quick smoke tests")
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

	// --force-url-update / FORCE_URL_UPDATE=true both enable forced URL refresh.
	if *forceURLUpdateFlag {
		cfg.ForceURLUpdate = true
	}
	// --force-refresh / FORCE_REFRESH=true resets promotion status after upsert.
	if *forceRefreshFlag {
		cfg.ForceRefresh = true
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
		zap.Bool("force_url_update", cfg.ForceURLUpdate),
		zap.Bool("force_refresh", cfg.ForceRefresh),
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
	// Compute the effective per-source fetch limit.
	// Priority: --limit flag > tenant config > no limit.
	// --force-refresh bypasses the tenant config but still respects --limit
	// (so you can test a refresh with --limit 5 without staging thousands of rows).
	effectiveLimit := 0
	if *limitFlag > 0 {
		effectiveLimit = *limitFlag
	} else if !cfg.ForceRefresh {
		for _, t := range tenants {
			if t.MaxIncentivesPerSource > 0 && (effectiveLimit == 0 || t.MaxIncentivesPerSource < effectiveLimit) {
				effectiveLimit = t.MaxIncentivesPerSource
			}
		}
	}

	if effectiveLimit > 0 {
		logger.Info("fetch limit active", zap.Int("limit_per_source", effectiveLimit))
	}

	reg := scrapers.NewRegistry()

	reg.Register(&scrapers.DSIREScraper{
		BaseURL: cfg.DSIREBaseURL, ScraperVersion: cfg.ScraperVersion,
		PageDelay: cfg.PageDelay, StateZIPs: stateZIPs, Logger: logger,
		Limit: effectiveLimit,
	})
	reg.Register(&scrapers.RewiringAmericaScraper{
		BaseURL: cfg.RewiringAmericaBaseURL, APIKey: cfg.RewiringAmericaAPIKey,
		ScraperVersion: cfg.ScraperVersion, StateZIPs: stateZIPs,
		Concurrency: cfg.RewiringAmericaConcurrency, Logger: logger,
		Limit: effectiveLimit,
	})
	reg.Register(&scrapers.EnergyStarScraper{
		BaseURL: cfg.EnergyStarAPIBaseURL, PageDelay: cfg.PageDelay,
		MaxConcurrency: cfg.MaxConcurrency, ScraperVersion: cfg.ScraperVersion,
		StateZIPs: stateZIPs, Logger: logger,
		Limit: effectiveLimit,
	})
	reg.Register(&scrapers.ConEdisonScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit})
	reg.Register(&scrapers.PNMScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit})
	reg.Register(&scrapers.XcelEnergyScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit})
	reg.Register(&scrapers.SRPScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit})
	reg.Register(&scrapers.PeninsulaCleanEnergyScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit})

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
	// canMarkStale is true when --force-refresh is active AND no --limit was
	// set, meaning the scraper fetched the COMPLETE set from the source.
	// With a partial fetch (--limit N), we can't know which programs are gone.
	canMarkStale := cfg.ForceRefresh && effectiveLimit == 0

	runScrapers := func() {
		runStarted := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()

		var totalUpserted int

		// Pre-register every source so that reset + stale detection runs
		// even for scrapers that return 0 programs (blocked, no pages found, etc).
		seenIDsBySource := make(map[string]map[string]struct{})
		if canMarkStale {
			for _, s := range activeScrapers {
				seenIDsBySource[s.Name()] = make(map[string]struct{})
			}
		}

		// flush is called immediately after each scraper finishes so rows are
		// persisted without waiting for the full run to complete.
		flush := func(source string, items []models.Incentive) {
			// Enforce max_incentives_per_source unless --force-refresh is set.
			// A refresh must stage all programs — limits are for regular scrape runs.
			if multiTenant && !cfg.ForceRefresh {
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
						// Bypass per-tenant limit on force-refresh so all programs
						// receive tenant tags and can be promoted.
						if !cfg.ForceRefresh && t.MaxIncentivesPerSource > 0 && tenantCount[t.ID] >= t.MaxIncentivesPerSource {
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
			// When force-refresh is active, UpsertToStaging atomically resets
			// stg_promotion_status to 'pending' inside the ON CONFLICT update —
			// no separate SQL call needed.
			dbStarted := time.Now()
			result, err := db.UpsertToStaging(database, items, cfg.ForceURLUpdate, cfg.ForceRefresh)
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
				zap.Bool("reset_to_pending", cfg.ForceRefresh),
				zap.Duration("db_elapsed", time.Since(dbStarted)),
			)

			// Track seen IDs for stale detection (full force-refresh only).
			if canMarkStale {
				if _, ok := seenIDsBySource[source]; !ok {
					seenIDsBySource[source] = make(map[string]struct{})
				}
				for _, item := range items {
					seenIDsBySource[source][item.ID] = struct{}{}
				}
			}
		}

		if cfg.ForceRefresh {
				runRehydrate(ctx, activeScrapers, database, logger, flush)
			} else {
				scrapers.RunListFlush(ctx, activeScrapers, logger, flush)
			}

		// Post-run: for sources that produced 0 items (scraper failed or returned
		// nothing), explicitly reset their promoted rows to pending so they are
		// re-promoted with existing data on the next promoter run.
		if cfg.ForceRefresh {
			for source, seenMap := range seenIDsBySource {
				if len(seenMap) == 0 {
					// Scraper ran but found nothing — reset ALL promoted rows for
					// this source to pending so they stay available for promotion.
					if err := db.ResetToPending(database, nil, source); err != nil {
						logger.Warn("force-refresh: reset promoted rows failed for zero-output scraper",
							zap.String("source", source),
							zap.Error(err),
						)
					} else {
						logger.Info("force-refresh: reset promoted rows for zero-output scraper",
							zap.String("source", source),
						)
					}
				}
			}
		}

		// After a full force-refresh, mark promoted rows that were NOT seen
		// in this scrape as stale — they've been removed from the upstream source.
		if canMarkStale {
			for source, seenMap := range seenIDsBySource {
				if len(seenMap) == 0 {
					continue // zero-output scrapers handled above; don't mark all as stale
				}
				seenIDs := make([]string, 0, len(seenMap))
				for id := range seenMap {
					seenIDs = append(seenIDs, id)
				}
				n, err := db.MarkStale(database, source, seenIDs)
				if err != nil {
					logger.Error("mark stale failed",
						zap.String("source", source),
						zap.Error(err),
					)
				} else if n > 0 {
					logger.Info("stale programs detected",
						zap.String("source", source),
						zap.Int64("stale_count", n),
					)
				}
			}
		}

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

// runRehydrate runs each scraper's RehydrateStream (if implemented), falling
// back to the normal Scrape path for scrapers that don't support it.
// This replaces the old "re-discover from scratch" force-refresh behaviour.
func runRehydrate(
	ctx context.Context,
	active []scrapers.Scraper,
	database *db.DB,
	logger *zap.Logger,
	flush func(source string, items []models.Incentive),
) {
	for _, s := range active {
		rh, ok := s.(scrapers.Rehydrater)
		if !ok {
			// Scraper doesn't implement Rehydrater — fall back to normal scrape.
			logger.Info("rehydrate: scraper has no RehydrateStream, using Scrape",
				zap.String("source", s.Name()))
			items, err := s.Scrape(ctx)
			if err != nil {
				logger.Error("rehydrate: scrape fallback failed",
					zap.String("source", s.Name()), zap.Error(err))
				continue
			}
			flush(s.Name(), items)
			continue
		}

		// Fetch existing staging records for this source.
		dbRecords, err := db.FetchStagingRecords(database, s.Name())
		if err != nil {
			logger.Error("rehydrate: fetch staging records failed",
				zap.String("source", s.Name()), zap.Error(err))
			continue
		}
		if len(dbRecords) == 0 {
			logger.Info("rehydrate: no staging records — skipping",
				zap.String("source", s.Name()))
			continue
		}

		// Convert db.StagingRecord → scrapers.RehydrateRecord.
		records := make([]scrapers.RehydrateRecord, len(dbRecords))
		for i, r := range dbRecords {
			rec := scrapers.RehydrateRecord{SourceID: r.SourceID}
			if r.State != nil {
				rec.State = *r.State
			}
			if r.ProgramURL != nil {
				rec.ProgramURL = *r.ProgramURL
			}
			if r.SourceURL != nil {
				rec.SourceURL = *r.SourceURL
			}
			records[i] = rec
		}

		logger.Info("rehydrate: starting",
			zap.String("source", s.Name()),
			zap.Int("staging_records", len(records)),
		)

		err = rh.RehydrateStream(ctx, records, func(items []models.Incentive) {
			flush(s.Name(), items)
		})
		if err != nil {
			logger.Error("rehydrate: RehydrateStream failed",
				zap.String("source", s.Name()), zap.Error(err))
		}
	}
}

type zapCronLogger struct{ z *zap.Logger }

func (l zapCronLogger) Info(msg string, keysAndValues ...interface{}) {
	l.z.Sugar().Infow(msg, keysAndValues...)
}
func (l zapCronLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	l.z.Sugar().Errorw(msg, append(keysAndValues, "error", err)...)
}
