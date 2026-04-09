#!/bin/bash
set -e

# ═══════════════════════════════════════════════════════════════
# STUN Max — One-Click Server Deployment
# ═══════════════════════════════════════════════════════════════
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/uk0/stun_max/main/install.sh | bash
#   # or
#   wget -qO- https://raw.githubusercontent.com/uk0/stun_max/main/install.sh | bash
#
# Options (env vars):
#   STUN_MAX_PASSWORD=xxx    Set dashboard password (default: auto-generated)
#   STUN_MAX_PORT=8080       Signal server port (default: 8080)
#   STUN_MAX_STUN_PORT=3478  STUN server port (default: 3478)
#   STUN_MAX_VERSION=latest  Release version (default: latest)
# ═══════════════════════════════════════════════════════════════

REPO="uk0/stun_max"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="/opt/stun_max"
WEB_DIR="${DATA_DIR}/web"
DB_FILE="${DATA_DIR}/stun_max.db"
LOG_DIR="/var/log"

PORT="${STUN_MAX_PORT:-8080}"
STUN_PORT="${STUN_MAX_STUN_PORT:-3478}"
STUN_HTTP_PORT="$((STUN_PORT + 1))"
VERSION="${STUN_MAX_VERSION:-latest}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${CYAN}[INFO]${NC} $1"; }
ok()    { echo -e "${GREEN}[OK]${NC} $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# ─── Pre-flight checks ───────────────────────────────────────

echo ""
echo -e "${BOLD}${CYAN}╔══════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}${CYAN}║${NC}  ${BOLD}⚡ STUN Max — Server Deployment${NC}             ${BOLD}${CYAN}║${NC}"
echo -e "${BOLD}${CYAN}╚══════════════════════════════════════════════╝${NC}"
echo ""

[ "$(id -u)" -ne 0 ] && error "Please run as root: sudo bash install.sh"

ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) error "Unsupported architecture: $ARCH" ;;
esac
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
[ "$OS" != "linux" ] && error "Only Linux is supported (got: $OS)"

info "Platform: ${OS}/${ARCH}"

# Check for required tools
for cmd in curl tar systemctl; do
    command -v $cmd &>/dev/null || error "Required command not found: $cmd"
done

# ─── Determine version ───────────────────────────────────────

if [ "$VERSION" = "latest" ]; then
    info "Fetching latest release..."
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    if [ -z "$VERSION" ]; then
        warn "No GitHub release found, downloading from main branch..."
        USE_MAIN=true
    else
        info "Latest release: ${VERSION}"
    fi
fi

# ─── Download binaries ───────────────────────────────────────

mkdir -p "$DATA_DIR" "$WEB_DIR"

if [ "${USE_MAIN:-false}" = true ]; then
    # Download pre-built binaries from release artifacts or build from source
    BASE_URL="https://github.com/${REPO}/raw/main/build"

    info "Downloading server binary..."
    curl -fsSL "https://github.com/${REPO}/releases/download/${VERSION:-v1.0.0}/stun_max-server-linux-${ARCH}" -o "${INSTALL_DIR}/stun_max-server" 2>/dev/null || {
        warn "Release download failed, trying alternative..."
        # Fallback: check if binaries exist locally
        error "Cannot download binaries. Please build manually: ./build.sh && sudo cp build/stun_max-server-linux-amd64 /usr/local/bin/stun_max-server"
    }
else
    RELEASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

    info "Downloading stun_max-server..."
    curl -fsSL "${RELEASE_URL}/stun_max-server-linux-${ARCH}" -o "${INSTALL_DIR}/stun_max-server" || \
        error "Failed to download server binary from ${RELEASE_URL}"

    info "Downloading stun_max-stunserver..."
    curl -fsSL "${RELEASE_URL}/stun_max-stunserver-linux-${ARCH}" -o "${INSTALL_DIR}/stun_max-stunserver" || \
        warn "STUN server download failed (optional)"

    info "Downloading web dashboard..."
    for f in index.html dashboard.js style.css; do
        curl -fsSL "https://raw.githubusercontent.com/${REPO}/${VERSION}/web/${f}" -o "${WEB_DIR}/${f}" 2>/dev/null || \
        curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/web/${f}" -o "${WEB_DIR}/${f}" 2>/dev/null || \
            warn "Failed to download web/${f}"
    done

    info "Downloading ip2region database..."
    curl -fsSL "${RELEASE_URL}/ip2region.xdb" -o "${DATA_DIR}/ip2region.xdb" 2>/dev/null || \
        warn "IP database download failed (optional, IP geolocation will be disabled)"
fi

chmod +x "${INSTALL_DIR}/stun_max-server" 2>/dev/null
chmod +x "${INSTALL_DIR}/stun_max-stunserver" 2>/dev/null

ok "Binaries installed"

# ─── Generate password ────────────────────────────────────────

if [ -n "$STUN_MAX_PASSWORD" ]; then
    PASSWORD="$STUN_MAX_PASSWORD"
    info "Using provided password"
else
    PASSWORD=$(head -c 32 /dev/urandom | md5sum | head -c 32)
    info "Generated dashboard password"
fi

# ─── Create systemd services ─────────────────────────────────

info "Creating systemd services..."

# Signal Server
cat > /etc/systemd/system/stun-max.service << EOF
[Unit]
Description=STUN Max Signal Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${DATA_DIR}
ExecStart=${INSTALL_DIR}/stun_max-server \\
    --addr :${PORT} \\
    --web-password ${PASSWORD} \\
    --web-dir ${WEB_DIR} \\
    --db ${DB_FILE} \\
    --ipdb ${DATA_DIR}/ip2region.xdb \\
    --stun-http http://127.0.0.1:${STUN_HTTP_PORT}
Restart=always
RestartSec=3
LimitNOFILE=65536

# Logging
StandardOutput=append:${LOG_DIR}/stun_max.log
StandardError=append:${LOG_DIR}/stun_max.log

[Install]
WantedBy=multi-user.target
EOF

# STUN Server
cat > /etc/systemd/system/stun-max-stun.service << EOF
[Unit]
Description=STUN Max STUN Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${INSTALL_DIR}/stun_max-stunserver \\
    --addr :${STUN_PORT} \\
    --http :${STUN_HTTP_PORT}
Restart=always
RestartSec=3

# Logging
StandardOutput=append:${LOG_DIR}/stun_max_stun.log
StandardError=append:${LOG_DIR}/stun_max_stun.log

[Install]
WantedBy=multi-user.target
EOF

ok "Systemd services created"

# ─── Enable and start services ────────────────────────────────

info "Starting services..."

systemctl daemon-reload

# Stop existing services if running
systemctl stop stun-max 2>/dev/null || true
systemctl stop stun-max-stun 2>/dev/null || true

# Also kill any manual instances
fuser -k ${PORT}/tcp 2>/dev/null || true
fuser -k ${STUN_PORT}/udp 2>/dev/null || true
sleep 1

systemctl enable stun-max stun-max-stun
systemctl start stun-max-stun
sleep 1
systemctl start stun-max
sleep 2

# Verify
if systemctl is-active --quiet stun-max; then
    ok "Signal server started"
else
    error "Signal server failed to start. Check: journalctl -u stun-max"
fi

if systemctl is-active --quiet stun-max-stun; then
    ok "STUN server started"
else
    warn "STUN server failed to start (optional)"
fi

# ─── Detect server IP ─────────────────────────────────────────

SERVER_IP=$(curl -4 -fsSL ifconfig.me 2>/dev/null || curl -4 -fsSL icanhazip.com 2>/dev/null || hostname -I | awk '{print $1}')

# ─── Firewall hints ──────────────────────────────────────────

if command -v ufw &>/dev/null; then
    info "Configuring UFW firewall..."
    ufw allow ${PORT}/tcp comment "STUN Max Signal" 2>/dev/null || true
    ufw allow ${STUN_PORT}/udp comment "STUN Max STUN" 2>/dev/null || true
    ok "Firewall rules added"
elif command -v firewall-cmd &>/dev/null; then
    info "Configuring firewalld..."
    firewall-cmd --permanent --add-port=${PORT}/tcp 2>/dev/null || true
    firewall-cmd --permanent --add-port=${STUN_PORT}/udp 2>/dev/null || true
    firewall-cmd --reload 2>/dev/null || true
    ok "Firewall rules added"
else
    warn "No firewall detected. Make sure ports ${PORT}/tcp and ${STUN_PORT}/udp are open."
fi

# ─── Print summary ───────────────────────────────────────────

echo ""
echo -e "${BOLD}${CYAN}╔══════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}${CYAN}║${NC}  ${BOLD}${GREEN}✓ STUN Max Deployed Successfully${NC}            ${BOLD}${CYAN}║${NC}"
echo -e "${BOLD}${CYAN}╚══════════════════════════════════════════════╝${NC}"
echo ""
echo -e "  ${BOLD}Dashboard:${NC}    http://${SERVER_IP}:${PORT}"
echo -e "  ${BOLD}Password:${NC}     ${YELLOW}${PASSWORD}${NC}"
echo -e "  ${BOLD}STUN Server:${NC}  ${SERVER_IP}:${STUN_PORT} (UDP)"
echo -e "  ${BOLD}WebSocket:${NC}    ws://${SERVER_IP}:${PORT}/ws"
echo ""
echo -e "  ${BOLD}Client connect:${NC}"
echo -e "  ${CYAN}./stun_max-cli --server ws://${SERVER_IP}:${PORT}/ws --room myroom --password secret --name mypc${NC}"
echo ""
echo -e "  ${BOLD}Management:${NC}"
echo -e "  systemctl status stun-max          # check status"
echo -e "  systemctl restart stun-max         # restart"
echo -e "  journalctl -u stun-max -f          # view logs"
echo -e "  cat ${LOG_DIR}/stun_max.log        # log file"
echo ""
echo -e "  ${BOLD}Uninstall:${NC}"
echo -e "  systemctl stop stun-max stun-max-stun"
echo -e "  systemctl disable stun-max stun-max-stun"
echo -e "  rm /etc/systemd/system/stun-max*.service"
echo -e "  rm ${INSTALL_DIR}/stun_max-*"
echo -e "  rm -rf ${DATA_DIR}"
echo ""
