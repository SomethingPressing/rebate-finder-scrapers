# =============================================================================
# Incenva Scraper Service — Makefile
#
# Usage:
#   make <target>
#
# All targets load config from .env automatically (via the Go config package).
# =============================================================================

.PHONY: help build scrape promote promote-dry stats pdf-scrape clean

# ── Default target ────────────────────────────────────────────────────────────
help:
	@echo ""
	@echo "  Incenva Scraper Service"
	@echo ""
	@echo "  Build"
	@echo "    make build           Build all binaries into bin/"
	@echo ""
	@echo "  Scraping"
	@echo "    make scrape          Run the scraper (all sources, RUN_ONCE=true)"
	@echo "    make pdf-scrape      Run the PDF scraper"
	@echo ""
	@echo "  Promotion"
	@echo "    make promote-dry     Preview pending staging rows (no writes)"
	@echo "    make promote         Promote pending rows to public.rebates"
	@echo ""
	@echo "  Diagnostics"
	@echo "    make stats           Print staging table summary"
	@echo ""
	@echo "  Maintenance"
	@echo "    make clean           Remove compiled binaries"
	@echo ""

# ── Build ─────────────────────────────────────────────────────────────────────
build:
	@mkdir -p bin
	go build -o bin/scraper       ./cmd/scraper
	go build -o bin/promoter      ./cmd/promoter
	go build -o bin/staging-stats ./cmd/staging-stats
	@if [ -d cmd/pdf-scraper ]; then go build -o bin/pdf-scraper ./cmd/pdf-scraper; fi
	@echo "✔  All binaries built in bin/"

# ── Scraping ──────────────────────────────────────────────────────────────────
scrape: bin/scraper
	RUN_ONCE=true ./bin/scraper

pdf-scrape: bin/pdf-scraper
	./bin/pdf-scraper

# ── Promotion ─────────────────────────────────────────────────────────────────
promote-dry: bin/promoter
	./bin/promoter --dry-run

promote: bin/promoter
	./bin/promoter

# ── Diagnostics ───────────────────────────────────────────────────────────────
stats: bin/staging-stats
	./bin/staging-stats

# ── Maintenance ───────────────────────────────────────────────────────────────
clean:
	rm -rf bin/
	@echo "✔  bin/ removed"

# ── Auto-build rules (build binary only if source is newer) ──────────────────
bin/scraper: $(shell find cmd/scraper db models config -name '*.go' 2>/dev/null)
	@mkdir -p bin
	go build -o bin/scraper ./cmd/scraper

bin/promoter: $(shell find cmd/promoter db models config -name '*.go' 2>/dev/null)
	@mkdir -p bin
	go build -o bin/promoter ./cmd/promoter

bin/staging-stats: $(shell find cmd/staging-stats db models config -name '*.go' 2>/dev/null)
	@mkdir -p bin
	go build -o bin/staging-stats ./cmd/staging-stats

bin/pdf-scraper: $(shell find cmd/pdf-scraper db models config -name '*.go' 2>/dev/null)
	@mkdir -p bin
	go build -o bin/pdf-scraper ./cmd/pdf-scraper
