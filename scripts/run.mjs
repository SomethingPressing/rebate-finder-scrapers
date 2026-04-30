#!/usr/bin/env node
/**
 * Run cmd/scraper from the scraper-service directory (one-shot).
 *
 * Usage:
 *   node scripts/run.mjs              # all sources
 *   node scripts/run.mjs all          # same
 *   node scripts/run.mjs dsireusa     # single source
 *   node scripts/run.mjs rewiring_america
 *   node scripts/run.mjs energy_star
 *
 * For long-running scheduled mode use PM2 or systemd — see README.md § Deployment.
 * Env (optional): SOURCE, LOG_LEVEL, LOG_FORMAT, DOTENV_PATH, etc.
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");

const allowed = new Set([
  "all",
  "dsireusa",
  "rewiring_america",
  "energy_star",
  "con_edison",
  "pnm",
  "xcel_energy",
]);

const sourceArg = process.argv[2] ?? "all";

if (!allowed.has(sourceArg)) {
  console.error(
    `Unknown scraper "${sourceArg}". Use: all | dsireusa | rewiring_america | energy_star | con_edison | pnm | xcel_energy`,
  );
  process.exit(1);
}

const env = { ...process.env };
if (!env.RUN_ONCE) env.RUN_ONCE = "true";

if (sourceArg !== "all") {
  env.SOURCE = sourceArg;
} else {
  delete env.SOURCE;
}

const result = spawnSync("go", ["run", "./cmd/scraper"], {
  cwd: scraperDir,
  stdio: "inherit",
  env,
});

if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
