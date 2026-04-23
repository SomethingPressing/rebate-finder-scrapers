# Incenva Scraper Service

A standalone Go service that fetches incentive programs from multiple sources and stages
them in a PostgreSQL table for human review before they go live in the consumer application.

**Related:** [incenva-rebate-finder](https://github.com/smythos/incenva-rebate-finder) — the consumer app that promotes staged rows to the live catalog.

---

## Table of Contents

1. [Architecture](#architecture)
2. [Prerequisites](#prerequisites)
3. [Quick Start](#quick-start)
4. [Configuration](#configuration)
5. [Running the Scraper](#running-the-scraper)
6. [PDF Scrapers](#pdf-scrapers)
7. [Reviewing Staged Data](#reviewing-staged-data)
8. [Promoting Staged Data](#promoting-staged-data-consumer-app)
9. [Deployment](#deployment)
10. [Docker](#docker)
11. [Data Sources](#data-sources)
12. [Design Decisions](#design-decisions)
13. [Project Structure](#project-structure)

---

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  cmd/scraper  (Go — runs on schedule or one-shot)        │
│                                                          │
│  DSIREUSA ──┐                                            │
│  Rewiring   ├─► scrapers.RunAll ─► rebates_staging       │
│  EnergyStar ┘          (GORM upsert, stg_status=pending) │
│                                                          │
│  cmd/pdf-scraper  (Go — stages from local PDF files)     │
│  Consumers Energy ──► rebates_staging (stg_status=pending│
└──────────────────────────────────────────────────────────┘
                                │
                                │  review in psql / Prisma Studio
                                ▼
┌──────────────────────────────────────────────────────────┐
│  prisma/scripts/promote-staging.ts  (in consumer app)    │
│                                                          │
│  rebates_staging (stg_status=pending)                    │
│        │                                                 │
│        ├─ prisma.rebate.upsert()  ──► rebates (draft)    │
│        └─ prisma.$executeRaw      ──► stg_status=promoted│
└──────────────────────────────────────────────────────────┘
```

> **Note:** This repo is responsible only for **scraping and staging**.
> Promotion (moving staged rows to the live `rebates` table) is handled by
> `prisma/scripts/promote-staging.ts` in the consumer application.

### Why a staging table?

| Problem | Solution |
|---------|----------|
| Bad scrape run corrupts live data | Scraper writes to `rebates_staging` only |
| Re-scraping resets admin-approved status | Status column excluded from DO UPDATE |
| Want to preview before publish | Inspect staging rows before promoting |
| Easy rollback | `DELETE FROM rebates_staging WHERE stg_promotion_status = 'pending'` |

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.22+ | https://go.dev/dl |
| PostgreSQL | 14+ | Shared with the consumer application |
| Node.js | 18+ | Optional — only needed for `npm run …` helper scripts |

---

## Quick Start

```bash
git clone <repository-url>
cd incenva-scraper-service

# Copy and configure environment
cp .env.example .env
# Edit .env: set DATABASE_URL and REWIRING_AMERICA_API_KEY at minimum

# Install Go dependencies
go mod download

# Run all scrapers once
RUN_ONCE=true go run ./cmd/scraper
```

---

## Configuration

All configuration is read from environment variables. Copy `.env.example` to `.env` and
fill in your values. The service walks up the directory tree looking for a `go.mod + .env`
pair and loads whichever it finds first.

To use a specific file:
```bash
DOTENV_PATH=/etc/incenva/scraper.env go run ./cmd/scraper
```

### Full variable reference

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | ✅ | — | PostgreSQL DSN |
| `RUN_ONCE` | | `false` | `true` = run once and exit; `false` = cron mode |
| `SCRAPER_INTERVAL` | | `@every 6h` | robfig/cron schedule string |
| `SCRAPER_VERSION` | | `1.0` | Written to `scraper_version` on every upsert |
| `LOG_LEVEL` | | `info` | `debug` \| `info` \| `warn` \| `error` |
| `LOG_FORMAT` | | `json` | `json` \| `console` (human-readable) |
| `DSIREUSA_BASE_URL` | | _(see .env.example)_ | DSIRE API base URL |
| `REWIRING_AMERICA_API_KEY` | ✅ for RA | — | Rewiring America API key |
| `REWIRING_AMERICA_BASE_URL` | | _(see .env.example)_ | Rewiring America API base URL |
| `ENERGY_STAR_BASE_URL` | | _(see .env.example)_ | Energy Star page URL |
| `CONSUMERS_ENERGY_CATALOG_PDF` | | — | Path to Consumers Energy catalog PDF |
| `CONSUMERS_ENERGY_APPLICATION_PDF` | | — | Path to Consumers Energy application PDF |
| `SOURCE` | | — | Restrict to one scraper: `dsireusa` \| `rewiring_america` \| `energy_star` |
| `DOTENV_PATH` | | — | Absolute path to a specific `.env` file |

---

## Running the Scraper

### One-shot — all scrapers

```bash
RUN_ONCE=true go run ./cmd/scraper
```

### One-shot — single scraper

```bash
RUN_ONCE=true go run ./cmd/scraper --source dsireusa
RUN_ONCE=true go run ./cmd/scraper --source rewiring_america
RUN_ONCE=true go run ./cmd/scraper --source energy_star
```

Equivalent via `SOURCE` env var (preferred for Docker/CI):
```bash
RUN_ONCE=true SOURCE=dsireusa go run ./cmd/scraper
```

### Continuous (cron schedule)

```bash
# All scrapers — runs immediately, then every SCRAPER_INTERVAL
go run ./cmd/scraper

# Single scraper on schedule
SOURCE=dsireusa go run ./cmd/scraper
```

Stop with `Ctrl+C` — the process drains the current run before exiting.

### Build and run binary

```bash
go build -o bin/scraper ./cmd/scraper

RUN_ONCE=true ./bin/scraper
RUN_ONCE=true ./bin/scraper --source energy_star
./bin/scraper   # scheduled mode
```

### npm helper scripts (optional, requires Node 18+)

| Command | Equivalent go command |
|---------|----------------------|
| `npm run run` | `RUN_ONCE=true go run ./cmd/scraper` (all sources) |
| `npm run run:dsireusa` | `RUN_ONCE=true SOURCE=dsireusa go run …` |
| `npm run run:rewiring_america` | same for Rewiring America |
| `npm run run:energy_star` | same for Energy Star |
| `npm run pdf` | `go run ./cmd/pdf-scraper` |
| `npm run go:deps` | `go mod download` |
| `npm run go:tidy` | `go mod tidy` |
| `npm run go:verify` | `go mod verify` |

> For the long-running scheduled mode (`RUN_ONCE=false`), use **PM2** or **systemd** — see [Deployment](#deployment). These npm scripts are one-shot helpers for manual runs and development only.

---

## PDF Scrapers

PDF scrapers extract incentive data from **local PDF files** and stage them in `rebates_staging`.

### cmd/pdf-scraper — PDF incentive extractor

Runs against any supported PDF source. Currently supports **Consumers Energy 2026**
(3 measures, 2 PDFs).

**PDF file resolution (highest priority first):**

```bash
# 1. CLI flags — explicit paths
go run ./cmd/pdf-scraper \
  --catalog     /path/to/catalog.pdf \
  --application /path/to/application.pdf

# 2. Environment variables
CONSUMERS_ENERGY_CATALOG_PDF=/path/to/catalog.pdf \
CONSUMERS_ENERGY_APPLICATION_PDF=/path/to/app.pdf \
go run ./cmd/pdf-scraper

# 3. .env defaults
go run ./cmd/pdf-scraper
```

**Human-readable console output:**
```bash
LOG_FORMAT=console go run ./cmd/pdf-scraper
```

**Verbose (includes full PDF text extract):**
```bash
LOG_LEVEL=debug LOG_FORMAT=console go run ./cmd/pdf-scraper
```

**Via npm helper:**
```bash
npm run pdf

# With explicit paths
LOG_FORMAT=console npm run pdf -- \
  --catalog     ~/Downloads/Consumers_Energy_Incentive_Catalog.pdf \
  --application ~/Downloads/Incentive-Application.pdf
```

### Consumers Energy 2026 — target measures

| # | Measure | IDs | Catalog pages | Application pages |
|---|---------|-----|---------------|-------------------|
| 1 | Air Conditioning — Split & Unitary | HV101a–HV101j | p.50 | p.23 |
| 2 | Interior Linear LED Tube Retrofits | LT101–LT126, LT207–LT209 | p.13–14 | p.10–11 |
| 3 | Refrigeration Compressors — Discus/Scroll | RL101, RL102 | p.85 | p.36 |

PDF page note: catalog uses `PDF page = printed page + 2`; application uses `PDF page = printed page`.

### Adding a new PDF scraper

1. Read the PDFs and note exact PDF page numbers for each measure.
2. Add a new `ceIncentiveSpec` entry in `scrapers/consumers_energy.go` (or create a new file for a different utility) with `CatalogPages`, `AppPages`, and `IncentiveRates`.
3. Test: `LOG_FORMAT=console go run ./cmd/pdf-scraper` — verify all rates appear and PDF text is non-empty.
4. Use the consumer app's promoter to push staged rows to the live catalog.

---

## Reviewing Staged Data

After a scrape, data lands in `rebates_staging` with `stg_promotion_status = 'pending'`.

```sql
-- How many rows are waiting?
SELECT stg_promotion_status, COUNT(*) FROM rebates_staging GROUP BY 1;

-- Preview pending rows
SELECT stg_source_id, program_name, utility_company, state, incentive_format, incentive_amount
FROM rebates_staging
WHERE stg_promotion_status = 'pending'
LIMIT 20;

-- Skip a bad row (it will not be promoted)
UPDATE rebates_staging SET stg_promotion_status = 'skipped' WHERE stg_source_id = '...';

-- Reset a row back to pending (re-try after fixing an issue)
UPDATE rebates_staging
SET stg_promotion_status = 'pending', stg_promoted_at = NULL, stg_rebate_id = NULL
WHERE stg_source_id = '...';
```

### Staging column reference

| Column | Type | Meaning |
|--------|------|---------|
| `stg_source_id` | `text UNIQUE` | Deterministic UUID — stable upsert key |
| `stg_promotion_status` | `text` | `pending` → `promoted` or `skipped` |
| `stg_promoted_at` | `timestamptz` | Promotion timestamp — `NULL` while pending/skipped |
| `stg_rebate_id` | `text` | UUID of the live `rebates` row — `NULL` until promoted |

---

## Promoting Staged Data (consumer app)

Promotion is handled by **`prisma/scripts/promote-staging.ts`** in the consumer application.
Run these commands from the consumer app's root:

```bash
# Preview what would be promoted (writes nothing)
pnpm scraper:promote:dry

# Promote pending rows → live rebates table (status = draft)
pnpm scraper:promote

# Promote + push to Supabase rebate_source_raw
pnpm scraper:promote:supabase

# Preview Supabase push without writing
pnpm scraper:promote:supabase:dry
```

**Idempotency:** Re-running is safe. Each upsert is keyed on `source_id`; only `pending`
rows are processed. A second run is a no-op unless new scrapes added more rows.

---

## Deployment

### Option A — PM2 on a VPS (recommended for production)

```bash
# Install PM2 globally
npm install -g pm2

# Clone and configure
git clone <repository-url> incenva-scraper-service
cd incenva-scraper-service
cp .env.example .env
# Edit .env: DATABASE_URL, REWIRING_AMERICA_API_KEY, RUN_ONCE=false, SCRAPER_INTERVAL, etc.

# Install Go modules and build binary
go mod download
go build -o bin/scraper ./cmd/scraper

# Start as a persistent PM2 process (reads env from .env automatically)
pm2 start bin/scraper --name "Incenva Scraper" --interpreter none

pm2 save
pm2 startup   # copy and run the command it prints to enable auto-start on reboot
```

Check status and logs:
```bash
pm2 status
pm2 logs "Incenva Scraper"
```

Restart after update:
```bash
git pull
go build -o bin/scraper ./cmd/scraper
pm2 restart "Incenva Scraper"
```

### Option B — systemd service (Ubuntu/Debian)

Create `/etc/systemd/system/incenva-scraper.service`:

```ini
[Unit]
Description=Incenva Scraper Service
After=network.target postgresql.service
Wants=postgresql.service

[Service]
Type=simple
User=www-data
WorkingDirectory=/var/www/incenva-scraper-service
ExecStart=/var/www/incenva-scraper-service/bin/scraper
Restart=on-failure
RestartSec=10
EnvironmentFile=/var/www/incenva-scraper-service/.env

[Install]
WantedBy=multi-user.target
```

Build and enable:
```bash
go build -o bin/scraper ./cmd/scraper

sudo systemctl daemon-reload
sudo systemctl enable incenva-scraper
sudo systemctl start incenva-scraper
sudo systemctl status incenva-scraper
```

View live logs:
```bash
journalctl -u incenva-scraper -f
```

Restart after update:
```bash
go build -o bin/scraper ./cmd/scraper
sudo systemctl restart incenva-scraper
```

### Option C — cron job (one-shot periodic runs)

Set `RUN_ONCE=true` in `.env` and add a crontab entry:

```bash
crontab -e
```

```cron
# Run all scrapers every 6 hours
0 */6 * * * cd /var/www/incenva-scraper-service && ./bin/scraper >> /var/log/incenva-scraper.log 2>&1
```

---

## Docker

```bash
# Build
docker build --target scraper -t incenva-scraper .

# Run one-shot (all sources)
docker run --env-file .env -e RUN_ONCE=true incenva-scraper

# Run on schedule
docker run --env-file .env incenva-scraper

# Single source
docker run --env-file .env -e RUN_ONCE=true -e SOURCE=dsireusa incenva-scraper
```

### docker-compose example

```yaml
services:
  scraper:
    build:
      context: .
      target: scraper
    env_file: .env
    environment:
      RUN_ONCE: "false"
      SCRAPER_INTERVAL: "@every 6h"
    restart: unless-stopped
    depends_on:
      - db

  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: rebate_finder
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
```

> If the consumer app has its own `docker-compose.yml`, add the scraper service there
> and point both at the same database container via `DATABASE_URL`.

---

## Data Sources

| Source | Type | Key required |
|--------|------|-------------|
| [DSIRE USA](https://programs.dsireusa.org) | REST JSON API | No |
| [Rewiring America](https://www.rewiringamerica.org) | REST JSON API | Yes — free at rewiringamerica.org/api |
| [Energy Star](https://www.energystar.gov/about/federal_tax_credits) | HTML (Colly) | No |
| Consumers Energy | Local PDF files | No |

---

## Design Decisions

### Deterministic IDs

Every scraper generates a UUID v5 from `(source_name, external_id)`. Re-scraping the same
program always produces the same UUID — no duplicates accumulate, and `ON CONFLICT (source_id)
DO UPDATE` refreshes data without creating new rows.

### Status preservation

The `status` column in `rebates` is **excluded** from `ON CONFLICT DO UPDATE`. A new scrape
never resets an admin-approved program back to `draft`.

### Rate limiting

- Colly scrapers: 500 ms delay between requests.
- Rewiring America: 200 ms delay between ZIP code requests.

---

## Project Structure

```
incenva-scraper-service/
├── cmd/
│   ├── scraper/main.go         ← fetch + stage entry point
│   └── pdf-scraper/main.go     ← PDF incentive extractor
├── config/
│   └── config.go               ← env config loader
├── db/
│   ├── client.go               ← GORM connection + AutoMigrate
│   └── upsert.go               ← batch upsert to rebates_staging
├── internal/logutil/logutil.go ← shared zap logger builder
├── models/
│   ├── incentive.go            ← scraper output type + helpers
│   ├── staged_rebate.go        ← GORM model for rebates_staging
│   └── string_slice.go         ← PostgreSQL text[] ↔ []string adapter
├── scrapers/
│   ├── base.go                 ← Scraper interface + Registry + RunAll
│   ├── colly_template.go       ← CollyBase + ParseAmount helpers
│   ├── dsireusa.go             ← DSIRE USA paginated REST scraper
│   ├── rewiring_america.go     ← Rewiring America ZIP-based REST scraper
│   ├── energy_star.go          ← Energy Star HTML scraper (Colly)
│   ├── pdf_extractor.go        ← generic PDF page extraction helper
│   └── consumers_energy.go     ← Consumers Energy PDF incentive extractor
├── scripts/
│   ├── run.mjs                 ← Node.js wrapper for cmd/scraper
│   ├── run-pdf.mjs             ← Node.js wrapper for cmd/pdf-scraper
│   └── go-mod.mjs              ← Node.js wrapper for go mod commands
├── .env.example                ← copy to .env and fill in values
├── .gitignore
├── Dockerfile
├── go.mod
├── package.json                ← optional npm scripts (Node 18+ required)
└── README.md
```
