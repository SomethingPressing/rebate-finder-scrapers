#!/usr/bin/env bash
# =============================================================================
# deploy.sh — Pull latest code, rebuild binaries, and restart Incenva Scraper
#
# Usage (from project root, as the app user or root):
#   bash scripts/deploy.sh
#
# Safe to run multiple times.
# =============================================================================

set -euo pipefail

GREEN='\033[0;32m'; BLUE='\033[0;34m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BOLD='\033[1m'; NC='\033[0m'
log()  { echo -e "\n${BLUE}[deploy]${NC} ${BOLD}$*${NC}"; }
ok()   { echo -e "  ${GREEN}✔${NC}  $*"; }
warn() { echo -e "  ${YELLOW}⚠${NC}  $*"; }
fail() { echo -e "\n${RED}[error]${NC} $*\n"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
ENV_FILE="$PROJECT_DIR/.env"
PM2_APP_NAME="${PM2_APP_NAME:-Incenva Scraper}"

cd "$PROJECT_DIR"

[[ -f "$ENV_FILE" ]] || fail ".env not found. Run setup-server.sh first."

export PATH="$PATH:/usr/local/go/bin"
command -v go &>/dev/null || fail "go not found in PATH. Is Go installed at /usr/local/go?"

log "1/4  git pull"
git pull
ok "Code updated"

log "2/4  go mod download"
go mod download 2>&1 | tail -3
ok "Modules up to date"

log "3/4  Build binaries"
go build -o bin/scraper ./cmd/scraper
ok "Built: bin/scraper"
go build -o bin/pdf-scraper ./cmd/pdf-scraper
ok "Built: bin/pdf-scraper"

log "4/4  PM2 restart"
if pm2 list 2>/dev/null | grep -q "$PM2_APP_NAME"; then
  pm2 restart "$PM2_APP_NAME"
  ok "Restarted '$PM2_APP_NAME'"
else
  warn "PM2 process '$PM2_APP_NAME' not found — start it with setup-server.sh"
fi

echo ""
echo -e "  ${GREEN}${BOLD}Deploy complete.${NC}"
echo ""
