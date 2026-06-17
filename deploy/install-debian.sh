#!/usr/bin/env bash
set -euo pipefail

# Usage: sudo ./install-debian.sh <printer_ip> <cups_queue> <egress_allow_cidr> [instance] [listen_port]
# Drugą i kolejne instancje na tej samej VM: podaj slug instancji (4. arg) i jawny
# listen_port (5. arg, ≠ 9443). Bez nich = instalacja instancji podstawowej.
PRINTER_IP="${1:?printer ip required}"
QUEUE="${2:-xp423b}"
ALLOW_CIDR="${3:?egress CIDR of the orchestrator required}"
INSTANCE="${4:-}"
LISTEN_PORT="${5:-}"

# Both values are interpolated into sed (delimiter '#') and a device URI below;
# an unexpected character would silently corrupt config.json seeding (the box
# then 503s every print) or the CUPS queue URI. Fail loudly up front instead.
if ! [[ "$PRINTER_IP" =~ ^[0-9A-Za-z._-]+$ ]]; then
  echo "ERROR: printer_ip ${PRINTER_IP@Q} zawiera niedozwolone znaki (dozwolone: [0-9A-Za-z._-])" >&2
  exit 1
fi
if ! [[ "$QUEUE" =~ ^[0-9A-Za-z._-]+$ ]]; then
  echo "ERROR: cups_queue ${QUEUE@Q} zawiera niedozwolone znaki (dozwolone: [0-9A-Za-z._-])" >&2
  exit 1
fi

if [ -n "$INSTANCE" ]; then
  if ! [[ "$INSTANCE" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
    echo "ERROR: instance ${INSTANCE@Q} zawiera niedozwolone znaki (dozwolone: [a-z0-9-])" >&2
    exit 1
  fi
  if [ -z "$LISTEN_PORT" ]; then
    echo "ERROR: instancja nazwana wymaga jawnego listen_port (5. argument), różnego od 9443" >&2
    exit 1
  fi
fi
if [ -n "$LISTEN_PORT" ]; then
  if ! [[ "$LISTEN_PORT" =~ ^[0-9]+$ ]] || [ "$LISTEN_PORT" -lt 1 ] || [ "$LISTEN_PORT" -gt 65535 ]; then
    echo "ERROR: listen_port ${LISTEN_PORT@Q} poza zakresem 1-65535" >&2
    exit 1
  fi
  if [ -n "$INSTANCE" ] && [ "$LISTEN_PORT" = 9443 ]; then
    echo "ERROR: instancja ${INSTANCE} nie może użyć portu 9443 (kolizja z primary)" >&2
    exit 1
  fi
fi

if [ -n "$INSTANCE" ]; then
  INSTALL_DIR="/opt/print-bridge-${INSTANCE}"
  SERVICE="print-bridge-${INSTANCE}"
else
  INSTALL_DIR=/opt/print-bridge
  SERVICE=print-bridge
fi
PORT="${LISTEN_PORT:-9443}"
UNIT_PATH="/etc/systemd/system/${SERVICE}.service"

apt-get update
apt-get install -y cups cups-client poppler-utils ufw openssl

# Paced LPD backend: the XP-423B print-server (10/100, Ethernut) drops segments
# when a GbE sender bursts >40-60 KB/s; Linux then backs off retransmissions and
# a multi-label job crawls for 30-50 s (= "second label prints a minute late",
# hardware-spike-findings.md). Neither stock backend works: socket:// loses its
# buffer on the early FIN ("completed" while nothing prints), lpd:// frames+ACKs
# correctly but cannot pace. lpdpaced trickles the data file at rate= B/s.
# Own binary in the backend dir (NOT a symlink into /opt): cupsd's AppArmor
# profile may not allow executing from /opt, and a missing backend fails loudly.
install -o root -g root -m 0755 ./lpdpaced /usr/lib/cups/backend/lpdpaced

# Raw queue (no PPD): CUPS must NOT touch the bytes — agent owns format.
# "lp" is the print-server's LPD queue name.
lpadmin -p "$QUEUE" -E -v "lpdpaced://${PRINTER_IP}/lp?rate=20000" -o raw
cupsenable "$QUEUE" || true
cupsaccept "$QUEUE" || true

id -u print-bridge >/dev/null 2>&1 || useradd --system --home "$INSTALL_DIR" --shell /usr/sbin/nologin print-bridge
mkdir -p "$INSTALL_DIR/data"

install -m 0755 ./print-bridge "$INSTALL_DIR/print-bridge"
if [ -n "$INSTANCE" ]; then
  sed -e "s#/opt/print-bridge#${INSTALL_DIR}#g" \
      -e "s#^Description=.*#Description=print-bridge (instance ${INSTANCE})#" \
      ./print-bridge.service > "$UNIT_PATH"
  chmod 0644 "$UNIT_PATH"
else
  install -m 0644 ./print-bridge.service "$UNIT_PATH"
fi

# Seed config.json on a FRESH install (an existing one is preserved untouched on
# re-install / update). The installer already knows the printer IP and queue, so
# write them in — and generate the auth token — leaving a box that needs no
# hand-editing. CRITICAL: printer_ip is the agent's OWN source of truth for the
# reachability pre-check and ~HS confirm; if it stays at the template placeholder
# the agent rejects every print with 503 even though the CUPS queue URI is right.
CONFIG="$INSTALL_DIR/config.json"
GEN_TOKEN=""
if [ ! -f "$CONFIG" ]; then
  install -m 0600 ./config.json.template "$CONFIG"
  GEN_TOKEN="$(openssl rand -hex 32)"
  sed -i \
    -e "s#\"printer_ip\": \"10.0.0.50\"#\"printer_ip\": \"${PRINTER_IP}\"#" \
    -e "s#\"cups_queue\": \"xp423b\"#\"cups_queue\": \"${QUEUE}\"#" \
    -e "s#\"listen_port\": 9443#\"listen_port\": ${PORT}#" \
    -e "s#\"instance\": \"\"#\"instance\": \"${INSTANCE}\"#" \
    -e "s#REPLACE_WITH_64_CHAR_TOKEN#${GEN_TOKEN}#" \
    "$CONFIG"
fi
chown -R print-bridge:print-bridge "$INSTALL_DIR"

# Self-update: the updater is ROOT-OWNED and OUTSIDE /opt (which is chowned to
# print-bridge) — a user must never be able to rewrite a script it can sudo
# (root escalation). The agent spawns it via `sudo -n`; the sudoers drop-in is
# validated with visudo before activation, a broken file is discarded rather
# than bricking sudo.
install -o root -g root -m 0755 ./update-bridge.sh /usr/local/sbin/update-bridge.sh
printf 'print-bridge ALL=(root) NOPASSWD: /usr/local/sbin/update-bridge.sh *\n' > /etc/sudoers.d/print-bridge.tmp
chmod 0440 /etc/sudoers.d/print-bridge.tmp
if visudo -cf /etc/sudoers.d/print-bridge.tmp >/dev/null; then
  mv /etc/sudoers.d/print-bridge.tmp /etc/sudoers.d/print-bridge
else
  echo "ERROR: sudoers nie przechodzi visudo -c" >&2
  rm -f /etc/sudoers.d/print-bridge.tmp
  exit 1
fi

# Firewall: only the the marketplace orchestrator egress IP may reach the agent port.
ufw allow from "$ALLOW_CIDR" to any port "$PORT" proto tcp

systemctl daemon-reload
systemctl enable --now "$SERVICE"
if [ -n "$GEN_TOKEN" ]; then
  echo "Installed instance '${INSTANCE:-<primary>}' (service ${SERVICE}, port ${PORT})."
  echo "config.json seeded (printer_ip=$PRINTER_IP, cups_queue=$QUEUE) — no edit needed."
  echo "print_token (hand this to the orchestrator): $GEN_TOKEN"
else
  echo "Installed. Existing $CONFIG kept unchanged; restart after edits: systemctl restart ${SERVICE}"
fi
