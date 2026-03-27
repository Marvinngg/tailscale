#!/bin/bash
# AntiGravity Mesh - Tailscale 精简构建脚本
#
# 保留: WireGuard, 控制平面, NAT穿透, netstack, MagicDNS, ACL,
#        Exit Node, Taildrop(文件传输), Tailnetlock(网络锁), adblock(广告拦截)
#
# 剥离: SSH, Serve/Funnel, Drive, 遥测, 自动更新, 诊断工具, K8s, 云集成 ...

set -euo pipefail

OMIT_TAGS=(
    # --- 远程访问 / 暴露 ---
    ts_omit_ssh               # Tailscale SSH（用不到）
    ts_omit_serve             # Tailscale Serve + Funnel（公网暴露）

    # --- 遥测 ---
    ts_omit_logtail           # 日志上报到 Tailscale Inc
    ts_omit_netlog            # 网络流量日志
    ts_omit_usermetrics       # 用户指标

    # --- 管理 / 更新 ---
    ts_omit_clientupdate      # 自动联网更新
    ts_omit_webclient         # 浏览器管理界面
    ts_omit_doctor            # 诊断工具
    ts_omit_cliconndiag       # 连接诊断
    ts_omit_debugeventbus     # 调试事件总线
    ts_omit_debugportmapper   # 调试端口映射
    ts_omit_capture           # 抓包

    # --- 平台集成（用不到的平台）---
    ts_omit_kube              # Kubernetes
    ts_omit_cloud             # 云平台
    ts_omit_aws               # AWS
    ts_omit_synology          # 群晖 NAS
    ts_omit_bird              # BGP 路由
    ts_omit_tap               # TAP 设备
    ts_omit_sdnotify          # systemd 通知
    ts_omit_systray           # 系统托盘
    ts_omit_desktop_sessions  # 桌面会话

    # --- 不需要的特性 ---
    ts_omit_drive             # Tailscale Drive（文件系统共享，不同于 Taildrop）
    ts_omit_ace               # 访问控制引擎（旧版）
    ts_omit_acme              # ACME/Let's Encrypt（客户端不需要）
    ts_omit_appconnectors     # App 连接器
    ts_omit_identityfederation # 身份联邦
    ts_omit_oauthkey          # OAuth 密钥管理
    ts_omit_relayserver       # DERP 中继服务器（客户端不需要内嵌）
    ts_omit_wakeonlan         # 局域网唤醒
    ts_omit_tpm               # TPM 芯片
    ts_omit_posture           # 设备合规检查（15人规模暂不需要）

    # --- 明确保留（不在此列表中）---
    # taildrop       → 保留：团队文件传输
    # tailnetlock    → 保留：防止未授权节点加入
    # peerapiclient  → 保留：Taildrop 依赖
    # peerapiserver  → 保留：Taildrop 依赖
    # netstack       → 保留：用户态 TCP/IP + adblock 挂载点
    # dns            → 保留：MagicDNS
    # portmapper     → 保留：NAT 穿透
    # health         → 保留：连接健康检查
)

TAGS=$(IFS=,; echo "${OMIT_TAGS[*]}")
OUTDIR="build"
mkdir -p "$OUTDIR"

echo "Building tailscaled (daemon)..."
go build -tags="$TAGS" -o "$OUTDIR/tailscaled" ./cmd/tailscaled/

echo "Building tailscale (CLI)..."
go build -tags="$TAGS" -o "$OUTDIR/tailscale" ./cmd/tailscale/

echo "Done."
ls -lh "$OUTDIR/tailscaled" "$OUTDIR/tailscale"
echo ""
"$OUTDIR/tailscale" version
