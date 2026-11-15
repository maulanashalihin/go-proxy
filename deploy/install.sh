#!/usr/bin/env bash
# Install go-proxy on a Linux server (systemd) without building from source.
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/maulanashalihin/go-proxy/main/deploy/install.sh \
#     | sudo bash -s -- https://target-website.com [port]
set -euo pipefail

TARGET_URL="${1:?Usage: sudo bash install.sh https://target-website.com [port]}"
PORT="${2:-18080}"
REPO="maulanashalihin/go-proxy"
DIR="/opt/go-proxy"
SVC_USER="go-proxy"

if [[ $EUID -ne 0 ]]; then
  echo "Run as root (use sudo)." >&2
  exit 1
fi

echo "==> Downloading go-proxy binary..."
curl -fsSL -o /tmp/go-proxy-bin \
  "https://github.com/$REPO/releases/latest/download/go-proxy-linux-amd64"

echo "==> Creating user and directories..."
id "$SVC_USER" &>/dev/null || useradd -r -s /usr/sbin/nologin "$SVC_USER"
mkdir -p "$DIR/cache"
mv /tmp/go-proxy-bin "$DIR/go-proxy"
chmod +x "$DIR/go-proxy"

echo "==> Writing $DIR/.env..."
TOKEN="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
cat > "$DIR/.env" <<EOF
TARGET_URL=$TARGET_URL
PORT=$PORT
CACHE_DIR=./cache
CACHE_ENABLED=true
CACHE_MAX_SIZE_MB=10240
TTL_HTML=5m
TTL_CSS=1h
TTL_JS=1h
TTL_IMAGE=24h
TTL_PDF=7d
TTL_DEFAULT=10m
REWRITE_HTML=true
REWRITE_CSS=true
REWRITE_JS=false
ORIGIN_TIMEOUT=30s
PURGE_TOKEN=$TOKEN
EOF
chown -R "$SVC_USER:$SVC_USER" "$DIR"
chmod 600 "$DIR/.env"

echo "==> Installing systemd service..."
cat > /etc/systemd/system/go-proxy.service <<EOF
[Unit]
Description=go-proxy reverse proxy cache
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
WorkingDirectory=$DIR
ExecStart=$DIR/go-proxy
Restart=always
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now go-proxy
sleep 3

if command -v ufw >/dev/null 2>&1; then
  echo "==> Opening firewall port $PORT..."
  ufw allow "$PORT"/tcp || true
fi

echo "==> Verifying..."
curl -fsS "http://localhost:$PORT/__health" && echo
echo
echo "Done. Proxy: http://<server-ip>:$PORT/ -> $TARGET_URL"
echo "Purge token (save this): $TOKEN"
echo "Logs: journalctl -u go-proxy -f"
