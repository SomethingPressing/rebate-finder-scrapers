# Category Refactor — Issue #82
## Refactor app categories and unify them across the app and scrapers

---

## Problem

Categories were broken in three distinct ways:

1. **Misclassification** — The Energy Star scraper passed the API's `product_category` field (e.g. `"HVAC"`) directly into keyword inference, causing commercial food-service equipment (ovens, griddles, holding cabinets) to be tagged as `HVAC`.

2. **Raw API values stored as tags** — When keyword inference found no match, the scraper stored the raw Energy Star API category string literally (e.g. `"Other"`, `"Building Products"`, `"General Income"`, `"Point-of-Sale Discount"`). These are internal API taxonomy values, not user-facing category names.

3. **Missing categories** — Electric panel upgrades, wiring upgrades, and other IRA HEAR electrification items had no category (`{Other}`) because no keywords existed for them.

---

## What was changed

### 1. Extended keyword inference (`scrapers/html_helpers.go`)

Added missing keyword → category mappings to `inferCategories()`:

| Keywords added | Category |
|---|---|
| `electric panel`, `electrical panel`, `panel upgrade` | `Electrification` |
| `electric wiring`, `wiring upgrade`, `service upgrade`, `load center` | `Electrification` |
| `commercial oven`, `commercial griddle`, `griddle`, `hot food holding`, `food holding`, `food warmer`, `food service equipment`, `commercial kitchen`, `commercial fryer`, `commercial steamer`, `commercial dishwasher` | `Appliances` |
| `heating & cooling` | `HVAC` |
| `building products` | `Weatherization` |
| `general income` | `Income Qualified` |
| `water cooler` | `Appliances` |

These keywords cover all product types found during the db-mode evaluation that were previously falling through to raw API values.

---

### 2. Fixed Energy Star category inference (`scrapers/energy_star.go`)

**Root cause:** `inferCategories(productGeneralName + " " + result.ProductCategory)` — appending the API's `product_category` string caused the keyword `"hvac"` to match for commercial ovens (because Energy Star classifies all commercial kitchen equipment under `"Heating & Cooling"` in their internal taxonomy).

**Fix:** Run keyword inference on `productGeneralName` only. Removed `result.ProductCategory` from the inference text entirely.

**Added `esProductCategoryToTag()`** — An explicit last-resort mapping for when both keyword inference AND the smart inferrer return nothing:

| Energy Star API value | Our taxonomy |
|---|---|
| `Building Products` | `Weatherization` |
| `Heating & Cooling` | `HVAC` |
| `Electronics`, `Office Equipment` | `Appliances` |
| `General Income` | `Income Qualified` |
| `Other`, `Rebate`, `Point-of-Sale Discount`, `Special Pricing` | *(skipped — not a real category)* |

---

### 3. Hybrid smart category inferrer (`internal/categoryinfer/`, `internal/llm/embed.go`)

To eliminate the need for ever-growing keyword lists, added a three-tier fallback used by **all scrapers**:

```
Tier 1 — inferCategories(text)
          Keyword substring matching — fast, free, runs first always.
          Covers the vast majority of programs.
          ↓ no match
Tier 2 — text-embedding-3-small cosine similarity
          Embeds the product name and compares against embeddings of all
          taxonomy category names. Threshold: 0.72.
          Handles paraphrases and novel product names without keyword additions.
          ↓ similarity < 0.72
Tier 3 — gpt-4o-mini single-category classification
          Explicit LLM call for genuinely ambiguous cases.
          Returns the single best taxonomy category name.
```

**Cost:** Category embeddings are computed once per process start and cached. Individual product results cached by input string — the same product name across 50 states costs **one** embedding call total.

**Opt-in:** Requires `OPENAI_API_KEY`. Without it, all scrapers fall back gracefully to tier 1 keyword-only inference with no error.

---

### 4. Wired into all scrapers (`cmd/scraper/main.go`)

One shared `CategoryInferrer` instance is created at startup and injected into every scraper:

- `EnergyStarScraper` — via `CategoryInferrer` field on the struct
- `ConEdisonScraper`, `PNMScraper`, `XcelEnergyScraper`, `SRPScraper`, `PeninsulaCleanEnergyScraper` — via `CategoryInferrer` field propagated into `PageExtractConfig` at scrape time

---

### 5. Fixed source name inconsistency (blocker for evaluator)

`EnergyStarScraper.Name()` returned `"energy_star"` but `models.NewIncentive("Energy Star", ...)` stored `source = 'Energy Star'` in the database — with a space and different casing. This caused `pnpm eval -- -source energy_star` to find zero rows.

**Fix:**
- Changed `NewIncentive(...)` to use `energyStarSource = "energy_star"` (the constant now matches `Name()`)
- Added idempotent DB migration that updated **3,040 existing rows**
- Updated `eval_testcases.json` source field from `"Energy Star"` → `"energy_star"`
- Cleaned up `apiSources` map and `resolveSourceURL` switch in the evaluator

---

### 6. Updated documentation

- **`docs/architecture.md`** — Added the three-tier inference diagram and description under a new "Hybrid category inference" section
- **`docs/adding-a-scraper.md`** — Added a "Category inference" section explaining how to wire `CategoryInferrer` into a new scraper, with code examples

---

## Result

| Before | After |
|---|---|
| Commercial ovens, griddles → `HVAC` (wrong) | → `Appliances` ✓ |
| Electric panel/wiring upgrades → `{Other}` (blank) | → `Electrification` ✓ |
| `pnpm eval -- -source energy_star` → "no rows found" | → works correctly ✓ |
| Novel product types require keyword additions | → handled by embedding + GPT-4o mini ✓ |
| Raw API values (`"Building Products"`, `"General Income"`) stored as tags | → mapped to correct taxonomy ✓ |

---

## Files changed

| File | Change |
|---|---|
| `scrapers/html_helpers.go` | Added ~20 keyword mappings to `inferCategories` |
| `scrapers/energy_star.go` | Fixed inference input, added `esProductCategoryToTag`, source name constant, `CategoryInferrer` field |
| `scrapers/extract_goquery.go` | Added `CategoryInferrer` to `PageExtractConfig`, used as fallback in `ExtractPageGoquery` |
| `scrapers/con_edison.go`, `pnm.go`, `xcel_energy.go`, `srp.go`, `peninsula_clean_energy.go` | Added `CategoryInferrer` field + propagation to `extractCfg` |
| `scrapers/reextract.go`, `rehydrate_api.go` | Updated `mapEnergyStarRecord` call signature (added `nil` for inferrer) |
| `internal/llm/embed.go` | New — `Embed()` and `ClassifyCategory()` methods on `Client` |
| `internal/categoryinfer/inferrer.go` | New — `CategoryInferrer` struct with three-tier logic |
| `config/config.go` | Added `OpenAIKey` field |
| `cmd/scraper/main.go` | Builds shared `CategoryInferrer`, injects into all scrapers |
| `db/migrations.go` | Added `migrateEnergyStarSourceName` migration |
| `testdata/eval_testcases.json` | `"Energy Star"` → `"energy_star"` in all 3 entries |
| `docs/architecture.md` | Added hybrid inference section |
| `docs/adding-a-scraper.md` | Added category inference guide |
