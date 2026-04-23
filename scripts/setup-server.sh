#!/usr/bin/env bash
# =============================================================================
# setup-server.sh — Idempotent server setup for Incenva Rebate Scraper
#
# Installs Go, builds the scraper binaries, writes .env, and registers the
# scheduled scraper with PM2 (or systemd — see comments at end).
#
# Prerequisites:
#   • The rebate-finder consumer app must already be set up (shared DB).
#   • Run setup-server.sh in rebate-finder first, or at least have DATABASE_URL.
#
# Usage:
#   sudo bash scripts/setup-server.sh
#
# Safe to run multiple times — every step checks if work is already done.
# =============================================================================

set -euo pipefail
IFS=$'\n\t'

# ── Color helpers ─────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'

log()  { echo -e "\n${BLUE}[setup]${NC} ${BOLD}$*${NC}"; }
ok()   { echo -e "  ${GREEN}✔${NC}  $*"; }
skip() { echo -e "  ${YELLOW}─${NC}  $* (already done)"; }
warn() { echo -e "  ${YELLOW}⚠${NC}  $*"; }
fail() { echo -e "\n${RED}[error]${NC} $*\n"; exit 1; }
hr()   { echo -e "${BLUE}────────────────────────────────────────────────────${NC}"; }

# ── Paths ─────────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
ENV_FILE="$PROJECT_DIR/.env"
BIN_DIR="$PROJECT_DIR/bin"

# ── Configurable defaults ─────────────────────────────────────────────────────
APP_USER="${APP_USER:-rf}"
APP_GROUP="${APP_GROUP:-rf}"
GO_VERSION="${GO_VERSION:-1.22.3}"
GO_ARCH="${GO_ARCH:-linux/amd64}"
PM2_APP_NAME="${PM2_APP_NAME:-Incenva Scraper}"
CONSUMER_APP_DIR="${CONSUMER_APP_DIR:-$HOME/rebate-finder}"

# ── Root guard ────────────────────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || fail "Run as root: sudo bash $0"

hr
echo ""
echo -e "  ${BOLD}Incenva Rebate Scraper — Server Setup${NC}"
echo ""
hr

# ─────────────────────────────────────────────────────────────────────────────
# STEP 1 — System user/group (may already exist from consumer app setup)
# ─────────────────────────────────────────────────────────────────────────────
log "1/6  System group and user"

if getent group "$APP_GROUP" &>/dev/null; then
  skip "Group '$APP_GROUP'"
else
  groupadd --system "$APP_GROUP"
  ok "Created group '$APP_GROUP'"
fi

if id "$APP_USER" &>/dev/null; then
  skip "User '$APP_USER'"
else
  useradd \
    --system \
    --gid "$APP_GROUP" \
    --shell /bin/bash \
    --home-dir "/home/$APP_USER" \
    --create-home \
    "$APP_USER"
  ok "Created user '$APP_USER'"
fi

# ─────────────────────────────────────────────────────────────────────────────
# STEP 2 — Go
# ─────────────────────────────────────────────────────────────────────────────
log "2/6  Go $GO_VERSION"

GO_INSTALLED_VER="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//' || true)"

if [[ "$GO_INSTALLED_VER" == "$GO_VERSION" ]]; then
  skip "Go $GO_VERSION"
else
  ARCH="${GO_ARCH//\//-}"  # linux/amd64 → linux-amd64
  GO_TARBALL="go${GO_VERSION}.${ARCH}.tar.gz"
  GO_URL="https://go.dev/dl/${GO_TARBALL}"

  ok "Downloading Go $GO_VERSION…"
  curl -fsSL "$GO_URL" -o "/tmp/$GO_TARBALL"

  # Remove any existing Go installation
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/$GO_TARBALL"
  rm -f "/tmp/$GO_TARBALL"

  # Add to PATH for root and the app user
  GO_PROFILE_LINE='export PATH=$PATH:/usr/local/go/bin'
  for PROFILE_FILE in /etc/profile.d/go.sh "/home/$APP_USER/.bashrc"; do
    if ! grep -qF "$GO_PROFILE_LINE" "$PROFILE_FILE" 2>/dev/null; then
      echo "$GO_PROFILE_LINE" >> "$PROFILE_FILE"
    fi
  done

  export PATH="$PATH:/usr/local/go/bin"
  ok "Installed Go $(go version | awk '{print $3}')"
fi

# Ensure go binary is in PATH for subsequent commands
export PATH="$PATH:/usr/local/go/bin"
command -v go &>/dev/null || fail "go binary not found. Check /usr/local/go/bin is in PATH."

# ─────────────────────────────────────────────────────────────────────────────
# STEP 3 — PM2 (needed to register the scraper process)
# ─────────────────────────────────────────────────────────────────────────────
log "3/6  PM2"

if command -v pm2 &>/dev/null; then
  skip "PM2 $(pm2 --version 2>/dev/null || echo 'installed')"
else
  command -v npm &>/dev/null || fail "npm not found. Install Node.js first (run rebate-finder/scripts/setup-server.sh)."
  npm install -g pm2 >/dev/null
  ok "Installed PM2"
fi

# ─────────────────────────────────────────────────────────────────────────────
# STEP 4 — .env file
# ─────────────────────────────────────────────────────────────────────────────
log "4/6  Environment file (.env)"

if [[ -f "$ENV_FILE" ]]; then
  skip ".env already exists — not overwriting"
  warn "To re-generate .env, delete it and re-run this script."
else
  cp "$PROJECT_DIR/.env.example" "$ENV_FILE"

  # Try to inherit DATABASE_URL from the consumer app's .env
  CONSUMER_ENV="$CONSUMER_APP_DIR/.env"
  if [[ -f "$CONSUMER_ENV" ]]; then
    INHERITED_DB_URL="$(grep -E '^DATABASE_URL=' "$CONSUMER_ENV" | head -1 || true)"
    if [[ -n "$INHERITED_DB_URL" ]]; then
      sed -i "s|^DATABASE_URL=.*|$INHERITED_DB_URL|" "$ENV_FILE"
      ok "DATABASE_URL inherited from consumer app .env"
    fi
  else
    warn "Consumer app .env not found at $CONSUMER_ENV"
    warn "Set DATABASE_URL in $ENV_FILE manually (same value as rebate-finder)."
  fi

  chown "$APP_USER:$APP_GROUP" "$ENV_FILE"
  chmod 640 "$ENV_FILE"
  ok "Created $ENV_FILE"
  warn "Review $ENV_FILE and set REWIRING_AMERICA_API_KEY and any other keys."
fi

# Validate DATABASE_URL
# shellcheck disable=SC2046
export $(grep -v '^#' "$ENV_FILE" | grep -E '^DATABASE_URL=' | xargs 2>/dev/null || true)
[[ -n "${DATABASE_URL:-}" ]] || fail "DATABASE_URL is not set in $ENV_FILE. Edit it and re-run."

# ─────────────────────────────────────────────────────────────────────────────
# STEP 5 — Build Go binaries
# ─────────────────────────────────────────────────────────────────────────────
log "5/6  Build Go binaries"

mkdir -p "$BIN_DIR"
chown -R "$APP_USER:$APP_GROUP" "$PROJECT_DIR"

cd "$PROJECT_DIR"

# Download / tidy modules
ok "Downloading Go modules…"
sudo -u "$APP_USER" bash -c "export PATH=\$PATH:/usr/local/go/bin; go mod download" 2>&1 | tail -3

# Build main scraper
ok "Building cmd/scraper…"
sudo -u "$APP_USER" bash -c "export PATH=\$PATH:/usr/local/go/bin; go build -o bin/scraper ./cmd/scraper"
ok "Built: bin/scraper"

# Build PDF scraper
ok "Building cmd/pdf-scraper…"
sudo -u "$APP_USER" bash -c "export PATH=\$PATH:/usr/local/go/bin; go build -o bin/pdf-scraper ./cmd/pdf-scraper"
ok "Built: bin/pdf-scraper"

# ─────────────────────────────────────────────────────────────────────────────
# STEP 6 — PM2 process
# ─────────────────────────────────────────────────────────────────────────────
log "6/6  PM2 process '$PM2_APP_NAME'"

# Check if process already registered
if sudo -u "$APP_USER" pm2 list 2>/dev/null | grep -q "$PM2_APP_NAME"; then
  ok "Restarting existing PM2 process…"
  sudo -u "$APP_USER" pm2 restart "$PM2_APP_NAME"
  ok "Restarted '$PM2_APP_NAME'"
else
  sudo -u "$APP_USER" bash -c "
    cd '$PROJECT_DIR'
    pm2 start bin/scraper \
      --name '$PM2_APP_NAME' \
      --interpreter none \
      --env-file '$ENV_FILE'
  "
  ok "Started PM2 process '$PM2_APP_NAME'"
fi

sudo -u "$APP_USER" pm2 save >/dev/null

# Configure PM2 startup on boot
STARTUP_CMD="$(sudo -u "$APP_USER" pm2 startup systemd \
  -u "$APP_USER" --hp "/home/$APP_USER" 2>/dev/null | grep '^sudo' | head -1 || true)"
if [[ -n "$STARTUP_CMD" ]]; then
  eval "$STARTUP_CMD" >/dev/null 2>&1 || true
fi
ok "PM2 startup configured"

# ─────────────────────────────────────────────────────────────────────────────
# Done
# ─────────────────────────────────────────────────────────────────────────────
hr
echo ""
echo -e "  ${GREEN}${BOLD}Setup complete!${NC}"
echo ""
echo "  PM2 status:  pm2 status"
echo "  Logs:        pm2 logs '$PM2_APP_NAME'"
echo "  Run once:    RUN_ONCE=true ./bin/scraper"
echo "  PDF scraper: ./bin/pdf-scraper --catalog /path/to/catalog.pdf --application /path/to/app.pdf"
echo ""
echo -e "  ${YELLOW}Remember to set in $ENV_FILE:${NC}"
echo "    • REWIRING_AMERICA_API_KEY"
echo "    • CONSUMERS_ENERGY_CATALOG_PDF / CONSUMERS_ENERGY_APPLICATION_PDF (for PDF scraper)"
echo ""
echo "  After any .env changes:"
echo "    pm2 restart '$PM2_APP_NAME'"
echo ""
hr
