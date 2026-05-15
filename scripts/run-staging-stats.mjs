#!/usr/bin/env node
/**
 * Live-updating staging stats — refreshes every 3 seconds until Ctrl+C.
 *
 * Usage:
 *   pnpm staging:stats                  # live watch, refresh every 3s
 *   pnpm staging:stats -- --once        # run once and exit
 *   pnpm staging:stats -- --interval 5  # custom refresh interval (seconds)
 *   pnpm staging:stats -- --once --json # one-shot JSON output
 */
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const scraperDir = path.join(__dirname, "..");

const args = process.argv.slice(2);
const once = args.includes("--once");
const intervalIdx = args.indexOf("--interval");
const intervalSecs = intervalIdx !== -1 ? (parseInt(args[intervalIdx + 1], 10) || 3) : 3;

// Forward args to Go binary (strip our own flags).
const goArgs = args.filter((a, i) => {
  if (a === "--once" || a === "--interval") return false;
  if (i > 0 && args[i - 1] === "--interval") return false;
  return true;
});

function runOnce() {
  const result = spawnSync("go", ["run", "./cmd/staging-stats", ...goArgs], {
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

// Live watch mode (default).
process.stdout.write("\x1b[?1049h"); // enter alternate screen buffer
const restore = () => { process.stdout.write("\x1b[?1049l"); process.exit(0); };
process.on("SIGINT", restore);
process.on("SIGTERM", restore);

while (true) {
  process.stdout.write("\x1b[H\x1b[2J"); // clear + cursor home
  console.log(`\x1b[2m  staging:stats  —  every ${intervalSecs}s  (Ctrl+C to stop)\x1b[0m\n`);
  runOnce();
  await new Promise((r) => setTimeout(r, intervalSecs * 1000));
}
