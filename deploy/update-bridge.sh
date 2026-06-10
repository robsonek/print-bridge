#!/usr/bin/env bash
set -euo pipefail

# Detached updater, runs as ROOT. Invoked by the agent as:
#   sudo -n /usr/local/sbin/update-bridge.sh <tag>
# (sudoers drop-in provisioned by install-debian.sh and self-healed below), or
# manually: sudo update-bridge.sh <tag>.
# Replaces the binary + CUPS backend, preserves config.json + data/, restarts,
# verifies /health. The agent appends this script's output to data/update.log.
TAG="${1:?tag required}"

# Defense-in-depth: the agent validates the tag, but the sudoers entry also
# allows DIRECT invocation by the print-bridge user — and the tag is
# interpolated into the download URL, so it must never contain '/' or '..'.
if ! [[ "$TAG" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z]+)*$ ]]; then
  echo "ERROR: invalid tag ${TAG@Q} (expected semver like v1.2.3)" >&2
  exit 1
fi

REPO="robsonek/print-bridge"
INSTALL_DIR=/opt/print-bridge
SELF=/usr/local/sbin/update-bridge.sh
SUDOERS=/etc/sudoers.d/print-bridge
LOGFILE="$INSTALL_DIR/data/update.log"
ARCH="$(dpkg --print-architecture)" # amd64 / arm64
ASSET="print-bridge-${TAG#v}-linux-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

# Ucieczka z cgroupy serwisu: gdy spawnuje nas agent (sudo z wnętrza
# print-bridge.service), `systemctl stop print-bridge` niżej zabiłoby TEN
# proces razem z całą cgroupą — Setpgid odłącza od grupy procesów, ale NIE od
# cgroupy systemd (updater umierał w połowie roboty, zaobserwowane na sprzęcie
# 2026-06-07: log urwany po sha256, serwis zostawał inactive). Re-exec do
# transient unitu (osobna cgroupa) z dopisywaniem wyjścia do LOGFILE.
# Ręczne `sudo update-bridge.sh` (spoza cgroupy serwisu) zostaje inline i
# pisze na terminal operatora.
if [ -z "${PB_UPDATE_DETACHED:-}" ] && grep -qs 'print-bridge\.service' /proc/self/cgroup; then
  if command -v systemd-run >/dev/null; then
    echo "re-exec do transient unitu (poza cgroupą print-bridge.service)"
    exec systemd-run --collect --quiet \
      --unit="print-bridge-update-$(date +%s)" \
      --property=StandardOutput="append:${LOGFILE}" \
      --property=StandardError="append:${LOGFILE}" \
      --setenv=PB_UPDATE_DETACHED=1 \
      "$SELF" "$TAG"
  fi
  echo "WARNING: brak systemd-run — kontynuuję w cgroupie serwisu (systemctl stop może zabić updater)" >&2
fi

echo "=== $(date -Is) update-bridge.sh start tag=${TAG} arch=${ARCH}"
sleep 3
TMP="$(mktemp -d)"
BIN="$INSTALL_DIR/print-bridge"
BAK="$INSTALL_DIR/print-bridge.bak"
STOPPED=0
UPDATE_OK=0

# Rollback: KAŻDA porażka po `systemctl stop` (nieudany install, nowa binarka
# padająca na starcie, timeout health-checka) bez tego trapa zostawiała serwis
# trwale wyłączony ze starą binarką już nadpisaną — zdalny self-update potrafił
# zaciemnić maszynę do ręcznego ssh. Przed stopem binarka idzie do $BAK; trap
# przywraca ją i restartuje serwis, o ile update nie zdążył się powieść.
rollback_on_failure() {
  rm -rf "$TMP"
  if [ "$UPDATE_OK" = 1 ] || [ "$STOPPED" = 0 ]; then
    return
  fi
  echo "=== $(date -Is) ERROR: update nieudany — rollback do poprzedniej binarki" >&2
  install -m 0755 "$BAK" "$BIN"
  chown print-bridge:print-bridge "$BIN"
  if ! systemctl restart print-bridge; then
    echo "=== $(date -Is) ERROR: serwis nie wstał po rollbacku — wymagana ręczna interwencja" >&2
  fi
}
trap rollback_on_failure EXIT

# Download under the REAL asset name. release.yml builds the .sha256 with
# `sha256sum "$TARBALL"` where TARBALL == $ASSET, so the checksum file embeds that
# exact filename. `sha256sum -c` re-opens the filename it reads FROM the checksum
# file, so the local file MUST be named $ASSET (not agent.tar.gz) or the check
# fails to find the file. (Bug #24-regression: downloading as agent.tar.gz made
# every legitimate update abort at the checksum step.)
curl -fsSL "$URL" -o "$TMP/$ASSET"
# #24: sha256 verification is MANDATORY, fail-closed. A missing/failed checksum
# download (no `|| true`) or a mismatch aborts the update via `set -e` BEFORE the
# binary is ever installed. The .sha256 is co-located with the tarball on the same
# HTTPS host, so this guards against a corrupted/truncated download (NOT active
# tampering — that would need an out-of-band signature, a separate larger change).
curl -fsSL "${URL}.sha256" -o "$TMP/${ASSET}.sha256"
(cd "$TMP" && sha256sum -c "${ASSET}.sha256")

tar -xzf "$TMP/$ASSET" -C "$TMP"
# Backup PRZED stopem: między stopem a backupem nie może być okna, w którym
# porażka gubi ostatnią działającą binarkę.
cp -f "$BIN" "$BAK"
systemctl stop print-bridge
STOPPED=1
install -m 0755 "$TMP/print-bridge" "$INSTALL_DIR/print-bridge"
# CUPS backend lives outside INSTALL_DIR and must stay root-owned (cupsd runs
# backends only from /usr/lib/cups/backend; see install-debian.sh).
[ -f "$TMP/lpdpaced" ] && install -o root -g root -m 0755 "$TMP/lpdpaced" /usr/lib/cups/backend/lpdpaced

# Self-update + self-heal of the privilege chain (idempotent). The updater is
# ROOT-OWNED and OUTSIDE /opt: /opt is chowned to print-bridge, and a user must
# never be able to rewrite a script it can sudo (root escalation). The sudoers
# entry is validated with visudo before activation; a broken file is discarded
# rather than bricking sudo. Legacy /opt copy removed (pre-sudoers location).
if [ -f "$TMP/update-bridge.sh" ]; then
  install -o root -g root -m 0755 "$TMP/update-bridge.sh" "$SELF"
fi
rm -f "$INSTALL_DIR/update-bridge.sh"
printf 'print-bridge ALL=(root) NOPASSWD: %s *\n' "$SELF" > "${SUDOERS}.tmp"
chmod 0440 "${SUDOERS}.tmp"
if visudo -cf "${SUDOERS}.tmp" >/dev/null; then
  mv "${SUDOERS}.tmp" "$SUDOERS"
else
  echo "ERROR: wygenerowany sudoers nie przechodzi visudo -c; pomijam" >&2
  rm -f "${SUDOERS}.tmp"
fi

chown -R print-bridge:print-bridge "$INSTALL_DIR"
systemctl start print-bridge

for i in $(seq 1 15); do
  sleep 2
  # -F + pełne pole JSON: niezakotwiczony `grep 1.0.0` traktuje kropki jak
  # wildcardy i łapie podciągi (np. adresy IP) — fałszywy sukces update'u.
  # BEZ -f: /health zwraca 503 przy degraded (drukarka off, cupsd down), ale
  # body wciąż niesie wersję — z -f curl wyrzuca body i trap cofałby DOBRĄ
  # binarkę tylko dlatego, że drukarka była offline w oknie update'u.
  if curl -sk "https://localhost:9443/api/v1/health" | grep -qF "\"version\":\"${TAG#v}\""; then
    UPDATE_OK=1
    rm -f "$BAK"
    echo "=== $(date -Is) update to ${TAG} verified"
    exit 0
  fi
  # Nowa binarka już martwa (systemd: failed) — nie ma na co czekać 30 s,
  # exit 1 odpala rollback z trapa.
  if systemctl is-failed --quiet print-bridge; then
    echo "=== $(date -Is) ERROR: serwis w stanie failed po starcie ${TAG}" >&2
    exit 1
  fi
done
echo "=== $(date -Is) ERROR: /health nie raportuje wersji ${TAG#v} po restarcie — rollback" >&2
exit 1
