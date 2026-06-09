#!/usr/bin/env node
/**
 * sync-tenants.mjs — Syncs config/tenants.json into the clients table.
 *
 * For each active tenant in tenants.json:
 *   - Upserts the client row (id + name) — safe to re-run, never overwrites
 *     fields customised in the DB.
 *   - The DB trigger auto-creates scraper_source_configs rows for any new client.
 *
 * Usage:
 *   pnpm sync:tenants
 *
 * Cron example (every hour):
 *   0 * * * * cd /path/to/rebate-finder-scrapers && pnpm sync:tenants >> /var/log/sync-tenants.log 2>&1
 *
 * Env:
 *   DATABASE_URL  — postgres connection string (read from .env if not set)
 *   TENANTS_FILE  — override path to tenants.json (default: config/tenants.json)
 */

import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const __dirname = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(__dirname, "..");

// Load .env
const envPath = path.join(root, ".env");
try {
  const envContent = readFileSync(envPath, "utf8");
  for (const line of envContent.split("\n")) {
    const match = line.match(/^([^#=]+)=(.*)$/);
    if (match) process.env[match[1].trim()] ??= match[2].trim().replace(/^["']|["']$/g, "");
  }
} catch { /* rely on environment variables already set */ }

const { default: pg } = await import("pg");
const { Client } = pg;

const TENANTS_FILE =
  process.env.TENANTS_FILE ?? path.join(root, "config", "tenants.json");

const DATABASE_URL = process.env.DATABASE_URL;
if (!DATABASE_URL) {
  console.error("DATABASE_URL is not set");
  process.exit(1);
}

async function main() {
  let tenants;
  try {
    tenants = JSON.parse(readFileSync(TENANTS_FILE, "utf8"));
  } catch {
    console.error(`Could not read tenants file at: ${TENANTS_FILE}`);
    process.exit(1);
  }

  const active = tenants.filter((t) => t.active !== false);
  console.log(`sync-tenants: found ${active.length} active tenant(s) in ${TENANTS_FILE}`);

  const client = new Client({ connectionString: DATABASE_URL });
  await client.connect();

  let created = 0;
  let skipped = 0;

  for (const tenant of active) {
    if (!tenant.id || !tenant.name) {
      console.warn(`  SKIP  tenant missing id or name:`, tenant);
      skipped++;
      continue;
    }

    const res = await client.query(
      `INSERT INTO clients (id, name, created_at)
       VALUES ($1, $2, NOW())
       ON CONFLICT (id) DO NOTHING
       RETURNING id`,
      [tenant.id, tenant.name]
    );

    if (res.rowCount > 0) {
      console.log(`  CREATED  ${tenant.id}  "${tenant.name}"`);
      created++;
    } else {
      console.log(`  EXISTS   ${tenant.id}`);
      skipped++;
    }
  }

  await client.end();
  console.log(`\nDone. ${created} created, ${skipped} already existed.`);
}

main().catch((err) => { console.error(err); process.exit(1); });
