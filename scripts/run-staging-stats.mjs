#!/usr/bin/env node
/**
 * Live-updating staging stats — refreshes every 3 seconds until Ctrl+C.
 *
 * Builds the binary once on first run, then reuses it for fast refreshes.
 * Rebuilds automatically when the source changes.
 *
 * Usage:
 *   pnpm staging:stats                  # live watch, refresh every 3s
 *   pnpm staging:stats -- --once        # run once and exit
 *   pnpm staging:stats -- --interval 5  # custom refresh interval (seconds)
 *   pnpm staging:stats -- --once --json # one-shot JSON output
 */
import { spawnSync, execSync } from "node:child_process";
import { existsSync, statSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");
const binaryPath = path.join(scraperDir, "bin", "staging-stats");
const srcDir = path.join(scraperDir, "cmd", "staging-stats");

const args = process.argv.slice(2);
const once = args.includes("--once");
const intervalIdx = args.indexOf("--interval");
const intervalSecs = intervalIdx !== -1 ? (parseInt(args[intervalIdx + 1], 10) || 3) : 3;

const goArgs = args.filter((a, i) => {
  if (a === "--once" || a === "--interval") return false;
  if (i > 0 && args[i - 1] === "--interval") return false;
  return true;
});

// Returns the latest mtime of any .go file in the stats source dir.
function srcMtime() {
  try {
    return readdirSync(srcDir)
      .filter(f => f.endsWith(".go"))
      .reduce((max, f) => Math.max(max, statSync(path.join(srcDir, f)).mtimeMs), 0);
  } catch { return 0; }
}

let lastBuiltMtime = 0;

function ensureBuilt() {
  const current = srcMtime();
  const binExists = existsSync(binaryPath);
  if (binExists && current <= lastBuiltMtime) return true;

  const result = spawnSync("go", ["build", "-o", binaryPath, "./cmd/staging-stats"], {
    cwd: scraperDir,
    stdio: ["ignore", "ignore", "inherit"],
    env: { ...process.env },
  });
  if (result.error || result.status !== 0) {
    console.error("staging-stats: build failed");
    return false;
  }
  lastBuiltMtime = current;
  return true;
}

function runOnce() {
  if (!ensureBuilt()) return 1;
  const result = spawnSync(binaryPath, goArgs, {
    cwd: scraperDir,
    stdio: "inherit",
    env: { ...process.env },
  });
  if (result.error) console.error(result.error.message);
  return result.status ?? 1;
}

if (once) {
  process.exit(runOnce());
}

// Live watch mode — build first, then loop fast using the binary.
// No alternate screen buffer so you can scroll up to see previous snapshots.
if (!ensureBuilt()) process.exit(1);

process.on("SIGINT", () => process.exit(0));
process.on("SIGTERM", () => process.exit(0));

while (true) {
  process.stdout.write("\x1b[2J\x1b[H"); // clear screen, cursor to top
  process.stdout.write(`\x1b[2m  staging:stats  —  every ${intervalSecs}s  (Ctrl+C to stop)\x1b[0m\n\n`);
  runOnce();
  await new Promise((r) => setTimeout(r, intervalSecs * 1000));
}
