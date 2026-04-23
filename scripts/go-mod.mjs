#!/usr/bin/env node
/**
 * Run Go module commands in the scraper-service directory.
 *
 * Usage:
 *   node scripts/go-mod.mjs           # go mod download
 *   node scripts/go-mod.mjs tidy      # go mod tidy
 *   node scripts/go-mod.mjs verify    # go mod verify
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");

const sub = process.argv[2] ?? "download";
const argsBySub = {
  download: ["mod", "download"],
  tidy:     ["mod", "tidy"],
  verify:   ["mod", "verify"],
};

const goArgs = argsBySub[sub];
if (!goArgs) {
  console.error(`Unknown command "${sub}". Use: download | tidy | verify`);
  process.exit(1);
}

const result = spawnSync("go", goArgs, {
  cwd: scraperDir,
  stdio: "inherit",
  env: process.env,
});

if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
