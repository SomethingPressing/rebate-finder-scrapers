# Architecture

## Overview

`rebate-finder-scrapers` is a standalone Go service that fetches energy incentive programs from multiple public sources and writes them into a PostgreSQL staging table (`rebates_staging`). Scraped data is never written directly to the live application — it sits in staging for review before a separate promotion step moves it to the consumer-facing `rebates` table.

```
┌─────────────────────────────────────────────────────────────────┐
│                   rebate-finder-scrapers (this repo)            │
│                                                                 │
│  ┌──────────────┐  ┌──────────────────┐  ┌──────────────────┐  │
│  │ cmd/scraper  │  │ cmd/pdf-scraper  │  │   (future)       │  │
│  │              │  │                  │  │                  │  │
│  │ REST / HTML  │  │ PDF extraction   │  │   new sources    │  │
│  │ scrapers     │  │ Consumers Energy │  │                  │  │
│  └──────┬───────┘  └────────┬─────────┘  └────────┬─────────┘  │
│         │                   │                     │            │
│         └───────────────────┴─────────────────────┘            │
│                             │                                   │
│                    db.UpsertToStaging()                         │
│                             │                                   │
└─────────────────────────────┼───────────────────────────────────┘
                              │
                              ▼
                     ┌─────────────────┐
                     │ rebates_staging │  (PostgreSQL)
                     │                 │
                     │  stg_status     │
                     │  = "pending"    │
                     └────────┬────────┘
                              │
                     human review (optional)
                              │
                              ▼
              pnpm scraper:promote   (consumer app)
                              │
                              ▼
                     ┌─────────────────┐
                     │    rebates      │  (live table, consumer app)
                     └─────────────────┘
```

## Entry Points

| Binary | Purpose |
|--------|---------|
| `cmd/scraper` | REST/HTML scrapers: DSIRE USA, Rewiring America, Energy Star. Runs on a cron schedule or once. |
| `cmd/pdf-scraper` | PDF extraction: Consumers Energy 2026 incentive catalog. Run manually after each PDF update. |

## Key Packages

| Package | Responsibility |
|---------|---------------|
| `config/` | Load and validate environment variables |
| `db/` | Database connection, AutoMigrate, idempotent migrations, upsert helpers |
| `models/` | Domain types (`Incentive`, `StagedRebate`) and DB-specific adapters |
| `scrapers/` | Scraper interface, registry, and all concrete scraper implementations |
| `internal/logutil/` | Shared zap logger factory (JSON or console output) |

## Data Flow

```
Source (API / HTML / PDF)
         │
         ▼
  Scraper returns []models.Incentive
         │
         ▼
  models.FromIncentive() → []models.StagedRebate
         │
         ▼
  db.UpsertToStaging()
  ON CONFLICT (stg_source_id) DO UPDATE  ← refreshes data, preserves promotion state
         │
         ▼
  rebates_staging  (stg_promotion_status = 'pending')
```

## Key Design Decisions

### 1. Staging-only writes
Scrapers never touch the live `rebates` table. All writes go to `rebates_staging`. A bad scrape run can be fully rolled back by deleting staging rows. Admin-approved statuses in `rebates` are never overwritten.

### 2. Deterministic UUIDs
Each scraper produces a stable `stg_source_id` via UUID v5 keyed on a source-specific external identifier (e.g. DSIRE program ID, Rewiring America program+ZIP, measure key for PDFs). Re-scraping the same program always produces the same UUID — no duplicate rows accumulate.

### 3. Program hash deduplication
`stg_program_hash = SHA-256(normalize(program_name)|normalize(utility_company))`. Source is intentionally excluded so the same program scraped by multiple sources (e.g. DSIRE and a PDF) merges into a single live rebate row. The consumer app's promoter uses this hash as the upsert key.

### 4. Status preservation on re-scrape
`ON CONFLICT DO UPDATE` refreshes all data columns but explicitly excludes `stg_promotion_status`, `stg_promoted_at`, and `stg_rebate_id`. A re-scrape never resets a row that has already been promoted or skipped.

### 5. Modular scraper registry
All scrapers implement the `scrapers.Scraper` interface. The registry in `scrapers/base.go` holds all registered scrapers and supports running one or all sequentially. Adding a new source requires only implementing the interface and registering it in `cmd/scraper/main.go`.

### 6. Shared DATABASE_URL
The scraper and consumer app share the same PostgreSQL instance and `DATABASE_URL`. The DSN sanitizer in `db/dsn.go` strips Prisma-specific query parameters (e.g. `?schema=public`) so the GORM/pgx driver accepts them.

### 7. AutoMigrate + hand-written migrations
GORM's `AutoMigrate` creates tables and adds missing columns on startup. Hand-written migrations in `db/migrations.go` handle changes AutoMigrate can't safely do (e.g. adding a `NOT NULL` column to a table that already has rows). AutoMigrate always runs first so the table exists before the migrations try to alter it.
