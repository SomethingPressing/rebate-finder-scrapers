# Incenva Scraper Service

A standalone Go service that fetches incentive programs from three sources (plus
local PDFs) and stages them in a PostgreSQL table for human review before they go
live in the consumer application.

---

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  cmd/scraper  (Go — runs on schedule or one-shot)        │
│                                                          │
│  DSIREUSA ──┐                                            │
│  Rewiring   ├─► scrapers.RunAll ─► rebates_staging       │
│  EnergyStar ┘          (GORM upsert, stg_status=pending) │
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
│                                       stg_rebate_id=UUID │
│                                                          │
│  Admin-set statuses (approved/published) are NEVER       │
│  overwritten — status is excluded from the update clause.│
└──────────────────────────────────────────────────────────┘
```

> **Note:** The promoter (`promote-staging.ts`) is a TypeScript/Prisma script
> that lives in the **consumer application**, not in this repo.  This service
> is responsible only for scraping and staging.

### Why a staging table?

| Problem | Solution |
|---------|----------|
| Bad scrape run corrupts live data | Scraper writes to `rebates_staging` only |
| Re-scraping resets admin-approved status | Status column excluded from DO UPDATE |
| Want to preview/transform before publish | Inspect staging rows before promoting |
| Easy rollback | `DELETE FROM rebates_staging WHERE stg_promotion_status = 'pending'` |

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go   | 1.22+   | https://go.dev/dl |
| PostgreSQL | 14+ | shared with the consumer application |
| Node.js | 18+ | optional — only needed for the `npm run …` helper scripts |

---

## Quick Start

### 1. Clone and configure

```bash
git clone <this-repo>
cd scraper-service

cp .env.example .env
# Edit .env — set DATABASE_URL and REWIRING_AMERICA_API_KEY at minimum
```

### 2. Install Go dependencies

```bash
go mod download
```

Or via npm helper:

```bash
npm run go:deps
```

### 3. Run the scraper (one-shot)

```bash
RUN_ONCE=true go run ./cmd/scraper
```

Or via npm helper:

```bash
npm run run
```

---

## Configuration

All configuration is read from environment variables.  Copy `.env.example` to
`.env` and fill in your values — the service walks up the directory tree looking
for a `go.mod + .env` pair and loads whichever it finds first.

To force a specific file:

```bash
DOTENV_PATH=/etc/incenva/scraper.env go run ./cmd/scraper
```

### Full variable reference

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | _(required)_ | PostgreSQL DSN |
| `RUN_ONCE` | `false` | `true` = run once and exit; `false` = cron mode |
| `SCRAPER_INTERVAL` | `@every 6h` | robfig/cron schedule string |
| `SCRAPER_VERSION` | `1.0` | Written to `scraper_version` on every upsert |
| `LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `LOG_FORMAT` | `json` | `json` \| `console` |
| `DSIREUSA_BASE_URL` | _(see .env.example)_ | DSIRE API base URL |
| `REWIRING_AMERICA_API_KEY` | _(required for RA)_ | Rewiring America API key |
| `REWIRING_AMERICA_BASE_URL` | _(see .env.example)_ | Rewiring America API base URL |
| `ENERGY_STAR_BASE_URL` | _(see .env.example)_ | Energy Star page URL |
| `CONSUMERS_ENERGY_CATALOG_PDF` | — | Path to Consumers Energy catalog PDF |
| `CONSUMERS_ENERGY_APPLICATION_PDF` | — | Path to Consumers Energy application PDF |
| `SOURCE` | — | Restrict to one scraper: `dsireusa` \| `rewiring_america` \| `energy_star` |
| `DOTENV_PATH` | — | Absolute path to an explicit `.env` file |

---

## Running the Scraper

### Option A — One-shot, all scrapers (fetch → stage → exit)

```bash
RUN_ONCE=true go run ./cmd/scraper
```

### Option B — One-shot, single scraper

```bash
RUN_ONCE=true go run ./cmd/scraper --source dsireusa
RUN_ONCE=true go run ./cmd/scraper --source rewiring_america
RUN_ONCE=true go run ./cmd/scraper --source energy_star
```

Or via `SOURCE` env var (equivalent, preferred for Docker/CI):

```bash
RUN_ONCE=true SOURCE=dsireusa go run ./cmd/scraper
```

### Option C — Continuous (cron schedule)

```bash
# All scrapers — runs immediately, then every SCRAPER_INTERVAL
go run ./cmd/scraper

# Single scraper on schedule
SOURCE=dsireusa go run ./cmd/scraper
```

Stop with `Ctrl+C` — the process finishes the current run gracefully.

### Option D — Build and run binary

```bash
go build -o bin/scraper ./cmd/scraper

# One-shot
RUN_ONCE=true ./bin/scraper

# Single source
RUN_ONCE=true ./bin/scraper --source energy_star

# Scheduled
./bin/scraper
```

### npm helper scripts (optional)

If you have Node.js installed, the `package.json` wrappers save some typing:

| Command | Equivalent |
|---------|-----------|
| `npm run run` | `RUN_ONCE=true go run ./cmd/scraper` |
| `npm run run:dsireusa` | `RUN_ONCE=true SOURCE=dsireusa go run …` |
| `npm run run:rewiring_america` | … |
| `npm run run:energy_star` | … |
| `npm run serve` | `go run ./cmd/scraper` (cron, all) |
| `npm run serve:dsireusa` | `SOURCE=dsireusa go run ./cmd/scraper` |
| `npm run pdf:consumers` | `go run ./cmd/pdf-scraper` |
| `npm run go:deps` | `go mod download` |
| `npm run go:tidy` | `go mod tidy` |
| `npm run go:verify` | `go mod verify` |

Override via env prefix: `LOG_FORMAT=console npm run run:dsireusa`

---

## PDF Scrapers

PDF scrapers extract incentive data from **local PDF files** and stage them in
`rebates_staging`.

### cmd/pdf-scraper — Consumers Energy

Extracts incentives for three measures from two Consumers Energy PDFs.

**PDF file resolution (highest priority first):**

```bash
# 1. CLI flags
go run ./cmd/pdf-scraper \
  --catalog     /path/to/Consumers_Energy_Incentive_Catalog_1.pdf \
  --application /path/to/Incentive-Application.pdf

# 2. Environment variables
CONSUMERS_ENERGY_CATALOG_PDF=/path/to/catalog.pdf \
CONSUMERS_ENERGY_APPLICATION_PDF=/path/to/app.pdf \
go run ./cmd/pdf-scraper

# 3. .env defaults
go run ./cmd/pdf-scraper
```

**Human-readable output:**

```bash
LOG_FORMAT=console go run ./cmd/pdf-scraper
```

**Verbose (includes full PDF text extract):**

```bash
LOG_LEVEL=debug LOG_FORMAT=console go run ./cmd/pdf-scraper
```

### Consumers Energy 2026 — Target measures

| # | Measure | Measure IDs | Catalog PDF pages | Application PDF pages |
|---|---------|-------------|------------------|-----------------------|
| 1 | Air Conditioning — Split & Unitary | HV101a–HV101j | p.50 | p.23 |
| 2 | Interior Linear LED Tube Retrofits | LT101–LT126, LT207–LT209 | p.13–14 | p.10–11 |
| 3 | Refrigeration Compressors — Discus/Scroll | RL101, RL102 | p.85 | p.36 |

PDF page note: catalog uses `PDF page = printed page + 2`; application uses `PDF page = printed page`.

---

## Reviewing Staged Data

After a scrape run, data lands in `rebates_staging` with `stg_promotion_status = 'pending'`.

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
```

### Staging column reference

| Column | Type | Meaning |
|--------|------|---------|
| `stg_source_id` | `text UNIQUE` | Deterministic UUID from the scraper — stable upsert key |
| `stg_promotion_status` | `text` | `pending` → `promoted` or `skipped` |
| `stg_promoted_at` | `timestamptz` | When promoted — `NULL` while pending/skipped |
| `stg_rebate_id` | `text` | UUID of the live `rebates` row — `NULL` until promoted |

---

## Promoting Staged Data (consumer app)

Promotion is handled by `prisma/scripts/promote-staging.ts` in the **consumer
application** — it uses Prisma so every column in `prisma/schema.prisma` is
automatically included. Run it from the consumer app's root:

```bash
# Dry run — previews what would be promoted, writes nothing
npm run scraper:promote:dry

# Live promotion to local DB
npm run scraper:promote

# Live promotion + push to Supabase
npm run scraper:promote:supabase

# Dry run with Supabase preview
npm run scraper:promote:supabase:dry
```

**Idempotency:** Re-running the promoter is safe — each upsert is keyed by
`source_id`, and only `pending` staging rows are processed. After a successful
run they become `promoted`, so a second run is a no-op.

---

## Docker

```bash
# Build
docker build --target scraper -t incenva-scraper .

# Run one-shot
docker run --env-file .env -e RUN_ONCE=true incenva-scraper

# Run on schedule
docker run --env-file .env incenva-scraper
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

---

## Data Sources

| Source | Type | Key |
|--------|------|-----|
| [DSIRE USA](https://programs.dsireusa.org) | REST JSON API | No key needed |
| [Rewiring America](https://www.rewiringamerica.org) | REST JSON API | API key required |
| [Energy Star](https://www.energystar.gov/about/federal_tax_credits) | HTML (Colly) | No key needed |
| Consumers Energy | Local PDF files | No key needed |

---

## Design Decisions

### Deterministic IDs

Every scraper generates a UUID v5 from `(source_name, external_id)`. Re-scraping
the same program always produces the same UUID:
- No duplicate rows accumulate over time.
- `ON CONFLICT (source_id) DO UPDATE` refreshes data without creating new rows.

### Status preservation

The `status` column in `rebates` is **excluded** from the `ON CONFLICT DO UPDATE`
clause. A new scrape never resets an admin-approved program back to `draft`.

### Rate limiting

- Colly scrapers: 500 ms delay between requests.
- Rewiring America: 200 ms delay between ZIP code requests.

---

## Project Structure

```
scraper-service/
├── cmd/
│   ├── scraper/main.go         ← fetch + stage entry point (Go)
│   └── pdf-scraper/main.go     ← Consumers Energy PDF extractor
├── config/
│   └── config.go               ← env config loader (walks up for go.mod + .env)
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
