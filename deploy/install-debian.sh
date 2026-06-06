#!/usr/bin/env bash
set -euo pipefail

# Usage: sudo ./install-debian.sh <printer_ip> <cups_queue> <egress_allow_cidr>
PRINTER_IP="${1:?printer ip required}"
QUEUE="${2:-xp423b}"
ALLOW_CIDR="${3:?egress CIDR of the orchestrator required}"
INSTALL_DIR=/opt/print-bridge

apt-get update
apt-get install -y cups cups-client poppler-utils ufw

# Raw queue (no PPD): CUPS must NOT touch the bytes — agent owns format.
# lpd:// NOT socket://: the XP-423B print-server drops its buffer on the FIN that
# the CUPS socket backend sends right after the data, so socket:// reports
# "completed" while nothing prints (verified on hardware, hardware-spike-findings.md).
# The LPD protocol frames by byte count + ACKs, so the server receives the whole
# job before the connection closes. "lp" is the print-server's LPD queue name.
lpadmin -p "$QUEUE" -E -v "lpd://${PRINTER_IP}/lp" -o raw
cupsenable "$QUEUE" || true
cupsaccept "$QUEUE" || true

id -u print-bridge >/dev/null 2>&1 || useradd --system --home "$INSTALL_DIR" --shell /usr/sbin/nologin print-bridge
mkdir -p "$INSTALL_DIR/data"

install -m 0755 ./print-bridge "$INSTALL_DIR/print-bridge"
install -m 0644 ./print-bridge.service /etc/systemd/system/print-bridge.service
install -m 0755 ./update-bridge.sh "$INSTALL_DIR/update-bridge.sh"
[ -f "$INSTALL_DIR/config.json" ] || install -m 0600 ./config.json.template "$INSTALL_DIR/config.json"
chown -R print-bridge:print-bridge "$INSTALL_DIR"

# Firewall: only the the marketplace orchestrator egress IP may reach the agent port.
ufw allow from "$ALLOW_CIDR" to any port 9443 proto tcp

systemctl daemon-reload
systemctl enable --now print-bridge
echo "Installed. Edit $INSTALL_DIR/config.json (token!) then: systemctl restart print-bridge"
