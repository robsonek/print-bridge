#!/usr/bin/env bash
set -euo pipefail

# Detached updater. Invoked as: /bin/sh update-bridge.sh <tag>
# Replaces the binary, preserves config.json + data/, restarts, verifies /health.
TAG="${1:?tag required}"
REPO="robsonek/print-bridge"
INSTALL_DIR=/opt/print-bridge
ARCH="$(dpkg --print-architecture)" # amd64 / arm64
ASSET="print-bridge-${TAG#v}-linux-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

sleep 3
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" -o "$TMP/agent.tar.gz"
# #24: sha256 verification is MANDATORY, fail-closed. A missing/failed checksum
# download (no `|| true`) or a mismatch aborts the update via `set -e` BEFORE the
# binary is ever installed. The .sha256 is co-located with the tarball on the same
# HTTPS host, so this guards against a corrupted/truncated download (NOT active
# tampering — that would need an out-of-band signature, a separate larger change).
curl -fsSL "${URL}.sha256" -o "$TMP/agent.tar.gz.sha256"
(cd "$TMP" && sha256sum -c "agent.tar.gz.sha256")

tar -xzf "$TMP/agent.tar.gz" -C "$TMP"
systemctl stop print-bridge
install -m 0755 "$TMP/print-bridge" "$INSTALL_DIR/print-bridge"
[ -f "$TMP/update-bridge.sh" ] && install -m 0755 "$TMP/update-bridge.sh" "$INSTALL_DIR/update-bridge.sh"
chown -R print-bridge:print-bridge "$INSTALL_DIR"
systemctl start print-bridge

for i in $(seq 1 15); do
  sleep 2
  if curl -fsk "https://localhost:9443/api/v1/health" | grep -q "$(echo "${TAG#v}")"; then
    echo "update to ${TAG} verified"
    exit 0
  fi
done
echo "warning: /health did not report version ${TAG#v} after restart" >&2
