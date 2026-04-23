#!/usr/bin/env node
/**
 * Run cmd/scraper from the scraper-service directory.
 *
 * Usage:
 *   node scripts/run.mjs              # all sources, RUN_ONCE=true by default
 *   node scripts/run.mjs all          # same
 *   node scripts/run.mjs dsireusa     # single source (SOURCE env)
 *   node scripts/run.mjs rewiring_america
 *   node scripts/run.mjs energy_star
 *   node scripts/run.mjs --serve      # RUN_ONCE=false (cron / long-running)
 *   node scripts/run.mjs dsireusa --serve
 *
 * Env (optional): RUN_ONCE, SOURCE, LOG_LEVEL, LOG_FORMAT, DOTENV_PATH, etc.
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");

const allowed = new Set(["all", "dsireusa", "rewiring_america", "energy_star"]);

const rawArgs = process.argv.slice(2);
const serve = rawArgs.includes("--serve");
const positional = rawArgs.filter((a) => !a.startsWith("--"));
const sourceArg = positional[0] ?? "all";

if (!allowed.has(sourceArg)) {
  console.error(
    `Unknown scraper "${sourceArg}". Use: all | dsireusa | rewiring_america | energy_star`,
  );
  process.exit(1);
}

const env = { ...process.env };
if (serve) {
  env.RUN_ONCE = "false";
} else if (env.RUN_ONCE === undefined || env.RUN_ONCE === "") {
  env.RUN_ONCE = "true";
}

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
