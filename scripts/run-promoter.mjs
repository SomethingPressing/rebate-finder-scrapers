#!/usr/bin/env node
/**
 * Promote pending staging rows to public.rebates.
 *
 * Usage:
 *   pnpm promote           # promote all pending rows
 *   pnpm promote:dry       # preview without writing
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");

const dry = process.argv.includes("--dry-run");
const args = ["run", "./cmd/promoter"];
if (dry) args.push("--dry-run");

const result = spawnSync("go", args, {
  cwd: scraperDir,
  stdio: "inherit",
  env: { ...process.env },
});

if (result.error) {
  console.error("[run-promoter] failed to spawn Go binary:", result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
