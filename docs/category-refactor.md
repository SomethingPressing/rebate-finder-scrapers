# Issue #82 — Refactor app categories and unify them across the app and scrapers

---

## User Story 1 — Normalized category data model

**As an** admin,
**I want** rebate programs to be linked to a canonical list of categories (HVAC, Solar, Appliances, etc.) stored in their own database table,
**So that** category tiles on the homepage and search filters always reflect real taxonomy entries and cannot break because of a typo in a raw string array.

**Acceptance criteria:**
- A `categories` table exists with `id`, `name` (unique), `slug` (unique), and `portfolio` (parent group)
- Rebates link to categories through a `rebate_categories` join table, not a raw `String[]` column
- The old `category_tag String[]` and `db_values String[]` columns are removed
- `pnpm db:seed` auto-populates all canonical categories from the shared taxonomy config so a fresh deployment requires no manual data entry
- Existing rebates are backfilled so no program loses its category after migration

---

## User Story 2 — Portfolio tiles driven by data, not hardcoded strings

**As an** admin,
**I want** homepage portfolio tiles (Energy Efficiency, Electric Vehicles, etc.) to be managed from the admin UI with drag-and-drop ordering and visibility toggles,
**So that** I can update what appears on the homepage without touching code or doing a deployment.

**Acceptance criteria:**
- A `portfolios` table exists; each row represents one homepage tile group
- Admin `HomepageSettings` page lists all portfolios and lets me reorder, show/hide, and link canonical categories to each tile
- Public homepage reads portfolios from the API, not from a hardcoded config
- API routes exist: `GET/POST/PATCH/DELETE /api/portfolios`, `PATCH /api/portfolios/reorder`, `GET /api/portfolios/public`
- `Portfolio` is no longer written by scrapers — it is derived at promotion time from linked categories using the shared taxonomy map

---

## User Story 3 — Implementing sector field

**As a** user browsing programs,
**I want** to see whether a rebate is offered by a Utility, a State agency, or the Federal government,
**So that** I can understand who is funding the program and whether it applies to me regardless of my utility provider.

**Acceptance criteria:**
- `implementing_sector` field exists on the `Rebate` model (`Utility` | `State` | `Federal`)
- All scrapers populate this field based on the program's administering body, not the product category
- The field is displayed on the public program page and in search results
- All existing promoted rebates are backfilled with the correct sector value

---

## User Story 4 — Single shared taxonomy across app and scrapers

**As a** developer,
**I want** the list of valid category names and their parent portfolio groups to be defined in one place that both the Next.js app and the Go scraper service import from,
**So that** a new category added in the app automatically becomes available in all scrapers without any manual synchronisation.

**Acceptance criteria:**
- `src/lib/categoryConfig.ts` is the single source of truth in the app — all views, hooks, and seed scripts import from it
- `models/taxonomy.go` in the scraper service mirrors the same category-to-portfolio mapping
- The promoter (`cmd/promoter`) uses `CategoryPortfolioMap` to auto-create category rows when promoting a staging row — no category needs to be created manually
- If a scraper produces a `category_tag` value that is not in the taxonomy map, it is flagged as unknown rather than silently stored

---

## User Story 5 — Correct category tags for all scrapers

**As a** user searching by category,
**I want** every rebate program to have an accurate category tag regardless of which scraper ingested it,
**So that** filtering by "Appliances" returns ovens and dryers, not HVAC equipment, and filtering by "Electrification" returns panel upgrades and wiring programs.

**Acceptance criteria:**
- DSIRE, Rewiring America, Energy Star, Con Edison, PNM, Xcel Energy, SRP, and Peninsula Clean Energy all produce category tags from the shared taxonomy
- Commercial food-service equipment (ovens, griddles, holding cabinets) is tagged `Appliances`, not `HVAC`
- Electric panel upgrades, wiring upgrades, and IRA HEAR electrification items are tagged `Electrification`
- Raw Energy Star API category strings (`"Other"`, `"Building Products"`, `"General Income"`, `"Point-of-Sale Discount"`) are never stored as user-facing tags
- A three-tier fallback handles novel product types without requiring keyword additions:
  1. Keyword substring matching (free, instant)
  2. `text-embedding-3-small` cosine similarity against all taxonomy categories (threshold 0.72)
  3. `gpt-4o-mini` explicit classification for low-confidence cases
- The smart inferrer is optional — scrapers work correctly without `OPENAI_API_KEY`, falling back to keyword-only inference

---

## User Story 6 — Evaluator works with correct source identifiers

**As a** developer running the data-quality evaluator,
**I want** `pnpm eval -- -source energy_star` to return results,
**So that** I can evaluate Energy Star data quality without having to know the internal casing used in the database.

**Acceptance criteria:**
- The `source` column in `rebates_staging` and `rebates` matches the scraper's `Name()` value (`"energy_star"`, `"dsireusa"`, etc.) exactly
- All existing rows where `source = 'Energy Star'` are migrated to `'energy_star'` via an idempotent DB migration
- `pnpm eval -- -source energy_star -n 5` runs successfully and returns scored results

---

## Summary table

| Area | Before | After |
|---|---|---|
| App category storage | Raw `String[]` on rebate | Normalized `Category` model + join table |
| Homepage tiles | Hardcoded `db_values` string match | Linked to `Category` rows via join; admin-managed |
| Portfolio grouping | Hardcoded array written by scrapers | Derived at promotion time from shared taxonomy map |
| Taxonomy definition | Duplicated separately in app TS and scraper Go | Single source of truth in each repo, kept in sync |
| Commercial kitchen equipment | Tagged as `HVAC` | Tagged as `Appliances` |
| Electric panel / wiring upgrades | `{Other}` — no category | Tagged as `Electrification` |
| Novel product types | Required keyword additions to ship a fix | Handled automatically by embedding + GPT-4o mini |
| `pnpm eval -- -source energy_star` | "no staging rows found" | Works correctly |
