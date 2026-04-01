#!/bin/bash
set -e

LAUNCHDAEMON_PLIST="/Library/LaunchDaemons/com.tailscale.tailscaled.plist"

if [ "$(id -u)" != "0" ]; then
    exec sudo bash "$0" "$@"
fi

echo "=== Tailscale Enhanced 卸载程序 ==="
echo ""

# disconnect first
/usr/local/bin/tailscale down 2>/dev/null || true
/usr/local/bin/tailscale logout 2>/dev/null || true

# stop and remove daemon
echo "停止并卸载 LaunchDaemon..."
launchctl unload "$LAUNCHDAEMON_PLIST" 2>/dev/null || true
pkill -9 -x tailscaled 2>/dev/null || true

# restore official binary if backup exists
if [ -f "/usr/local/bin/tailscaled.official" ]; then
    echo "恢复官方 tailscaled..."
    mv /usr/local/bin/tailscaled.official /usr/local/bin/tailscaled
else
    rm -f /usr/local/bin/tailscaled
fi
rm -f /usr/local/bin/tailscale

# remove state
echo "清除 state..."
rm -rf /var/db/tailscale
rm -f /var/log/tailscaled.log

echo ""
echo "✓ 卸载完成"
