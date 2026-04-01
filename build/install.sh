#!/bin/bash
set -e

# ── Antigravity Tailscale Installer (macOS) ─────────────────────────
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
DAEMON_PLIST="/Library/LaunchDaemons/com.tailscale.tailscaled.plist"
RECEIVER_PLIST="/Library/LaunchAgents/com.tailscale.file-receiver.plist"
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

REAL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo nobody)}"
REAL_HOME=$(eval echo "~$REAL_USER")

echo "=== Antigravity Tailscale Installer ==="
echo "  Server:    $HEADSCALE_URL"
echo "  Exit node: ${EXIT_NODE:-none}"
echo "  User:      $REAL_USER ($REAL_HOME)"
echo ""

# ── detect arch ─────────────────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
  arm64|aarch64) ARCH="arm64" ;;
  x86_64)        ARCH="amd64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

if [ "$(uname -s)" != "Darwin" ]; then
  echo "This installer is for macOS only."
  exit 1
fi

# ── step 1: stop existing services ─────────────────────────────────
echo "[1/6] Stopping existing services..."
launchctl unload "$DAEMON_PLIST" 2>/dev/null || true
su "$REAL_USER" -c "launchctl unload '$RECEIVER_PLIST' 2>/dev/null" || true
sleep 1
pkill -x tailscaled 2>/dev/null || true
sleep 1
# clean stale tunnel routes
route -q -n delete -inet 0/1 2>/dev/null || true
route -q -n delete -inet 128.0/1 2>/dev/null || true

# ── step 2: download binaries ──────────────────────────────────────
echo "[2/6] Downloading binaries..."
TMPDIR=$(mktemp -d)
DOWNLOAD_OK=true
curl -fsSL -o "$TMPDIR/tailscaled" "$RELEASE_URL/tailscaled-darwin-${ARCH}" 2>/dev/null || DOWNLOAD_OK=false
curl -fsSL -o "$TMPDIR/tailscale"  "$RELEASE_URL/tailscale-darwin-${ARCH}" 2>/dev/null || DOWNLOAD_OK=false

if [ "$DOWNLOAD_OK" = false ]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)"
  if [ -f "$SCRIPT_DIR/tailscaled" ] && [ -f "$SCRIPT_DIR/tailscale" ]; then
    echo "  Using local binaries"
    cp "$SCRIPT_DIR/tailscaled" "$TMPDIR/tailscaled"
    cp "$SCRIPT_DIR/tailscale" "$TMPDIR/tailscale"
  else
    echo "Error: download failed and no local binaries found"
    rm -rf "$TMPDIR"
    exit 1
  fi
fi

# ── step 3: install binaries and directories ───────────────────────
echo "[3/6] Installing binaries..."
install -m 755 "$TMPDIR/tailscaled" /usr/local/bin/tailscaled
install -m 755 "$TMPDIR/tailscale"  /usr/local/bin/tailscale
rm -rf "$TMPDIR"

mkdir -p /var/run/tailscale
mkdir -p /Library/Tailscale

# ── step 4: write LaunchDaemon (tailscaled) ────────────────────────
echo "[4/6] Configuring daemon..."
cat > "$DAEMON_PLIST" << 'PLIST'
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

# ── step 5: write LaunchAgent (auto file receiver) ─────────────────
echo "[5/6] Configuring file receiver..."
cat > "$RECEIVER_PLIST" << PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.tailscale.file-receiver</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/tailscale</string>
        <string>file</string>
        <string>get</string>
        <string>--loop</string>
        <string>--conflict=rename</string>
        <string>${REAL_HOME}/Downloads/</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardErrorPath</key>
    <string>/tmp/tailscale-file-receiver.log</string>
</dict>
</plist>
PLIST

# ── step 6: start and connect ─────────────────────────────────────
echo "[6/6] Starting and connecting..."
launchctl load "$DAEMON_PLIST"
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

# start file receiver as the real user
su "$REAL_USER" -c "launchctl load '$RECEIVER_PLIST'" 2>/dev/null || true

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
echo "=== Installed ==="
echo "  Daemon:        /usr/local/bin/tailscaled"
echo "  CLI:           /usr/local/bin/tailscale"
echo "  Daemon log:    /var/log/tailscaled.log"
echo "  File receiver: auto → $REAL_HOME/Downloads/"
echo "  Bypass config: /Library/Tailscale/bypass-routes (optional)"
echo ""
echo "Done."
