#!/bin/bash
set -euo pipefail

OMIT_TAGS=(
    ts_omit_ssh ts_omit_serve
    ts_omit_logtail ts_omit_netlog ts_omit_usermetrics
    ts_omit_clientupdate ts_omit_webclient ts_omit_doctor ts_omit_cliconndiag
    ts_omit_debugeventbus ts_omit_debugportmapper ts_omit_capture
    ts_omit_kube ts_omit_cloud ts_omit_aws ts_omit_synology ts_omit_bird
    ts_omit_tap ts_omit_sdnotify ts_omit_systray ts_omit_desktop_sessions
    ts_omit_drive ts_omit_ace ts_omit_acme ts_omit_appconnectors
    ts_omit_identityfederation ts_omit_oauthkey ts_omit_relayserver
    ts_omit_wakeonlan ts_omit_tpm ts_omit_posture
)

TAGS=$(IFS=,; echo "${OMIT_TAGS[*]}")
OUTDIR="build"
mkdir -p "$OUTDIR"

EXT=""
if [ "${GOOS:-}" = "windows" ]; then EXT=".exe"; fi

go build -tags="$TAGS" -o "$OUTDIR/tailscaled${EXT}" ./cmd/tailscaled/
go build -tags="$TAGS" -o "$OUTDIR/tailscale${EXT}" ./cmd/tailscale/

ls -lh "$OUTDIR/tailscaled${EXT}" "$OUTDIR/tailscale${EXT}"
