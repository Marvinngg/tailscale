#!/bin/bash
# 部署精简版 Tailscale 到远程 Linux 节点
# 用法:
#   ./deploy.sh user@host              部署到单个节点
#   ./deploy.sh user@host1 user@host2  部署到多个节点
#
# 前提: 先运行 build.sh 生成 linux/amd64 二进制
#   GOOS=linux GOARCH=amd64 bash build.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_DIR="$SCRIPT_DIR/build"

if [ ! -f "$BUILD_DIR/tailscaled" ] || [ ! -f "$BUILD_DIR/tailscale" ]; then
    echo "错误: build/ 目录下没有找到构建产物。"
    echo "请先运行: GOOS=linux GOARCH=amd64 bash build.sh"
    exit 1
fi

if [ $# -eq 0 ]; then
    echo "用法: $0 user@host [user@host2 ...]"
    exit 1
fi

for target in "$@"; do
    echo ""
    echo "========================================"
    echo "  部署到 $target"
    echo "========================================"

    # 上传
    echo "→ 上传二进制..."
    scp -q "$BUILD_DIR/tailscaled" "$BUILD_DIR/tailscale" "$target:/tmp/"

    # 部署
    echo "→ 停止服务、替换二进制、启动..."
    ssh "$target" bash -s <<'REMOTE_SCRIPT'
set -e
sudo systemctl stop tailscaled 2>/dev/null || true
# 备份原版（仅首次）
[ ! -f /usr/sbin/tailscaled.orig ] && sudo cp /usr/sbin/tailscaled /usr/sbin/tailscaled.orig 2>/dev/null || true
[ ! -f /usr/bin/tailscale.orig ] && sudo cp /usr/bin/tailscale /usr/bin/tailscale.orig 2>/dev/null || true
# 替换
sudo cp /tmp/tailscaled /usr/sbin/tailscaled
sudo cp /tmp/tailscale /usr/bin/tailscale
sudo chmod +x /usr/sbin/tailscaled /usr/bin/tailscale
# 启动
sudo systemctl start tailscaled
sleep 2
REMOTE_SCRIPT

    # 验证
    echo "→ 验证..."
    ssh "$target" "tailscale version && echo '---' && tailscale status 2>/dev/null | head -10"
    echo "✓ $target 部署完成"
done

echo ""
echo "========================================"
echo "  全部部署完成"
echo "========================================"
