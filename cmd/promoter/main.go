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
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/config"
	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/jobsync"
	"github.com/incenva/rebate-scraper/internal/logutil"
	"github.com/incenva/rebate-scraper/models"
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

	// Derive client identity from the first active tenant in tenants.json.
	// In single-tenant mode (all tenants share one DB) this stamps the clients
	// row and client_id on rebates without any extra env vars.
	// In multi-tenant mode the per-tenant loop below overrides opts.Client for
	// each tenant individually, so this default is never used.
	if len(tenants) > 0 {
		t := tenants[0]
		opts.Client = models.ClientRow{
			ID:          t.ID,
			Name:        t.Name,
			Region:      t.Region,
			UtilityType: t.UtilityType,
			IsDemo:      t.IsDemo,
		}
	}

	// ── Job sync client (Ingestion Monitor) ──────────────────────────────────
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
		// Pass the first tenant so runSingleTenant can trigger the sync using
		// the app_url and sync_secret from tenants.json.
		var firstTenant *config.TenantConfig
		if len(tenants) > 0 {
			firstTenant = &tenants[0]
		}
		runSingleTenant(cfg, opts, logger, *dryRun, priority, firstTenant, jobs)
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

		tenantOpts := opts
		tenantOpts.Client = models.ClientRow{
			ID:          tenant.ID,
			Name:        tenant.Name,
			Region:      tenant.Region,
			UtilityType: tenant.UtilityType,
			IsDemo:      tenant.IsDemo,
		}

		// Create per-source promoter_run jobs before promoting.
		ctx := context.Background()
		promoterJobIDs := createPromoterJobs(ctx, stagingDB, jobs, logger)

		start := time.Now()
		result, err := db.PromoteTenant(stagingDB, tenantDB, tenant.ID, tenantOpts)
		elapsed := time.Since(start)

		tenantDB.Close() //nolint:errcheck

		if err != nil {
			logger.Error("promote tenant failed",
				zap.String("tenant", tenant.ID),
				zap.Error(err),
				zap.Duration("elapsed", elapsed),
			)
			finishPromoterJobs(ctx, jobs, promoterJobIDs, 0, err, logger)
			totalFailed++
			continue
		}

		if result.StagingRows == 0 {
			logger.Info("tenant up to date — nothing to promote",
				zap.String("tenant", tenant.ID),
			)
			finishPromoterJobs(ctx, jobs, promoterJobIDs, 0, nil, logger)
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
		finishPromoterJobs(ctx, jobs, promoterJobIDs, result.Promoted, nil, logger)

		if !*dryRun && tenant.AppURL != "" && tenant.SyncSecret != "" {
			triggerTypesenseSync(tenant.AppURL, tenant.SyncSecret, logger)
		}
	}

	fmt.Printf("\n[promoter] done — %d tenant(s), %d row(s) promoted, %d tenant(s) failed\n",
		len(tenants), totalPromoted, totalFailed)

	if totalFailed > 0 {
		os.Exit(1)
	}
}

// runSingleTenant runs the original single-DB promotion pipeline.
// Used when no active tenants are configured, or when all tenants share DATABASE_URL.
// tenant is the first entry from tenants.json (nil when no tenants.json exists) and
// is used only to read app_url and sync_secret for the Typesense sync trigger.
func runSingleTenant(cfg *config.Config, opts db.PromoteOptions, logger *zap.Logger, dryRun bool, priority []string, tenant *config.TenantConfig, jobs *jobsync.Client) {
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

	// Create per-source promoter_run jobs before promoting.
	ctx := context.Background()
	promoterJobIDs := createPromoterJobs(ctx, database, jobs, logger)

	start := time.Now()
	result, err := db.Promote(database, opts)
	elapsed := time.Since(start)

	if err != nil {
		finishPromoterJobs(ctx, jobs, promoterJobIDs, 0, err, logger)
		logger.Fatal("promoter run failed", zap.Error(err), zap.Duration("elapsed", elapsed))
	}

	if dryRun {
		finishPromoterJobs(ctx, jobs, promoterJobIDs, result.Promoted, nil, logger)
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
		zap.Int("archived", result.Archived),
		zap.Int("zips_written", result.ZipsWritten),
		zap.Int("links_written", result.LinksWritten),
		zap.Duration("elapsed", elapsed),
	)

	fmt.Printf("\n[promoter] done — %d staging row(s) → %d program(s)"+
		" (%d merge(s), %d archived, %d zip(s), %d link(s), %d failed) in %s\n",
		result.Promoted, result.Programs,
		result.Merged, result.Archived, result.ZipsWritten, result.LinksWritten,
		result.Failed, elapsed.Round(time.Millisecond),
	)

	finishPromoterJobs(ctx, jobs, promoterJobIDs, result.Promoted, nil, logger)

	if !dryRun && tenant != nil && tenant.AppURL != "" && tenant.SyncSecret != "" {
		triggerTypesenseSync(tenant.AppURL, tenant.SyncSecret, logger)
	}

	if result.Failed > 0 {
		os.Exit(1)
	}
}

// createPromoterJobs queries pending staging rows grouped by source and creates
// one promoter_run job per source in the Ingestion Monitor.
// Returns a map of source → job ID for later updates.
func createPromoterJobs(ctx context.Context, database *db.DB, jobs *jobsync.Client, logger *zap.Logger) map[string]string {
	bySource, err := db.PendingBySource(database)
	if err != nil {
		logger.Warn("jobsync: pending-by-source query failed", zap.Error(err))
		return nil
	}

	jobIDs := make(map[string]string, len(bySource))
	now := time.Now()
	for source, count := range bySource {
		n := count
		jobID, err := jobs.CreateJob(ctx, jobsync.CreateJobRequest{
			Type:      "promoter_run",
			Source:    source,
			RowCount:  jobsync.IntPtr(n),
			StartedAt: jobsync.TimePtr(now),
		})
		if err != nil {
			logger.Warn("jobsync: create promoter_run failed",
				zap.String("source", source), zap.Error(err))
			continue
		}
		jobIDs[source] = jobID
	}
	return jobIDs
}

// finishPromoterJobs marks all source jobs as completed or failed.
// When promoted is 0 and err is nil, jobs are still marked completed (nothing to do).
func finishPromoterJobs(ctx context.Context, jobs *jobsync.Client, jobIDs map[string]string, promoted int, promoteErr error, logger *zap.Logger) {
	now := time.Now()
	for source, jobID := range jobIDs {
		upd := jobsync.UpdateJobRequest{
			CompletedAt: jobsync.TimePtr(now),
		}
		if promoteErr != nil {
			upd.Status = "failed"
			upd.ErrorCount = jobsync.IntPtr(1)
			upd.ErrorMessage = jobsync.StrPtr(promoteErr.Error())
		} else {
			upd.Status = "completed"
			upd.RowsProcessed = jobsync.IntPtr(promoted)
		}
		if err := jobs.UpdateJob(ctx, jobID, upd); err != nil {
			logger.Warn("jobsync: update promoter_run failed",
				zap.String("source", source), zap.Error(err))
		}
	}
}

// triggerTypesenseSync calls POST /api/typesense/sync on the Next.js app so
// the search index reflects newly promoted and archived rebates.
// Non-fatal — a sync failure is logged as a warning but never stops promotion.
func triggerTypesenseSync(appURL, secret string, logger *zap.Logger) {
	url := strings.TrimRight(appURL, "/") + "/api/typesense/sync"

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		logger.Warn("typesense sync trigger: build request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Warn("typesense sync trigger: request failed", zap.Error(err))
		return
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		logger.Warn("typesense sync trigger: unexpected status",
			zap.Int("status", resp.StatusCode), zap.String("url", url))
		return
	}

	logger.Info("typesense sync triggered", zap.String("url", url))
}
