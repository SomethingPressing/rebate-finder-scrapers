#!/usr/bin/env bash
# =============================================================================
# deploy-fly.sh — Build Docker image and deploy to fly.io
#
# Usage:
#   bash scripts/deploy-fly.sh
#
# Prerequisites:
#   - flyctl installed (https://fly.io/docs/hands-on/install-flyctl/)
#   - Logged in: fly auth login
#   - App created: fly apps create incenva-scraper  (or run setup-fly.sh)
#   - Secrets set: see setup-fly.sh
# =============================================================================

set -euo pipefail

GREEN='\033[0;32m'; BLUE='\033[0;34m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BOLD='\033[1m'; NC='\033[0m'
log()  { echo -e "\n${BLUE}[fly-deploy]${NC} ${BOLD}$*${NC}"; }
ok()   { echo -e "  ${GREEN}✔${NC}  $*"; }
warn() { echo -e "  ${YELLOW}⚠${NC}  $*"; }
fail() { echo -e "\n${RED}[error]${NC} $*\n"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
FLY_APP="${FLY_APP:-incenva-scraper}"

cd "$PROJECT_DIR"

command -v fly &>/dev/null || fail "flyctl not found. Install: https://fly.io/docs/hands-on/install-flyctl/"

fly status --app "$FLY_APP" &>/dev/null || fail "App '$FLY_APP' not found on fly.io. Run setup-fly.sh first."

log "1/2  Deploy to fly.io (builds Docker image remotely)"
fly deploy --app "$FLY_APP" --remote-only
ok "Image built and deployed"

log "2/2  Trigger a one-off scrape run to verify"
fly machine run \
  --app "$FLY_APP" \
  --image "registry.fly.io/${FLY_APP}:latest" \
  --env RUN_ONCE=true \
  --restart no \
  2>/dev/null && ok "One-off run started (check logs with: fly logs --app $FLY_APP)" \
  || warn "Could not trigger one-off run — deploy still succeeded. Run manually: fly machine run --app $FLY_APP"

echo ""
echo -e "  ${GREEN}${BOLD}Deploy complete.${NC}"
echo ""
echo "  Useful commands:"
echo "    fly logs --app $FLY_APP              # tail logs"
echo "    fly status --app $FLY_APP            # machine status"
echo "    fly secrets list --app $FLY_APP      # verify secrets"
echo ""
