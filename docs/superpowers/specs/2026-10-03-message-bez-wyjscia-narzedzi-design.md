# `message` koperty błędu bez wyjścia narzędzi i tekstu systemów zewnętrznych — design

**Data:** 2026-10-03
**Status:** projekt do review (implementacja po GO; wydanie v0.9.0 — później, w oknie z userem)
**Baza:** `origin/main` `fbf2dcc` (merge PR #15: utwardzenie v0.8.0), gałąź
`fix/message-bez-wyjscia-narzedzi`.

## 1. Cel i granice

Agent dokleja do `message` koperty błędu surowe wyjście narzędzi (`pdfinfo`,
`pdftoppm`, `lp`) oraz tekst błędów IPP, sieci, panelu i `~HS` drukarki.
Etykieta PDF to dane odbiorcy, a `pdfinfo` cytuje fragmenty dokumentu. Sonda
(odtworzona 2026-10-03 na poppler z Homebrew): PDF z
`/Encrypt << /Filter /SEKRET-BODY >>` →
`pdfinfo -f 1 -l -1` kończy się kodem 1 z
`Syntax Error: Couldn't find the 'SEKRET-BODY' security handler`. Dziś agent
odsyła to jako `INVALID_PDF` z
`message = "PDF render failed: pdfinfo failed (invalid PDF?): exit status 1: Syntax Error: Couldn't find the 'SEKRET-BODY' security handler\n"`.
Ta poprawna koperta trafia w marketplace-manage do `print_jobs.error_message`,
ActivityLog, logów i `failed_jobs`.

Reguła (jedna dla wszystkich wystąpień):

- `message` = **stały literał agenta + nazwa narzędzia/operacji + kod**
  (kod wyjścia narzędzia, status IPP, status HTTP panelu) **albo stała klasa
  błędu**, gdy kodu nie ma. Liczby w literałach agenta (strona, wymiary z
  guardu MediaBox, liczba prób) zostają — to nasz format, nie tekst z zewnątrz.
- Pełny błąd (wyjście narzędzia, tekst IPP/sieci/panelu/`~HS`) trafia
  **wyłącznie** do lokalnego logu agenta (`log.Printf`) na granicy, która
  buduje kopertę.
- Kody, statusy HTTP, `Retryable()`, klucze i wartości `details` oraz kształty
  sukcesu — **bez zmian**. Zmienia się wyłącznie tekst `message`.

Poza zakresem:

- endpoint `health` (`reach_error`, `host_status_error`, `cups_error` niosą
  tekst błędu z zewnątrz — §8);
- kody i statusy, także istniejące dziwactwa klasyfikacji (np. błąd
  `os.MkdirTemp` daje 422 `INVALID_PDF` — §8);
- zmiany w marketplace-manage (tam trwa PR-1 coder-10; tu tylko census, §6);
- backend CUPS `cmd/lpdpaced` (pisze do logu CUPS, nie do koperty).

Decyzja lidera (2026-10-03, przed specem): `details.panel_state` zostaje bez
zmian (wariant A) — §3.4.

## 2. Census (stan na `fbf2dcc`)

Legenda klasyfikacji:

- **E** — emisja koperty z tekstem z zewnątrz w `message` (do naprawy, §3);
- **Ź** — źródło błędu, które dociera do E (dostaje tekst publiczny, §3.2);
- **D** — `details` z tekstem z zewnątrz (decyzja A, §3.4);
- **S** — stały literał agenta (ewentualnie liczby albo wartości agenta) — bez zmian;
- **L** — tylko lokalny log; **H** — health (poza zakresem);
- **K** — konstrukcja niebędąca błędem (format ZPL, URL, sentinel bez tekstu z zewnątrz).

### 2.1 C1 — emisje koperty

```
grep -rn --include='*.go' -E 'apierr\.New|WithDetail|writeError' internal cmd | grep -v '_test.go'
```

47 wierszy:

| # | Wiersz (surowy wynik) | Klasa | Uwagi |
|---|---|---|---|
| 1 | `internal/printer/reset.go:76: return apierr.New(apierr.CodePrintTimeout, why+" — "+what, 503).WithDetail("reset_sent", sent)` | S | `why`/`what` — stałe literały, `sent` bool |
| 2 | `internal/printer/reset.go:117: return ResetOutcome{}, apierr.New(apierr.CodePrinterOffline,` | **E** | `:118` `+err.Error()` — błąd `WebPanel.Status` |
| 3 | `internal/printer/reset.go:121: return ResetOutcome{}, apierr.New(apierr.CodePrinterBusy,` | S | |
| 4 | `internal/printer/reset.go:132: return ResetOutcome{}, apierr.New(apierr.CodePrinterOffline,` | **E** | `:133` `+err.Error()` — błąd `WebPanel.Reset` |
| 5 | `internal/printer/reset.go:162: return out, apierr.New(apierr.CodePrinterOffline,` | **E** | `:163` `+st.State` — tekst z HTML panelu |
| 6 | `internal/printer/reset.go:164: WithDetail("panel_state", st.State)` | **D** | decyzja A (§3.4) |
| 7 | `internal/printer/reset.go:173: return out, apierr.New(apierr.CodePrinterOffline,` | S | `:174` literał + `strconv.Itoa(maxPolls)` |
| 8 | `internal/printer/print.go:52: … apierr.New(apierr.CodeInvalidPDF, "PDF render failed: "+err.Error(), 422)` | **E** | błąd `Renderer.PDFToZPL` (§2.3) |
| 9 | `internal/printer/print.go:56: … "payload is neither PDF nor ZPL", 422)` | S | |
| 10 | `internal/printer/print.go:60: … "printer not reachable on socket :9100", 503)` | S | błąd `Reachable` ignorowany |
| 11 | `internal/printer/print.go:63: … "CUPS queue is paused/disabled", 503)` | S | błąd `QueuePaused` ignorowany |
| 12 | `internal/printer/print.go:68: … "lp submit failed: "+err.Error(), 503)` | **E** | błąd `Submitter.Submit` (§2.3) |
| 13 | `internal/printer/print.go:97: … "job poll failed: "+err.Error(), 503)` | **E** | błąd `Poller.JobState` (§2.3) |
| 14 | `internal/printer/print.go:108: … "job canceled by CUPS", 422)` | S | |
| 15 | `internal/printer/print.go:110: … "job aborted by CUPS", 503)` | S | |
| 16 | `internal/printer/print.go:119: return Result{CUPSJobID: id}, apierr.New(apierr.CodeQueuePaused,` | S | `:120` literał |
| 17 | `internal/printer/print.go:121: WithDetail("ipp_job_state", state).` | S | int ze stanu IPP |
| 18 | `internal/printer/print.go:122: WithDetail("cups_job_id", id)` | S | `strconv.Itoa` id joba |
| 19 | `internal/printer/print.go:127: … "context canceled while polling", 503)` | S | |
| 20 | `internal/printer/print.go:132: … "job did not complete within confirm timeout", 503)` | S | |
| 21 | `internal/printer/print.go:167: … "printer reports media-empty (~HS)", 503)` | S | |
| 22 | `internal/printer/print.go:169: … "printer paused (~HS)", 503)` | S | |
| 23 | `internal/printer/print.go:173: … "printer head/cover open (~HS)", 503)` | S | |
| 24 | `internal/printer/print.go:180: … "printer fault (~HS): "+hs.Raw, 503)` | **E** | surowa linia 1 `~HS`; gałąź U4, dziś nieosiągalna |
| 25 | `internal/printer/print.go:192: … "context canceled while verifying", 503)` | S | |
| 26 | `internal/printer/print.go:198: return Result{CUPSJobID: id}, apierr.New(apierr.CodePrinterOffline,` | **E** | `:199` `+lastErr.Error()` — błąd sondy `~HS` |
| 27 | `internal/printer/print.go:201: return Result{CUPSJobID: id}, apierr.New(apierr.CodePrintTimeout,` | S | `:202` literał |
| 28 | `internal/server/respond.go:24: func writeError(w http.ResponseWriter, e *apierr.Error) {` | K | definicja — serializuje `*apierr.Error` bez zmian |
| 29 | `internal/server/handlers.go:94: … "Idempotency-Key header required", …` | S | |
| 30 | `internal/server/handlers.go:111: … "idempotency store unavailable, retry", …` | S | błąd store tylko w logu `:108` |
| 31 | `internal/server/handlers.go:130: writeError(w, apierr.New(apierr.CodePrintUnconfirmed,` | S | `:131` literał |
| 32 | `internal/server/handlers.go:132: WithDetail("original_fault", rec.Fault).` | S | kod `apierr` zapisany przez agenta (`persistPending`) |
| 33 | `internal/server/handlers.go:133: WithDetail("cups_job_id", rec.CUPSJobID))` | S | id joba zapisany przez agenta |
| 34 | `internal/server/handlers.go:143: … "pending job has no resumable cups_job_id", …` | S | |
| 35 | `internal/server/handlers.go:160: … "invalid JSON body", …` | S | |
| 36 | `internal/server/handlers.go:169: … "label_base64/pdf_base64 missing or not base64", …` | S | |
| 37 | `internal/server/handlers.go:211: writeError(w, perr)` | K | przekazuje E/S z `Printer` (#8–#27) |
| 38 | `internal/server/handlers.go:244: writeError(w, e)` | K | przekazuje E/S/D z `Resetter` (#1–#7) |
| 39 | `internal/server/handlers.go:259: … "invalid JSON", …` | S | |
| 40 | `internal/server/handlers.go:263: writeError(w, updateError(err))` | K | przekazuje #41–#44 |
| 41 | `internal/server/handlers.go:284: return apierr.New(apierr.CodeInvalidRequest, err.Error(), http.StatusUnprocessableEntity)` | **E** | `err.Error()` — dziś goły sentinel (stały tekst), ale wkleja `err`; opakowany sentinel przeniósłby tekst opakowania |
| 42 | `internal/server/handlers.go:286: return apierr.New(apierr.CodeUpdateInProgress,` | S | `:287` literał |
| 43 | `internal/server/handlers.go:298: … updateFailedMessages[reason], …` | S | stałe per `reason` (v0.8.0) |
| 44 | `internal/server/handlers.go:299: WithDetail("reason", reason)` | S | stała `update.Reason*` |
| 45 | `internal/server/middleware.go:19: … "X-Print-Token header required", …` | S | |
| 46 | `internal/server/middleware.go:23: … "invalid token", …` | S | |
| 47 | `internal/apierr/apierr.go:62: func (e *Error) WithDetail(k string, v any) *Error {` | K | definicja |

Wynik: **E** = 9 emisji (#2, #4, #5, #8, #12, #13, #24, #26, #41), **D** = 1 (#6).

### 2.2 C2/C3 — formatowanie błędów i konkatenacje w pakietach z zakresu

```
grep -rnE '\.Error\(\)|fmt\.Errorf|%v|%s|%q|%w|string\(out\)|CombinedOutput|\.Output\(\)|errors\.New' internal/printer internal/server internal/update cmd/print-bridge --include='*.go' | grep -v '_test.go'
```

70 wierszy:

| Wiersze (surowy wynik, skrócony do `plik:linia`) | Klasa | Uwagi |
|---|---|---|
| `internal/printer/watchdog.go:111`, `:114` | L | `rerr` to `*apierr.Error` (po zmianie stały `message`); szczegół zalogowany na granicy |
| `internal/printer/reset.go:118`, `:133` | **E** | = C1 #2, #4 |
| `internal/printer/zplgf.go:50`, `:127` | K | format ZPL |
| `cmd/print-bridge/main.go:29`, `:32`, `:38`, `:43`, `:157`, `:169`, `:172`, `:285` | L | `log.Fatalf`/`log.Printf` |
| `cmd/print-bridge/main.go:51`, `:73` | K | adres drukarki z configu |
| `cmd/print-bridge/main.go:107`, `:147` | L | |
| `cmd/print-bridge/main.go:217`, `:237`, `:252` | H | `reach_error`, `host_status_error`, `cups_error` — poza zakresem (§8) |
| `internal/printer/webpanel.go:58` | Ź | `panel %s: HTTP %d` — ścieżka stała + status; dociera do C1 #2/#4 |
| `internal/printer/print.go:52`, `:68`, `:97`, `:199` | **E** | = C1 #8, #12, #13, #26 |
| `internal/printer/pdfrender.go:120`, `:122` | Ź | `pdfinfo` CombinedOutput — **wyjście w błędzie** (przypadek SEKRET-BODY) |
| `internal/printer/pdfrender.go:126`, `:132`, `:144` | Ź (S) | literał agenta + liczby (strony, wymiary z guardu MediaBox) |
| `internal/printer/pdfrender.go:174`, `:175` | Ź | `pdftoppm` (ścieżka jednym wywołaniem) — **wyjście w błędzie** |
| `internal/printer/pdfrender.go:182`, `:214` | Ź (S) | literał + liczby |
| `internal/printer/pdfrender.go:206`, `:207` | Ź | `pdftoppm` per strona — **wyjście w błędzie** |
| `internal/printer/pdfrender.go:228` | Ź | `decode %s: %w` — tekst `image/png` o rastrze z `pdftoppm` |
| `internal/printer/cups.go:22` | K | sentinel `ErrJobGone` — kierowany do `verify()`, nie do `message` (`print.go:94-96`) |
| `internal/printer/cups.go:45` | K | URL IPP z nazwą kolejki |
| `internal/printer/cups.go:55` | Ź | `no job id in lp output: %q` — **wyjście `lp` w błędzie** |
| `internal/printer/cups.go:82`, `:84` | Ź | `lp` CombinedOutput — **wyjście w błędzie** |
| `internal/printer/cups.go:86` | Ź | przekazuje `:55` |
| `internal/printer/cups.go:122` | Ź (S) | literał |
| `internal/printer/cups.go:196` | Ź | `IPP error 0x%04x %s` — kod statusu + nazwa z tabeli goipp |
| `internal/server/handlers.go:108`, `:142`, `:205`, `:219`, `:297` | L | |
| `internal/server/handlers.go:270` | K | komentarz |
| `internal/server/handlers.go:284` | **E** | = C1 #41 |
| `internal/server/respond.go:20` | L | |
| `internal/update/update.go:50`, `:51`, `:56` | K | sentinele (stały tekst) |
| `internal/update/update.go:73`, `:133`, `:138`, `:151`, `:162`, `:166`, `:173`, `:188`, `:190`, `:195`, `:201`, `:206`, `:214` | L | `StartError` ze ścieżkami — do koperty idzie wyłącznie `updateFailedMessages[reason]` (v0.8.0); `:138` pisze do `data/update.log` |

```
grep -rnE '"\s*\+\s*[a-zA-Z]|[a-zA-Z)]\s*\+\s*"' internal/printer internal/server internal/update cmd/print-bridge --include='*.go' | grep -v '_test.go'
```

13 wierszy: `reset.go:76` (S), `reset.go:118` (E), `reset.go:133` (E),
`reset.go:163` (**E** — `+st.State`, niewidoczny dla C2), `reset.go:174` (S),
`print.go:52`/`:68`/`:97` (E), `print.go:180` (**E** — `+hs.Raw`, niewidoczny
dla C2), `print.go:199` (E), `pdfrender.go:177`/`:209` (K — wzorzec `Glob`),
`update.go:73` (L).

### 2.3 C4 — błędy zwracane bez formatowania (źródła dla E)

```
grep -rnE 'return .*\b(err|readErr|werr)\)?$|return io\.ReadAll' internal/printer --include='*.go' | grep -v '_test.go'
```

21 wierszy:

| Wiersz | Klasa | Dokąd dociera |
|---|---|---|
| `internal/printer/hoststatus.go:153`, `:159`, `:197` | Ź | błąd sondy `~HS` (dial/zapis/odczyt; tekst z adresem `IP:9100`) → `print.go:199` (E) i health (H) |
| `internal/printer/webpanel.go:50` | Ź | `NewRequestWithContext` (zły `BaseURL` z configu) → C1 #2/#4 |
| `internal/printer/webpanel.go:54` | Ź | transport `Do` — `url.Error` z pełnym URL panelu → C1 #2/#4 |
| `internal/printer/webpanel.go:60` | Ź | błąd `io.ReadAll` → C1 #2/#4 |
| `internal/printer/webpanel.go:67`, `:86` | Ź | przekazują `get` |
| `internal/printer/pdfrender.go:100`, `:106` | Ź | `os.MkdirTemp`/`os.WriteFile` — ścieżki tmp → C1 #8 |
| `internal/printer/pdfrender.go:179`, `:211` | Ź | `filepath.Glob` → C1 #8 |
| `internal/printer/pdfrender.go:224` | Ź | `os.ReadFile` rastra → C1 #8 |
| `internal/printer/pdfrender.go:228` | Ź | = C2 |
| `internal/printer/cups.go:111` | Ź | przekazuje `doIPP` → C1 #13 |
| `internal/printer/cups.go:135` | H | `PrinterReasons` → health `cups_error`; `QueuePaused` ignoruje błąd |
| `internal/printer/cups.go:153`, `:157` | Ź | `EncodeBytes`, `NewRequestWithContext` (lokalne) → C1 #13 |
| `internal/printer/cups.go:162` | Ź | transport `Do` — `url.Error` z URL kolejki → C1 #13 |
| `internal/printer/cups.go:168` | Ź | dekoder goipp (tekst biblioteki + offset) → C1 #13 |
| `internal/printer/cups.go:180` | Ź | `checkIPPStatus` (`:196`) → C1 #13 |

**C5** (dodane po Codex r1 #5 — C4 szuka tylko zmiennych `err`, a błąd może
wracać wprost z wywołania biblioteki):

```
grep -rnE 'return [^/]*\b[A-Za-z_][A-Za-z0-9_]*\.[A-Z][A-Za-z0-9_]*\([^)]*\)\s*$' internal/printer internal/server internal/update --include='*.go' | grep -v '_test.go'
```

22 wiersze: `zplgf.go:79`, `:102`, `:128` (K — budowa ZPL), `webpanel.go:58`,
`pdfrender.go:175`, `:182`, `:207`, `cups.go:55`, `:84`, `:122` (już w C2),
`print.go:56`, `:60`, `:63`, `:108`, `:110`, `:127`, `:132`, `:192` (S — C1),
`cups.go:72` (K — `bytes.Repeat`), `store_adapter.go:18` (L — błąd
`SavePending` tylko w logu `handlers.go:205`), `update.go:73` (L) oraz
**`internal/printer/cups.go:57` `return strconv.Atoi(m[1])` — Ź**: przepełnione
id w wyjściu `lp` daje `*strconv.NumError`, który cytuje cyfry z wyjścia
narzędzia → `Submit` → E2.

### 2.4 Self-update (`internal/update`)

Agent nie pobiera niczego z GitHuba. Pobranie robi `deploy/update-bridge.sh`
(`URL=` w `:51`, `curl -fsSL` w `:158` i `:164`) asynchronicznie, po 202. Jego
wyjście trafia wyłącznie do `data/update.log`. Po stronie Go do koperty idą
tylko: 422 z sentinelem (C1 #41), 409 ze stałym literałem (#42) i 500 ze
stałym tekstem per `reason` (#43–#44). `StartError` (ścieżki) — tylko log
(`handlers.go:297`).

### 2.5 Sondy wykonawcze (2026-10-03, `go1.27.1 darwin/arm64`)

Program w scratchpadzie (`exec` + `httptest`), wyniki:

| Wejście | `err.Error()` | `*exec.ExitError` / kod | `*exec.Error` | `context.Canceled` |
|---|---|---|---|---|
| skrypt: `echo SEKRET >&2; exit 3` | `exit status 3` | tak / 3 | nie | nie |
| skrypt: `kill -9 $$` | `signal: killed` | tak / −1 | nie | nie |
| binarka nieobecna w `PATH` | `exec: "…": executable file not found in $PATH` | nie | tak | nie |
| `CommandContext` z martwym ctx | `context canceled` | nie | nie | tak |
| `GET` na zamknięty serwer, ścieżka `/SEKRET-PANEL/…` | `Get "http://127.0.0.1:55757/SEKRET-PANEL/cgi-bin/status.cgi": dial tcp …: connection refused` | — | — | — |

Wniosek: wyjście narzędzia nigdy nie jest w `exec`-owym `err` (jest tylko w
`out`, który agent dokleja sam), a URL żądania jest w `url.Error`. Kod wyjścia
jest dostępny przez `errors.As(err, &*exec.ExitError)`.

## 3. Decyzje

### 3.1 Mechanizm: tekst publiczny niesiony przez błąd, budowany wyłącznie ze stałych i liczb

Nowy plik `internal/printer/publicerr.go`:

```go
// publicError niesie tekst dla koperty błędu (message) obok pełnego błędu.
// Tekst publiczny powstaje WYŁĄCZNIE ze stałych literałów agenta i liczb
// (kod wyjścia, status IPP/HTTP, numer strony) — nigdy z wyjścia narzędzia ani
// tekstu systemu zewnętrznego. Error() zwraca pełny szczegół bez zmian
// względem v0.8.0 (log agenta, health) — nigdy nie trafia do message.
type publicError struct {
	public string
	err    error
}

func (e *publicError) Error() string { return e.err.Error() }
func (e *publicError) Unwrap() error { return e.err }

func withPublic(public string, err error) error { return &publicError{public: public, err: err} }

// ownErrorf: błąd z literałem agenta; argumenty WYŁĄCZNIE liczbowe — tekst
// publiczny i Error() są wtedy identyczne.
func ownErrorf(format string, args ...any) error { … }

// toolOutcome: "<tool> exited with code N" | "<tool> terminated by signal" |
// "<tool> not started (request context ended)" | "<tool> could not be started".
// tool = stała nazwa binarki u wołającego.
func toolOutcome(tool string, err error) string { … }

// publicMessage: prefix + ": " + tekst publiczny pierwszego publicError w
// łańcuchu err (errors.As), a bez niego sam prefix. Nigdy err.Error().
func publicMessage(prefix string, err error) string { … }
```

- Granica (miejsce budujące kopertę) robi dwie rzeczy: `log.Printf(… %v, err)`
  (pełny błąd, lokalnie) i `apierr.New(kod, publicMessage(prefiks, err), status)`.
- **Jedna reguła fallbacku na KAŻDEJ granicy E1–E3, E6–E7:** błąd bez
  `publicError` w łańcuchu (lokalny I/O, `strconv`, nieznany typ, atrapa w
  teście) daje **sam prefiks** — stały literał granicy. To jedna gałąź
  `publicMessage` (fail-closed): nowy, nieopisany rodzaj błędu nie przenosi
  tekstu do koperty. Granice E4, E5, E8, E9 nie mają tekstu publicznego w
  ogóle (stały literał). Mutant fallbacku: §5 (MF).
- `Error()` źródeł się nie zmienia (te same `fmt.Errorf` co dziś), więc logi i
  pola health mają identyczny tekst jak w v0.8.0.
- Odrzucone: (a) lista typów błędów rozpoznawanych na granicy (np.
  `*exec.ExitError`, `*url.Error`) — nowy typ = wyciek; (b) przycinanie lub
  maskowanie `err.Error()` — tekst z zewnątrz nadal przechodzi; (c) osobne
  kody/`details` dla klas — zmiana kontraktu, poza zakresem.

### 3.2 Źródła (Ź) — tekst publiczny

| Źródło | Tekst publiczny | `Error()` (log) |
|---|---|---|
| `pdfrender.go:120-122` `pdfinfo` | `toolOutcome("pdfinfo", err)` | bez zmian: `pdfinfo failed (invalid PDF?): %v: %s` |
| `pdfrender.go:126` | `pdfinfo: no Page size (invalid PDF?)` (`ownErrorf`) | ten sam |
| `pdfrender.go:132` | `pdfinfo reports %d pages but enumerated %d (invalid PDF?)` (`ownErrorf`) | ten sam |
| `pdfrender.go:144` | `PDF page %d is %.0fx%.0fmm, exceeding the %dmm roll … (allegro-api#10120)` (`ownErrorf`; liczby z `pdfinfo` parsowane regexem `[0-9.]+` → `ParseFloat` → `%.0f`, czyli tylko cyfry) | ten sam |
| `pdfrender.go:174-175` `pdftoppm` | `toolOutcome("pdftoppm", err)` | bez zmian |
| `pdfrender.go:182` | `pdftoppm produced no png` (`ownErrorf`) | ten sam |
| `pdfrender.go:206-207` `pdftoppm` per strona | `toolOutcome("pdftoppm", err) + " (page N)"` | bez zmian |
| `pdfrender.go:214` | `pdftoppm produced %d pngs for page %d, want 1` (`ownErrorf`) | ten sam |
| `pdfrender.go:228` decode rastra | `raster decode failed` | bez zmian |
| `pdfrender.go:100`, `:106`, `:179`, `:211`, `:224` (tmp, `Glob`, odczyt) | — (granica daje sam prefiks) | bez zmian |
| `cups.go:55` brak job id | `no job id in lp output` | bez zmian (`%q` wyjścia) |
| `cups.go:57` `strconv.Atoi` (przepełnione id) | — (granica daje sam prefiks: `lp submit failed`) | bez zmian (`strconv.Atoi: parsing "…": value out of range`) |
| `cups.go:82-84` `lp` | `toolOutcome("lp", err)` | bez zmian |
| `cups.go:122` | `job-state not found in IPP response` (`ownErrorf`) | ten sam |
| `cups.go:160-162` transport IPP | `IPP transport error` | bez zmian |
| `cups.go:167-168` dekoder goipp | `invalid IPP response` | bez zmian |
| `cups.go:196` status IPP | `IPP error 0x%04x` (sam kod liczbowy) | bez zmian (z nazwą goipp) |
| `cups.go:153`, `:157` (kodowanie, budowa żądania) | — (sam prefiks) | bez zmian |
| `webpanel.go:52-54` transport panelu | `brak połączenia z panelem` | bez zmian |
| `webpanel.go:57-58` HTTP ≠ 200 | `HTTP %d` | bez zmian: `panel %s: HTTP %d` |
| `webpanel.go:60` odczyt odpowiedzi | `przerwany odczyt odpowiedzi panelu` | bez zmian |
| `webpanel.go:48-50` (zły `BaseURL`) | — (sam prefiks) | bez zmian |
| `hoststatus.go:153`, `:159`, `:197` sonda `~HS` | — (granica `print.go:199` ma stałą klasę) | bez zmian (health) |

`ErrJobGone` zostaje gołym sentinelem (`cups.go:108`): `JobState` czyta
`resp.Code` przed zwróceniem błędu, więc opakowanie błędu statusu w
`publicError` nie zmienia tej ścieżki.

### 3.3 Granice (E) — nowy `message`

| # | Granica | Kod / HTTP | `message` v0.8.0 | `message` v0.9.0 | Log (`log.Printf`) |
|---|---|---|---|---|---|
| E1 | `print.go:52` | `INVALID_PDF` 422 | `PDF render failed: ` + `err.Error()` | `publicMessage("PDF render failed", err)` | `print: PDF render failed: %v` |
| E2 | `print.go:68` | `CUPS_UNAVAILABLE` 503 | `lp submit failed: ` + `err.Error()` | `publicMessage("lp submit failed", err)` | `print: lp submit failed: %v` |
| E3 | `print.go:97` | `CUPS_UNAVAILABLE` 503 | `job poll failed: ` + `err.Error()` | `publicMessage("job poll failed", err)` | `print: job %d poll failed: %v` |
| E4 | `print.go:180` | `PRINTER_OFFLINE` 503 | `printer fault (~HS): ` + `hs.Raw` | `printer fault (~HS)` | `print: job %d ~HS fault (U4): %q` (`hs.Raw`) |
| E5 | `print.go:198-199` | `PRINTER_OFFLINE` 503 | `printer unreachable during ~HS verification: ` + `lastErr.Error()` | `printer unreachable during ~HS verification` | `print: job %d no ~HS answer within budget: %v` |
| E6 | `reset.go:117-118` | `PRINTER_OFFLINE` 503 | `panel drukarki (status.cgi) niedostępny: ` + `err.Error()` | `publicMessage("panel drukarki (status.cgi) niedostępny", err)` | `reset: panel status.cgi niedostępny: %v` |
| E7 | `reset.go:132-133` | `PRINTER_OFFLINE` 503 | `func=reset nie powiódł się: ` + `err.Error()` | `publicMessage("func=reset nie powiódł się", err)` | `reset: func=reset nie powiódł się: %v` |
| E8 | `reset.go:162-163` | `PRINTER_OFFLINE` 503 + `details.panel_state` | `po resecie panel raportuje fault: ` + `st.State` | `po resecie panel raportuje fault (stan w details.panel_state)` | `reset: po resecie panel raportuje fault: %q` |
| E9 | `handlers.go:284` | `INVALID_REQUEST` 422 | `err.Error()` | `update.ErrInvalidTag.Error()` albo `update.ErrInvalidInstance.Error()` — tekst SENTINELA, nie `err` (dziś identyczny) | `admin/update: rejected: %v` |

- E4: emisja wydzielona do funkcji (`hsFaultError`) — gałąź jest dziś
  nieosiągalna przez `Print` (§2.3 specu v0.8.0), więc test i mutant celują w
  tę funkcję (§5).
- E5: brak kodu (sonda `~HS` daje błędy sieci) → stała klasa. Dziś `lastErr`
  to m.in. `dial tcp <IP>:9100: …`.
- Przykłady v0.9.0: `PDF render failed: pdfinfo exited with code 1`,
  `PDF render failed: pdftoppm exited with code 99 (page 2)`,
  `lp submit failed: lp exited with code 1`,
  `lp submit failed: no job id in lp output`,
  `job poll failed: IPP transport error`,
  `job poll failed: IPP error 0x0401`,
  `panel drukarki (status.cgi) niedostępny: HTTP 503`,
  `func=reset nie powiódł się: brak połączenia z panelem`.
- **Warunek konsumenta (lider):** każdy `message` niepusty —
  `app/Modules/Printing/Bridge/PrintBridgeClient.php:207` (marketplace-manage) przy pustym `message` przechodzi na fragment
  body. Wszystkie prefiksy E1–E9 są niepustymi literałami, a `publicMessage`
  zwraca co najmniej prefiks. Pin: `checkEnvelopeInvariants` w
  `internal/server/contract_test.go` (`message` niepusty dla KAŻDEGO przypadku
  golden) + test jednostkowy `publicMessage` (§5).

### 3.4 `details.panel_state` (decyzja lidera: A)

`details.panel_state` (`reset.go:164`) zostaje bez zmian — tekst stanu z
firmware'u print-servera (np. `"Paper Jam"`, może być `""`), a nie z dokumentu.
Klasa usuwana tym specem (dane etykiety/odbiorcy wracające kopertą) go nie
obejmuje; w `app/` marketplace-manage nikt nie czyta `details.panel_state`
(§6). Tekst panelu znika tylko z `message` (E8). Świadome residuum tej samej
klasy (pola, które PHP wyświetla i czyta): §8.

### 3.5 Wersja

Zmiana tekstu na drucie → wydanie **v0.9.0** (konwencja repo: tag `v*`
uruchamia `.github/workflows/release.yml`; ostatni tag `v0.8.0`). W
`docs/error-contract.md` adnotacje „od v0.9.0”. Tag, release i
`print-bridge:update` — tylko w oknie deployu z userem, NIE w tym PR.

## 4. Kontrakt: przed → po (od v0.9.0)

| Endpoint | Przypadek | v0.8.0 | v0.9.0 |
|---|---|---|---|
| print-jobs | `pdfinfo` kończy się kodem ≠ 0 | 422 `INVALID_PDF`, message z wyjściem `pdfinfo` | 422 `INVALID_PDF`, `PDF render failed: pdfinfo exited with code N` |
| print-jobs | `pdftoppm` kończy się kodem ≠ 0 | 422, message z wyjściem | 422, `PDF render failed: pdftoppm exited with code N` (` (page N)` w ścieżce per strona) |
| print-jobs | narzędzie zabite / nie wystartowało | 422, `signal: killed` / tekst `exec` | 422, `… terminated by signal` / `… not started (request context ended)` / `… could not be started` |
| print-jobs | guard MediaBox, liczba stron, brak `Page size`, liczba PNG | 422, literał agenta + liczby | bez zmian |
| print-jobs | błąd lokalny renderu (tmp, `Glob`, odczyt rastra) | 422, tekst OS ze ścieżką tmp | 422, `PDF render failed` |
| print-jobs | decode rastra | 422, tekst `image/png` | 422, `PDF render failed: raster decode failed` |
| print-jobs | `lp` kończy się kodem ≠ 0 | 503 `CUPS_UNAVAILABLE`, message z wyjściem `lp` | 503, `lp submit failed: lp exited with code N` |
| print-jobs | `lp` bez `request id` | 503, `%q` wyjścia `lp` | 503, `lp submit failed: no job id in lp output` |
| print-jobs | IPP: transport / dekoder / status / brak `job-state` | 503, tekst `url.Error` z URL / goipp / `IPP error 0x… nazwa` / literał | 503, `job poll failed: IPP transport error` / `invalid IPP response` / `IPP error 0x0401` / `job-state not found in IPP response` |
| print-jobs | brak odpowiedzi `~HS` w budżecie | 503 `PRINTER_OFFLINE`, z tekstem sieci | 503, `printer unreachable during ~HS verification` |
| print-jobs | bezpiecznik U4 (dziś nieosiągalny) | 503, z linią `~HS` | 503, `printer fault (~HS)` |
| printer-reset | panel (status.cgi / func=reset) niedostępny | 503 `PRINTER_OFFLINE`, tekst `url.Error` albo `panel …: HTTP N` | 503, `…: brak połączenia z panelem` / `…: HTTP N` / `…: przerwany odczyt odpowiedzi panelu` |
| printer-reset | fault po resecie | 503 + `details.panel_state`, message z tekstem panelu | 503 + `details.panel_state` (bez zmian), `po resecie panel raportuje fault (stan w details.panel_state)` |
| update | 422 zły tag / instancja | `err.Error()` (= tekst sentinela) | tekst sentinela (bajty identyczne dla gołego sentinela) |
| wszystkie | kody, HTTP, `Retryable()`, `details`, sukcesy | — | bez zmian |

Zmiany w `docs/error-contract.md` (każda oznaczona „od v0.9.0”):

- nagłówek — opis v0.9.0 (wyłącznie treść `message`);
- §2 — inwariant `message`: bez wyjścia narzędzi i tekstu systemów
  zewnętrznych; pełny błąd tylko w logu agenta; do v0.8.0 potrafił je nieść;
- §2.3 — `panel_state`: tekst firmware'u panelu, `message` go nie powtarza;
- §7 — sekcja v0.9.0;
- §8 — nowe testy.

`contract_doc_test.go` parsuje tylko tabele ze znacznikami `kontrakt:` (§2.1,
§2.2, §1.x) — żaden wiersz tabel się nie zmienia (kody, HTTP, `details`).

## 5. Testy (TDD: RED zapisany przed kodem)

### 5.1 Harness i asercje

Znacznik `SEKRET-<miejsce>` jest unikalny per test. Log przechwytuje
`log.SetOutput` (wzorzec `internal/server/respond_test.go:31-33`), z
przywróceniem w `t.Cleanup`. Fałszywe narzędzia to skrypty `#!/bin/sh` w
`t.TempDir()` + `t.Setenv("PATH", dir)`. Żaden test repo nie używa
`t.Parallel` (grep → 0), więc globalne `PATH`, `TMPDIR` i log są bezpieczne.

Warianty asercji (każdy test ma dokładnie jeden, wskazany w tabeli §5.2):

- **W1 — znacznik wstrzyknięty w tekst z zewnątrz:** (1) znacznika NIE MA w
  kopercie — w `json.Marshal(*apierr.Error)` (dokładnie to, co pisze
  `writeError`), a w testach przez `Router()` — w surowym body; (2) kod i HTTP
  równe dzisiejszym; (3) `message` RÓWNY literałowi z §3.3; (4) znacznik JEST w
  przechwyconym logu.
- **W2 — tekstu z zewnątrz nie da się oznaczyć** (status liczbowy, tekst
  biblioteki, literał agenta): (2) i (3) jak W1; (4′) log zawiera DOKŁADNY
  oczekiwany `Error()` źródła (np. `panel /cgi-bin/status.cgi: HTTP 503`).
- **W3 — decyzja A (`details.panel_state`):** znacznika NIE MA w `message`;
  `details` RÓWNE `{"panel_state": "<znacznik>"}`; (2), (3), (4) jak W1.

### 5.2 Testy miejsc

`internal/printer/message_leak_test.go` — przez `Printer.Print` /
`PrinterResetter.Reset` z prawdziwymi źródłami (`PDFRenderer`, `CUPSClient`,
`WebPanel`), wyłącznie istniejące API:

| Test | Miejsce | Wejście | Oczekiwany `message` | W |
|---|---|---|---|---|
| T1 | E1 ← `pdfinfo` | fałszywy `pdfinfo`: `Couldn't find the 'SEKRET-BODY' security handler` na stderr, `exit 1` | `PDF render failed: pdfinfo exited with code 1` | W1 |
| T2 | E1 ← `pdftoppm` (jedno wywołanie) | fałszywy `pdfinfo` (1 strona 288×432 pt, `rot: 0`), fałszywy `pdftoppm`: znacznik, `exit 99` | `PDF render failed: pdftoppm exited with code 99` | W1 |
| T3 | E1 ← `pdftoppm` per strona | `pdfinfo` z `rot: 90`, `pdftoppm` jak T2 | `PDF render failed: pdftoppm exited with code 99 (page 1)` | W1 |
| T4 | E1 ← `pdfrender.go:144` (guard MediaBox) | `pdfinfo` z 595×842 pt | `PDF render failed: PDF page 1 is 210x297mm, exceeding the 102mm roll in both orientations — MediaBox likely A4 not A6 (allegro-api#10120)` | W2 |
| T4a | E1 ← `pdfrender.go:126` | `pdfinfo` bez linii `Page size` | `PDF render failed: pdfinfo: no Page size (invalid PDF?)` | W2 |
| T4b | E1 ← `pdfrender.go:132` | `pdfinfo`: `Pages: 2`, jedna linia `Page size` | `PDF render failed: pdfinfo reports 2 pages but enumerated 1 (invalid PDF?)` | W2 |
| T4c | E1 ← `pdfrender.go:182` | `pdftoppm` `exit 0` bez plików | `PDF render failed: pdftoppm produced no png` | W2 |
| T4d | E1 ← `pdfrender.go:214` | ścieżka per strona, `pdftoppm` tworzy 2 pliki | `PDF render failed: pdftoppm produced 2 pngs for page 1, want 1` | W2 |
| T4e | E1 ← `pdfrender.go:228` | `pdftoppm` `exit 0`, plik `out-1.png` z treścią `SEKRET-RASTER` (nie PNG) | `PDF render failed: raster decode failed` | W2 (`decode out-1.png: …`) |
| T4f | E1 ← `pdfrender.go:100` (fallback na prawdziwym źródle) | `t.Setenv("TMPDIR", "<tmp>/SEKRET-TMP/nie-ma")` | `PDF render failed` | W1 |
| T5 | E1 ← fallback (atrapa) | `Renderer` zwraca `errors.New("SEKRET-RENDER")` | `PDF render failed` | W1 |
| T6 | E2 ← `cups.go:84` (`lp`) | prawdziwy `CUPSClient.Submit`, fałszywy `lp`: znacznik, `exit 2` | `lp submit failed: lp exited with code 2` | W1 |
| T7 | E2 ← `cups.go:55` | fałszywy `lp`: `SEKRET-LPOUT`, `exit 0` | `lp submit failed: no job id in lp output` | W1 |
| T7a | E2 ← `cups.go:57` (fallback na prawdziwym źródle) | fałszywy `lp`: `request id is q-91827364550918273645509182736455`, `exit 0` (znacznik = cyfry) | `lp submit failed` | W1 |
| T8 | E3 ← `cups.go:162` (transport IPP) | `CUPSClient.JobState`, `httpc` z `RoundTripper` zwracającym `errors.New("SEKRET-DIAL")`; URL neutralny (znacznik TYLKO w przyczynie) | `job poll failed: IPP transport error` | W1 |
| T8b | E3 ← `cups.go:162` (URL w `url.Error`) | `ippURL` = zamknięty `httptest` + `/printers/SEKRET-Q` (znacznik TYLKO w URL) | `job poll failed: IPP transport error` | W1 |
| T8a | E3 ← `cups.go:157` (fallback na prawdziwym źródle) | `ippURL` z bajtem `0x7f` i `SEKRET-URL` | `job poll failed` | W1 |
| T9 | E3 ← `cups.go:168` (dekoder) | `httptest` zwraca `SEKRET-IPP` + śmieci | `job poll failed: invalid IPP response` | W2 |
| T10 | E3 ← `cups.go:196` (status) | `httptest` zwraca wiadomość IPP ze statusem 0x0401 | `job poll failed: IPP error 0x0401` | W2 (`IPP error 0x0401 client-error-forbidden`) |
| T10a | E3 ← `cups.go:122` | `httptest`: status OK, bez atrybutu `job-state` | `job poll failed: job-state not found in IPP response` | W2 |
| T12 | E5 | `Prober` zwraca `errors.New("SEKRET-HSERR")` w całym budżecie | `printer unreachable during ~HS verification` | W1 |
| T13 | E6 ← `webpanel.go:54` (transport) | `WebPanel`, `HTTPC` z `RoundTripper` zwracającym `errors.New("SEKRET-PANELDIAL")`; `BaseURL` neutralny (znacznik TYLKO w przyczynie) | `panel drukarki (status.cgi) niedostępny: brak połączenia z panelem` | W1 |
| T13b | E6 ← `webpanel.go:54` (URL w `url.Error`) | `BaseURL` = zamknięty `httptest` + `/SEKRET-PANEL` (znacznik TYLKO w URL) | `panel drukarki (status.cgi) niedostępny: brak połączenia z panelem` | W1 |
| T13a | E6 ← `webpanel.go:50` (fallback na prawdziwym źródle) | `BaseURL` z bajtem `0x7f` i `SEKRET-BASEURL` | `panel drukarki (status.cgi) niedostępny` | W1 |
| T14 | E6 ← `webpanel.go:58` (HTTP) | `httptest` 503 | `panel drukarki (status.cgi) niedostępny: HTTP 503` | W2 (`panel /cgi-bin/status.cgi: HTTP 503`) |
| T14a | E6 ← `webpanel.go:60` (odczyt body) | `httptest`: `Content-Length: 1000`, 10 bajtów i zerwanie | `panel drukarki (status.cgi) niedostępny: przerwany odczyt odpowiedzi panelu` | W2 (`unexpected EOF`) |
| T15 | E6 ← fallback (atrapa) | `PanelAPI` zwraca `errors.New("SEKRET-PANELERR")` | `panel drukarki (status.cgi) niedostępny` | W1 |
| T16 | E7 | panel `Ready`; `RoundTripper` zwraca błąd `SEKRET-RESET` tylko dla `function.cgi` | `func=reset nie powiódł się: brak połączenia z panelem` | W1 |
| T17 | E8 | po resecie `redtext` `SEKRET-STATE` | `po resecie panel raportuje fault (stan w details.panel_state)` | W3 |
| T23 | ciągłość `Error()` (health, log) | `CUPSClient.PrinterReasons` na `httptest` ze statusem 0x0401 oraz z `RoundTripper` zwracającym `errors.New("SEKRET-DIAL")`; `WebPanel.Status` na 503 oraz z `RoundTripper` zwracającym `errors.New("SEKRET-PANELDIAL")` | CAŁE `Error()` RÓWNE dzisiejszemu (deterministyczne): `IPP error 0x0401 client-error-forbidden`, `Post "<ippURL>": SEKRET-DIAL`, `panel /cgi-bin/status.cgi: HTTP 503`, `Get "<BaseURL>/cgi-bin/status.cgi": SEKRET-PANELDIAL` | — |

`internal/server/message_leak_test.go` — przez `Router()` (świat jak
`contract_test.go`, z prawdziwymi źródłami tam, gdzie się da):

| Test | Miejsce | Wejście | Oczekiwany `message` | W |
|---|---|---|---|---|
| T18 | E9 | `Updater` zwraca `fmt.Errorf("SEKRET-TAG: %w", update.ErrInvalidTag)`; drugi przypadek z `ErrInvalidInstance` | tekst sentinela | W1 |
| T19 | E1, **prawdziwy `pdfinfo`** | PDF z `/Encrypt << /Filter /SEKRET-BODY >>` (bajty jak w sondzie §1); `t.Skip`, gdy `pdfinfo` nie ma w `PATH` (CI instaluje `poppler-utils`) | `PDF render failed: pdfinfo exited with code 1` | W1 |
| T20 | E2 | prawdziwy `printer.NewCUPSClient` jako `Submitter`, fałszywy `lp` w `PATH` | `lp submit failed: lp exited with code 2` | W1 |
| T21 | E8 | `panelTransport` z `redtext` `SEKRET-STATE` po resecie | jak T17 | W3 |

Nowe symbole (etapy (b) i (c) w §5.4):

| Test | Plik | Co |
|---|---|---|
| T11 | `internal/printer/hsfault_test.go` | E4: `hsFaultError` z `HostStatus{Raw: "SEKRET-HS"}` → `PRINTER_OFFLINE` 503, `message` = `printer fault (~HS)`, W1 |
| T22 | `internal/printer/publicerr_test.go` | `toolOutcome` — 4 klasy z prawdziwych procesów (skrypt `exit 3`, skrypt `kill -9 $$`, brak binarki, martwy ctx) z dokładnym tekstem; `publicMessage` — z `publicError`, z `publicError` opakowanym `%w`, bez `publicError` (= prefiks), zawsze niepusty i nigdy nie zawiera `err.Error()`; `publicError.Error()` = tekst źródła; `errors.Is(withPublic(x, base), base)`; `ownErrorf` — tekst publiczny = `Error()` |

### 5.3 Zmiany istniejących testów

Golden (`internal/server/contract_test.go`) zmieniane **jawnie** — przypadki z
atrapami zwracającymi zwykłe `errors.New` dają fallback (sam prefiks); panel
jest prawdziwym `WebPanel` nad `panelTransport`:

| Przypadek | v0.8.0 | v0.9.0 |
|---|---|---|
| `pdf-nie-renderuje` | `PDF render failed: pdftoppm: exit status 1` | `PDF render failed` |
| `hs-milczy-w-budzecie` | `printer unreachable during ~HS verification: i/o timeout` | `printer unreachable during ~HS verification` |
| `lp-padlo` | `lp submit failed: lp: scheduler not responding` | `lp submit failed` |
| `ipp-padlo` | `job poll failed: ipp: connection refused` | `job poll failed` |
| `reset-panel-niedostepny` | `panel drukarki (status.cgi) niedostępny: panel /cgi-bin/status.cgi: HTTP 503` | `panel drukarki (status.cgi) niedostępny: HTTP 503` |
| `reset-func-reset-padl` | `func=reset nie powiódł się: panel /admin/cgi-bin/function.cgi?func=reset: HTTP 500` | `func=reset nie powiódł się: HTTP 500` |
| `reset-fault-po-resecie` | `po resecie panel raportuje fault: Paper Jam` | `po resecie panel raportuje fault (stan w details.panel_state)` (`details` bez zmian) |
| `reset-fault-pusty-stan` | `po resecie panel raportuje fault: ` | `po resecie panel raportuje fault (stan w details.panel_state)` (`details.panel_state == ""` bez zmian) |

Test U4 `TestVerifyEveryHealthyFaultHasDedicatedCase`
(`internal/printer/print_test.go:572`) rozpoznaje bezpiecznik po prefiksie
`"printer fault (~HS): "`. Zmienia się na równość z literałem
`"printer fault (~HS)"`, bez zmiany sensu.

### 5.4 Etapy RED (wyniki wpisywane z biegów, nie z przewidywań)

Brakujący symbol w jednym pliku `_test.go` uniemożliwia kompilację całego
pakietu testów, więc RED idzie etapami. Do raportu trafia surowy wynik każdego
etapu:

- **(a) Istniejące API, baza `fbf2dcc`:** `internal/printer/message_leak_test.go`,
  `internal/server/message_leak_test.go`, zmiany golden i testu U4 (§5.3).
  Kompilują się na bazie. Bieg `go test ./internal/printer ./internal/server`
  → zapisana lista FAIL per test z przyczyną (która asercja W1–W3 padła).
  Oczekiwanie do potwierdzenia biegiem: każdy test z §5.2 (poza T11, T22) i
  każdy zmieniony golden jest czerwony, a T23 zielony (pin ciągłości).
- **(b) Refaktor bez zmiany zachowania:** wydzielenie `hsFaultError` z
  DZISIEJSZYM tekstem (`"printer fault (~HS): "+hs.Raw`, bez logu) + T11 → bieg
  → T11 czerwony na zachowaniu (znacznik w kopercie, brak logu), reszta pakietu
  bez zmian względem (a).
- **(c) Nowe symbole:** `internal/printer/publicerr_test.go` (T22) → bieg →
  błąd kompilacji „undefined: toolOutcome/publicMessage/withPublic/ownErrorf”
  (zapisany jako „brak symbolu”, NIE jako dowód regresji). Zachowania nowych
  symboli dowodzą mutanty MS1–MS7 (§5.5).
- **GREEN:** implementacja §3 → `go test ./...` zielone.

### 5.5 Mutanty ręczne

Każdy na pliku z kopii zapasowej (przywracanie z kopii, nie z gita), bieg
`go test` pakietu(ów) testów z kolumny RED. Wynik w tabeli raportu.

| # | Mutacja (przywrócenie wklejania / zepsucie symbolu) | RED (co najmniej) |
|---|---|---|
| M1 | E1: `"PDF render failed: "+err.Error()` | T1, T2, T3, T4f, T5, T19 |
| M2 | `pdfinfo` (`pdfrender.go:120-122`): tekst publiczny z `info` | T1, T19 |
| M3 | `pdftoppm` jedno wywołanie: tekst publiczny z `out` | T2 |
| M4 | `pdftoppm` per strona: tekst publiczny z `out` | T3 |
| M4a | `pdfrender.go:126/132/144/182/214`: `ownErrorf` → `fmt.Errorf` (bez tekstu publicznego), każdy osobno | T4a / T4b / T4 / T4c / T4d |
| M4b | decode rastra: tekst publiczny `err.Error()` | T4e |
| M5 | E2: `"lp submit failed: "+err.Error()` | T6, T7, T7a, T20 |
| M6 | `lp`: tekst publiczny z `out` | T6, T20 |
| M7 | `parseJobID`: tekst publiczny z `lpOutput` | T7 |
| M8 | E3: `"job poll failed: "+err.Error()` | T8, T8a, T8b, T9, T10 (T10a NIE: dla `ownErrorf` tekst publiczny = `Error()` — sonda Codex r2) |
| M9 | transport IPP: tekst publiczny `err.Error()` | T8, T8b |
| M9a | transport IPP: `Error()` gubi przyczynę, zostawia URL (`Post "<URL>"`) | T8 (W1-4), T23 |
| M10 | dekoder IPP: tekst publiczny `err.Error()` | T9 |
| M10a | status IPP: tekst publiczny z nazwą goipp (`%s` statusu) | T10 |
| M10b | `cups.go:122`: `ownErrorf` → `fmt.Errorf` | T10a |
| M11 | E4 (`hsFaultError`): `"printer fault (~HS): "+hs.Raw` | T11 |
| M12 | E5: `+": "+lastErr.Error()` | T12 |
| M13 | E6: `+": "+err.Error()` | T13, T13a, T13b, T14, T14a, T15 |
| M14 | E7: `+": "+err.Error()` | T16 |
| M15 | transport panelu: tekst publiczny `err.Error()` | T13, T13b, T16 |
| M15c | transport panelu: `Error()` gubi przyczynę, zostawia URL | T13 (W1-4), T16 (W1-4), T23 |
| M15a | HTTP panelu: tekst publiczny = `Error()` (`panel …: HTTP N`) | T14 |
| M15b | odczyt body panelu: tekst publiczny `err.Error()` | T14a |
| M16 | E8: `+": "+st.State` | T17, T21 |
| M17 | E9: `err.Error()` | T18 |
| MF | `publicMessage` — fallback: `prefix+": "+err.Error()` zamiast samego prefiksu | T4f, T5, T7a, T8a, T13a, T15, T22 |
| MS1 | `publicError.Error()` zwraca `e.public` | T1 (log, W1-4), T14 (W2-4′), T22, T23 |
| MS2 | `publicError.Unwrap()` zwraca `nil` | T22 (`errors.Is`) |
| MS3 | `publicMessage` ignoruje `publicError` (zawsze prefiks) | T1, T6, T14, T22 |
| MS4 | `withPublic` zapisuje `public: ""` | T1, T13, T22 |
| MS5 | `ownErrorf`: tekst publiczny `""` | T4, T22 |
| MS6 | `toolOutcome`: brak gałęzi „signal” (kod −1 → `exited with code -1`) | T22 |
| MS7 | `toolOutcome`: brak gałęzi ctx (martwy ctx → `could not be started`) | T22 |
| L1–L9 | usunięcie `log.Printf` na granicy E1–E9, każda osobno | test(y) miejsca (asercja 4 / 4′) |

## 6. Wpływ na klienta PHP (census read-only, marketplace-manage `4161fc89`)

Klient czyta `message` w jednym miejscu:
`app/Modules/Printing/Bridge/PrintBridgeClient.php:207`
(`$envelope['message'] !== '' ? … : bodyExcerpt(...)`), potem przekazuje go do
wyjątków (`PrinterUnavailableException`, `PrintValidationException`,
`PrintUnconfirmedException`), logów, notyfikacji Filament, `print_jobs.error_message`
i ActivityLog. Klasyfikacja w kliencie — wyłącznie po `code` i statusie HTTP
(`:211`, `:224`).

Census pięcioma przebiegami (katalog kat. 5, „kierunek odwrotny”), surowe wyniki:

- (α) `grep -rnE "(str_contains|stripos|strpos|str_starts_with|str_ends_with|preg_match|Str::contains|Str::startsWith|Str::is)\([^;]*->getMessage\(\)" app` → 3 trafienia:
  `app/Support/Http/TransportFailureText.php:24` (`cURL error` — wyjątki transportu;
  używany w `MplPowerB2BClient`, `SupplierDescriptionService`, `AbstractAdapter`),
  `app/Modules/Campaign/Support/CampaignErrorText.php:66` (kampanie),
  `app/Modules/Invoicing/Jobs/UploadInvoiceToAllegroJob.php:324` (`[409-orphan]`, faktury).
  Żadne nie dostaje wyjątku z modułu Printing.
- (β) skrypt: zmienna z `->getMessage()` dopasowana w ≤ 60 liniach → 2 trafienia,
  oba `app/Models/Automation/AutomationHandoffRegistry.php:365`/`:370`
  (`QueryException` indeksu unikalnego) — nie Printing.
- (γ) skrypt: metoda z parametrem `string $message|$msg|$error|$err|$text|$reason|…`
  dopasowanym w ciele → 16 trafień: `app/Modules/Order/Models/OrderRefund.php:435`,
  `app/Modules/Marketplace/Operations/Services/UnlistedProductPublisher.php:277-314`
  (błędy Allegro, 11 wierszy), `app/Modules/Inventory/Models/StockBatchDeduction.php:65`
  i `:76`, `app/Modules/Campaign/Services/SmsPartsCalculator.php:56`,
  `app/Modules/Invoicing/Bridge/SubiektBridgeClient.php:1199` — żadne nie
  dostaje komunikatu agenta druku.
- (δ) `grep -rnE "causedByConcurrencyError|causedByLostConnection|Detector" app` →
  `app/Modules/Order/Services/OrderImportService.php:807` i `:1580`,
  `app/Modules/Discovery/Services/DiscoverySourceScanner.php:114`
  (`QueryException`), `BotMessageDetector` (wiadomości dyskusji) — nie Printing.
- (ε) detektory vendora wołane z całym wyjątkiem — żaden z powyższych torów nie
  obejmuje wyjątków Printing.
- Literały agenta: `grep -rnE "PDF render failed|lp submit failed|job poll failed|printer unreachable during|printer fault \(~HS\)|status\.cgi\) niedostępny|func=reset nie powiódł|panel raportuje fault|pdfinfo|pdftoppm" app tests resources config` → 0 trafień.
- Predykaty na `message`/`error_message` w Printing i akcjach Filament:
  wyłącznie niepustość (`app/Modules/Printing/Bridge/PrintBridgeClient.php:207`,
  `app/Filament/Resources/PrintJobResource/Actions/ReprintUnconfirmedAction.php:43`,
  `app/Filament/Resources/PrintJobResource/Actions/ConfirmPrintJobAction.php:43`).
- Grupowanie po treści: `ErrorFingerprint::make()` (`app/Support/ErrorEvents/ErrorFingerprint.php:51`)
  łączy klasę wyjątku z znormalizowanym `message`. Skutek v0.9.0: błędy druku
  dostaną nowe fingerprinty (stabilniejsze — bez zmiennego wyjścia narzędzi);
  stare grupy przestaną rosnąć. Bez wpływu na logikę.
- `details.panel_state`: w `app/` nieczytany (tylko testy walidatora koperty:
  `tests/Unit/Printing/PrintBridgePayloadValidatorTest.php:199-200`).

**Zmian w PHP nie wymaga.** Warunek: `message` niepusty (§3.3).

## 7. Wydanie (instrukcja dla operatora — NIE wykonywać w tym PR)

1. Merge do `main` (CI: `go vet`, `go test`, build z ldflags).
2. W oknie deployu z userem: `git tag v0.9.0 && git push origin v0.9.0`,
   zielony `release.yml`, Release z tarballami i `.sha256`.
3. Każda instancja po kolei: `php artisan print-bridge:update v0.9.0 --printer=<id> --wait`,
   potem `php artisan print-bridge:update --printer=<id> --status` → pole
   `version` dokładnie `0.9.0` (bez `--printer` komenda kończy się błędem
   przed odczytem health: `app/Modules/Printing/Console/PrintBridgeUpdateCommand.php:47-51`
   w marketplace-manage).
4. Diagnostyka po wdrożeniu (pełne wyjście narzędzi jest od v0.9.0 tylko w
   logu agenta): `journalctl -u print-bridge` dla instancji podstawowej,
   `journalctl -u print-bridge-<slug>` dla nazwanej (`deploy/update-bridge.sh:41`, `:44`).

## 8. Ryzyka rezydualne

- **Świadome residuum tej samej klasy (decyzja lidera, bez follow-upu):**
  tekst z zewnątrz niosą nadal `details.panel_state` (fault po resecie,
  `internal/printer/reset.go:164`), `panel_before` w 200 resetu
  (`internal/server/handlers.go:249`) oraz w health: `reach_error`,
  `host_status` (surowa linia 1 `~HS`), `host_status_2`, `host_status_error`,
  `cups_reasons`, `cups_error` (`cmd/print-bridge/main.go:217`, `:231`,
  `:232`, `:237`, `:249`, `:252`). PHP świadomie wyświetla i czyta tekst
  firmware'u/CUPS: `panel_before` w notyfikacji resetu
  (`app/Filament/Resources/PrinterResource/Actions/ResetPrinterAction.php:48`),
  `host_status` jako powód „down” w
  `app/Modules/Printing/Jobs/PrinterHealthCheckJob.php:115`, a
  `print-bridge:update --status` wypisuje cały health
  (`app/Modules/Printing/Console/PrintBridgeUpdateCommand.php:150-153`). To nie
  są dane z dokumentu, więc poza klasą tego PR.
- Pełne wyjście narzędzi (w tym fragmenty dokumentu) zostaje w lokalnym logu
  agenta (journald hosta) — świadomie, to jedyne miejsce diagnostyki.
- Mniej informacji w notyfikacji PHP: operator widzi narzędzie i kod, a
  szczegół czyta w logu agenta (`journalctl -u print-bridge[-<slug>]`, §7).
- Istniejące dziwactwa klasyfikacji bez zmian: lokalny błąd renderu (tmp) i
  zabicie narzędzia przez budżet kontekstu dają 422 `INVALID_PDF`
  (nie-retryable). Poza zakresem (kody bez zmian).
- `ownErrorf` przyjmuje `...any`; reguła „tylko liczby” jest w komentarzu i w
  review, nie w typie. Każde wywołanie w tym PR ma wyłącznie argumenty
  liczbowe (§3.2).

## 9. Self-review (pre-flight, 2026-10-03)

Aktualizacja po Codex r1 (2026-10-03). Sprawdzone drzewo: `fbf2dcc` (+ zmiany niezacommitowane: tylko ten dokument) ·
dokument: `docs/superpowers/specs/2026-10-03-message-bez-wyjscia-narzedzi-design.md`
Niewykonane kontrole: `refs-check.sh` sekcje USES/DEPTRAC/MUTATION/ENV (pod PHP —
nie dotyczy repo Go); `lint-plan-blocks.sh` (pod PHP — nie dotyczy, brak planu
z blokami PHP); `run-mutants.py` (PHPUnit/OTR — nie dotyczy: mutanty Go ręcznie, §5).

| Kategoria | Status | Dowód mechaniczny (komenda → exit/wynik) | Wniosek |
|---|---|---|---|
| 1 Atomowość, locki | nie dotyczy | — | zmienia się wyłącznie tekst `message` i logi; brak nowego stanu, locków, kolejności operacji |
| 2 Granice tenanta | nie dotyczy | — | agent obsługuje jedną drukarkę, bez organizacji; po stronie PHP zero zmian |
| 3 Test niedowodzący | ocenione | §5.1 warianty W1–W3 per test; §5.5: mutant per miejsce (M1–M17 z a/b), fallback (MF), każdy nowy symbol (MS1–MS7), log (L1–L9) | W2 dla miejsc bez znacznika (status, tekst biblioteki, literał) — log z dokładnym `Error()`; W3 dla `panel_state` (decyzja A); E4 nieosiągalne przez `Print` → T11/M11 na `hsFaultError`; etapy RED §5.4 (kompilacja pakietu) |
| 4 Deploy | nie dotyczy | — | klamra: system jednoosobowy; §7 to instrukcja wydania w oknie z userem |
| 5 Inwentarz | sprawdzone | C1 → 47 wierszy, C2 → 70, C3 → 13, C4 → 21, C5 → 22 (§2, surowe wyniki, każdy sklasyfikowany); golden: `grep -n 'panel raportuje fault\|PDF render failed: \|lp submit failed: \|job poll failed: \|verification: \|niedostępny: \|nie powiódł się: ' internal/server/contract_test.go` → 8 przypadków (§5.3); test U4 `print_test.go:572`; PHP α–ε (§6) | 9 emisji E + 1 D; C3 łapie `+st.State` i `+hs.Raw`, których C2 nie widzi; C5 dodane po r1 (`cups.go:57`) |
| 6 Census i skanery | sprawdzone | j.w. + `census_beta_gamma.py` (β 2, γ 16 trafień) | żadne trafienie β/γ/δ nie dotyczy Printing |
| 7 Semantyka vendora | sprawdzone | sonda §2.5 (`exec.ExitError`/`exec.Error`/ctx/`url.Error`); goipp `Status.String()` (moduł `github.com/OpenPrinting/goipp@v1.2.0`, plik `status.go`, linie 79–85: nazwa z tabeli albo `0x%4.4x`); prawdziwy `pdfinfo` z `/Filter /SEKRET-BODY` → exit 1 + znacznik | tekst publiczny IPP bez nazwy goipp (same cyfry) |
| 8 Zero-change | ocenione | §4; `Error()` źródeł bez zmian → health i logi identyczne; E9: tekst sentinela = dzisiejsze bajty dla gołego sentinela | jedyna zmiana na drucie: tekst `message` w wierszach §4 |
| 9 Spójność z repo | sprawdzone | `gorefs.py fbf2dcc <spec>` → 132 odwołań Go, 0 problemów; `refs-check.sh --base 4161fc89` (z marketplace-manage) → REFS PHP zgodne | `refs-check.sh` nie rozpoznaje ścieżek Go — stąd `gorefs.py` |
| 10 Edge cases | ocenione | `toolOutcome`: 4 klasy z sondy §2.5; `%.0f` z `ParseFloat` regexu `[0-9.]+` → tylko cyfry | pusty `message` niemożliwy (prefiks zawsze niepusty) |
| 11 Harness | ocenione | `grep -c t.Parallel` → 0 we wszystkich `_test.go`; `t.Setenv` przywraca `PATH`; `log.SetOutput` z przywróceniem w `t.Cleanup` | globalny stan bezpieczny przy sekwencyjnych testach |
| 12 Logowanie | ocenione | jeden `log.Printf` na granicę (E1–E9) | watchdog nadal loguje `rerr` (`watchdog.go:111`) — teraz stały tekst; szczegół jest już w logu granicy |
| 13 Migracje | nie dotyczy | — | brak bazy w zakresie (idempotency store bez zmian) |
| 14 SQL | nie dotyczy | — | brak SQL |
| 15 Autoryzacja i sekrety | ocenione | — | `message` traci tekst z zewnątrz; log lokalny (journald) zawiera pełne wyjście — świadomie (§8) |
| 16 Bramki CI | sprawdzone | `.github/workflows/build.yml`: `go vet ./...`, `go test ./...` (z `poppler-utils`), build z ldflags; bez `-race` i bez gofmt | lokalnie dodatkowo `gofmt -l` |
| 17 Idempotencja skutków zewnętrznych | nie dotyczy | — | ścieżki druku/resetu bez zmian poza tekstem; `Result.CUPSJobID` i `persistPending` nietknięte |
| 18 Automat nad silnikiem | nie dotyczy | — | brak automatu/klasyfikacji sterującej |

## 10. Rundy review

| Runda | Sesja Codexa | Werdykt | P1/P2/P3 |
|---|---|---|---|
| r1 | `01a0ff18-99a2-7da3-b94b-640e72aa3d63` | GO-Z-POPRAWKAMI | 0/5/2 |
| r2 | ta sama (`resume`) | GO-Z-POPRAWKAMI | 0/1/1 |

Findingi r1 (wszystkie CONFIRMED sondą; poprawki zaakceptowane przez lidera
przed r2):

| # | P | Finding | Status / poprawka |
|---|---|---|---|
| 1 | P2 | wspólne asercje niewykonalne dla T4/T9/T10/T14 (brak znacznika) i T17/T21 (znacznik w `details` z decyzji A) | CONFIRMED → §5.1 warianty W1/W2/W3, kolumna W w §5.2 |
| 2 | P2 | brak testów/mutantów: decode rastra, odczyt body panelu, literały `pdfrender.go:126/132/182/214`, `cups.go:122`; ścieżki bez opakowania tylko pośrednio | CONFIRMED → T4a–T4f, T10a, T14a; fallback na prawdziwych źródłach T4f/T7a/T8a/T13a; ciągłość `Error()` T23; mutanty M4a/M4b/M10a/M10b/M15a/M15b/MF |
| 3 | P2 | 8 zmian golden, nie 7 (`reset-fault-pusty-stan`, `contract_test.go:535`) | CONFIRMED → §5.3 |
| 4 | P2 | RED na bazie: brak symbolu blokuje kompilację całego pakietu | CONFIRMED → §5.4 etapy (a)–(c), wyniki z biegów; zachowanie nowych symboli = MS1–MS7 |
| 5 | P3 | census pominął `cups.go:57` (`strconv.Atoi` cytuje cyfry z wyjścia `lp`) | CONFIRMED → C5 (§2.3), wiersz §3.2, T7a |
| 6 | P2 | §7: `--status` wymaga `--printer`; unit nazwanej instancji `print-bridge-<slug>` | CONFIRMED → §7 pkt 3–4, §8 |
| 7 | P3 | residuum niepełne (`host_status`, `host_status_2`, `cups_reasons`); PHP już wyświetla/czyta część pól | CONFIRMED → §8 (brzmienie zatwierdzone przez lidera), §3.4 |

Findingi r2 (r1 #1, #3–#7 zamknięte; #2 częściowo):

| # | P | Klasa | Finding | Status / poprawka |
|---|---|---|---|---|
| r2-1 | P2 | ta sama klasa co r1 #2 | T23 transport sprawdzał prefiks `Post "`; znacznik w URL — błąd skrócony do `Post "<URL>"` (bez przyczyny) zostawiał T23 i T8 zielone (sonda Codexa) | CONFIRMED; klasa = „znacznik w części tekstu, którą mutant zostawia” — wszystkie wystąpienia: T8, T13, T23 (transport IPP i panelu) → deterministyczny `RoundTripper` ze znacznikiem w PRZYCZYNIE (T8, T13), warianty URL T8b/T13b, równość całego `Error()` w T23, mutanty M9a/M15c. T16 i testy narzędzi już mają znacznik w przyczynie |
| r2-2 | P3 | wprowadzony poprawką r1 | M8 nie zabija T10a (`ownErrorf`: tekst publiczny = `Error()`) | CONFIRMED → T10a usunięty z RED M8 (zostaje przy M10b) |

## 11. Wyniki implementacji (2026-10-03, z biegów)

Kamień (1) przyjęty przez lidera po r2 (poprawki r2 wniesione, bez r3).

**Etapy RED (§5.4):**

- (a) baza `fbf2dcc` + testy na istniejącym API: `go test ./internal/printer ./internal/server`
  → FAIL. Czerwone: wszystkie testy §5.2 poza T11/T22 (T1–T10a, T12–T21; T4–T4e jako
  podtesty `TestLeakRenderOwnLiterals`) oraz 8 przypadków golden z §5.3. Przyczyny zgodne z
  §5.4: znacznik w kopercie (np. T1: `PDF render failed: pdfinfo failed (invalid PDF?): exit
  status 1: Syntax Error: Couldn't find the 'SEKRET-BODY' security handler\n`; T19 to samo na
  prawdziwym `pdfinfo` przez `Router()`), inny `message` (T9: `job poll failed: Message
  truncated at 0x1f`; T10: `… IPP error 0x0401 client-error-forbidden`; T14: `… panel
  /cgi-bin/status.cgi: HTTP 503`), pusty log (T4: `message` już zgodny, brak wpisu w logu).
  Zielone: T23 (`TestSourceErrorTextUnchanged`) i U4 (`TestVerifyEveryHealthyFaultHasDedicatedCase`).
- (b) refaktor `hsFaultError` z dzisiejszym tekstem + T11 → T11 FAIL (`message = "printer fault
  (~HS): \x02SEKRET-HS,0,0\x03"`, znacznik w kopercie, pusty log); reszta pakietu jak w (a).
- (c) `publicerr_test.go` → `[build failed]`: `undefined: toolOutcome`, `withPublic`,
  `ownErrorf`, `publicMessage` (brak symbolu, nie dowód regresji).
- GREEN: `go test ./... -count=1` → 11 pakietów `ok`; T19 wykonany (nie pominięty).

**Mutanty (§5.5):** 48 (M1–M17 z wariantami, MF, MS1–MS7, L1–L9 z wariantami) — wszystkie
KILLED, każdy na wskazanych testach, zero błędów kompilacji; pliki przywracane z kopii i
sprawdzane SHA-256. M8 nie zabija T10a (zgodnie z r2-2). M9a → `TestLeakIPPTransportCause`,
`TestSourceErrorTextUnchanged`; M15c → `TestLeakPanelTransportCause`,
`TestLeakFuncResetTransport`, `TestSourceErrorTextUnchanged`.

**Bramki (`.github/workflows/build.yml` + gofmt):** `gofmt -l .` → pusto; `go vet ./...` →
exit 0; `go test ./... -count=1` → exit 0 (poppler lokalnie); build z ldflags → exit 0.
Dodatkowo (CI tego nie robi): `go test -race ./internal/printer ./internal/server` → ok.

**Review kodu Codexem:** `codex exec review --uncommitted -m gpt-6-astra -c model_reasoning_effort="high"`,
sesja `01a0ff3b-e162-7930-bd4b-d65ceef4303f` → „No actionable regressions found” (0 findingów).
