# Deployment

## Options

| Mode | Best for |
|------|----------|
| [PM2 on Ubuntu VPS](#pm2-on-ubuntu-vps) | Production — same server as the consumer app |
| [Docker / docker-compose](#docker--docker-compose) | Isolated container deployments |
| [Cron / systemd](#cron--systemd) | Lightweight, no process manager |

The scraper shares the same PostgreSQL database as the consumer app (`rebate-finder`). Both read from the same `DATABASE_URL`.

For full server prerequisites (Node, Go, PostgreSQL, Nginx, SSL), see the consumer app's [deployment guide](../../rebate-finder/docs/deployment.md).

---

## PM2 on Ubuntu VPS

### 1. Clone and build

```bash
# as rf user
cd ~
git clone <repository-url> rebate-finder-scrapers
cd rebate-finder-scrapers

# Build the scraper binary
go build -o bin/scraper ./cmd/scraper
go build -o bin/pdf-scraper ./cmd/pdf-scraper
```

### 2. Configure environment

```bash
cp .env.example .env
nano .env   # set DATABASE_URL and any API keys
```

### 3. Start with PM2

```bash
# Scheduled mode (runs every 6 hours)
pm2 start bin/scraper \
  --name "Incenva Scraper" \
  --interpreter none \
  --env-file .env

# Check it's running
pm2 status
pm2 logs "Incenva Scraper" --lines 30
```

### 4. Save PM2 state so it survives reboots

```bash
pm2 save
# If pm2 startup hasn't been run yet on this server:
pm2 startup   # copy and run the printed command as root
```

### 5. Deploy updates

```bash
cd ~/rebate-finder-scrapers
git pull
go build -o bin/scraper ./cmd/scraper
pm2 restart "Incenva Scraper"
```

### Running the PDF scraper manually

The PDF scraper is a one-shot tool — run it manually whenever the PDFs are updated:

```bash
cd ~/rebate-finder-scrapers
LOG_FORMAT=console ./bin/pdf-scraper \
  --catalog  /path/to/Consumers_Energy_Incentive_Catalog.pdf \
  --application /path/to/Incentive-Application.pdf
```

Then promote the new staging rows:

```bash
cd ~/rebate-finder
pnpm scraper:promote
```

---

## Docker / docker-compose

### Build the image

```bash
docker build -t incenva-scraper .
```

### Run once

```bash
docker run --rm \
  --env-file .env \
  -e RUN_ONCE=true \
  incenva-scraper
```

### Scheduled (docker-compose)

```yaml
# docker-compose.yml
services:
  scraper:
    build: .
    env_file: .env
    environment:
      RUN_ONCE: "false"
      SCRAPER_INTERVAL: "@every 6h"
    restart: unless-stopped
    depends_on:
      - db

  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: rebate_finder
      POSTGRES_USER: rf
      POSTGRES_PASSWORD: changeme
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
```

```bash
docker-compose up -d
docker-compose logs -f scraper
```

### Override PDF paths in Docker

```bash
docker run --rm \
  --env-file .env \
  -v /host/path/to/pdfs:/pdfs \
  -e CONSUMERS_ENERGY_CATALOG_PDF=/pdfs/catalog.pdf \
  -e CONSUMERS_ENERGY_APPLICATION_PDF=/pdfs/application.pdf \
  --entrypoint /pdf-scraper \
  incenva-scraper
```

> **Note:** The Dockerfile only builds `cmd/scraper`. To run the PDF scraper in Docker you need to modify the Dockerfile to also build `cmd/pdf-scraper`, or run it locally.

---

## Cron / systemd

### Cron (simplest)

```bash
# Edit rf user's crontab
crontab -e

# Run all scrapers at 2 AM daily
0 2 * * * cd /home/rf/rebate-finder-scrapers && RUN_ONCE=true /home/rf/rebate-finder-scrapers/bin/scraper >> /var/log/incenva-scraper.log 2>&1
```

### systemd service + timer

Create `/etc/systemd/system/incenva-scraper.service`:

```ini
[Unit]
Description=Incenva Rebate Scraper
After=network.target postgresql.service

[Service]
Type=oneshot
User=rf
WorkingDirectory=/home/rf/rebate-finder-scrapers
EnvironmentFile=/home/rf/rebate-finder-scrapers/.env
ExecStart=/home/rf/rebate-finder-scrapers/bin/scraper
StandardOutput=journal
StandardError=journal
```

Create `/etc/systemd/system/incenva-scraper.timer`:

```ini
[Unit]
Description=Run Incenva Scraper every 6 hours

[Timer]
OnBootSec=5min
OnUnitActiveSec=6h

[Install]
WantedBy=timers.target
```

Enable and start:

```bash
# run as root
systemctl daemon-reload
systemctl enable --now incenva-scraper.timer

# Check status
systemctl status incenva-scraper.timer
journalctl -u incenva-scraper -n 50
```

---

## Environment Variables for Production

| Variable | Value |
|----------|-------|
| `DATABASE_URL` | Same as consumer app's `DATABASE_URL` |
| `RUN_ONCE` | `false` for PM2/systemd scheduled; `true` for cron |
| `SCRAPER_INTERVAL` | `@every 6h` or cron expression e.g. `0 2 * * *` |
| `LOG_FORMAT` | `json` (default) for structured logs in production |
| `LOG_LEVEL` | `info` |
| `SCRAPER_VERSION` | Bump this when you deploy a new version |
| `REWIRING_AMERICA_API_KEY` | Required if running Rewiring America scraper |

---

## Checking Logs

```bash
# PM2
pm2 logs "Incenva Scraper" --lines 100

# systemd
journalctl -u incenva-scraper -n 100 --no-pager

# Docker
docker-compose logs -f scraper
```
