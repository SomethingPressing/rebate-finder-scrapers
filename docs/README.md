# Incenva Scraper Service — Documentation

This folder contains all technical documentation for the `rebate-finder-scrapers` service.

| File | Contents |
|------|----------|
| [architecture.md](architecture.md) | System design, data flow, and key design decisions |
| [getting-started.md](getting-started.md) | Local setup, environment variables, running scrapers |
| [scrapers.md](scrapers.md) | Every scraper — how each one works, what it fetches, configuration |
| [pdf-scraper.md](pdf-scraper.md) | PDF extraction deep-dive: flags, page offsets, adding new measures |
| [database.md](database.md) | Schema reference for `rebates_staging` and `pdf_scrape_raw` |
| [staging-and-promotion.md](staging-and-promotion.md) | Full staging → review → promotion lifecycle |
| [deployment.md](deployment.md) | Production deployment (Ubuntu VPS, PM2, Docker) |
| [adding-a-scraper.md](adding-a-scraper.md) | Step-by-step guide to implementing a new scraper |
