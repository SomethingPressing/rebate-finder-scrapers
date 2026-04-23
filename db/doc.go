// Package db provides PostgreSQL access for the scraper service.
//
// # Where scraped data is written
//
// All incentive rows produced by scrapers (cmd/scraper) — every source
// (dsireusa, rewiring_america, energy_star, etc.) — are persisted only through
// [UpsertToStaging], which upserts into the shared rebates_staging table.
// cmd/scraper must not insert or upsert into the live rebates table.
//
// Moving staged rows into production rebates is handled exclusively by
// cmd/promoter via [PromotePending] (see promote.go).
//
// # Idempotency (no duplicate rebate rows)
//
//   - Staging: [UpsertToStaging] uses ON CONFLICT (source_id) — re-scraping updates
//     the same staging row; it does not insert a second row for the same program.
//   - Live rebates: [PromotePending] uses INSERT … ON CONFLICT (id) DO UPDATE — running
//     the promoter again only touches rows still marked pending; already-promoted
//     staging rows are skipped, and existing rebates rows are updated in place.
package db
