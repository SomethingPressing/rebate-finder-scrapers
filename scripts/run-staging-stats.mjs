#!/usr/bin/env node
/**
 * Print analytics for the rebates_staging table.
 *
 * Usage:
 *   pnpm staging:stats           # human-readable terminal report
 *   pnpm staging:stats -- --json # machine-readable JSON
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");

// Forward any extra args (e.g. --json) to the binary.
const extraArgs = process.argv.slice(2);

const result = spawnSync("go", ["run", "./cmd/staging-stats", ...extraArgs], {
  cwd: scraperDir,
  stdio: "inherit",
  env: { ...process.env },
});

if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
