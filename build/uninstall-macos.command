#!/bin/bash
set -e

if [ "$(id -u)" != "0" ]; then
    exec sudo bash "$0" "$@"
fi

echo "=== Marvin Tailscale 完整卸载 ==="
echo ""

REAL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo nobody)}"
REAL_HOME=$(eval echo "~$REAL_USER")

# ── 1. 停止所有 Tailscale 进程 ──
echo "[1/5] 停止服务..."
/usr/local/bin/tailscale down 2>/dev/null || true
launchctl unload /Library/LaunchDaemons/com.tailscale.tailscaled.plist 2>/dev/null || true
launchctl unload /Library/LaunchDaemons/com.tailscale.*.plist 2>/dev/null || true
su "$REAL_USER" -c 'launchctl unload ~/Library/LaunchAgents/com.tailscale.*.plist 2>/dev/null' || true
launchctl unload /Library/LaunchAgents/com.tailscale.*.plist 2>/dev/null || true
su "$REAL_USER" -c 'osascript -e "quit app \"Tailscale\"" 2>/dev/null' || true
pkill -9 -x tailscaled 2>/dev/null || true
sleep 2

# ── 2. 删除程序文件 ──
echo "[2/5] 删除程序..."
rm -f /usr/local/bin/tailscaled /usr/local/bin/tailscale
rm -rf /Applications/Tailscale.app

# ── 3. 删除配置文件 ──
echo "[3/5] 删除配置..."
rm -f /Library/LaunchDaemons/com.tailscale.tailscaled.plist
rm -f /Library/LaunchAgents/com.tailscale.file-receiver.plist
rm -f "$REAL_HOME/Library/LaunchAgents/com.tailscale.file-receiver.plist"

# ── 4. 删除数据 ──
echo "[4/5] 删除数据..."
rm -rf /Library/Tailscale
rm -rf /etc/tailscale
rm -rf /var/lib/tailscale
rm -rf /var/run/tailscale
rm -rf /var/db/tailscale
rm -f /var/log/tailscaled.log
rm -rf "$REAL_HOME/.local/share/tailscale"
rm -rf "$REAL_HOME/.config/tailscale"

# ── 5. 清理网络 ──
echo "[5/5] 清理网络..."
route -q -n delete -inet 0/1 2>/dev/null || true
route -q -n delete -inet 128.0/1 2>/dev/null || true
route -q -n delete -inet6 ::/1 2>/dev/null || true
route -q -n delete -inet6 8000::/1 2>/dev/null || true

echo ""
echo "卸载完成。"
echo ""
echo "如果之前用 brew 安装过: brew uninstall tailscale"
