# Getting Started

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.24+ | https://go.dev/dl/ |
| PostgreSQL | 14+ | Local or shared with consumer app |
| Node.js | 18+ (optional) | https://nodejs.org — only needed for `npm run` helpers |

> The scraper connects to the **same database** as the consumer app. Point `DATABASE_URL` at your existing Postgres instance.

---

## 1. Clone and install dependencies

```bash
git clone <repository-url>
cd rebate-finder-scrapers

# Download Go modules
go mod download

# Optional: verify checksums
go mod verify
```

---

## 2. Configure environment variables

```bash
cp .env.example .env
```

Edit `.env` — the only strictly required variable is `DATABASE_URL`:

```env
# Required
DATABASE_URL=postgresql://user:password@localhost:5432/rebate_finder

# Required only for Rewiring America scraper
REWIRING_AMERICA_API_KEY=your_key_here

# Optional — defaults shown
RUN_ONCE=false
SCRAPER_INTERVAL=@every 6h
SCRAPER_VERSION=1.0
LOG_LEVEL=info
LOG_FORMAT=json
```

See [Environment Variables](#environment-variables) below for the full reference.

---

## 3. Run the scrapers

### All sources (one-shot)

```bash
RUN_ONCE=true go run ./cmd/scraper
```

### Single source

```bash
SOURCE=dsireusa      RUN_ONCE=true go run ./cmd/scraper
SOURCE=rewiring_america RUN_ONCE=true go run ./cmd/scraper
SOURCE=energy_star   RUN_ONCE=true go run ./cmd/scraper
```

### Scheduled mode (runs every 6 hours)

```bash
go run ./cmd/scraper
```

### PDF scraper (Consumers Energy)

```bash
# Human-readable output
LOG_FORMAT=console go run ./cmd/pdf-scraper \
  --catalog  /path/to/Consumers_Energy_Incentive_Catalog.pdf \
  --application /path/to/Incentive-Application.pdf
```

### Using npm helpers (optional)

```bash
npm run run                  # all sources (RUN_ONCE=true)
npm run run:dsireusa
npm run run:rewiring_america
npm run run:energy_star
npm run pdf                  # PDF scraper (uses env var paths)
```

---

## 4. Verify staging rows

After a scrape run, check the staging table directly in `psql`:

```sql
-- How many rows are pending?
SELECT stg_promotion_status, COUNT(*)
FROM rebates_staging
GROUP BY 1;

-- Preview what was scraped
SELECT stg_source_id, program_name, utility_company, state, incentive_format, incentive_amount
FROM rebates_staging
WHERE stg_promotion_status = 'pending'
ORDER BY created_at DESC
LIMIT 20;
```

Or open Prisma Studio in the consumer app:

```bash
cd ../rebate-finder
pnpm prisma studio
```

---

## 5. Promote staging rows to live rebates

Promotion is handled by the **consumer app** (`rebate-finder`), not this service:

```bash
cd ../rebate-finder
pnpm scraper:promote:dry   # preview
pnpm scraper:promote       # promote pending rows
```

See [staging-and-promotion.md](staging-and-promotion.md) for the full lifecycle.

---

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | Yes | — | PostgreSQL DSN. Shared with consumer app. |
| `RUN_ONCE` | | `false` | `true` = run all scrapers once and exit |
| `SOURCE` | | — | Restrict to one scraper: `dsireusa`, `rewiring_america`, `energy_star` |
| `SCRAPER_INTERVAL` | | `@every 6h` | [robfig/cron](https://pkg.go.dev/github.com/robfig/cron) schedule expression |
| `SCRAPER_VERSION` | | `1.0` | Written to `scraper_version` column on every upsert |
| `LOG_LEVEL` | | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | | `json` | `json` (structured) or `console` (human-readable key=value) |
| `REWIRING_AMERICA_API_KEY` | For RA | — | Free key from [rewiringamerica.org/api](https://www.rewiringamerica.org/api) |
| `DSIREUSA_BASE_URL` | | `https://programs.dsireusa.org` | Override DSIRE API base URL |
| `REWIRING_AMERICA_BASE_URL` | | `https://api.rewiringamerica.org` | Override RA API base URL |
| `ENERGY_STAR_BASE_URL` | | `https://www.energystar.gov` | Override Energy Star page URL |
| `CONSUMERS_ENERGY_CATALOG_PDF` | For PDF | — | Path to Incentive Catalog PDF |
| `CONSUMERS_ENERGY_APPLICATION_PDF` | For PDF | — | Path to Incentive Application PDF |
| `DOTENV_PATH` | | — | Absolute path to a specific `.env` file (useful in Docker/CI) |

---

## Logging

```bash
# Structured JSON (default — production)
go run ./cmd/scraper

# Human-readable (development)
LOG_FORMAT=console go run ./cmd/scraper

# Verbose debug output
LOG_LEVEL=debug LOG_FORMAT=console go run ./cmd/scraper
```

---

## Building a binary

```bash
# cmd/scraper
go build -o bin/scraper ./cmd/scraper

# cmd/pdf-scraper
go build -o bin/pdf-scraper ./cmd/pdf-scraper

# Both
go build ./...
```

---

## Troubleshooting

### `relation "rebates_staging" does not exist`

The staging table is created by GORM AutoMigrate on first run. If you see this error it means the DB connection is failing before AutoMigrate runs. Check `DATABASE_URL`.

### `db: DATABASE_URL is required`

Set `DATABASE_URL` in your `.env` file or export it directly:

```bash
export DATABASE_URL=postgresql://user:pass@localhost:5432/rebate_finder
go run ./cmd/scraper
```

### `ERROR: column "source_id" does not exist`

This is safe to ignore on a fresh database. The migration that references `source_id` is a no-op when the column doesn't exist (it guards against the legacy column from older deployments).

### Rewiring America returns 0 results

Check that `REWIRING_AMERICA_API_KEY` is set and valid. The API returns 401 with an invalid key.

### PDF scraper exits with "catalog PDF not found"

Set the env vars or pass CLI flags:

```bash
go run ./cmd/pdf-scraper \
  --catalog  /absolute/path/to/catalog.pdf \
  --application /absolute/path/to/application.pdf
```
