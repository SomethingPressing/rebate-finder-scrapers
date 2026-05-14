#!/usr/bin/env node
/**
 * Run the scraper data-quality evaluator against GPT-4o.
 *
 * Usage:
 *   pnpm eval                                    # all scrapers, 2 rows each
 *   pnpm eval -- -source dsireusa               # one scraper, 2 rows
 *   pnpm eval -- -source con_edison -n 5        # one scraper, 5 rows
 *   pnpm eval -- -mode testcases -source srp    # testcases mode
 *   pnpm eval -- -output json                   # machine-readable JSON
 *   pnpm eval -- -debug                         # show raw LLM I/O
 *
 * Requires: DATABASE_URL and OPENAI_API_KEY (set in .env or environment).
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");

const extraArgs = process.argv.slice(2).filter(a => a !== "--");

const result = spawnSync("go", ["run", "./cmd/evaluator", ...extraArgs], {
  cwd: scraperDir,
  stdio: "inherit",
  env: { ...process.env },
});

if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
