#!/bin/bash
# Antigravity Tailscale (ag2) uninstaller — companion to install.sh
# Usage: curl -fsSL <release-url>/uninstall.sh | sudo bash
# or:    sudo bash uninstall.sh

set +e

if [ "$(id -u)" != "0" ]; then
  echo "Error: run with sudo"; exit 1
fi

if [ "$(uname -s)" != "Darwin" ]; then
  echo "macOS only."; exit 1
fi

REAL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo nobody)}"
REAL_UID=$(id -u "$REAL_USER")
HEADSCALE_HOST="hs.263onet.com"

echo "=== Antigravity Tailscale Uninstaller ==="
echo "  Real user: $REAL_USER (uid=$REAL_UID)"
echo ""

echo "[1/8] Disconnecting + logout..."
/usr/local/bin/tailscale down 2>/dev/null
/usr/local/bin/tailscale logout 2>/dev/null
sleep 1

echo "[2/8] Stopping daemon + agent..."
launchctl bootout system/com.tailscale.tailscaled 2>/dev/null \
  || launchctl unload /Library/LaunchDaemons/com.tailscale.tailscaled.plist 2>/dev/null
su "$REAL_USER" -c "launchctl bootout gui/$REAL_UID/com.tailscale.file-receiver 2>/dev/null \
  || launchctl unload ~/Library/LaunchAgents/com.tailscale.file-receiver.plist 2>/dev/null" || true
pkill -9 -x tailscaled 2>/dev/null
pkill -9 -x tailscale  2>/dev/null
sleep 1

echo "[3/8] Removing binaries..."
rm -f /usr/local/bin/tailscaled /usr/local/bin/tailscale

echo "[4/8] Removing LaunchDaemon + LaunchAgent..."
rm -f /Library/LaunchDaemons/com.tailscale.tailscaled.plist
rm -f /Library/LaunchAgents/com.tailscale.file-receiver.plist

echo "[5/8] Removing 263onet CA from System Keychain..."
for i in 1 2 3 4 5; do
  if security find-certificate -c "263onet Internal Headscale CA" \
       /Library/Keychains/System.keychain >/dev/null 2>&1; then
    security delete-certificate -c "263onet Internal Headscale CA" \
      /Library/Keychains/System.keychain 2>/dev/null
  else
    break
  fi
done
if security find-certificate -c "263onet" /Library/Keychains/System.keychain >/dev/null 2>&1; then
  echo "  WARN: CA still present — please remove manually via Keychain Access.app"
else
  echo "  CA removed."
fi

echo "[6/8] Cleaning /etc/hosts..."
sed -i '' "/$HEADSCALE_HOST/d" /etc/hosts

echo "[7/8] Cleaning runtime dirs + logs..."
rm -rf /var/run/tailscale /Library/Tailscale
rm -f /var/log/tailscaled.log /tmp/tailscale-file-receiver.log

echo "[8/8] Cleaning DNS resolver + routes + caches..."
rm -f /etc/resolver/*.tailscale* /etc/resolver/search.tailscale 2>/dev/null
route -q -n delete -inet 0/1 2>/dev/null
route -q -n delete -inet 128.0/1 2>/dev/null
route -q -n delete -inet6 ::/1 2>/dev/null
route -q -n delete -inet6 8000::/1 2>/dev/null
dscacheutil -flushcache
killall -HUP mDNSResponder 2>/dev/null

echo ""
echo "=== Verification ==="
which tailscale 2>/dev/null && echo "  WARN: tailscale binary still exists" || echo "  ✓ tailscale binary removed"
security find-certificate -c 263onet /Library/Keychains/System.keychain >/dev/null 2>&1 \
  && echo "  WARN: 263onet CA still present" || echo "  ✓ 263onet CA removed"
grep -q "$HEADSCALE_HOST" /etc/hosts && echo "  WARN: /etc/hosts still has $HEADSCALE_HOST" || echo "  ✓ /etc/hosts clean"

echo ""
echo "Done. You may want to reinstall the official Tailscale.app from https://tailscale.com/download/mac"
