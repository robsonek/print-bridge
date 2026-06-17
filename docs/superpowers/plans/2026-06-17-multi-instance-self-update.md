# Wieloinstancyjny self-update (slug-based) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Self-update przez API ma trafiać w tę instancję agenta, która go wywołała, i obsłużyć N instancji print-bridge na jednej VM.

**Architecture:** Jeden slug instancji rządzi ścieżką (`/opt/print-bridge[-<slug>]`), nazwą usługi (`print-bridge[-<slug>].service`) i portem (czytanym z config.json). Pusty slug = instancja podstawowa, zachowanie bajt-w-bajt jak dziś. Slug płynie: config.json → agent → `SpawnUpdater` (2. argv) → `update-bridge.sh`, walidowany na każdym z trzech poziomów (defense-in-depth, jak istniejący `tag`).

**Tech Stack:** Go 1.x (`internal/config`, `internal/update`, `cmd/print-bridge`), bash (`deploy/update-bridge.sh`, `deploy/install-debian.sh`), systemd, CUPS.

## Global Constraints

- Slug instancji regex: `^[a-z0-9][a-z0-9-]*$` — identyczny w config.go, update.go, update-bridge.sh, install-debian.sh.
- Pusty slug = instancja podstawowa: ścieżki/nazwa usługi/port/wywołanie updatera muszą być identyczne jak przed zmianą (zero migracji instancji 1 na .120).
- Bez nowych zależności runtime: w bashu odczyt portu z config.json przez `grep -oE` (NIE `jq` — nie ma go na liście pakietów installera).
- Bezpieczeństwo bez zmian: JEDEN root-owny skrypt `/usr/local/sbin/update-bridge.sh`, JEDNA linia sudoers z wildcardem `* `. Brak kopii skryptu per instancja.
- Wzorzec testów: Go-only. Skrypty bash weryfikowane statycznie (`bash -n`, ścieżki guard-rejection) + ręczny test HW poza tym planem.
- Commit messages kończ linią: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`

---

### Task 1: Pole `Instance` w configu + walidacja slugu

**Files:**
- Modify: `internal/config/config.go` (struct `Config`, `Validate()`, import)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.Instance string` (tag `json:"instance"`, default `""`); `Config.Validate()` zwraca błąd dla niepustego slugu nie pasującego do `^[a-z0-9][a-z0-9-]*$`.

- [ ] **Step 1: Napisz failujące testy**

W `internal/config/config_test.go` dopisz na końcu pliku:

```go
func TestLoadInstanceFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"print_token":"t","cups_queue":"q","printer_ip":"1.2.3.4","instance":"2"}`), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Instance != "2" {
		t.Errorf("instance = %q, want \"2\"", c.Instance)
	}
}

func TestInstanceDefaultsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Instance != "" {
		t.Errorf("default instance = %q, want empty (primary)", c.Instance)
	}
}

func TestValidateInstanceSlug(t *testing.T) {
	good := []string{"", "2", "warehouse", "label-2", "a1"}
	for _, s := range good {
		c := validConfig()
		c.Instance = s
		if err := c.Validate(); err != nil {
			t.Errorf("instance %q: Validate musi przyjąć, got %v", s, err)
		}
	}
	bad := []string{"-2", "../x", "a/b", "A2", "a b", "a.b", "a_b"}
	for _, s := range bad {
		c := validConfig()
		c.Instance = s
		if err := c.Validate(); err == nil {
			t.Errorf("instance %q: Validate musi odrzucić (ucieczka ścieżki / injection)", s)
		}
	}
}
```

- [ ] **Step 2: Uruchom testy — muszą się wywalić na kompilacji**

Run: `go test ./internal/config/ -run 'TestLoadInstanceFromFile|TestInstanceDefaultsEmpty|TestValidateInstanceSlug' -v`
Expected: FAIL — `c.Instance undefined (type Config has no field or method Instance)`.

- [ ] **Step 3: Dodaj pole `Instance` do structu**

W `internal/config/config.go`, w definicji `type Config struct` wstaw nowe pole zaraz po `ListenPort` (linia 13):

```go
	ListenPort         int    `json:"listen_port"`
	Instance           string `json:"instance"` // slug instancji; "" = podstawowa. Steruje ścieżką/nazwą unitu/portem w self-update.
	PrintToken         string `json:"print_token"`
```

- [ ] **Step 4: Dodaj regex slugu i walidację**

W `internal/config/config.go` dodaj import `"regexp"` (blok importów, linie 4-10) oraz zmienną pakietową pod importami:

```go
// instanceRe ogranicza slug instancji — leci do ścieżek (/opt/print-bridge-<slug>)
// i nazwy unitu systemd w update-bridge.sh; bez tego '/' albo '..' = ucieczka
// ścieżki / wstrzyknięcie do systemctl. Pusty slug (primary) jest dozwolony osobno.
var instanceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
```

W `func (c Config) Validate()` dodaj sprawdzenie zaraz po bloku `ListenPort` (po linii z `out of range 1-65535`):

```go
	if c.Instance != "" && !instanceRe.MatchString(c.Instance) {
		return fmt.Errorf("instance %q invalid (expected slug [a-z0-9-], np. \"2\")", c.Instance)
	}
```

- [ ] **Step 5: Uruchom testy — mają przejść**

Run: `go test ./internal/config/ -v`
Expected: PASS (wszystkie, łącznie z istniejącymi).

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): pole instance (slug) z walidacją [a-z0-9-]

Slug tożsamości instancji dla wieloinstancyjnego self-update; "" = primary.
Walidacja jak dla tagu — broni ścieżek i systemctl przed '/' i '..'.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `SpawnUpdater` przekazuje slug + wpięcie w agenta

**Files:**
- Modify: `internal/update/update.go` (`SpawnUpdater`)
- Modify: `cmd/print-bridge/main.go:96-101` (closure `Updater`)
- Test: `internal/update/update_test.go`

**Interfaces:**
- Consumes: `config.Config.Instance` (Task 1).
- Produces: `update.SpawnUpdater(scriptPath, logPath, tag, instance string) error` — dokleja `instance` jako kolejny argv `sudo` TYLKO gdy niepusty; pusty = wywołanie identyczne jak przed zmianą.

- [ ] **Step 1: Zaktualizuj istniejące wywołania w testach + dopisz nowe testy**

W `internal/update/update_test.go` zmień trzy istniejące wywołania `SpawnUpdater` tak, by przekazywały pusty slug (4. argument):

- w `TestSpawnUpdaterRunsViaSudoAndLogs`:
  `if err := SpawnUpdater("/usr/local/sbin/update-bridge.sh", logPath, "v1.2.3", ""); err != nil {`
- w `TestSpawnUpdaterRejectsBadTagBeforeSpawning`:
  `if err := SpawnUpdater("/usr/local/sbin/update-bridge.sh", logPath, "latest; rm -rf /", ""); err == nil {`
- w `TestSpawnUpdaterUnwritableLogIsError`:
  `err := SpawnUpdater("/usr/local/sbin/update-bridge.sh", "/nonexistent-dir/update.log", "v1.2.3", "")`

Dopisz na końcu pliku dwa nowe testy:

```go
func TestSpawnUpdaterAppendsInstance(t *testing.T) {
	dir := t.TempDir()
	fake := dir + "/fake-sudo"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"FAKE-SUDO $@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := sudoBin
	sudoBin = fake
	t.Cleanup(func() { sudoBin = orig })

	logPath := dir + "/update.log"
	if err := SpawnUpdater("/usr/local/sbin/update-bridge.sh", logPath, "v1.2.3", "2"); err != nil {
		t.Fatalf("SpawnUpdater: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(logPath)
		if strings.Contains(string(b), "FAKE-SUDO -n /usr/local/sbin/update-bridge.sh v1.2.3 2") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log nie zawiera argu instancji: %q", string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSpawnUpdaterRejectsBadInstance(t *testing.T) {
	dir := t.TempDir()
	logPath := dir + "/update.log"
	if err := SpawnUpdater("/usr/local/sbin/update-bridge.sh", logPath, "v1.2.3", "../evil"); err == nil {
		t.Fatal("zły slug instancji musi być odrzucony przed spawnem")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("zły slug nie powinien nawet tworzyć logu")
	}
}
```

- [ ] **Step 2: Uruchom testy — muszą się wywalić**

Run: `go test ./internal/update/ -run 'TestSpawnUpdater' -v`
Expected: FAIL — `not enough arguments in call to SpawnUpdater` (sygnatura jeszcze 3-argumentowa).

- [ ] **Step 3: Zmień sygnaturę i logikę `SpawnUpdater`**

W `internal/update/update.go` dodaj regex pakietowy zaraz pod `var tagRe = ...` (linia 26):

```go
var instanceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
```

Zamień całą funkcję `SpawnUpdater` na:

```go
// SpawnUpdater validates tag (and instance, when set), then launches scriptPath
// detached via `sudo -n`, appending the updater's combined output to logPath. The
// instance slug is forwarded as the script's 2nd arg ONLY when non-empty, so a
// primary-instance update is byte-for-byte the pre-multi-instance invocation.
func SpawnUpdater(scriptPath, logPath, tag, instance string) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	if instance != "" && !instanceRe.MatchString(instance) {
		return fmt.Errorf("invalid instance %q (expected slug [a-z0-9-])", instance)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("updater log %s: %w", logPath, err)
	}
	// The child inherits a dup of the fd at Start(); the parent's copy can be
	// closed right after.
	defer logf.Close()
	fmt.Fprintf(logf, "=== %s spawn updater tag=%s instance=%q script=%s\n",
		time.Now().Format(time.RFC3339), tag, instance, scriptPath)

	args := []string{"-n", scriptPath, tag}
	if instance != "" {
		args = append(args, instance)
	}
	cmd := exec.Command(sudoBin, args...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}
```

- [ ] **Step 4: Wepnij `cfg.Instance` w agenta**

W `cmd/print-bridge/main.go`, w closure `Updater` (linie 96-101), zamień dwie linie:

```go
			log.Printf("admin/update: spawning updater tag=%s instance=%q script=%s log=%s", tag, cfg.Instance, script, logPath)
			return update.SpawnUpdater(script, logPath, tag, cfg.Instance)
```

- [ ] **Step 5: Uruchom testy pakietu + build całości**

Run: `go test ./internal/update/ -v && go build ./...`
Expected: PASS + build bez błędów (potwierdza, że call-site w main.go się zgadza).

- [ ] **Step 6: Pełny zestaw testów**

Run: `go test ./...`
Expected: wszystkie PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/update/update.go internal/update/update_test.go cmd/print-bridge/main.go
git commit -m "$(cat <<'EOF'
feat(update): SpawnUpdater przekazuje slug instancji do skryptu

Dokleja instance jako 2. argv sudo tylko gdy niepusty — primary bez zmian.
Agent przekazuje cfg.Instance; walidacja slugu jako defense-in-depth.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `update-bridge.sh` — wyliczanie instancji ze slugu

**Files:**
- Modify: `deploy/update-bridge.sh`

**Interfaces:**
- Consumes: argv `<tag> [instance]` (slug przekazywany przez `SpawnUpdater` z Task 2).
- Produces: skrypt operujący na `/opt/print-bridge[-<slug>]`, usłudze `print-bridge[-<slug>]` i porcie z config.json tej instancji.

> Uwaga: bash glue nie ma harnessu Go-TDD. Weryfikacja = `bash -n` (składnia) + ścieżki guard-rejection (bezpieczne, kończą się przed `dpkg`/`curl`/`systemctl`) + przegląd. Funkcjonalny happy-path sprawdzany ręcznie na sprzęcie poza planem.

- [ ] **Step 1: Dodaj parsowanie i walidację slugu**

W `deploy/update-bridge.sh`, zaraz po bloku walidacji `TAG` (po linii 18, przed `REPO=`), wstaw:

```bash
INSTANCE="${2:-}"
# Slug instancji leci do ścieżek i nazwy unitu systemd — ściśle ograniczony
# (bez '/', '..', spacji), inaczej ucieczka ścieżki / wstrzyknięcie do systemctl.
# Pusty = instancja podstawowa (zachowanie sprzed wieloinstancyjności).
if [ -n "$INSTANCE" ] && ! [[ "$INSTANCE" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
  echo "ERROR: invalid instance ${INSTANCE@Q} (expected slug [a-z0-9-])" >&2
  exit 1
fi
```

- [ ] **Step 2: Wylicz `INSTALL_DIR`, `SERVICE`, `PORT` ze slugu**

W tym samym pliku zamień blok stałych (linie 20-27, od `REPO=` do `URL=`) na:

```bash
REPO="robsonek/print-bridge"
if [ -n "$INSTANCE" ]; then
  INSTALL_DIR="/opt/print-bridge-${INSTANCE}"
  SERVICE="print-bridge-${INSTANCE}"
else
  INSTALL_DIR=/opt/print-bridge
  SERVICE=print-bridge
fi
SELF=/usr/local/sbin/update-bridge.sh
SUDOERS=/etc/sudoers.d/print-bridge
LOGFILE="$INSTALL_DIR/data/update.log"
ARCH="$(dpkg --print-architecture)" # amd64 / arm64
ASSET="print-bridge-${TAG#v}-linux-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

# Port health-checka z config.json TEJ instancji (nie sztywne 9443). Bez jq —
# nie ma go w zależnościach installera; grep wyłuskuje liczbę, fallback 9443.
PORT="$(grep -oE '"listen_port"[[:space:]]*:[[:space:]]*[0-9]+' "$INSTALL_DIR/config.json" 2>/dev/null | grep -oE '[0-9]+' | head -n1)"
[ -n "$PORT" ] || PORT=9443
```

- [ ] **Step 3: Skieruj cgroup-escape na właściwy unit**

Zamień blok cgroup-escape (linie 37-48). Kluczowe zmiany: `grep -qsF "${SERVICE}.service"`, nazwa transient unitu z `$SERVICE`, oraz przekazanie `$INSTANCE` do re-exec:

```bash
if [ -z "${PB_UPDATE_DETACHED:-}" ] && grep -qsF "${SERVICE}.service" /proc/self/cgroup; then
  if command -v systemd-run >/dev/null; then
    echo "re-exec do transient unitu (poza cgroupą ${SERVICE}.service)"
    exec systemd-run --collect --quiet \
      --unit="${SERVICE}-update-$(date +%s)" \
      --property=StandardOutput="append:${LOGFILE}" \
      --property=StandardError="append:${LOGFILE}" \
      --setenv=PB_UPDATE_DETACHED=1 \
      "$SELF" "$TAG" ${INSTANCE:+"$INSTANCE"}
  fi
  echo "WARNING: brak systemd-run — kontynuuję w cgroupie serwisu (systemctl stop może zabić updater)" >&2
fi
```

- [ ] **Step 4: Podmień wszystkie `print-bridge` (usługa) i port na `$SERVICE`/`$PORT`**

W `deploy/update-bridge.sh` zamień (każde wystąpienie nazwy unitu i portu pochodzące z health-checka):

- start banner (linia ~50): dodaj instancję do logu:
  ```bash
  echo "=== $(date -Is) update-bridge.sh start tag=${TAG} instance=${INSTANCE:-<primary>} arch=${ARCH}"
  ```
- rollback (linia ~71): `if ! systemctl restart "$SERVICE"; then`
- stop (linia ~96): `systemctl stop "$SERVICE"`
- start (linia ~122): `systemctl start "$SERVICE"`
- health-check (linia ~131): `if curl -sk "https://localhost:${PORT}/api/v1/health" | grep -qF "\"version\":\"${TAG#v}\""; then`
- is-failed (linia ~139): `if systemctl is-failed --quiet "$SERVICE"; then`

`BIN`, `BAK` (linie 53-54), `install ... "$INSTALL_DIR/print-bridge"` (98) i `chown -R ... "$INSTALL_DIR"` (121) już używają `$INSTALL_DIR` — zostają bez zmian. CUPS backend (`/usr/lib/cups/backend/lpdpaced`, 101) i sudoers (112-119) są współdzielone — bez zmian.

- [ ] **Step 5: Weryfikacja składni**

Run: `bash -n deploy/update-bridge.sh && command -v shellcheck >/dev/null && shellcheck -S warning deploy/update-bridge.sh || echo "shellcheck pominięty (brak w PATH)"`
Expected: `bash -n` bez wyjścia (OK); shellcheck bez błędów krytycznych (warningi typu SC2086 dla `${INSTANCE:+...}` są zamierzone — celowo bez cudzysłowów dla pominięcia pustego argu).

- [ ] **Step 6: Weryfikacja guard-rejection (bezpieczna, kończy się przed efektami)**

Run: `bash deploy/update-bridge.sh v1.2.3 "BAD/slug"; echo "exit=$?"`
Expected: `ERROR: invalid instance ...` na stderr, `exit=1`, BEZ pobierania/instalacji.

Run: `bash deploy/update-bridge.sh "latest; rm -rf /" 2>/dev/null; echo "exit=$?"`
Expected: `exit=1` (zły tag dalej odrzucany).

- [ ] **Step 7: Commit**

```bash
git add deploy/update-bridge.sh
git commit -m "$(cat <<'EOF'
feat(update): update-bridge.sh wylicza katalog/usługę/port ze slugu

Drugi argv = slug instancji: INSTALL_DIR, SERVICE i port (z config.json)
per instancja. Pusty slug = primary 1:1. cgroup-grep celuje w konkretny unit
(naprawia też latentny false-match między print-bridge a print-bridge-N).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Installer + szablon configu dla nazwanej instancji

**Files:**
- Modify: `deploy/install-debian.sh`
- Modify: `deploy/config.json.template`
- Modify: `README.md` (krótka sekcja o drugiej instancji)

**Interfaces:**
- Consumes: argv `<printer_ip> <cups_queue> <egress_allow_cidr> [instance] [listen_port]`.
- Produces: kompletną instancję (katalog `/opt/print-bridge[-<slug>]`, unit `print-bridge[-<slug>].service`, config z `instance`+`listen_port`, reguła ufw na porcie instancji). Współdzielony updater/sudoers/user.

- [ ] **Step 1: Dodaj `instance` do szablonu configu**

W `deploy/config.json.template` wstaw pole `instance` zaraz po `listen_port` (linia 2):

```json
{
  "listen_port": 9443,
  "instance": "",
  "print_token": "REPLACE_WITH_64_CHAR_TOKEN",
```

- [ ] **Step 2: Sprawdź, że szablon to nadal poprawny JSON**

Run: `python3 -c "import json,sys; json.load(open('deploy/config.json.template')); print('JSON OK')"`
Expected: `JSON OK`.

- [ ] **Step 3: Parsuj nowe argi i waliduj w installerze**

W `deploy/install-debian.sh` zamień nagłówek argów (linie 4-8) na:

```bash
# Usage: sudo ./install-debian.sh <printer_ip> <cups_queue> <egress_allow_cidr> [instance] [listen_port]
# Drugą i kolejne instancje na tej samej VM: podaj slug instancji (4. arg) i jawny
# listen_port (5. arg, ≠ 9443). Bez nich = instalacja instancji podstawowej.
PRINTER_IP="${1:?printer ip required}"
QUEUE="${2:-xp423b}"
ALLOW_CIDR="${3:?egress CIDR of the orchestrator required}"
INSTANCE="${4:-}"
LISTEN_PORT="${5:-}"
```

Zaraz po istniejącym bloku walidacji `PRINTER_IP`/`QUEUE` (po linii 20, przed `apt-get update`) dodaj:

```bash
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
```

Usuń starą linię `INSTALL_DIR=/opt/print-bridge` (była linia 8) — teraz wyliczana powyżej.

- [ ] **Step 4: Renderuj unit per instancja**

W `deploy/install-debian.sh` zamień instalację unitu (linia 45):

```bash
if [ -n "$INSTANCE" ]; then
  sed -e "s#/opt/print-bridge#${INSTALL_DIR}#g" \
      -e "s#^Description=.*#Description=print-bridge (instance ${INSTANCE})#" \
      ./print-bridge.service > "$UNIT_PATH"
  chmod 0644 "$UNIT_PATH"
else
  install -m 0644 ./print-bridge.service "$UNIT_PATH"
fi
```

- [ ] **Step 5: Seeduj `instance` i `listen_port` do configu**

W bloku seedowania configu (linie 58-62) rozszerz `sed -i` o dwa podstawienia:

```bash
  sed -i \
    -e "s#\"printer_ip\": \"10.0.0.50\"#\"printer_ip\": \"${PRINTER_IP}\"#" \
    -e "s#\"cups_queue\": \"xp423b\"#\"cups_queue\": \"${QUEUE}\"#" \
    -e "s#\"listen_port\": 9443#\"listen_port\": ${PORT}#" \
    -e "s#\"instance\": \"\"#\"instance\": \"${INSTANCE}\"#" \
    -e "s#REPLACE_WITH_64_CHAR_TOKEN#${GEN_TOKEN}#" \
    "$CONFIG"
```

(Dla primary `INSTANCE=""`, `PORT=9443` → oba nowe podstawienia są no-opami, config primary bez zmian.)

- [ ] **Step 6: ufw na porcie instancji + enable właściwego unitu**

Zamień regułę ufw (linia 83):

```bash
ufw allow from "$ALLOW_CIDR" to any port "$PORT" proto tcp
```

Zamień `systemctl enable --now print-bridge` (linia 86):

```bash
systemctl enable --now "$SERVICE"
```

Zaktualizuj końcowe komunikaty (linie 87-92), by raportowały instancję i port:

```bash
if [ -n "$GEN_TOKEN" ]; then
  echo "Installed instance '${INSTANCE:-<primary>}' (service ${SERVICE}, port ${PORT})."
  echo "config.json seeded (printer_ip=$PRINTER_IP, cups_queue=$QUEUE) — no edit needed."
  echo "print_token (hand this to the orchestrator): $GEN_TOKEN"
else
  echo "Installed. Existing $CONFIG kept unchanged; restart after edits: systemctl restart ${SERVICE}"
fi
```

- [ ] **Step 7: Weryfikacja składni + guard-rejection**

Run: `bash -n deploy/install-debian.sh && command -v shellcheck >/dev/null && shellcheck -S warning deploy/install-debian.sh || echo "shellcheck pominięty"`
Expected: `bash -n` OK; brak błędów krytycznych.

Run: `bash deploy/install-debian.sh 1.2.3.4 q 10.0.0.0/8 "BAD SLUG" 9444 2>&1 | head -1; echo "exit=${PIPESTATUS[0]}"`
Expected: `ERROR: instance ... niedozwolone znaki`, `exit=1`, przed `apt-get`.

Run: `bash deploy/install-debian.sh 1.2.3.4 q 10.0.0.0/8 2 9443 2>&1 | head -1; echo "exit=${PIPESTATUS[0]}"`
Expected: `ERROR: instancja 2 nie może użyć portu 9443 ...`, `exit=1`.

Run: `bash deploy/install-debian.sh 1.2.3.4 q 10.0.0.0/8 2 2>&1 | head -1; echo "exit=${PIPESTATUS[0]}"`
Expected: `ERROR: instancja nazwana wymaga jawnego listen_port ...`, `exit=1`.

- [ ] **Step 8: Dopisz sekcję do README**

W `README.md` dodaj krótką sekcję (po opisie instalacji) — dokładny tekst:

```markdown
## Druga drukarka na tej samej VM

Każda instancja agenta obsługuje jedną drukarkę. Drugą stawiasz osobną instancją
na tym samym Debianie — nie potrzeba nowej VM:

```bash
sudo ./install-debian.sh <ip_drukarki_2> <kolejka_cups_2> <egress_cidr> 2 9444
```

Slug `2` daje katalog `/opt/print-bridge-2`, usługę `print-bridge-2.service` i
config z `"instance": "2"`, `"listen_port": 9444`. Self-update przez API
(`POST /api/v1/admin/update`) trafia wtedy w tę instancję — wspólny root-owny
`update-bridge.sh` wylicza cel ze slugu z config.json. Instancja podstawowa
(bez slugu) działa jak dotąd: `/opt/print-bridge`, `print-bridge.service`, 9443.
```

- [ ] **Step 9: Commit**

```bash
git add deploy/install-debian.sh deploy/config.json.template README.md
git commit -m "$(cat <<'EOF'
feat(install): instalacja nazwanej instancji (slug + port)

install-debian.sh [instance] [listen_port]: katalog/unit/config/ufw per
instancja, render unitu z szablonu sed-em, seed instance+listen_port.
Primary bez argów = 1:1. README: sekcja o drugiej drukarce na tej samej VM.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- Model tożsamości (slug, pusty=primary) → Task 1 (config), Task 3/4 (derywacja). ✓
- `update-bridge.sh` parametryzacja (INSTALL_DIR/SERVICE/port/cgroup) → Task 3. ✓
- Strona Go (`Instance`, `SpawnUpdater`, wpięcie main) → Task 1 + Task 2. ✓
- Installer + systemd + template → Task 4. ✓
- Kompatybilność wsteczna → wymuszona w każdym tasku (pusty slug = no-op), testy `TestInstanceDefaultsEmpty`, guard-tests. ✓
- Testy config/update → Task 1 Step 1, Task 2 Step 1. ✓
- Kryteria akceptacji 1-6 → odpowiednio Task 3 (1,2,4), Task 2 (3), Task 4 (5), Task 1+2 Step 6 (6). ✓

**Placeholder scan:** brak TBD/TODO; każdy krok ma realny kod lub konkretną komendę z oczekiwanym wyjściem.

**Type consistency:** regex `^[a-z0-9][a-z0-9-]*$` identyczny w config.go/update.go/oba skrypty. `SpawnUpdater(scriptPath, logPath, tag, instance string)` — ta sama 4-argumentowa sygnatura w definicji (T2 S3), wywołaniu w main.go (T2 S4) i wszystkich testach (T2 S1). `cfg.Instance` ↔ `Config.Instance` (T1). `$SERVICE`/`$INSTALL_DIR`/`$PORT` spójne w update-bridge.sh i install-debian.sh.
