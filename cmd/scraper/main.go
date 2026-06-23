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
	"github.com/incenva/rebate-scraper/internal/categoryinfer"
	"github.com/incenva/rebate-scraper/internal/jobsync"
	"github.com/incenva/rebate-scraper/internal/segmentinfer"
	"github.com/incenva/rebate-scraper/internal/llm"
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
	refreshAgeFlag     := flag.Int("refresh-age", 7, "with --force-refresh: only re-scrape programs not updated within this many days (0 = re-scrape all regardless of age)")
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

	// refreshAge is the minimum age (in days) a program must have before it is
	// re-scraped during --force-refresh. 0 means re-scrape everything.
	refreshAge := time.Duration(*refreshAgeFlag) * 24 * time.Hour

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

	// ── Clean up any stalled runs from a previous crash / kill ───────────────
	if err := database.ResetStalledRuns(); err != nil {
		logger.Warn("failed to reset stalled runs", zap.Error(err))
	} else {
		logger.Info("stalled runs cleared")
	}

	// ── DB-driven tenant config override ─────────────────────────────────────
	// Try loading scraper source config from the admin portal DB. When rows
	// exist they override tenants.json; if the table doesn't exist yet (admin
	// app not set up) we silently fall back to the file-based config.
	if dbTenants, dbErr := config.LoadTenantsFromDB(database); dbErr != nil {
		logger.Warn("failed to load tenant config from DB, using file", zap.Error(dbErr))
	} else if dbTenants != nil {
		// dbTenants is non-nil: DB config table exists and should be respected.
		// Even if empty (all sources disabled), override tenants.json so we
		// don't accidentally run all scrapers.
		tenants = dbTenants
		multiTenant = true
		logger.Info("loaded scraper config from DB", zap.Int("active_sources", len(tenants)))
	}

	// ── Job sync client (Ingestion Monitor) ──────────────────────────────────
	// Prefer env vars; fall back to first active tenant's app_url / sync_secret.
	appURL, syncSecret := cfg.AppURL, cfg.SyncSecret
	if (appURL == "" || syncSecret == "") && len(tenants) > 0 {
		if appURL == "" {
			appURL = tenants[0].AppURL
		}
		if syncSecret == "" {
			syncSecret = tenants[0].SyncSecret
		}
	}
	jobs := jobsync.New(appURL, syncSecret)

	// ── ZIP data ──────────────────────────────────────────────────────────────
	stateZIPs, zipErr := zipdata.LoadPath(cfg.ZipCSVPath)
	if zipErr != nil {
		logger.Warn("uszips.csv not loaded — ZipCodes field will be empty", zap.Error(zipErr))
	} else {
		logger.Info("uszips.csv loaded", zap.Int("states", len(stateZIPs)))
	}

	// ── Smart category + segment inferrers (optional — requires OPENAI_API_KEY) ─
	var catInferrer *categoryinfer.CategoryInferrer
	var segInferrer *segmentinfer.SegmentInferrer
	if cfg.OpenAIKey != "" {
		llmClient := llm.NewClient(cfg.OpenAIKey)
		catInferrer = categoryinfer.New(llmClient, 0)
		segInferrer = segmentinfer.New(llmClient, 0)
		logger.Info("smart category + segment inferrers enabled (embedding + GPT-4o mini)")
	} else {
		logger.Info("smart category + segment inferrers disabled — set OPENAI_API_KEY to enable")
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
		StateZIPs: stateZIPs, Logger: logger, Limit: effectiveLimit,
		CategoryInferrer: catInferrer,
	})
	reg.Register(&scrapers.ConEdisonScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit, CategoryInferrer: catInferrer, SegmentInferrer: segInferrer})
	reg.Register(&scrapers.PNMScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit, CategoryInferrer: catInferrer, SegmentInferrer: segInferrer})
	reg.Register(&scrapers.XcelEnergyScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit, CategoryInferrer: catInferrer, SegmentInferrer: segInferrer})
	reg.Register(&scrapers.SRPScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit, CategoryInferrer: catInferrer, SegmentInferrer: segInferrer})
	reg.Register(&scrapers.PeninsulaCleanEnergyScraper{ScraperVersion: cfg.ScraperVersion, Logger: logger, ProxyURL: cfg.ProxyURL, Limit: effectiveLimit, CategoryInferrer: catInferrer, SegmentInferrer: segInferrer})

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
		// Shadow activeScrapers with a schedule-filtered subset so the rest of the
		// function works without changes. In multi-tenant mode we re-read the DB to
		// get up-to-date schedule + last_run_at values before deciding what to run.
		activeScrapers := activeScrapers

		// Free any sources whose last run stalled (no heartbeat for 5+ min).
		if multiTenant {
			stalledCutoff := time.Now().UTC().Add(-5 * time.Minute)
			if freed, err := database.ResetStalledByHeartbeat(stalledCutoff); err != nil {
				logger.Warn("stall check failed", zap.Error(err))
			} else if len(freed) > 0 {
				logger.Warn("freed stalled sources", zap.Strings("sources", freed))
			}
		}

		if multiTenant {
			if freshRows, err := database.LoadActiveSourceConfigs(); err == nil && freshRows != nil {
				scheduleMap := make(map[string]db.ScraperSourceConfigRow, len(freshRows))
				for _, r := range freshRows {
					scheduleMap[r.Source] = r
				}
				var due []scrapers.Scraper
				for _, s := range activeScrapers {
					if row, ok := scheduleMap[s.Name()]; !ok || isSourceDue(row) {
						due = append(due, s)
					}
				}
				if len(due) == 0 {
					logger.Info("schedule check: no sources due on this tick — skipping run")
					return
				}
				if len(due) < len(activeScrapers) {
					names := make([]string, len(due))
					for i, s := range due {
						names[i] = s.Name()
					}
					logger.Info("schedule check: running only due sources", zap.Strings("sources", names))
				}
				activeScrapers = due
			}
		}

		runStarted := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()

		var totalUpserted int
		// Per-source upserted counts so MarkRunFinish records accurate program counts.
		sourceCounts := make(map[string]int)

		// Build source -> clientID map from tenants (for DB run logging).
		sourceClientID := make(map[string]string)
		for _, t := range tenants {
			for _, src := range t.Sources {
				sourceClientID[src] = t.ID
			}
		}

		// Record schedule-triggered run start in DB for each active source.
		runLogIDs := make(map[string]string)
		for _, s := range activeScrapers {
			clientIDForSource := sourceClientID[s.Name()]
			if id, err := database.MarkRunStart(clientIDForSource, s.Name(), "schedule"); err == nil {
				runLogIDs[s.Name()] = id
			}
		}

		// Register a scraper_run job per source in the Ingestion Monitor.
		scraperJobIDs := make(map[string]string, len(activeScrapers))
		now := time.Now()
		for _, s := range activeScrapers {
			jobID, err := jobs.CreateJob(ctx, jobsync.CreateJobRequest{
				Type:      "scraper_run",
				Source:    s.Name(),
				StartedAt: jobsync.TimePtr(now),
			})
			if err != nil {
				logger.Warn("jobsync: create scraper_run failed",
					zap.String("source", s.Name()), zap.Error(err))
			} else {
				scraperJobIDs[s.Name()] = jobID
			}
		}

		// Start a heartbeat goroutine for each run so stall detection works.
		heartbeatStops := make(map[string]func(), len(runLogIDs))
		for src, runLogID := range runLogIDs {
			if runLogID != "" {
				heartbeatStops[src] = startHeartbeat(ctx, database, runLogID, logger)
			}
		}
		// stopAllHeartbeats is a safety-net defer; individual sources stop their
		// own heartbeat in onSourceDone so the "running" state clears immediately.
		defer func() {
			for _, stop := range heartbeatStops {
				stop()
			}
		}()

		// onSourceDone is called by RunListFlush after each scraper finishes.
		// It stops that source's heartbeat and marks the run complete so the
		// UI updates immediately instead of waiting for the full batch to end.
		onSourceDone := func(src string, scrapeErr error, elapsed time.Duration) {
			if stopHB, ok := heartbeatStops[src]; ok {
				stopHB()
				delete(heartbeatStops, src) // prevent double-stop in the defer
			}
			runLogID := runLogIDs[src]
			if runLogID != "" {
				clientID := sourceClientID[src]
				status := "success"
				if scrapeErr != nil {
					status = "error"
				}
				_ = database.MarkRunFinish(clientID, src, runLogID, status, sourceCounts[src], int(elapsed.Seconds()), scrapeErr)
			}

			// Update Ingestion Monitor job.
			if jobID := scraperJobIDs[src]; jobID != "" {
				upd := jobsync.UpdateJobRequest{
					RowsProcessed: jobsync.IntPtr(sourceCounts[src]),
					CompletedAt:   jobsync.TimePtr(time.Now()),
				}
				if scrapeErr != nil {
					upd.Status = "failed"
					upd.ErrorCount = jobsync.IntPtr(1)
					upd.ErrorMessage = jobsync.StrPtr(scrapeErr.Error())
				} else {
					upd.Status = "completed"
				}
				if err := jobs.UpdateJob(ctx, jobID, upd); err != nil {
					logger.Warn("jobsync: update scraper_run failed",
						zap.String("source", src), zap.Error(err))
				}
			}
		}

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
			sourceCounts[source] += result.Upserted
			logger.Info("staging upsert complete",
				zap.String("source", source),
				zap.Int("upserted", result.Upserted),
				zap.Int("skipped_no_url", result.Skipped),
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
				runRehydrate(ctx, activeScrapers, database, logger, flush, refreshAge)
			} else {
				scrapers.RunListFlush(ctx, activeScrapers, logger, flush, onSourceDone)
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

		// Register next scheduled run per source in the Ingestion Monitor.
		// Only when running in scheduled (non-RUN_ONCE) mode so the Upcoming Jobs
		// table shows when each source will next be scraped.
		if !cfg.RunOnce && multiTenant {
			if freshRows, err := database.LoadActiveSourceConfigs(); err == nil {
				scheduleIntervals := map[string]time.Duration{
					"every_6h":  6 * time.Hour,
					"every_12h": 12 * time.Hour,
					"daily":     24 * time.Hour,
					"weekly":    7 * 24 * time.Hour,
				}
				for _, s := range activeScrapers {
					for _, row := range freshRows {
						if row.Source != s.Name() {
							continue
						}
						d, ok := scheduleIntervals[row.Schedule]
						if !ok {
							break
						}
						nextRun := time.Now().Add(d)
						if _, err := jobs.CreateJob(ctx, jobsync.CreateJobRequest{
							Type:        "scraper_run",
							Source:      s.Name(),
							ScheduledAt: jobsync.TimePtr(nextRun),
						}); err != nil {
							logger.Warn("jobsync: register next scheduled run failed",
								zap.String("source", s.Name()), zap.Error(err))
						}
						break
					}
				}
			}
		}
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

	// ── On-demand job polling ─────────────────────────────────────────────────
	// stageItems is a minimal flush used by on-demand jobs: tags incentives and
	// writes them to staging, without the stale-tracking bookkeeping that the
	// scheduled runScrapers closure maintains internally.
	stageItems := func(source string, items []models.Incentive) {
		if multiTenant {
			for i := range items {
				for _, t := range tenants {
					if t.MatchesIncentive(items[i].State, &items[i].UtilityCompany, items[i].ServiceTerritory, items[i].AvailableNationwide, items[i].ZipCodes) {
						items[i].TenantIDs = append(items[i].TenantIDs, t.ID)
					}
				}
			}
		}
		if _, err := db.UpsertToStaging(database, items, cfg.ForceURLUpdate, false); err != nil {
			logger.Error("on-demand staging upsert failed",
				zap.String("source", source),
				zap.Error(err),
			)
		}
	}

	// Poll scraper_jobs every 30 s for manual run requests queued by the admin UI.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				job, err := database.ClaimJob()
				if err != nil {
					logger.Warn("job poll failed", zap.Error(err))
					continue
				}
				if job == nil {
					continue
				}
				logger.Info("claimed on-demand job", zap.String("source", job.Source), zap.String("client_id", job.ClientID))
				go func(src, clientID string) {
					runLogID, startErr := database.MarkRunStart(clientID, src, "manual")
					if startErr != nil {
						logger.Warn("MarkRunStart failed", zap.Error(startErr))
					}
					jobCtx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
					defer cancel()
					if runLogID != "" {
						stopHB := startHeartbeat(jobCtx, database, runLogID, logger)
						defer stopHB()
					}
					start := time.Now()
					scraper := reg.Get(src)
					if scraper == nil {
						logger.Warn("unknown source in job", zap.String("source", src))
						_ = database.MarkRunFinish(clientID, src, runLogID, "error", 0, 0, fmt.Errorf("unknown source: %s", src))
						return
					}
					items, scrapeErr := scraper.Scrape(jobCtx)
					elapsed := int(time.Since(start).Seconds())
					if scrapeErr != nil {
						logger.Error("on-demand scrape failed",
							zap.String("source", src),
							zap.Error(scrapeErr),
						)
						_ = database.MarkRunFinish(clientID, src, runLogID, "error", 0, elapsed, scrapeErr)
						return
					}
					stageItems(src, items)
					_ = database.MarkRunFinish(clientID, src, runLogID, "success", len(items), elapsed, nil)
					logger.Info("on-demand scrape complete",
						zap.String("source", src),
						zap.Int("programs", len(items)),
						zap.Int("elapsed_s", elapsed),
					)
				}(job.Source, job.ClientID)
			case <-quit:
				return
			}
		}
	}()

	<-quit

	logger.Info("shutdown signal received — stopping cron")
	ctx := c.Stop()
	<-ctx.Done()
	logger.Info("cron stopped cleanly")
}

// runRehydrate runs each scraper's RehydrateStream (if implemented), falling
// back to the normal Scrape path for scrapers that don't support it.
// refreshAge filters out records updated more recently than the given duration
// (0 means re-scrape everything regardless of age).
func runRehydrate(
	ctx context.Context,
	active []scrapers.Scraper,
	database *db.DB,
	logger *zap.Logger,
	flush func(source string, items []models.Incentive),
	refreshAge time.Duration,
) {
	staleAfter := time.Now().UTC()
	if refreshAge > 0 {
		staleAfter = time.Now().UTC().Add(-refreshAge)
	}

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

		// Filter to only records not updated within refreshAge.
		// When refreshAge is 0, staleAfter == now so all records are included.
		var stale []db.StagingRecord
		for _, r := range dbRecords {
			if r.UpdatedAt.Before(staleAfter) {
				stale = append(stale, r)
			}
		}
		skipped := len(dbRecords) - len(stale)
		if skipped > 0 {
			logger.Info("rehydrate: skipping recently updated records",
				zap.String("source", s.Name()),
				zap.Int("stale", len(stale)),
				zap.Int("skipped_recent", skipped),
				zap.Duration("refresh_age", refreshAge),
			)
		}
		if len(stale) == 0 {
			logger.Info("rehydrate: all records are recent — nothing to refresh",
				zap.String("source", s.Name()))
			continue
		}

		// Convert db.StagingRecord → scrapers.RehydrateRecord.
		records := make([]scrapers.RehydrateRecord, len(stale))
		for i, r := range stale {
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

// isSourceDue returns true when a source should run on the current tick based
// on its configured schedule and when it last ran.
// Unknown or empty schedule values are treated as "run every tick".
// startHeartbeat launches a goroutine that updates last_heartbeat_at every 30 s
// for the given run log. Call the returned stop function when the run finishes.
func startHeartbeat(ctx context.Context, database *db.DB, runLogID string, logger *zap.Logger) func() {
	hbCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := database.UpdateHeartbeat(runLogID); err != nil {
					logger.Warn("heartbeat update failed",
						zap.String("run_log_id", runLogID),
						zap.Error(err),
					)
				}
			case <-hbCtx.Done():
				return
			}
		}
	}()
	return cancel
}

func isSourceDue(row db.ScraperSourceConfigRow) bool {
	if row.Schedule == "manual_only" {
		return false
	}
	// Always retry sources that failed — don't wait out the full schedule interval
	// after an error. last_run_at is set at run START so a killed/crashed run
	// would otherwise block the source for the entire schedule period.
	if row.LastRunStatus == "error" {
		return true
	}
	if row.LastRunAt == nil {
		return true // never run → always due
	}
	intervals := map[string]time.Duration{
		"every_6h":  6 * time.Hour,
		"every_12h": 12 * time.Hour,
		"daily":     24 * time.Hour,
		"weekly":    7 * 24 * time.Hour,
	}
	d, ok := intervals[row.Schedule]
	if !ok {
		return true // unknown schedule → run every tick
	}
	return time.Since(*row.LastRunAt) >= d
}

type zapCronLogger struct{ z *zap.Logger }

func (l zapCronLogger) Info(msg string, keysAndValues ...interface{}) {
	l.z.Sugar().Infow(msg, keysAndValues...)
}
func (l zapCronLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	l.z.Sugar().Errorw(msg, append(keysAndValues, "error", err)...)
}
