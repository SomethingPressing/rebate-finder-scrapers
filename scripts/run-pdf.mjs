#!/usr/bin/env node
/**
 * Run the Consumers Energy PDF incentive extractor (cmd/pdf-scraper).
 *
 * Extracts and stages incentive data for three measures from two PDF files.
 *
 * # PDF file resolution (highest priority first)
 *
 *   1. --catalog / --application CLI flags (passed through to Go)
 *   2. CONSUMERS_ENERGY_CATALOG_PDF / CONSUMERS_ENERGY_APPLICATION_PDF env vars
 *   3. .env defaults (set automatically by config.Load)
 *
 * # Usage
 *
 *   # Standard run — logs + stages to rebates_staging:
 *   node scripts/run-pdf.mjs
 *
 *   # Also capture raw PDF text in pdf_scrape_raw (audit trail):
 *   node scripts/run-pdf.mjs --save-supabase
 *
 *   # Explicit PDF paths:
 *   node scripts/run-pdf.mjs \
 *     --catalog ~/Downloads/Consumers_Energy_Incentive_Catalog_1.pdf \
 *     --application ~/Downloads/"Incentive-Application (1).pdf"
 *
 *   # Human-readable console output:
 *   LOG_FORMAT=console node scripts/run-pdf.mjs
 *
 *   # Verbose:
 *   LOG_LEVEL=debug LOG_FORMAT=console node scripts/run-pdf.mjs
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");

// Forward all CLI args straight to the Go binary.
const extraArgs = process.argv.slice(2);

const env = { ...process.env };

if (extraArgs.length) {
  console.error(`[pdf-scraper] args: ${extraArgs.join(" ")}`);
}

const result = spawnSync(
  "go",
  ["run", "./cmd/pdf-scraper", ...extraArgs],
  { cwd: scraperDir, stdio: "inherit", env },
);

if (result.error) {
  console.error("Failed to start pdf-scraper:", result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
