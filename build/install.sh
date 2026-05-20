#!/bin/bash
set -e

# ── Antigravity Tailscale Installer (macOS) ─────────────────────────
# Usage:
#   curl -fsSL https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag2/install.sh | sudo bash -s -- --key=hskey-auth-xxxx
#
# Options:
#   --key=KEY          Headscale pre-auth key (required)
#   --server=URL       Headscale server (default: https://hs.263onet.com:8443)
#   --exit-node=IP     Exit node IP (default: 100.96.0.1)
#   --no-exit-node     Don't set exit node
# ─────────────────────────────────────────────────────────────────────

HEADSCALE_URL="https://hs.263onet.com:8443"
EXIT_NODE="100.96.0.1"
AUTH_KEY=""
DAEMON_PLIST="/Library/LaunchDaemons/com.tailscale.tailscaled.plist"
RECEIVER_PLIST="/Library/LaunchAgents/com.tailscale.file-receiver.plist"
RELEASE_URL="https://github.com/Marvinngg/tailscale/releases/download/v1.94.2-ag2"

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

# ── step 1: clean up ALL existing Tailscale installations ─────────
echo "[1/7] Cleaning up existing Tailscale..."

# Stop our daemon
launchctl unload "$DAEMON_PLIST" 2>/dev/null || true
su "$REAL_USER" -c "launchctl unload '$RECEIVER_PLIST' 2>/dev/null" || true

# Stop official App Store / standalone Tailscale
su "$REAL_USER" -c 'osascript -e "quit app \"Tailscale\"" 2>/dev/null' || true

# Kill any running tailscaled regardless of source
pkill -9 -x tailscaled 2>/dev/null || true
sleep 2

# Remove official App Store version
if [ -d "/Applications/Tailscale.app/Contents/_MASReceipt" ]; then
  echo "  Removing App Store Tailscale..."
  rm -rf "/Applications/Tailscale.app"
fi

# Remove official standalone version (but keep state for migration)
if [ -d "/Applications/Tailscale.app" ] && [ ! -d "/Applications/Tailscale.app/Contents/_MASReceipt" ]; then
  echo "  Removing standalone Tailscale.app..."
  rm -rf "/Applications/Tailscale.app"
fi

# Remove brew-installed tailscale (if present)
if command -v brew &>/dev/null && brew list tailscale &>/dev/null 2>&1; then
  echo "  Removing brew Tailscale..."
  brew uninstall tailscale 2>/dev/null || true
fi

# Remove old binaries from any location
rm -f /usr/local/bin/tailscaled /usr/local/bin/tailscale 2>/dev/null || true

# Remove old LaunchDaemons (various names used by different versions)
for plist in \
  /Library/LaunchDaemons/com.tailscale.tailscaled.plist \
  /Library/LaunchDaemons/com.tailscale.*.plist; do
  launchctl unload "$plist" 2>/dev/null || true
done

# Remove old LaunchAgents
su "$REAL_USER" -c 'launchctl unload ~/Library/LaunchAgents/com.tailscale.*.plist 2>/dev/null' || true
launchctl unload /Library/LaunchAgents/com.tailscale.*.plist 2>/dev/null || true

# Clean stale tunnel routes
route -q -n delete -inet 0/1 2>/dev/null || true
route -q -n delete -inet 128.0/1 2>/dev/null || true
route -q -n delete -inet6 ::/1 2>/dev/null || true
route -q -n delete -inet6 8000::/1 2>/dev/null || true

echo "  Cleanup done."

# ── step 2: download binaries ──────────────────────────────────────
echo "[2/7] Downloading binaries..."
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
echo "[3/7] Installing binaries..."
install -m 755 "$TMPDIR/tailscaled" /usr/local/bin/tailscaled
install -m 755 "$TMPDIR/tailscale"  /usr/local/bin/tailscale
rm -rf "$TMPDIR"

# ad-hoc sign (macOS Sequoia+ kills unsigned binaries in system paths)
codesign --force --sign - /usr/local/bin/tailscaled 2>/dev/null || true
codesign --force --sign - /usr/local/bin/tailscale  2>/dev/null || true

mkdir -p /var/run/tailscale
mkdir -p /Library/Tailscale

# ── step 4: install 263onet self-signed CA into System keychain ────
# (required so tailscale CLI / tailscaled can TLS to https://hs.263onet.com:8443)
echo "[4/7] Installing 263onet CA certificate..."
CA_PATH=$(mktemp -t 263onet-ca.XXXXXX.crt)
cat > "$CA_PATH" << 'CERT'
-----BEGIN CERTIFICATE-----
MIIFbzCCA1egAwIBAgIUFxzvTe4cYNMTVv6n9/kODXlVOFAwDQYJKoZIhvcNAQEL
BQAwRzELMAkGA1UEBhMCQ04xEDAOBgNVBAoMBzI2M29uZXQxJjAkBgNVBAMMHTI2
M29uZXQgSW50ZXJuYWwgSGVhZHNjYWxlIENBMB4XDTI2MDUwODA3MzUzN1oXDTM2
MDUwNTA3MzUzN1owRzELMAkGA1UEBhMCQ04xEDAOBgNVBAoMBzI2M29uZXQxJjAk
BgNVBAMMHTI2M29uZXQgSW50ZXJuYWwgSGVhZHNjYWxlIENBMIICIjANBgkqhkiG
9w0BAQEFAAOCAg8AMIICCgKCAgEAzHnVAdVpxukt9CHT3QtFZAbMz1Pe9Fr4Z0fC
YwjZSZIQHozReH4fFEbvmUf33+2Bg527QOKyVXnUjvCK8h4T6byD8Igt293kz3yw
dL9Pvn6YdZaJqkeagG0hvlrnTSAy/hLIUUcThPMdx4TwwMSuLtqkG+V/hXfNOAmp
7BhHDv3YzHYvYJ59Dijf+sIp7+wqYRmwvZ8MfNZDMfnHzqRwl27C2kZitY/fcZkr
I79c7OAenR6rdDJzjy9mxscKylga4cqiQvKem8OWnYBM4XMsJKGB8P2wUMfg63Ca
rCp9iSiIGpHeEyyuVm9yTCFYrmiImpVGRh9CB5zjlr3FBFzzY3TNXNDmczCcI11m
pYtd9XteALvWOo/Snz/YIvqHU2vToA+IAIP2smJQXFBmdv98Cta5n72JjbyvDhXO
CO+0OT04Yv3zgWDDfvEVQHiwu0Us46B/IxIEF8tY+e/Wbwt+EZskMwIREldz+lW2
AWFhf/Ol+GqklcSZ2XwN+m7uAt1XBiKTLDs7gNvBuUIvczTfMo/s+Do1R8ZylWZk
QmwnseMxPobkFnjVlPTQ2duJ/4D73OhjfNDq1YFaSMRDBWOKVYVZEKKRbp0CbQZ4
/t9Bx/CqUa7gri8NYypP6dISjQU/jB6HvsuaaVdxQrk9NedvZmxtmJprGnVS3hbC
tLJwSLUCAwEAAaNTMFEwHQYDVR0OBBYEFB23tWBo+PMUo+qUphOg80PQ/iSfMB8G
A1UdIwQYMBaAFB23tWBo+PMUo+qUphOg80PQ/iSfMA8GA1UdEwEB/wQFMAMBAf8w
DQYJKoZIhvcNAQELBQADggIBAI0be/wFVAll+Zoqk2nDm0eDxJ4pd1mof+plTkk7
xu9p+NjQk4DVnvHyxKTNBuS8I0Ebu6BeCeaUKH2oirjxIKoUGz5/MhWQ7oWOdbDp
mpye+w2P1tHb5qJHAqSuPr9w3ubpIbBnO0qGqGGGg/o9E9XC6x5LUbMMj/cfwEIi
LtsIqNF4t9cLKzn617wgiMj2O129zMM2ERYmLrl2j7Df9fKOm2fItC7kMGiatP4Z
oopxtx7o111wCIscQBALPXDJqQv9ZTSgmlc3vNSUJl/M4R8jxopARQGrR/ANyfu1
0WsQOeaUGD9iovBpn4BDty6ltPiSkKeAZ4mYQ43cFmbZvLYOx/b4AZ6LO8sU9jJG
Jx+4xN5jzDTqbB1+G2lRqMynh9KJeohBGlYOf4Zx5S5l8rAt7znwAslOAGqBNLFM
8TJQylNXkND+nv9cuizYUhsSocfk3eMZpBUY7TFSGW7PvH/l/HN6miIg03yz6efa
rOcmCht6S6K3L3l7h9iIIvdHypI64rS/YnSh39wUgRmkeKaOXMKXuPfsL5oUwh2P
tTaHklcUU06p6xKOJiAwF69CWxKYYsdhAjm/R+GAWJK+w5M6qIBq7ZzqLmdJrnSl
98Ll+Fa8j7dgsk7dE7hEcHw2kmDtzSK1TfZr4YYXjFXczf7ik2AbSvzfD9v9ssJG
94AL
-----END CERTIFICATE-----
CERT
security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "$CA_PATH" 2>/dev/null \
  && echo "  CA cert trusted." \
  || echo "  WARN: failed to import CA (may already be trusted)."
rm -f "$CA_PATH"

# ── step 5: write LaunchDaemon (tailscaled) ────────────────────────
echo "[5/7] Configuring daemon..."
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
echo "[6/7] Configuring file receiver..."
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
echo "[7/7] Starting and connecting..."
launchctl load "$DAEMON_PLIST"
sleep 3

EXIT_FLAG=""
if [ -n "$EXIT_NODE" ]; then
  EXIT_FLAG="--exit-node=$EXIT_NODE"
fi

LAN_FLAG=""
if [ -n "$EXIT_NODE" ]; then
  LAN_FLAG="--exit-node-allow-lan-access"
fi

/usr/local/bin/tailscale up \
  --login-server="$HEADSCALE_URL" \
  --auth-key="$AUTH_KEY" \
  --accept-routes \
  --accept-dns \
  --unattended \
  --reset \
  $EXIT_FLAG \
  $LAN_FLAG

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
