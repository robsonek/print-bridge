# Wieloinstancyjny self-update (slug-based) — design

**Data:** 2026-06-17
**Status:** zaakceptowany do implementacji

## Problem

Agent print-bridge jest single-printer singletonem. Można postawić drugą instancję na
tej samej VM (osobny katalog, port, config, unit), ale **self-update przez API jest
zahardkodowany na instancję 1** i cicho aktualizuje nie tę instancję, którą wywołano.

Łańcuch dowodowy (stan przed zmianą):

- `deploy/update-bridge.sh:21` — `INSTALL_DIR=/opt/print-bridge` (binarka, backup, log).
- `deploy/update-bridge.sh:96,122` (+ rollback `:71`, `:139`) — `systemctl stop/start/restart/is-failed print-bridge`, nazwa unitu sztywna.
- `deploy/update-bridge.sh:131` — health-check `https://localhost:9443/...`, port sztywny.
- `deploy/update-bridge.sh:37` — cgroup-escape grepuje `print-bridge\.service`.
- `cmd/print-bridge/main.go:268-273` — `updaterScript()` zawsze zwraca współdzieloną kopię
  systemową `/usr/local/sbin/update-bridge.sh`; fallback obok binarki i tak przegrywa,
  a sudoers whitelistuje wyłącznie ścieżkę systemową → `sudo -n` na inną ścieżkę = odmowa.
- `internal/update/update.go:55` — `SpawnUpdater` przekazuje skryptowi tylko `<tag>`,
  brak pojęcia „która instancja".

**Skutek:** kliknięcie self-update na instancji 2 (port 9444) odpala wspólny skrypt, który
zatrzymuje/podmienia/health-checkuje **instancję 1**, a instancji 2 nie tyka. Rollback dotyczy
tylko instancji 1.

## Cel

Self-update przez `POST /api/v1/admin/update` ma trafiać w **tę instancję, która go wywołała**,
i obsłużyć N instancji na jednej VM. Pełna ścieżka: updater + strona Go + installer + systemd
świadome nazwanej instancji, tak by instancję 2 dało się postawić jedną komendą instalatora.

## Model tożsamości

Jeden **slug instancji** `INSTANCE` rządzi wszystkim. Pusty = instancja podstawowa (primary,
dzisiejsze zachowanie). Niepusty slug `s`:

- katalog instalacji: `/opt/print-bridge-<s>`
- usługa systemd: `print-bridge-<s>.service`
- port HTTP: **czytany z `config.json` danej instancji** (nie przekazywany jako argument)

Slug walidowany regexem `^[a-z0-9][a-z0-9-]*$` (brak `/`, `..`, spacji, znaków regexowych) — nie
ucieknie ze ścieżki `/opt/print-bridge-<s>` ani nie wstrzyknie się do `systemctl`.

## Zmiany komponentów

### 1. `deploy/update-bridge.sh` — parametryzacja

Nowa sygnatura: `update-bridge.sh <tag> [instance]`.

- `INSTANCE="${2:-}"`; gdy niepusty — walidacja slugu (regex jw.), w przeciwnym razie błąd i exit.
- `INSTALL_DIR` i `SERVICE` wyliczane ze slugu:
  - pusty → `INSTALL_DIR=/opt/print-bridge`, `SERVICE=print-bridge`
  - `s` → `INSTALL_DIR=/opt/print-bridge-<s>`, `SERVICE=print-bridge-<s>`
- Port health-checka czytany z `$INSTALL_DIR/config.json` przez `grep -oE`
  (bez zależności od `jq` — nie ma go na liście pakietów installera), z walidacją numeryczną
  i fallbackiem 9443. Health-check używa `https://localhost:${PORT}/api/v1/health`.
- cgroup-escape (`:37`) grepuje konkretny unit: `grep -qsF "${SERVICE}.service" /proc/self/cgroup`.
  Transient unit z `systemctl stop` celuje w `$SERVICE`.
- Wszystkie `systemctl stop/start/restart/is-failed` operują na `$SERVICE`.
- `BIN`, `BAK`, `LOGFILE` wyliczane z `$INSTALL_DIR`.

**Bezpieczeństwo bez zmian:** nadal JEDEN root-owny skrypt `/usr/local/sbin/update-bridge.sh`
i JEDNA linia sudoers `print-bridge ALL=(root) NOPASSWD: /usr/local/sbin/update-bridge.sh *`
— wildcard `*` już dopuszcza drugi argument. Brak kopii per-instancja → brak problemu
„skrypt instancji 2 się starzeje". Self-update `$SELF` i self-heal sudoers (`:108-119`)
działają dla wszystkich instancji (wspólny skrypt = zawsze ta sama wersja).

### 2. Strona Go

- `internal/config/config.go`:
  - nowe pole `Instance string` z tagiem `json:"instance"`, default `""` (w `Default()`).
  - `Validate()`: jeśli `Instance != ""` musi pasować do `^[a-z0-9][a-z0-9-]*$`, inaczej błąd.
- `internal/update/update.go`:
  - `SpawnUpdater(scriptPath, logPath, tag, instance string)` — dokleja `instance` jako kolejny
    argument `exec.Command` **tylko gdy niepusty**. Dla primary wywołanie jest bajt-w-bajt jak
    dziś (`sudo -n script tag`).
  - `updaterScript()` w `main.go` — bez zmian (wspólny skrypt systemowy).
- `cmd/print-bridge/main.go:96-101`: closure `Updater` przekazuje `cfg.Instance` do `SpawnUpdater`.

### 3. Installer + systemd

- `deploy/install-debian.sh`:
  - dwa opcjonalne argi pozycyjne: `[instance]` (4.) i `[listen_port]` (5.).
  - pusty `instance` → zachowanie dzisiejsze 1:1 (`INSTALL_DIR=/opt/print-bridge`, unit
    `print-bridge.service`, port 9443).
  - niepusty `instance` → **wymaga** jawnego `listen_port` różnego od 9443 (inaczej błąd:
    kolizja portu); walidacja slugu jak w skrypcie i kodzie.
  - `INSTALL_DIR` i nazwa unitu wyliczane ze slugu; unit renderowany `sed`-em z istniejącego
    `print-bridge.service` (podmiana `/opt/print-bridge` → `/opt/print-bridge-<s>` i Description).
  - config seedowany z `instance` i `listen_port`.
  - `ufw allow from "$ALLOW_CIDR" to any port $LISTEN_PORT proto tcp` (zamiast sztywnego 9443 w `:83`).
  - osobna kolejka CUPS — już brana z arg 2 (`<cups_queue>`), bez zmian w mechanizmie.
  - root-owny updater + sudoers instalowane jak dziś (idempotentne; re-instalacja przy
    drugiej instancji nieszkodliwa).
  - user/grupa `print-bridge` współdzielone między instancjami.
- `deploy/print-bridge.service` — primary nietknięty; służy też za szablon do `sed`.
- `deploy/config.json.template` — dochodzi `"instance": ""`.

### 4. Kompatybilność wsteczna

Instancja 1 (lab .120): brak migracji. Pusty slug daje identyczne ścieżki, nazwę usługi, port,
wywołanie updatera i grep cgroupy co dziś. `sudo update-bridge.sh v1.2.3` z ręki (bez 2. argu)
dalej aktualizuje primary.

## Testy

Wzorzec repo: testy Go-only.

- `internal/config/config_test.go`: parsowanie pola `instance`; `Validate()` przyjmuje pusty
  i poprawny slug, odrzuca złe znaki.
- `internal/update/update_test.go`: `SpawnUpdater` dokleja arg instancji gdy ustawiony, pomija
  gdy pusty (przez podmianę `sudoBin` na fake, weryfikacja przekazanych argów).
- Skrypt bash: brak testów Go-owalnych; weryfikacja przez przegląd kodu + ręczny test na sprzęcie
  wg etykiety HW (zgoda na pulę etykiet, operator obserwuje). Sam mechanizm self-update sprawdzalny
  na `/health` bez druku.

## Ryzyka i decyzje

- **Kolizja portu**: port secondary musi być jawny i ≠ 9443 — wymuszane w installerze.
- **Druga drukarka**: osobna kolejka CUPS + osobny `printer_ip` w configu instancji — już wspierane.
- **Firewall**: każda instancja otwiera własny port w ufw.
- **Odczyt portu w bashu bez jq**: `grep -oE '"listen_port"[[:space:]]*:[[:space:]]*[0-9]+'`
  + ekstrakcja liczby; fallback 9443 gdy nie znaleziono.

## Kryteria akceptacji

1. `update-bridge.sh v0.5.5 2` aktualizuje wyłącznie `/opt/print-bridge-2` i `print-bridge-2.service`,
   health-checkuje port instancji 2; `/opt/print-bridge` i `print-bridge.service` nietknięte.
2. `update-bridge.sh v0.5.5` (bez 2. argu) zachowuje się dokładnie jak przed zmianą.
3. `POST /api/v1/admin/update` na instancji ze slugiem `s` w configu spawnuje updater z arg `s`.
4. Rollback przy nieudanym update'cie cofa binarkę właściwej instancji i restartuje jej unit.
5. `install-debian.sh <ip> <queue> <cidr> 2 9444` stawia kompletną instancję 2 (katalog, unit,
   config z `instance:"2"` i `listen_port:9444`, reguła ufw na 9444) bez naruszania instancji 1.
6. Wszystkie istniejące testy Go przechodzą; nowe testy `config`/`update` przechodzą.
