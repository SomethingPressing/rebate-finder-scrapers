package models

// ScraperSchema is the PostgreSQL schema that owns all Go-side tables
// (rebates_staging, pdf_scrape_raw).
//
// It is set once at process startup by db.Connect() using the value from the
// SCRAPER_DB_SCHEMA environment variable (default: "scraper").
//
// Prisma manages only the public schema, so tables in ScraperSchema are
// invisible to `prisma db push` and will never be accidentally dropped.
var ScraperSchema = "scraper"
