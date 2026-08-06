# Incenva Scraper Service

A standalone Go service that fetches incentive programs from multiple sources, stages them in PostgreSQL for review, and promotes them into the live catalog of each tenant's **Incenva Rebate Finder** site.

**Related repos:**
- [rebate-finder](https://github.com/SomethingPressing/rebate-finder) — the Next.js consumer app (one deployment per tenant)
- [rebate-finder-deployement](https://github.com/SomethingPressing/rebate-finder-deployement) — provisioning and deployment scripts
- [rebate-finder-broker](https://github.com/SomethingPressing/rebate-finder-broker) — the staging broker service (in progress, see "Where this is headed" below)

---

## Architecture

```
┌────────────────────────────────────────────────────────────────┐
│  cmd/scraper  (Go — Fly.io scheduled machine, or cron/PM2)     │
│                                                                │
│  DSIRE USA · Rewiring America · Energy Star (API/HTML)         │
│  Con Edison · PNM · Xcel · SRP · Peninsula Clean Energy (HTML) │
│         │                                                      │
│         └──► scrapers.RunAll ──► scraper.rebates_staging       │
│                        (GORM upsert, deterministic source ids) │
│                                                                │
│  cmd/pdf-scraper — stages from local PDFs (Consumers Energy)   │
└────────────────────────────────────────────────────────────────┘
                         │
                         │  review (release gate / psql)
                         ▼
┌────────────────────────────────────────────────────────────────┐
│  cmd/promoter  (Go — this repo)                                │
│                                                                │
│  Single-tenant: promotes pending staging rows into the same    │
│    database's public.rebates.                                  │
│  Multi-tenant:  reads the shared staging DB, then for each     │
│    active tenant in config/tenants.json connects to that       │
│    tenant's database (TENANT_<ID>_DB_URL) and upserts into     │
│    its public.rebates, zipcodes, and category links.           │
└────────────────────────────────────────────────────────────────┘
```

The scraper owns the `scraper` PostgreSQL schema (GORM models + migrations in this repo); Prisma in the consumer app owns `public`. `prisma db push` never touches `scraper.*`.

**Program identity:** every scraper derives a deterministic UUID from `(source, external id)`, and every row carries a `program_hash` built from normalized program name + utility company — deliberately *excluding* the source, so the same program found by two sources merges instead of duplicating. The hash algorithm must stay byte-identical to its TypeScript twin in the consumer app (`prisma/scripts/lib/map-rebate-from-source-raw.ts`).

### Where this is headed

The multi-tenant promoter's direct writes into tenant databases (and the `TENANT_*_DB_URL` secrets they require) are being replaced by a publish–subscribe design: bots write to one shared staging database, a broker routes entries into per-tenant queues, and tenant sites pull over HTTP. See the architecture spec in `rebate-finder/docs/specifications/shared-scraper-hub.md` and the v0.8 plan. This repo keeps ownership of the staging schema.

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.24+ | matches `go.mod` |
| PostgreSQL | 14+ | staging database (shared with the consumer app in single-tenant mode) |
| Node.js | 18+ | optional — only for the `npm run …` helper wrappers |

---

## Quick start

```bash
git clone git@github.com:SomethingPressing/rebate-finder-scrapers.git
cd rebate-finder-scrapers

cp .env.example .env    # set DATABASE_URL and REWIRING_AMERICA_API_KEY at minimum
go mod download

# Run all scrapers once
RUN_ONCE=true go run ./cmd/scraper

# Or via make
make scrape
```

Full setup, every environment variable, and per-scraper details: **[docs/getting-started.md](docs/getting-started.md)** and **[docs/scrapers.md](docs/scrapers.md)**.

---

## The binaries

| Binary | What it does |
|--------|--------------|
| `cmd/scraper` | Fetches and stages programs from all API/HTML sources. One-shot (`RUN_ONCE=true`) or cron mode (`SCRAPER_INTERVAL`). Restrict with `--source <name>` or `SOURCE=`. |
| `cmd/pdf-scraper` | Extracts incentives from local PDF files (Consumers Energy catalog) into staging. See [docs/pdf-scraper.md](docs/pdf-scraper.md). |
| `cmd/promoter` | Moves pending staging rows into live `public.rebates` — single-tenant or multi-tenant (per `config/tenants.json`). `--dry-run` previews. |
| `cmd/staging-stats` | Analytics report on the staging table (`--json` for machine-readable). |
| `cmd/evaluator` | Data-quality evaluator: GPT-4o re-extracts fields from stored raw responses (or curated test pages) and diffs them against what the scraper stored. Needs `OPENAI_API_KEY`. |
| `cmd/migrate` | Named, idempotent data migrations that touch staging *and* tenant databases (clean → re-scrape → re-promote). |
| `cmd/purge-no-url` | Deletes staging rows with no program or application URL — rows that can never be surfaced. |

Common invocations are wrapped twice, pick your flavor:

```bash
make help          # build, scrape, scrape-<source>, promote, promote-dry, stats, pdf-scrape, purge-no-url
npm run            # run:<source>, refresh:<source>, run:force:<source>, pdf, staging:stats,
                   # scraper:promote[:dry], eval, migrate, sync:tenants
```

`refresh:*` re-runs a scrape and resets `stg_promotion_status` to `pending` so the promoter re-pushes fresh data — the full lifecycle is in [docs/staging-and-promotion.md](docs/staging-and-promotion.md).

---

## Data sources

| Source | Territory | Type | Key required |
|--------|-----------|------|--------------|
| [DSIRE USA](https://programs.dsireusa.org) | Nationwide | REST JSON API | No |
| [Rewiring America](https://www.rewiringamerica.org) | Nationwide (by ZIP) | REST JSON API | Yes — free at rewiringamerica.org/api |
| [Energy Star](https://www.energystar.gov) | Federal | HTML | No |
| Con Edison | NY | HTML | No |
| PNM | NM | HTML | No |
| Xcel Energy | CO / MN / WI | HTML | No |
| SRP | AZ | HTML | No |
| Peninsula Clean Energy | CA | HTML | No |
| Consumers Energy | MI | Local PDF files | No |

HTML scrapers use Colly with rate limiting (`PAGE_DELAY_MS`, default 500 ms) and a headless-browser fallback for JavaScript-heavy pages. Per-scraper details: [docs/scrapers.md](docs/scrapers.md).

---

## Multi-tenant configuration

Tenants live in `config/tenants.json` (see `config/tenants.example.json`): id, active flag, sources, location filter (states / utilities / service areas / ZIP codes), per-source cap, and the *name* of the env var holding that tenant's database URL (`TENANT_<ID_UPPER>_DB_URL`). The URLs themselves are Fly secrets in production — never committed.

With no active tenants in `TENANTS_FILE`, everything behaves single-tenant against `DATABASE_URL`.

---

## Deployment

Production runs on **Fly.io** as a pure background worker — no HTTP services, `RUN_ONCE=true`, scheduled machine runs every 6 hours (`fly.toml`). First-time setup and deploys are scripted in the deployment repo (`scripts/scraper/setup-fly.sh`, `deploy-fly.sh`) and in this repo (`scripts/deploy-fly.sh`).

```bash
# Build image locally
docker build --target scraper -t incenva-scraper .

# Deploy to Fly
bash scripts/deploy-fly.sh
```

- Fly specifics: **[docs/fly-deployment.md](docs/fly-deployment.md)**
- On-VPS variant (PM2/systemd, single-tenant): **[docs/deployment.md](docs/deployment.md)** and `scripts/setup-server.sh`
- Full provisioning (droplet + app + scraper): the [rebate-finder-deployement](https://github.com/SomethingPressing/rebate-finder-deployement) repo

---

## Configuration highlights

Everything is env-driven; `.env.example` is the authoritative, commented list. The ones you'll touch most:

| Variable | Default | Meaning |
|----------|---------|---------|
| `DATABASE_URL` | — | Staging PostgreSQL (required) |
| `TENANTS_FILE` | `config/tenants.json` | Tenant config; empty string disables multi-tenant mode |
| `RUN_ONCE` | `false` | `true` = one pass and exit; `false` = cron mode |
| `SCRAPER_INTERVAL` | `@every 1h` | robfig/cron schedule |
| `SOURCE` | — | Restrict to one scraper |
| `FORCE_REFRESH` | `false` | Reset written rows to `pending` so the promoter re-pushes them |
| `SCRAPER_DB_SCHEMA` | `scraper` | Schema separation from Prisma's `public` |
| `REWIRING_AMERICA_API_KEY` | — | Required for the Rewiring America scraper |
| `PAGE_DELAY_MS` / `MAX_CONCURRENCY` | `500` / `3` | Global scraper tuning |
| `APP_URL` / `PROMOTER_SYNC_SECRET` | — | Register runs in the app's Ingestion Monitor |
| `OPENAI_API_KEY` | — | Only for `cmd/evaluator` |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `json` | zap logging (`console` for humans) |

---

## Project structure

```
rebate-finder-scrapers/
├── cmd/
│   ├── scraper/            fetch + stage entry point
│   ├── pdf-scraper/        PDF incentive extractor
│   ├── promoter/           staging → live promotion (single- and multi-tenant)
│   ├── staging-stats/      staging analytics report
│   ├── evaluator/          GPT-4o data-quality evaluator
│   ├── migrate/            named cross-database data migrations
│   └── purge-no-url/       remove unsurfaceable rows
├── config/                 env config loader + tenants.json loader
├── db/                     GORM client, migrations, upsert, promoter internals
├── models/                 Incentive, StagedRebate, program-hash helpers
├── scrapers/               one file per source + Colly/browser/PDF helpers
├── evaluator/              evaluator internals
├── internal/logutil/       shared zap logger builder
├── scripts/                Node wrappers, deploy-fly.sh, setup-server.sh, migrations
├── testdata/ · testing/    fixtures and test helpers
├── docs/                   full documentation (see below)
├── Dockerfile              distroless scraper image (Go 1.24 builder)
├── fly.toml                Fly.io background-worker config
└── Makefile                build/scrape/promote/stats targets
```

---

## Documentation

Full docs live in **[docs/](docs/README.md)**:

| Doc | Contents |
|-----|----------|
| [architecture.md](docs/architecture.md) | System design, data flow, design decisions |
| [getting-started.md](docs/getting-started.md) | Local setup, all environment variables |
| [scrapers.md](docs/scrapers.md) | Every scraper in detail |
| [pdf-scraper.md](docs/pdf-scraper.md) | PDF extraction deep-dive |
| [database.md](docs/database.md) | `rebates_staging` schema reference |
| [staging-and-promotion.md](docs/staging-and-promotion.md) | Staging → review → promotion lifecycle |
| [deployment.md](docs/deployment.md) / [fly-deployment.md](docs/fly-deployment.md) | VPS and Fly.io deployment |
| [adding-a-scraper.md](docs/adding-a-scraper.md) | Implementing a new scraper step by step |
