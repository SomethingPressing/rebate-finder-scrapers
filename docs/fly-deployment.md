# fly.io Deployment

The scraper runs as a multi-tenant background worker on fly.io. A single fly.io app services all tenants; tenant configuration lives in `config/tenants.json`; per-tenant DB connection strings are stored as fly.io secrets.

---

## Prerequisites

```bash
# Install flyctl
curl -L https://fly.io/install.sh | sh

# Log in
fly auth login
```

---

## First-time setup

Use the setup script from the deployment repo:

```bash
bash scripts/scraper/setup-fly.sh
```

Or manually:

### 1. Create the app

```bash
fly apps create incenva-scraper --org personal
```

### 2. Set secrets

```bash
# Required: Rewiring America API key
fly secrets set REWIRING_AMERICA_API_KEY=<your-key> --app incenva-scraper

# Per-tenant DB URLs — one secret per tenant
# Naming convention: TENANT_<ID_UPPER>_DB_URL
fly secrets set TENANT_ACME_DB_URL="postgresql://user:pass@host:5432/dbname" --app incenva-scraper
```

### 3. Configure tenants

Edit `config/tenants.json` and add an entry for each active tenant:

```json
[
  {
    "id": "acme",
    "name": "ACME Corp",
    "active": true,
    "sources": ["dsireusa", "rewiring_america", "energy_star"],
    "db_url_env": "TENANT_ACME_DB_URL",
    "scraper_db_schema": "scraper"
  }
]
```

Fields:
| Field | Description |
|-------|-------------|
| `id` | Lowercase slug. Used in log tags. |
| `name` | Display name for logs. |
| `active` | Set `false` to skip without removing the entry. |
| `sources` | Which scrapers to run. Subset of: `dsireusa`, `rewiring_america`, `energy_star`, `con_edison`, `pnm`, `xcel_energy`, `srp`, `peninsula_clean_energy` |
| `db_url_env` | Name of the fly.io secret that holds this tenant's `DATABASE_URL`. |
| `scraper_db_schema` | PostgreSQL schema for staging tables. Default: `scraper`. |

### 4. Deploy

```bash
bash scripts/deploy-fly.sh
```

---

## Deploying updates

```bash
# Pull latest, update tenants.json if needed, then:
bash scripts/deploy-fly.sh
```

---

## Scheduling

fly.io does not run containers on a built-in timer out of the box. The two options:

### Option A: Scheduled machine (recommended)

After deploying the image, create a scheduled machine that starts every 6 hours:

```bash
fly machine run \
  --app incenva-scraper \
  --image registry.fly.io/incenva-scraper:latest \
  --schedule "0 */6 * * *" \
  --env RUN_ONCE=true \
  --restart no
```

The machine spins up, runs all tenants, and shuts down. fly.io bills only for the runtime.

### Option B: GitHub Actions cron

```yaml
# .github/workflows/scraper.yml
on:
  schedule:
    - cron: "0 */6 * * *"
jobs:
  scrape:
    runs-on: ubuntu-latest
    steps:
      - uses: superfly/flyctl-actions/setup-flyctl@master
      - run: fly machine run --app incenva-scraper --image registry.fly.io/incenva-scraper:latest --env RUN_ONCE=true --restart no
        env:
          FLY_API_TOKEN: ${{ secrets.FLY_API_TOKEN }}
```

---

## Triggering a manual run

```bash
fly machine run \
  --app incenva-scraper \
  --image registry.fly.io/incenva-scraper:latest \
  --env RUN_ONCE=true \
  --restart no
```

Run only one scraper across all tenants:

```bash
fly machine run \
  --app incenva-scraper \
  --image registry.fly.io/incenva-scraper:latest \
  --env RUN_ONCE=true \
  --env SOURCE=dsireusa \
  --restart no
```

---

## Logs

```bash
fly logs --app incenva-scraper
```

---

## Adding a new tenant

1. Provision a Postgres DB for the tenant (Supabase / Neon / bare Postgres)
2. Deploy the Next.js app on the VPS pointing at that DB
3. Add the tenant entry to `config/tenants.json`
4. Set the DB URL secret:
   ```bash
   fly secrets set TENANT_NEWCLIENT_DB_URL="postgres://..." --app incenva-scraper
   ```
5. Commit and deploy:
   ```bash
   git add config/tenants.json
   git commit -m "add tenant: newclient"
   bash scripts/deploy-fly.sh
   ```

---

## Secrets reference

| Secret | Required | Description |
|--------|----------|-------------|
| `REWIRING_AMERICA_API_KEY` | Yes (if using rewiring_america source) | API key from rewiringamerica.org |
| `TENANT_<ID>_DB_URL` | Yes, one per tenant | PostgreSQL connection string for that tenant |
| `SCRAPER_PROXY_URL` | No | HTTP/SOCKS5 proxy for scrapers blocked by CDN/WAF |
