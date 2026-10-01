# Utwardzenie agenta (follow-upy kontraktu v2) — design

**Data:** 2026-10-01
**Status:** projekt do review (implementacja po GO; wydanie v0.8.0 — później)
**Baza:** `origin/main` `95fe5a4` (merge PR #14: kontrakt HTTP v2 + testy golden),
gałąź `feat/utwardzenie-agenta`.

## 1. Cel i granice

Domknąć pięć problemów wykrytych przy przypinaniu kontraktu HTTP v2
(`docs/error-contract.md`). Cztery z nich zmieniają drut (U1, U2, U3, U5), więc
wymagają wydania agenta (v0.8.0) i jawnej zmiany testów golden z PR #14.

| # | Problem | Decyzja |
|---|---------|---------|
| U1 | błąd po stronie agenta na `/admin/update` wraca jako 422 `INVALID_REQUEST` z lokalną ścieżką w `message` | nowy kod `UPDATE_FAILED` 500 z `details.reason`, `message` bez ścieżek |
| U2 | brak blokady równoległych aktualizacji | nowy kod `UPDATE_IN_PROGRESS` 409; lock wspólny dla handlera i `update-bridge.sh` |
| U3 | reset drukarki bez budżetu czasu — może przekroczyć `WriteTimeout`, klient dostaje zerwane połączenie | budżet czasu + semafor świadomy ctx; po przekroczeniu 503 `PRINT_TIMEOUT` z `details.reset_sent` |
| U4 | martwa gałąź `!hs.Healthy()` w `verify()` | **zostaje** jako bezpiecznik inwariantu; komentarz + testy przypinające |
| U5 | tag bez `v` daje 202, a pobranie pada asynchronicznie | wymagany `v`: 422 `INVALID_REQUEST` przed spawnem (agent i skrypt) |

Poza zakresem: zmiany po stronie klienta PHP (tylko „wymagania dla PHP”, §6),
wykonanie wydania i aktualizacja hostów (§7 to instrukcja, nie krok tego PR),
odpowiedzi bez koperty N6/N8 (stdlib), ścieżka druku poza U4.

## 2. Census (stan na `95fe5a4`)

### 2.1 `/admin/update`

- Handler: `internal/server/handlers.go:240-252`. Każdy błąd `Updater` →
  `apierr.New(CodeInvalidRequest, err.Error(), 422)` (`:247-248`).
- Okablowanie: `cmd/print-bridge/main.go:96-101` — domknięcie woła
  `update.SpawnUpdater(script, logPath, tag, cfg.Instance)`, log
  `absUnder(exeDir, "data/update.log")` (`:98`).
- `internal/update/update.go:43-69` — źródła błędów:
  - `:34` zły tag (wejście),
  - `:48` zła instancja — nieosiągalna w produkcji: slug walidowany na starcie
    (`internal/config/config.go:124-125`),
  - `:52` `fmt.Errorf("updater log %s: %w", logPath, err)` — **błąd agenta,
    ścieżka instalacji w `message`** (golden `update-blad-logu-aktualizatora`
    w `internal/server/contract_test.go` przypina to dziś jako 422),
  - `:68` błąd `cmd.Start()` (np. brak `sudo`) — **błąd agenta jako 422**.
- `ValidateTag`: `^v?\d+\.\d+\.\d+([-.][0-9A-Za-z]+)*$` (`update.go:26`), więc
  `0.7.0` przechodzi; to samo w skrypcie (`deploy/update-bridge.sh:15`). URL
  pobrania używa `${TAG}` (`update-bridge.sh:42`), a tagi wydań to `v*`
  (`.github/workflows/release.yml`, `on.push.tags: ['v*']`) → 202, a potem
  `curl -f` pada asynchronicznie (wpis tylko w `data/update.log`). Golden
  `update-tag-bez-v-echo` przypina dziś 202.
- Brak guardu równoległości: handler go nie ma, choć zapowiada go komentarz
  `internal/server/router.go:14-16`; skrypt nie ma `flock`.
- Przekazanie locka handler → skrypt jest niemożliwe: `sudo` domyślnie zamyka
  deskryptory ≥ 3 (`closefrom`), a etap 2 skryptu działa w transient unicie
  `systemd-run` (`update-bridge.sh:59-70`), który nie dziedziczy FD procesu
  wywołującego. Skrypt musi wziąć lock sam, w etapie 2.
- Instancje: katalog `/opt/print-bridge[-<slug>]`, unit, port i `data/` są per
  instancja (`update-bridge.sh:30-39`, `deploy/install-debian.sh:46-51`).
  Wspólne dla wszystkich instancji: skrypt `/usr/local/sbin/update-bridge.sh`
  (`update-bridge.sh:37,130-132`), sudoers `/etc/sudoers.d/print-bridge`
  (`:38,134-141`) i backend `/usr/lib/cups/backend/lpdpaced` (`:123`).

### 2.2 `/admin/printer-reset`

- Handler: `handlers.go:226-238` podaje `r.Context()` bez limitu (`:227`).
- `internal/printer/reset.go:57-106`: `sync.Mutex` (`:36`, `:58`) bez ctx;
  status panelu (`:61`), `func=reset` (`:71`), do `MaxPolls`=15 prób co 2 s
  (`main.go:74`); klient panelu ma timeout 8 s (`internal/printer/webpanel.go:44`)
  i respektuje ctx (`http.NewRequestWithContext`, `webpanel.go:48`).
- Sonda ~HS (`QueryHostStatus`, `internal/printer/hoststatus.go:149-200`) używa
  ctx **tylko przy dial** (`:151`); potem ma własny deadline gniazda 5 s
  (`:157`); po pierwszym stringu deadline odczytu jest zastępowany przez
  „teraz + 300 ms” (`:181`, zwykle skrócenie). Po wygaśnięciu ctx
  w trakcie odczytu sonda kończy się sama, w ≤ ~5,3 s.
- Najgorszy przypadek: 8 + 8 + 15 × (2 + 8) + (5 + 5,3) ≈ 176 s. `WriteTimeout` =
  `confirm_timeout_sec` + 60 = 90 s przy domyślnym 30 (`main.go:123`,
  `config.go:51`) to deadline **zapisu odpowiedzi**, nie licznik zamykający
  połączenie: handler pracuje dalej, a po 90 s jego odpowiedź nie da się już
  zapisać. Klient dostaje zerwane połączenie, gdy handler skończy, albo wcześniej
  traci cierpliwość sam. Klient PHP ma `client_timeout` 120 s i `retry(3)` na
  `ConnectionException` (`PrintBridgeClient::buildRequest`: `retry(3, 500, fn => $e
  instanceof ConnectionException)`). Skutek dziś: reset dłuższy niż 90 s kończy
  się `ConnectionException` (timeout albo EOF) → PHP **ponawia POST resetu do
  3 razy** → do trzech `func=reset` z jednego kliknięcia.
- Watchdog woła ten sam `PrinterResetter.Reset` z kontekstem pętli, bez limitu
  (`main.go:78-88`). Reset HTTP czekający na mutexie trzymanym przez watchdoga
  czeka bez ograniczenia.
- Jedyna emisja `PRINT_TIMEOUT` z resetu (`reset.go:79-81`) zachodzi tylko po
  anulowaniu kontekstu w `select` pętli (dziś: rozłączenie klienta). Anulowanie w
  trakcie pierwszego statusu (`:61-64`) albo `func=reset` (`:71-73`) daje dziś
  `PRINTER_OFFLINE`; błąd statusu w ostatniej iteracji pętli (`:85-88`) też kończy
  się `PRINTER_OFFLINE` „nie wrócił do Ready” (`:104-105`).

### 2.3 `verify()` (U4)

`internal/printer/print.go:166-175`: case'y `hs.PaperOut`, `hs.Paused`,
`hs.HeadOpen`, potem `!hs.Healthy()`. `Healthy()` =
`!PaperOut && !Paused && !HeadOpen` (`internal/printer/hoststatus.go:74-76`).
W chwili sprawdzania `!hs.Healthy()` wszystkie trzy pola są `false`, więc
gałąź jest nieosiągalna. Jest jednak jedynym miejscem, które gwarantuje
inwariant „`verify()` nigdy nie zwraca `printed`, gdy `Healthy() == false`”.
Gdyby ktoś dopisał nowy fault do `Healthy()` (np. `BufferFull`) bez osobnego
case'a, to bez tej gałęzi `verify()` przeszłoby do `!hs.Draining()` i zwróciło
fałszywe `printed`, podczas gdy health (`main.go:229`) by zdegradował.

## 3. Decyzje

### U1 — `UPDATE_FAILED` (500)

- Nowy kod `apierr.CodeUpdateFailed = "UPDATE_FAILED"`, HTTP 500,
  `Retryable() == false` (operator ponawia ręcznie).
- `details.reason` (string), zawsze obecne:

  | `reason` | Kiedy |
  |----------|-------|
  | `log_unavailable` | nie da się otworzyć `data/update.log` |
  | `lock_unavailable` | nie da się otworzyć/sprawdzić `data/update.lock` albo `data/update.pending` (U2) |
  | `spawn_failed` | `cmd.Start()` nie powiódł się; także nieznany błąd `Updater` |

- `message` — stały tekst per `reason`, **bez ścieżek i bez `err.Error()`**.
  Pełny błąd (ze ścieżką) trafia tylko do logu agenta (`log.Printf`).
- Błędy wejścia zostają `INVALID_REQUEST`: 400 (zły JSON), 422 (zły/brak tagu,
  zła instancja). Komunikaty to stałe teksty bez ścieżek.
- Mechanizm: pakiet `update` zwraca błędy typowane — `ErrInvalidTag`,
  `ErrInvalidInstance`, `ErrInProgress` (sentinele) i `*StartError{Reason, Err}`.
  Handler mapuje je przez `errors.Is`/`errors.As`; HTTP zostaje w pakiecie
  `server`, `update` nie importuje `apierr`.
- Odrzucone: 503 (klasa „przejściowy” sugerowałaby automatyczne ponowienie,
  a brak logu czy `sudo` nie mija sam); `INVALID_REQUEST` z `details.reason`
  (zostawiałoby błąd agenta w klasie „popraw żądanie”).

### U2 — `UPDATE_IN_PROGRESS` (409) i lock

- Nowy kod `apierr.CodeUpdateInProgress = "UPDATE_IN_PROGRESS"`, HTTP 409,
  `Retryable() == false` (operator czeka na koniec — `health.version` — i
  ponawia ręcznie, jeśli trzeba).
- Pliki w katalogu danych instancji (lock per instancja):
  - `data/update.lock` — `flock` trzymany przez `update-bridge.sh` przez **cały**
    przebieg etapu 2 (FD 9 żyje do `exit`, także w trakcie rollbacku z trapa).
    Przeżywa restart agenta, bo trzyma go proces skryptu, nie agenta.
  - `data/update.pending` — znacznik okna „spawn → flock skryptu” (~0,5–2 s:
    `sudo` + re-exec do `systemd-run` + start unitu) z **tokenem właściciela**
    (128 bitów z `crypto/rand`, hex). Token idzie do skryptu jako 3. argument
    (`update-bridge.sh <tag> <instancja-albo-""> <token>`; sudoers dopuszcza
    dowolne argumenty: `update-bridge.sh *`). TTL 60 s — na wypadek, gdyby
    skrypt w ogóle nie wystartował.
- **Jeden punkt synchronizacji: `flock` na `update.lock`.** Każda operacja na
  znaczniku (sprawdzenie, zastąpienie przeterminowanego, utworzenie, sprzątanie
  po błędzie po stronie handlera; porównanie tokenu i skasowanie po stronie
  skryptu) dzieje się pod tym lockiem. Handler trzyma go przez sprawdzenie,
  rezerwację i spawn (milisekundy), potem zwalnia; skrypt czeka na niego
  `flock -w 10` i trzyma do końca przebiegu. Znacznik pokrywa okno między
  zwolnieniem locka przez handler a wzięciem go przez skrypt. W tym oknie nowe
  żądanie dostaje lock, widzi świeży znacznik i odpowiada 409.
- Mutex w procesie jest zbędny: `flock` dotyczy opisu otwartego pliku, a każde
  `Start` otwiera `update.lock` osobno, więc równoległe wywołania w jednym
  procesie też się wykluczają (drugie dostaje `EWOULDBLOCK` → 409 bez czekania).
  Deskryptor locka handlera ma `O_CLOEXEC` (domyślnie w `os.OpenFile`), więc
  `sudo` go nie dziedziczy.
- Handler (`update.Spawner.Start`):
  1. walidacja tagu i instancji → `ErrInvalidTag` / `ErrInvalidInstance`;
  2. `update.lock`: `open(O_RDONLY|O_CREATE|O_NONBLOCK|O_NOFOLLOW)` + `fstat` —
     musi to być zwykły plik; inaczej (symlink, FIFO, brak katalogu) →
     `StartError{lock_unavailable}`;
  3. `flock(LOCK_EX|LOCK_NB)`: zajęty → `ErrInProgress`; inny błąd →
     `lock_unavailable`. Lock trzymany do końca `Start` (zwolnienie w `defer`);
  4. `update.pending` przez `Lstat`: nie istnieje → dalej; istnieje i nie jest
     zwykłym plikiem → `lock_unavailable`; młodszy niż TTL → `ErrInProgress`;
     przeterminowany → `Remove` (błąd → `lock_unavailable`);
  5. utworzenie znacznika `open(O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW, 0644)`,
     zapis tokenu, `Close`. Każdy błąd → usunięcie niepełnego znacznika i
     `lock_unavailable`;
  6. otwarcie logu → błąd: usunięcie znacznika (pod lockiem to na pewno nasz),
     `StartError{log_unavailable}`;
  7. `cmd.Start()` → błąd: jak wyżej, `StartError{spawn_failed}`.
- Skrypt — walidacja tokenu razem z tagiem i instancją na górze (`^[0-9a-f]{32}$`
  albo pusty), re-exec przekazuje go dalej. Zaraz po bloku re-exec
  (`update-bridge.sh:59-70`), przed nagłówkiem `=== start` (`:72`), czyli
  wyłącznie w etapie 2 albo w ręcznym uruchomieniu inline:
  1. jeśli `data/update.lock` istnieje i jest symlinkiem albo nie jest zwykłym
     plikiem (np. FIFO, na którym `open` by zawisł) — odmowa (`exit 1`);
  2. `exec 9>>"$LOCK"` (dopisywanie — tworzy plik, nigdy nie obcina) i
     `flock -w 10 9`. Po 10 s: `exit 1` „inna aktualizacja tej instancji w toku”
     (nic nie zmienione, trap nie robi rollbacku, bo `STOPPED=0`);
  3. pod lockiem, gdy token niepusty: znacznik musi być zwykłym plikiem (nie
     symlinkiem) i zawierać dokładnie ten token, inaczej `exit 1` „zlecenie
     nieaktualne” — skrypt opóźniony ponad TTL, którego znacznik przejął nowszy
     update, nie wykona się zamiast niego i nie skasuje cudzego znacznika.
     Zgodny → `rm -f "$PENDING"`. Token pusty (uruchomienie ręczne albo agent
     < v0.8.0): bez sprawdzania i bez kasowania znacznika;
  4. **host-wide lock** (decyzja lidera): `exec 8>>/run/print-bridge-update.lock`
     i `flock -w 600 8` z wpisem „czekam na aktualizację innej instancji”.
     `/run` należy do roota (0755), więc nikt poza rootem nie podłoży tam pliku.
     Po timeoucie: `exit 1` z wpisem w logu, bez zmian w binarce. Kolejność
     „lock instancji → lock hosta” wyklucza zakleszczenie (lock hosta jest
     zawsze brany drugi).
- Przeploty (wszystkie operacje na znaczniku pod `flock`, więc wystarczy
  rozważyć, kto trzyma lock):
  - żądanie w trakcie `Start` innego żądania → `EWOULDBLOCK` → 409;
  - żądanie po spawnie, zanim skrypt weźmie lock → świeży znacznik → 409.
    Skrypt czeka na lock w `flock -w 10`; Linux nie przekazuje zwolnionego
    `flock` czekającemu (budzi go, a ten próbuje ponownie), więc ciągły strumień
    żądań mógłby go wyprzedzać aż do timeoutu (§8);
  - żądanie, gdy skrypt trzyma lock (także po restarcie agenta) → 409;
  - skrypt opóźniony ponad TTL: nowe żądanie pod lockiem zastępuje znacznik
    tokenem B; spóźniony skrypt A po wzięciu locka widzi B ≠ A → kończy się bez
    zmian; skrypt B dostaje lock po nim i rusza.
- Zgodność wersji: agent v0.8.0 zawsze podaje instancję jako 2. argument (także
  pustą) i token jako 3. Skrypt v0.7.0 czyta `${2:-}` i ignoruje 3. argument,
  a skrypt v0.8.0 bez tokenu działa jak przy uruchomieniu ręcznym. Po rollbacku
  na binarkę v0.7.0 (skrypt jest już v0.8.0) agent v0.7.0 nie tworzy znacznika,
  więc skrypt bierze tylko locki.
- Skutek dla wielu instancji: drugi update **tej samej** instancji → 409.
  Update **innej** instancji → handler 202, a skrypt czeka na koniec pierwszego
  (do 600 s) zamiast ścigać się o wspólny skrypt, sudoers i `lpdpaced`.
- `flock` pochodzi z `util-linux` (pakiet essential w Debianie), więc jest na
  każdym hoście. Testy Go nie potrzebują binarki `flock`: lock skryptu
  symulują osobnym deskryptorem albo procesem pomocniczym (§5).
- Odrzucone:
  - lock trzymany przez handler — sudo/systemd-run nie przekazują FD (§2.1);
  - sam lock bez znacznika — okno spawn → flock skryptu (przeplot w §3 U2);
  - znacznik i lock jako dwa niezależne mechanizmy (sonda locka zwalniana przed
    rezerwacją znacznika) — Codex r2 odtworzył przeplot, w którym skrypt
    kasuje znacznik nowszego żądania;
  - `flock -n` w skrypcie — handler trzyma lock przez spawn, więc skrypt
    zgłoszonego właśnie update'u mógłby trafić na zajęty lock i się poddać;
  - handler czekający, aż skrypt weźmie lock — opóźnia 202 i wymaga pollingu;
  - lock host-wide w handlerze — wymagałby katalogu zapisywalnego przez
    `print-bridge`, wspólnego dla instancji (np. `/run/lock` z bitem sticky:
    `protected_regular` blokuje rootowi `O_CREAT` na cudzym pliku, a każdy
    lokalny użytkownik mógłby podłożyć lock).

### U3 — budżet czasu resetu

- `Handlers.ResetTimeout`: handler wywołuje `Resetter` z
  `context.WithTimeout(r.Context(), ResetTimeout)` (wzorem `printContext`).
- Budżet: `min(WriteTimeout − 10 s, 100 s)` = `min(confirm_timeout_sec + 50 s, 100 s)`,
  czyli domyślnie **80 s**. To mniej niż `WriteTimeout` (90 s), więc handler zdąży
  wysłać kopertę, i mniej niż domyślny `client_timeout` klienta PHP (120 s).
  Sufit 100 s pilnuje drugiego warunku także przy dużym `confirm_timeout_sec`.
  Zdrowy reset trwa kilka–kilkanaście sekund (spike #14: krótka niedostępność
  print-servera → `Printing` → `Ready`), więc 80 s go nie utnie.
- `PrinterResetter`: `sync.Mutex` → semafor (kanał 1-elementowy). Zajęcie w
  dwóch krokach: najpierw próba bez blokowania (`select` z `default`) — wolny
  semafor jest zajmowany zawsze, niezależnie od stanu ctx; dopiero gdy jest
  zajęty, czekanie w `select` z `ctx.Done()`. Po zajęciu jawne `ctx.Err()`.
  Dzięki temu „martwy ctx + wolny semafor” zawsze przechodzi przez sprawdzenie
  po zajęciu (deterministycznie, bez losowania `select`), a czekanie na
  trwający reset (HTTP albo watchdoga) mieści się w budżecie.
- `details.reset_sent` (boolean): `true` = żądanie `func=reset` zostało
  **rozpoczęte**, więc panel mógł je wykonać; `false` = na pewno nie wysłano.
  Granice sprawdzania kontekstu:

  | Gdzie kontekst się kończy | `reset_sent` |
  |---------------------------|--------------|
  | w oczekiwaniu na semafor (inny reset w toku) albo zaraz po jego zajęciu | `false` |
  | pierwszy status bez `Printing` przy wygasłym ctx (sprawdzenie przed `func=reset`) | `false` |
  | w trakcie pierwszego odczytu statusu panelu (błąd + `ctx.Err() != nil`) | `false` |
  | między statusem a `func=reset` (jawne `ctx.Err()` przed wysłaniem) | `false` |
  | w trakcie żądania `func=reset` (błąd + `ctx.Err() != nil`) | `true` |
  | w pętli oczekiwania na `Ready`: `select`, błąd statusu albo niekońcowy status przy `ctx.Err() != nil`, koniec prób przy `ctx.Err() != nil` | `true` |

  Wtedy wynik to 503 `PRINT_TIMEOUT`.
- **Pierwszeństwo odpowiedzi panelu.** `WebPanel.Status` może zwrócić poprawny
  stan, choć ctx wygasł zaraz po odebraniu odpowiedzi (`webpanel.go:56-60`:
  ciało jest czytane i zamykane przed parsowaniem, bez ponownego sprawdzenia
  ctx). Reguła: stan, który **rozstrzyga wynik**, wygrywa z wygasłym ctx, bo to
  fakt o drukarce:
  - pierwszy status `Printing` → 409 `PRINTER_BUSY` (nic nie wysłano);
  - status po resecie `Ready` → 200 (reset się udał; końcowa sonda ~HS jak niżej);
  - status po resecie z faultem → 503 `PRINTER_OFFLINE` z `details.panel_state`.
  Wygasły ctx kończy operację tylko tam, gdzie wynik nie jest jeszcze
  rozstrzygnięty: przed wysłaniem `func=reset` (`reset_sent: false`) i po
  niekońcowym statusie w pętli, także przy `PollInterval == 0`
  (`reset_sent: true`). `message` mówi, czy minął budżet
  (`context.DeadlineExceeded`), czy klient się rozłączył (`context.Canceled`), i
  czy `func=reset` mógł zostać wysłany („sprawdź panel przed ponowieniem”).
  Dzisiejsza emisja z `reset.go:81` dostaje te same `details`. Błąd transportu
  panelu przy **żywym** kontekście zostaje `PRINTER_OFFLINE` jak dziś.
- Panel `Ready` jest autorytatywny. Końcowa sonda ~HS: jeśli kontekst wygasł
  przed nią, dial pada → `hs_ok: false`; jeśli wygaśnie w trakcie odczytu, sonda
  kończy się sama (≤ ~5,3 s, §2.2) z takim wynikiem, jaki dostanie. W obu
  przypadkach odpowiedź to 200 — bez zmiany względem dziś.
- Margines: najdłuższe przekroczenie budżetu to dokończenie sondy ~HS
  (~5,3 s), więc odpowiedź wychodzi przed `WriteTimeout` (80 + 5,3 < 90 s).
  Ogólnie margines 10 s > 5,3 s dla każdego `confirm_timeout_sec`.
- Zmiana zachowania: reset, którego budżet (80 s) minie, zanim panel
  rozstrzygnie wynik, a który dziś skończyłby się sukcesem przed 90 s
  (odpowiedź jeszcze zapisywalna), od v0.8.0 dostaje 503 `PRINT_TIMEOUT` —
  z `reset_sent` zależnym od fazy (np. `false`, gdy budżet zjadło czekanie na
  inny reset). Odczyt `Ready` (także odebrany tuż po wygaśnięciu ctx) zostaje
  200, nawet gdy końcowa sonda ~HS skończy się po budżecie.
- Watchdog bez zmian (kontekst pętli bez limitu, nie dotyczy go `WriteTimeout`).
- Uzasadnienie: koperta 503 przed `WriteTimeout` kończy dzisiejsze zerwanie
  połączenia **i** automatyczne ponawianie POST resetu przez PHP (`retry(3)` na
  `ConnectionException`), czyli nawet trzy `func=reset` z jednego kliknięcia.

### U4 — bezpiecznik w `verify()` zostaje

- Zmiany zachowania brak. Gałąź `case !hs.Healthy():` zostaje z komentarzem:
  dziś nieosiągalna (dowód §2.3), istnieje jako bezpiecznik dla przyszłych
  faultów w `Healthy()`.
- Testy (tylko `*_test.go`):
  1. inwariant: każda niepusta kombinacja `PaperOut`/`Paused`/`HeadOpen` → błąd,
     nigdy `printed`; kody zgodnie z kolejnością case'ów;
  2. strażnik pola: refleksja po polach `HostStatus` (bool → `true`,
     int → 1 i 1<<30, string → `"x"`, każde osobno). Dla każdej wartości, przy
     której `Healthy() == false`, `verify()` musi zwrócić błąd **z dedykowanego
     case'a**, nie z bezpiecznika (`message` ≠ `"printer fault (~HS): …"`).
     Dopisanie faultu do `Healthy()` bez osobnego case'a czerwieni ten test,
     choć bezpiecznik nadal chroni produkcję przed fałszywym `printed`.

### U5 — wymagany `v` w tagu

- `ValidateTag`: `^v\d+\.\d+\.\d+([-.][0-9A-Za-z]+)*$`; skrypt
  (`update-bridge.sh:15`) — ta sama zmiana (obrona w głąb przy ręcznym albo
  bezpośrednim wywołaniu przez sudo).
- `0.7.0` → 422 `INVALID_REQUEST` przed spawnem, z komunikatem wymagającym `v`.
  Echo `tag` w 202 bez zmian (wejście), więc od v0.8.0 zawsze zaczyna się od `v`.
- Odrzucone: normalizacja do `v` — dwa miejsca do utrzymania (agent i skrypt),
  niejasne echo w 202, a cichy pad zamienia się tylko w inną niejawność. Klient
  PHP przekazuje tag bez zmian (formularz z placeholderem `v0.1.0`, argument
  komendy `print-bridge:update`), więc jawny 422 trafia prosto do operatora.

## 4. Kontrakt: przed → po (od v0.8.0)

| Endpoint | Przypadek | v0.7.0 | v0.8.0 |
|----------|-----------|--------|--------|
| update | błąd otwarcia logu aktualizatora | 422 `INVALID_REQUEST`, message ze ścieżką | 500 `UPDATE_FAILED`, `details.reason: "log_unavailable"`, message bez ścieżki |
| update | `cmd.Start()` nieudany | 422 `INVALID_REQUEST`, `err.Error()` | 500 `UPDATE_FAILED`, `reason: "spawn_failed"` |
| update | inny (nieznany) błąd `Updater` | 422 `INVALID_REQUEST`, `err.Error()` | 500 `UPDATE_FAILED`, `reason: "spawn_failed"` |
| update | `update.lock` albo `update.pending` nie jest zwykłym plikiem (symlink, FIFO), brak katalogu, błąd zapisu znacznika | — (nie istniał) | 500 `UPDATE_FAILED`, `reason: "lock_unavailable"` |
| update | aktualizacja tej instancji w toku (świeży znacznik albo lock skryptu) | 202 (drugi updater ściga się z pierwszym) | 409 `UPDATE_IN_PROGRESS` |
| update | równoległe żądanie w trakcie `Start` innego (flock handlera zajęty) | 202 ×2 | 409 `UPDATE_IN_PROGRESS` |
| update | tag bez `v` (`0.7.0`) | 202 + asynchroniczny pad pobrania | 422 `INVALID_REQUEST` |
| update | zły/brak tagu | 422 `INVALID_REQUEST` | bez zmian (nowy stały tekst: wymaga `v`) |
| update | zła instancja (nieosiągalne w produkcji) | 422, message z wartością slugu | 422, stały tekst |
| update | zły JSON | 400 `INVALID_REQUEST` | bez zmian |
| update | update **innej** instancji w toku | 202, oba skrypty się ścigają | 202, skrypt czeka na lock hosta (≤ 600 s) |
| printer-reset | reset dłuższy niż `WriteTimeout` | odpowiedź niezapisywalna → `ConnectionException` w PHP → do 3 POST | 503 `PRINT_TIMEOUT`, `reset_sent` (w budżecie 80 s) |
| printer-reset | budżet (80 s) mija, zanim panel rozstrzygnie wynik, a dziś sukces przyszedłby przed `WriteTimeout` (90 s) | 200 `reset_ok` | 503 `PRINT_TIMEOUT`, `reset_sent` wg fazy (§3 U3) |
| printer-reset | rozstrzygający status panelu odebrany tuż po wygaśnięciu ctx (`Printing` przed resetem / `Ready` / fault po resecie) | 409 / 200 / 503 `PRINTER_OFFLINE` | bez zmian (pierwszeństwo odpowiedzi panelu) |
| printer-reset | kontekst kończy się w trakcie pierwszego statusu albo `func=reset` | 503 `PRINTER_OFFLINE` (błąd transportu ctx) | 503 `PRINT_TIMEOUT`, `reset_sent` wg §3 U3 |
| printer-reset | kontekst kończy się przy błędzie statusu w ostatniej próbie | 503 `PRINTER_OFFLINE` „nie wrócił do Ready” | 503 `PRINT_TIMEOUT`, `reset_sent: true` |
| printer-reset | klient rozłączył się w pętli (`select`) | 503 `PRINT_TIMEOUT` bez `details` | 503 `PRINT_TIMEOUT` z `details.reset_sent` |
| printer-reset | czekanie na trwający reset (watchdog/HTTP) | bez limitu | w budżecie; po nim 503 `PRINT_TIMEOUT`, `reset_sent: false` |
| printer-reset | kontekst kończy się w końcowej sondzie ~HS po `Ready` | 200 | 200 (bez zmian) |
| print-jobs | (U4) | — | bez zmian |

Zmiany w `docs/error-contract.md` (każda oznaczona „od v0.8.0”):

- nagłówek — opis v0.8.0, stabilność „v0.4.1–v0.7.0”;
- §1.3 — budżet resetu; §1.4 — wymagany `v` i blokada;
- §2 — zdanie o lokalnej ścieżce w message 422 na `update` (od v0.8.0 jej nie ma);
- §2.1 — dwa nowe kody;
- §2.2 — wiersze `UPDATE_FAILED`, `UPDATE_IN_PROGRESS`, `PRINT_TIMEOUT` reset z
  `reset_sent` (zamiast wiersza bez `details`), zawężony wiersz 422;
- §2.3 — `details` nowych emisji;
- §3 — retry automatyczny dotyczy tylko `print-jobs`; mutacje admin nie są
  ponawiane automatycznie, każdy ich błąd to decyzja operatora;
- §5 N7 — reset już nie przekracza `WriteTimeout`;
- §6 — reguła dla nowego kodu: klasa HTTP decyduje o retry na `print-jobs`;
  kody tylko z mutacji admin są zawsze „ręcznie”;
- §7 — sekcja v0.8.0; §8 — opis reguły retry w teście dokumentu.

Konsekwencja dla testu dokumentu (`contract_doc_test.go`): reguła „retry
automatyczny ⇔ wszystkie emisje 5xx” zmienia się na „retry automatyczny ⇔ kod
ma emisję na `print-jobs` i wszystkie emisje na `print-jobs` są 5xx”. Na
obecnych 14 kodach obie reguły dają ten sam wynik (różnią się dopiero dla
`UPDATE_FAILED`: 500 tylko na `update` → „nie — ponów ręcznie”). Jedynym kodem z
`Retryable() ≠ retry automatyczny` zostaje `PRINTER_BUSY`.

## 5. Testy (TDD: RED zapisany przed kodem)

Każdy punkt to test, który na bazie `95fe5a4` jest czerwony (albo nie
kompiluje się z powodu brakującego API), a po zmianie zielony. Wyjątki — piny
zielone od razu, bo zachowanie się nie zmienia: U4 (kombinacje i strażnik
pola), „Ready przy wygasłym ctx w trakcie ~HS → 200”, trzy przypadki
pierwszeństwa odpowiedzi panelu (pierwszy `Printing` → 409, `Ready` po resecie →
200, fault po resecie → `PRINTER_OFFLINE`) oraz nowa reguła retry w teście
dokumentu (dla 14 obecnych kodów daje ten sam wynik co stara).

- `internal/update`:
  - `TestValidateTag` — `1.2.3` przechodzi z listy poprawnych na niepoprawne;
  - `Spawner.Start`:
    - zły tag/instancja → sentinel (bez spawnu, bez znacznika);
    - świeży znacznik → `ErrInProgress`; przeterminowany (`os.Chtimes`) → spawn
      z nowym tokenem;
    - zajęty `update.lock` (osobny deskryptor w procesie testu — `flock` dotyczy
      opisu otwartego pliku, więc koliduje także w tym samym procesie) →
      `ErrInProgress`;
    - `update.lock` jako symlink albo FIFO → `lock_unavailable` **bez
      zawieszenia** (FIFO: `O_NONBLOCK`); `update.pending` jako symlink albo FIFO
      (także świeży) → `lock_unavailable`, nie 409;
    - brak katalogu danych → `lock_unavailable`;
    - log w nieistniejącym katalogu → `log_unavailable`, znacznik usunięty;
      brakująca binarka sudo → `spawn_failed`, znacznik usunięty;
    - sukces z fałszywym sudo → znacznik z tokenem (32 znaki hex), lock wolny po
      powrocie; argv w logu: `-n <skrypt> <tag> <instancja> <token>` (zmiana
      jawna względem dzisiejszego `-n <skrypt> <tag>` dla instancji podstawowej);
    - lock trzymany przez cały `Start`: pierwsze `Start` zablokowane w środku
      (log jako FIFO bez czytelnika — `open(O_WRONLY)` czeka; sygnał wejścia =
      pojawienie się znacznika). W tym czasie (a) `flock(LOCK_NB)` z osobnego
      deskryptora testu dostaje `EWOULDBLOCK`, (b) drugie `Start` zwraca
      `ErrInProgress` **zanim** pierwsze zostanie odblokowane (limit czekania
      tylko na ścieżce błędu). Zabija mutanta „sonda locka zwalniana przed
      rezerwacją”. FIFO: `syscall.Mkfifo` na Linuksie i macOS;
  - test międzyprocesowy: fałszywe sudo uruchamia proces pomocniczy (binarka
    testu w trybie helpera), który jak skrypt bierze `flock` (czekając),
    sprawdza token, kasuje znacznik i czeka. **Nowy** `Spawner` (jak po restarcie agenta) dostaje
    `ErrInProgress`; po zakończeniu helpera — spawn. Czekanie z deadline'em, jak
    w istniejących testach spawnu;
  - kontrakt Go ↔ skrypt (wzorem `checksum_contract_test.go`): nazwy
    `data/update.lock` i `data/update.pending` takie same w Go i w skrypcie;
    `flock -w 10 9` po bloku re-exec i przed `systemctl stop`; sprawdzenie tokenu
    i `rm -f "$PENDING"` po `flock`; host-wide `flock -w 600 8` na
    `/run/print-bridge-update.lock` po locku instancji; walidacja tokenu i
    przekazanie go w re-exec; regex tagu w skrypcie wymaga `v`; odmowa przy
    symlinku / nie-zwykłym pliku.
- `internal/printer`:
  - reset — każda granica z tabeli §3 U3, z kontekstem kontrolowanym przez test
    (atrapa panelu z hakami na status/`func=reset`, bez sleepów): semafor zajęty
    przez zablokowany pierwszy reset → `false`; martwy ctx przy wolnym semaforze
    → `false` **i panel nietknięty** (zero wywołań `Status`) — zabija mutanta
    „brak sprawdzenia po zajęciu”; ctx kończony w trakcie pierwszego statusu →
    `false`; pierwszeństwo odpowiedzi panelu przy ctx wygaszanym w haku
    odpowiedzi: pierwszy `Printing` → 409, `Ready` po resecie → 200, fault po
    resecie → `PRINTER_OFFLINE` z `panel_state`, niekońcowy status przy
    `PollInterval == 0` → `PRINT_TIMEOUT` `true`; między
    statusem a `func=reset` → `false`; w trakcie `func=reset` → `true`; w pętli
    (`select`; błąd statusu przy `PollInterval == 0`; ostatnia próba) → `true`;
    `DeadlineExceeded` vs `Canceled` → różny message; Ready przy wygasłym ctx w
    trakcie ~HS → 200 (pin);
  - U4: test kombinacji i strażnik pola (§3 U4).
- `internal/server`:
  - golden (`contract_test.go`) zmieniane **jawnie**: `update-tag-bez-v-echo`
    (202) → `update-tag-bez-v` (422); `update-blad-logu-aktualizatora` (422) →
    500 `UPDATE_FAILED` `log_unavailable`; `reset-kontekst-anulowany` —
    `details.reset_sent`; nowe przypadki `spawn_failed`, `lock_unavailable`,
    `UPDATE_IN_PROGRESS` (lock i znacznik), `PRINT_TIMEOUT` z budżetu (kontekst
    żądania z deadline'em w przeszłości → `reset_sent: false`; deterministycznie,
    bo obie gałęzie — `ctx.Done()` i sprawdzenie po zajęciu semafora — dają ten
    sam wynik). Ścieżka 202 przechodzi przez
    prawdziwy `update.Spawner` z fałszywym sudo;
  - handler stosuje `ResetTimeout` (deadline w ctx przekazanym do `Resetter`);
  - `contract_doc_test.go` — reguła retry z §4.
- `cmd/print-bridge`: `resetBudget(confirmTimeoutSec)` = 80 s dla 30, sufit
  100 s, zawsze ≤ `WriteTimeout` − 10 s.
- `internal/apierr`: nowe kody w liście nie-retryable.
- Mutanty (jak w PR #14, na kopii repo): m.in. 500 → 503 dla `UPDATE_FAILED`,
  ścieżka w message, brak `rm` znacznika przy
  błędzie spawnu, zwolnienie locka przed rezerwacją znacznika, brak
  `O_NONBLOCK`, brak sprawdzenia typu znacznika, `reset_sent` odwrócone, brak
  `ctx.Err()` po zajęciu semafora, brak sprawdzenia ctx po niekońcowym statusie,
  brak budżetu w handlerze, regex `v?`, usunięcie bezpiecznika U4 razem z dodaniem pola do
  `Healthy()`.

## 6. Wpływ na klienta PHP

Klient (walidator koperty przyjmuje dowolny niepusty `code` i dowolne wartości
`details`):

| Odpowiedź | Klasyfikacja w PHP | Co widzi operator |
|-----------|--------------------|-------------------|
| 500 `UPDATE_FAILED` | `status >= 500` → `PrinterUnavailableException` | „Aktualizacja nieudana” + message |
| 409 `UPDATE_IN_PROGRESS` | 4xx spoza listy retry → `PrintValidationException('UPDATE_IN_PROGRESS')` | „Aktualizacja nieudana” + message |
| 422 tag bez `v` | `PrintValidationException('INVALID_REQUEST')` | „Aktualizacja nieudana” + message |
| 503 `PRINT_TIMEOUT` (reset) | `PRINT_RETRY_CODES` → `PrinterUnavailableException` (akcja nie ponawia) | „Błąd resetu” + message (czy `func=reset` wysłano) |

- **Zmian w PHP nie wymaga.** Akcje admin nie ponawiają automatycznie
  (wyjątek: warstwa HTTP przy `ConnectionException`; U3 usuwa tę ścieżkę dla
  resetu).
- Wymaganie opcjonalne dla PHP: osobna notyfikacja dla `UPDATE_IN_PROGRESS`
  („Aktualizacja już trwa — sprawdź wersję za chwilę”) zamiast „Aktualizacja
  nieudana”.
- Ryzyko po stronie PHP: `retry(3)` na `ConnectionException` dla POST update.
  Jeśli pierwsze żądanie wystartowało aktualizator, a odpowiedź zginęła, retry
  dostanie 409 i operator zobaczy „nieudana”, choć aktualizacja trwa. Handler
  odpowiada w milisekundach, więc okno jest bardzo małe. Pełne rozwiązanie po
  stronie PHP to brak retry dla POST mutacji (backlog PHP, R10).
- **Wymaganie dla PHP (przed wydaniem v0.8.0) — w toku:** poprawka
  `print-bridge:update` (z `--printer` i `--status`): komenda musi ustawiać
  kontekst organizacji przed odczytem drukarki — dziś pada na jego braku.
  Wydanie (§7) zakłada ją jako warunek wstępny.
- Kolejność wdrożenia (decyzja lidera): najpierw PR PHP, potem v0.8.0.

## 7. Wydanie (instrukcja dla operatora — NIE wykonywać w tym PR)

Warunek wstępny: poprawka komendy `print-bridge:update` (kontekst organizacji,
§6) jest już na produkcji.

1. Merge do `main` (CI: vet, test, build).
2. `git tag v0.8.0 && git push origin v0.8.0`; poczekać na zielony `release.yml`
   i Release z tarballami `amd64`/`arm64` i plikami `.sha256`.
3. Dla **każdej** instancji agenta (każda to osobny rekord drukarki w aplikacji),
   **po kolei**: `php artisan print-bridge:update v0.8.0 --printer=<id> --wait`.
   Kolejna instancja dopiero po zakończeniu poprzedniej. Pierwsza aktualizacja
   do v0.8.0 przechodzi jeszcze przez skrypt v0.7.0 (blokady jeszcze nie ma);
   nowy skrypt instaluje się podczas niej.
4. `php artisan print-bridge:update --status` → każda instancja raportuje `0.8.0`.

Ochrona U2 (lock, znacznik, token) i wymóg `v` w skrypcie działają dopiero
przy aktualizacjach uruchamianych przez agenta ≥ v0.8.0. Aktualizacja
v0.7.0 → v0.8.0 idzie jeszcze starym handlerem i starym skryptem — bez tokenu,
z dotychczasowym zachowaniem.

Ścieżka zapasowa: akcja „Aktualizuj agenta” w panelu (zasób drukarek) z tagiem
`v0.8.0`, po kolei; weryfikacja przez „Test połączenia” (`health.version`).

## 8. Ryzyka rezydualne

- Pierwsza aktualizacja na v0.8.0 idzie skryptem v0.7.0 (bez locka i bez
  wymogu `v` w skrypcie). Agent v0.8.0 chroni od następnej.
- `sudo` odrzucające wywołanie (np. brak sudoers): `cmd.Start()` się udaje, skrypt
  nie startuje, znacznik blokuje kolejne próby do 60 s (409). Błąd jest w
  `data/update.log` jak dziś.
- Ręczne `sudo update-bridge.sh` (bez tokenu) w oknie między zwolnieniem locka
  przez handler a wzięciem go przez skrypt handlera: ręczny przebieg bierze lock
  pierwszy, skrypt handlera czeka 10 s i kończy się „inna aktualizacja w toku”
  (w logu), mimo że handler odpowiedział 202.
- Głodzenie locka instancji: każde żądanie trzyma `flock` przez mikrosekundy
  (sprawdzenie świeżego znacznika → 409), ale Linux nie gwarantuje kolejności
  oczekujących. Strumień żądań przez 10 s mógłby więc wyprzedzać skrypt aż do
  jego timeoutu: aktualizacja nie rusza (wpis w logu), a znacznik zostaje do
  TTL. Akcje admin są ręczne, więc taki strumień jest nierealny; mocniejszy
  mechanizm postępu jest poza zakresem.
- Update innej instancji czeka na lock hosta do 600 s. `--wait` w komendzie PHP
  może w tym czasie zgłosić timeout, choć aktualizacja dojdzie do skutku —
  dlatego §7 każe aktualizować po kolei.
- Symlink/FIFO w `data/`: handler i skrypt odmawiają, ale sprawdzenie w skrypcie
  to TOCTOU (ten sam poziom zaufania do katalogu `data/` co istniejący `append:`
  logu przez systemd i `chown -R`).
- Reset: kontekst przerywa żądanie HTTP do panelu, ale panel mógł je już odebrać
  — stąd `reset_sent: true` od chwili rozpoczęcia `func=reset`.

## 9. Self-review

- Każda decyzja ma dowód plik:linia w §2. Rozbieżności z decyzjami lidera:
  brak. U4 zmienione z „usuń” na „zostaw + przypnij” — decyzja lidera po
  zgłoszeniu. Host-wide lock i trzeci `reason` — zaakceptowane przez lidera.
- Brak nazw hostów, adresów, tokenów i nazw klientów; ścieżki tylko instalacyjne
  (`/opt/print-bridge…`, `/run/…`), publiczne w repo.
- Kontrakt zmienia się addytywnie (nowe kody, nowe `details`) albo z
  „błędne → poprawne” (422 dla błędu agenta, 202 dla tagu, który i tak padnie).
  Jedno przejście sukces → błąd jest świadome: reset, którego budżet mija, zanim
  panel rozstrzygnie wynik, a który dziś zdążyłby przed `WriteTimeout`, dostaje
  503 zamiast 200 (§3 U3, §4). Golden i dokument zmieniają się jawnie w tym samym PR.
- Odstępstwo od zaakceptowanego projektu U2: mutex w procesie usunięty, bo po
  przeniesieniu wszystkich operacji na znaczniku pod `flock` jest zbędny (§3 U2).
