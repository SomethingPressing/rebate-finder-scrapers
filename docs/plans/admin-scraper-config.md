# Plan: Admin-Managed Scraper Configuration

**Status:** Draft — awaiting review  
**Date:** 2026-06-08

---

## Overview

Move scraper configuration out of `config/tenants.json` and into the admin portal, with per-source location filters and the ability to trigger scrape runs from the UI.

---

## Current State

- Config lives in `config/tenants.json` — a flat file with one global `location_filter` shared across all sources
- Scrapers are CLI-only — no HTTP server, no way to trigger from outside
- Location filter is applied at tag-time (after scraping, not during), so it controls what gets promoted to this tenant's DB — not what gets scraped
- No run history or status visibility anywhere in the admin UI

---

## Problem Breakdown

Three distinct problems to solve:

1. **Config is in a file** — cannot be edited from the admin portal
2. **Location filter is global** — cannot be configured per source (DSIREUSA needs the zip list, PNM doesn't need any filter, SRP is always AZ)
3. **No run trigger** — no way to kick off a scrape from the UI

---

## Data Model Changes (Prisma — rebate-finder repo)

### New model: `ScraperSourceConfig`

| Field | Type | Notes |
|---|---|---|
| `source` | String (PK) | `pnm`, `srp`, `dsireusa`, etc. |
| `active` | Boolean | Whether this source is enabled |
| `states` | String[] | State codes to filter programs |
| `utilities` | String[] | Utility name substrings to filter |
| `serviceAreas` | String[] | Service territory substrings |
| `zipCodes` | String[] | Explicit ZIP codes |
| `maxIncentives` | Int? | Cap per run (null = unlimited) |
| `lastRunAt` | DateTime? | Timestamp of last completed run |
| `lastRunStatus` | Enum | `idle / running / success / error` |
| `lastRunCount` | Int? | Programs found in last run |
| `lastRunDuration` | Int? | Duration in seconds |
| `lastRunError` | String? | Error message if failed |

### New model: `ScraperRunLog`

| Field | Type | Notes |
|---|---|---|
| `id` | UUID (PK) | |
| `source` | String | FK to ScraperSourceConfig |
| `status` | Enum | `queued / running / success / error` |
| `startedAt` | DateTime | |
| `finishedAt` | DateTime? | |
| `programCount` | Int? | Programs upserted |
| `error` | String? | Error message if failed |

### New model: `ScraperJob`

| Field | Type | Notes |
|---|---|---|
| `id` | UUID (PK) | |
| `source` | String | Which source to run |
| `status` | Enum | `queued / picked_up` |
| `createdAt` | DateTime | When the admin triggered it |
| `pickedUpAt` | DateTime? | When the scraper claimed it |

---

## Initial Seed (from tenants.json)

One-time migration script converts the current `tenants.json` into `ScraperSourceConfig` rows:

- National sources (`dsireusa`, `rewiring_america`, `energy_star`) → copy the current global `zip_codes` list into their row
- Utility sources (`pnm`→NM, `srp`→AZ, `con_edison`→NY, `xcel_energy`→CO/MN/TX/WI, `peninsula_clean_energy`→CA) → empty filter (geography is baked into the scraper itself)

---

## Scraper Changes (scrapers repo)

### 1. Read config from DB

Add `LoadTenantsFromDB(db)` in `config/tenants.go`. On startup the scraper checks if `ScraperSourceConfig` rows exist — if yes, use DB config; if no, fall back to `tenants.json`. Clean migration path with no flag day.

### 2. Per-source location filter

Currently `MatchesIncentive()` applies the global filter to all sources. Change it so each incentive is matched against the filter for its specific source. Global filter becomes the fallback when no per-source row exists.

### 3. Run trigger via DB polling

Add a check for `ScraperJob` rows with `status = queued` at startup and on each cron tick. When found, the scraper claims the job (marks `picked_up`), runs that source, then writes results back. No HTTP server needed — the admin portal writes a row, the scraper picks it up.

Polling interval: the scraper should check for queued jobs every ~60 seconds in addition to its scheduled full runs, so "Run Now" feels responsive.

### 4. Write run status back to DB

After each run (scheduled or triggered):
- Update `ScraperSourceConfig.lastRunAt`, `lastRunStatus`, `lastRunCount`, `lastRunDuration`
- Insert a row into `ScraperRunLog`

---

## Admin API Endpoints (Next.js — rebate-finder repo)

| Method | Route | Purpose |
|---|---|---|
| GET | `/api/admin/scraper/sources` | List all source configs with current status |
| PUT | `/api/admin/scraper/sources/:source` | Update a source config (toggle, filters, cap) |
| POST | `/api/admin/scraper/sources/:source/run` | Queue an on-demand run |
| GET | `/api/admin/scraper/sources/:source/runs` | Run history for a source |

---

## Admin UI Screens

### Screen 1 — Sources List (`/admin/scraper`)

Table with one row per source:

- Source name
- Active toggle
- Last run: timestamp, status badge (`idle / running / success / error`), program count
- "Run Now" button (disabled while `status = running`)
- "Configure" link → per-source detail

First-time banner: *"You haven't run your first scrape yet. Enable the sources you want and click Run All to populate your database."* with a "Run All Active Sources" button.

### Screen 2 — Per-Source Config (`/admin/scraper/:source`)

**Location Filter section:**
- States: multi-select dropdown (all US states, with Select All / Clear)
- ZIP Codes: tag input (paste a list or add individually)
- Utilities: tag input (substring matches)
- Service Areas: tag input

**Run Settings section:**
- Active toggle
- Max incentives per run (numeric, blank = unlimited)
- Save button

**Run History section:**
- Paginated table: started at, status, program count, duration, error

---

## Open Questions (resolve before starting)

1. **Shared DB?** The admin portal DB (`rebate-finder`) and the scrapers need to be the same Postgres instance, or the scrapers need a direct connection to the admin DB. Currently the scrapers use `TENANT_ID_DB_URL` which may point to a different DB. This needs to be confirmed before the schema work starts.

2. **Status write-back permissions** — the scraper will need write access to the `ScraperSourceConfig` and `ScraperRunLog` tables in the admin DB. If these are separate Postgres instances today, decide whether to merge them or have the scraper call an admin API endpoint to write status back.

3. **Polling frequency** — the scraper's current cron interval drives full runs. For "Run Now" to feel responsive, a separate lightweight polling loop (every 60s) should check for queued jobs without triggering a full scrape.

---

## Implementation Sequence

1. Confirm shared DB setup (open question 1 above)
2. Add Prisma models (`ScraperSourceConfig`, `ScraperRunLog`, `ScraperJob`) → `db push`
3. Write seed script from `tenants.json` → run once
4. Build admin API endpoints (list, update, queue run)
5. Build admin UI — sources list first, then per-source editor
6. Update scrapers repo — DB config loader, per-source filter, job polling, status write-back
7. End-to-end test with one source (e.g. PNM): edit in UI → Run Now → status updates → programs in DB
8. Roll out to remaining sources
9. Retire `tenants.json`
