#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DAEMON_BIN="$SCRIPT_DIR/tailscaled"
CLI_BIN="$SCRIPT_DIR/tailscale"
LAUNCHDAEMON_PLIST="/Library/LaunchDaemons/com.tailscale.tailscaled.plist"
STANDALONE_PKG_URL="https://pkgs.tailscale.com/stable/Tailscale-1.94.2-macos.pkg"
STANDALONE_PKG="/tmp/Tailscale-standalone.pkg"

# ── escalate to root ──────────────────────────────────────────────────────────
if [ "$(id -u)" != "0" ]; then
    exec sudo bash "$0" "$@"
fi

echo "=== Tailscale Enhanced Installer ==="
echo ""

# ── sanity: our binaries must be present ─────────────────────────────────────
if [ ! -f "$DAEMON_BIN" ] || [ ! -f "$CLI_BIN" ]; then
    echo "ERROR: tailscaled / tailscale not found next to this script"
    echo "  expected: $DAEMON_BIN"
    exit 1
fi

# ── step 1: remove App Store version if present ──────────────────────────────
if [ -d "/Applications/Tailscale.app/Contents/_MASReceipt" ]; then
    echo "[1/4] App Store Tailscale detected — removing (standalone will replace it)..."
    # stop the App Store app gracefully first
    sudo -u "$SUDO_USER" osascript -e 'quit app "Tailscale"' 2>/dev/null || true
    sleep 1
    # kill any remaining processes from the App Store bundle
    pkill -f "io.tailscale.ipn.macos" 2>/dev/null || true
    sleep 1
    rm -rf /Applications/Tailscale.app
    echo "  done."
else
    echo "[1/4] No App Store Tailscale found — skipping removal."
fi

# ── step 2: install standalone Tailscale (for GUI tray) ──────────────────────
if [ ! -d "/Applications/Tailscale.app" ]; then
    echo "[2/4] Installing standalone Tailscale (GUI + system integration)..."
    # backup state before pkg install (pkg installer clears /var/db/tailscale)
    if [ -d "/var/db/tailscale" ] && [ "$(ls -A /var/db/tailscale 2>/dev/null)" ]; then
        cp -a /var/db/tailscale /var/db/tailscale.bak
        echo "  state backed up."
    fi
    echo "  Downloading from pkgs.tailscale.com..."
    curl -L --progress-bar -o "$STANDALONE_PKG" "$STANDALONE_PKG_URL"
    echo "  Installing .pkg..."
    installer -pkg "$STANDALONE_PKG" -target / > /dev/null
    rm -f "$STANDALONE_PKG"
    # restore state if pkg wiped it
    if [ -d "/var/db/tailscale.bak" ]; then
        rm -rf /var/db/tailscale
        mv /var/db/tailscale.bak /var/db/tailscale
        echo "  state restored."
    fi
    echo "  done."
else
    echo "[2/4] Standalone Tailscale GUI already present — skipping."
fi

# ── step 3: stop daemon before replacing binary ───────────────────────────────
echo "[3/4] Installing enhanced tailscaled..."

launchctl unload "$LAUNCHDAEMON_PLIST" 2>/dev/null || true
sleep 1
for i in 1 2 3 4 5; do
    pgrep -x tailscaled > /dev/null 2>&1 || break
    sleep 1
done
pkill -9 -x tailscaled 2>/dev/null || true
sleep 1

# clean up stale tunnel routes from previous daemon (belt-and-suspenders;
# the daemon's HookCleanUp also does this, but routes added via -iface may
# survive utun destruction on some macOS versions)
echo "  cleaning stale tunnel routes..."
route -q -n delete -inet 0/1 2>/dev/null || true
route -q -n delete -inet 128.0/1 2>/dev/null || true
route -q -n delete -inet6 ::/1 2>/dev/null || true
route -q -n delete -inet6 8000::/1 2>/dev/null || true

# replace binaries
cp "$DAEMON_BIN" /usr/local/bin/tailscaled
chmod 755 /usr/local/bin/tailscaled
cp "$CLI_BIN" /usr/local/bin/tailscale
chmod 755 /usr/local/bin/tailscale

# create socket directory (GUI looks for /var/run/tailscale/tailscaled.sock)
mkdir -p /var/run/tailscale

# ensure LaunchDaemon plist is correct, with explicit socket path matching GUI expectation
cat > "$LAUNCHDAEMON_PLIST" << 'PLIST'
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
chmod 644 "$LAUNCHDAEMON_PLIST"

# create state directory
mkdir -p /var/db/tailscale
chmod 700 /var/db/tailscale

# ── step 4: start daemon ──────────────────────────────────────────────────────
echo "[4/4] Starting tailscaled..."
launchctl load "$LAUNCHDAEMON_PLIST"
sleep 2

# verify
if pgrep -x tailscaled > /dev/null 2>&1; then
    VER=$(/usr/local/bin/tailscaled --version 2>/dev/null | head -1)
    echo ""
    echo "✓ Installation complete!"
    echo "  tailscaled: $VER"
    echo ""
    echo "Next step — connect to the private network:"
    echo ""
    echo "  tailscale up --login-server https://hs.marvinai.qzz.io:8443"
    echo ""
    echo "  Then approve this machine in the Headscale dashboard."
    echo "  (You can delete the old App Store node entry from Headscale.)"
    echo ""
    echo "  Open Tailscale.app for the tray icon / exit node UI."
    echo ""
else
    echo ""
    echo "✗ tailscaled failed to start. Check logs:"
    echo "  tail -50 /var/log/tailscaled.log"
    exit 1
fi
