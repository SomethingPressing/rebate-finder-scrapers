#!/usr/bin/env node
/**
 * Run a named data migration via cmd/migrate.
 *
 * Migrations require access to both the scraper staging DB and all tenant
 * live databases — they use the same config as the scraper and promoter.
 *
 * Usage:
 *   pnpm migrate <migration-name>
 *   pnpm migrate 001_energy_star_name_fix
 *   pnpm migrate 001_energy_star_name_fix --dry-run
 *
 * Available migrations:
 *   001_energy_star_name_fix  — Deletes Energy Star rows from staging + all tenant
 *                           live DBs, then re-scrapes and re-promotes with
 *                           corrected program names (no fabricated " Rebate"
 *                           suffix). Run this after deploying the scraper fix.
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "../..");

const args = process.argv.slice(2).filter((a) => a !== "--");

if (args.length === 0 || args[0] === "--help" || args[0] === "-h") {
  console.log("Usage: pnpm migrate <migration-name> [--dry-run]");
  console.log("");
  console.log("Available migrations:");
  console.log("  001_energy_star_name_fix  — clean + re-scrape + re-promote Energy Star");
  console.log("");
  console.log("Flags:");
  console.log("  --dry-run   Show what would be done without making changes");
  process.exit(args.length === 0 ? 1 : 0);
}

console.log(`[migrate] Running migration: ${args[0]}`);
if (args.includes("--dry-run")) {
  console.log("[migrate] DRY RUN — no changes will be made");
}

const result = spawnSync("go", ["run", "./cmd/migrate", ...args], {
  cwd: scraperDir,
  stdio: "inherit",
  env: { ...process.env },
});

if (result.error) {
  console.error("[migrate] failed to spawn:", result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
