#!/bin/bash
set -e

# ── Antigravity Tailscale Installer ─────────────────────────────────
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Marvinngg/tailscale/antigravity/v1.94.2-macos-exitnode/build/install.sh | sudo bash -s -- --key=hskey-auth-xxxx
#
# Options:
#   --key=KEY          Headscale pre-auth key (required)
#   --server=URL       Headscale server (default: https://hs.marvinai.qzz.io:8443)
#   --exit-node=IP     Exit node IP (default: 100.64.0.1)
#   --no-exit-node     Don't set exit node
# ─────────────────────────────────────────────────────────────────────

HEADSCALE_URL="https://hs.marvinai.qzz.io:8443"
EXIT_NODE="100.64.0.1"
AUTH_KEY=""
PLIST="/Library/LaunchDaemons/com.tailscale.tailscaled.plist"
RELEASE_URL="https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag1"

# ── parse args ──────────────────────────────────────────────────────
for arg in "$@"; do
  case "$arg" in
    --key=*)       AUTH_KEY="${arg#--key=}" ;;
    --server=*)    HEADSCALE_URL="${arg#--server=}" ;;
    --exit-node=*) EXIT_NODE="${arg#--exit-node=}" ;;
    --no-exit-node) EXIT_NODE="" ;;
    *) echo "Unknown option: $arg"; exit 1 ;;
  esac
done

if [ -z "$AUTH_KEY" ]; then
  echo "Error: --key is required"
  echo "Usage: curl -fsSL .../install.sh | sudo bash -s -- --key=hskey-auth-xxxx"
  exit 1
fi

# ── root check ──────────────────────────────────────────────────────
if [ "$(id -u)" != "0" ]; then
  echo "Error: run with sudo"
  exit 1
fi

echo "=== Antigravity Tailscale Installer ==="
echo "  Server:    $HEADSCALE_URL"
echo "  Exit node: ${EXIT_NODE:-none}"
echo ""

# ── detect arch ─────────────────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
  arm64|aarch64) ARCH="arm64" ;;
  x86_64)        ARCH="amd64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac
OS=$(uname -s | tr '[:upper:]' '[:lower:]')

if [ "$OS" != "darwin" ]; then
  echo "This installer is for macOS only."
  exit 1
fi

# ── stop existing daemon ────────────────────────────────────────────
echo "[1/5] Stopping existing tailscaled..."
launchctl unload "$PLIST" 2>/dev/null || true
sleep 1
pkill -x tailscaled 2>/dev/null || true
sleep 1

# clean stale tunnel routes
route -q -n delete -inet 0/1 2>/dev/null || true
route -q -n delete -inet 128.0/1 2>/dev/null || true

# ── download binaries ───────────────────────────────────────────────
echo "[2/5] Downloading binaries..."
TMPDIR=$(mktemp -d)
curl -fsSL -o "$TMPDIR/tailscaled" "$RELEASE_URL/tailscaled-${OS}-${ARCH}" || {
  # fallback: binaries might be next to this script (local install)
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if [ -f "$SCRIPT_DIR/tailscaled" ] && [ -f "$SCRIPT_DIR/tailscale" ]; then
    echo "  Using local binaries from $SCRIPT_DIR"
    cp "$SCRIPT_DIR/tailscaled" "$TMPDIR/tailscaled"
    cp "$SCRIPT_DIR/tailscale" "$TMPDIR/tailscale"
  else
    echo "Error: failed to download and no local binaries found"
    rm -rf "$TMPDIR"
    exit 1
  fi
}
curl -fsSL -o "$TMPDIR/tailscale" "$RELEASE_URL/tailscale-${OS}-${ARCH}" 2>/dev/null || true

# ── install binaries ────────────────────────────────────────────────
echo "[3/5] Installing..."
install -m 755 "$TMPDIR/tailscaled" /usr/local/bin/tailscaled
install -m 755 "$TMPDIR/tailscale" /usr/local/bin/tailscale
rm -rf "$TMPDIR"

mkdir -p /var/run/tailscale
mkdir -p /Library/Tailscale

# ── write LaunchDaemon plist ────────────────────────────────────────
echo "[4/5] Configuring LaunchDaemon..."
cat > "$PLIST" << 'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.tailscale.tailscaled</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/tailscaled</string>
        <string>--tun=utun</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardErrorPath</key>
    <string>/var/log/tailscaled.log</string>
    <key>StandardOutPath</key>
    <string>/var/log/tailscaled.log</string>
</dict>
</plist>
PLIST

# ── start and connect ──────────────────────────────────────────────
echo "[5/5] Starting and connecting..."
launchctl load "$PLIST"
sleep 3

EXIT_FLAG=""
if [ -n "$EXIT_NODE" ]; then
  EXIT_FLAG="--exit-node=$EXIT_NODE"
fi

/usr/local/bin/tailscale up \
  --login-server="$HEADSCALE_URL" \
  --auth-key="$AUTH_KEY" \
  --reset \
  $EXIT_FLAG

sleep 3

# ── verify ──────────────────────────────────────────────────────────
echo ""
echo "=== Status ==="
/usr/local/bin/tailscale status

if [ -n "$EXIT_NODE" ]; then
  echo ""
  echo "=== Exit Node Test ==="
  MYIP=$(curl -s --connect-timeout 10 ifconfig.me 2>/dev/null || echo "timeout")
  echo "Public IP: $MYIP"
fi

echo ""
echo "Done. Logs: /var/log/tailscaled.log"
