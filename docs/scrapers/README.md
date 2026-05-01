# Scrapers Reference

## Overview

All scrapers implement the `scrapers.Scraper` interface:

```go
type Scraper interface {
    Name()  string                          // stable identifier, e.g. "dsireusa"
    Scrape(ctx context.Context) ([]models.Incentive, error)
}
```

Scrapers are registered in `cmd/scraper/main.go` and run sequentially by `scrapers.RunAll()`.

---

## Scraper Index

| Scraper | Source ID | Type | State(s) | File |
|---------|-----------|------|----------|------|
| DSIRE USA | `dsireusa` | REST API | All 50 states | [dsireusa.md](dsireusa.md) |
| Rewiring America | `rewiring_america` | REST API | All 50 states | [rewiring-america.md](rewiring-america.md) |
| Energy Star | `energy_star` | HTML scraper | Nationwide | [energy-star.md](energy-star.md) |
| Con Edison | `con_edison` | Sitemap + HTML | NY | [con-edison.md](con-edison.md) |
| PNM | `pnm` | Sitemap + HTML | NM | [pnm.md](pnm.md) |
| Xcel Energy | `xcel_energy` | Sitemap + HTML | CO, MN, WI, ND, SD, NM | [xcel-energy.md](xcel-energy.md) |
| SRP | `srp` | Sitemap + HTML | AZ | [srp.md](srp.md) |
| Peninsula Clean Energy | `peninsula_clean_energy` | Sitemap + HTML | CA | [peninsula-clean-energy.md](peninsula-clean-energy.md) |

---

## Shared Infrastructure

| Topic | File |
|-------|------|
| HTML helpers, category inference, sitemap parser, amount parsing, PDF extraction | [shared.md](shared.md) |

---

## Running Scrapers

```bash
# Direct (Go)
SOURCE=dsireusa                RUN_ONCE=true go run ./cmd/scraper
SOURCE=rewiring_america        RUN_ONCE=true go run ./cmd/scraper
SOURCE=energy_star             RUN_ONCE=true go run ./cmd/scraper
SOURCE=con_edison              RUN_ONCE=true go run ./cmd/scraper
SOURCE=pnm                     RUN_ONCE=true go run ./cmd/scraper
SOURCE=xcel_energy             RUN_ONCE=true go run ./cmd/scraper
SOURCE=srp                     RUN_ONCE=true go run ./cmd/scraper
SOURCE=peninsula_clean_energy  RUN_ONCE=true go run ./cmd/scraper

# Makefile shortcuts
make scrape           # all sources
make scrape-coned     # Con Edison only
make scrape-pnm       # PNM only
make scrape-xcel      # Xcel Energy only
make scrape-srp       # SRP only
make scrape-pce       # Peninsula Clean Energy only

# pnpm helpers
pnpm run:dsireusa
pnpm run:rewiring_america
pnpm run:energy_star
pnpm run:con_edison
pnpm run:pnm
pnpm run:xcel_energy
pnpm run:srp
pnpm run:peninsula_clean_energy
```

---

## Adding a New Scraper

See [adding-a-scraper.md](../adding-a-scraper.md) for a step-by-step guide.
