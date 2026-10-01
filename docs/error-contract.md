# Kontrakt HTTP print-bridge (v2)

Opisuje to, co agent **v0.7.0** faktycznie wysyła na drucie. Kontrakt na drucie
jest identyczny od **v0.4.1** (w `v0.4.1..v0.7.0` nie zmieniła się żadna emisja
`apierr.New`/`WithDetail`, żaden kształt sukcesu ani tag JSON). Kolumna „Od
wersji” mówi, od którego wydania obowiązuje dany element (`git tag --contains`
commitu, który go wprowadził).

Dokument jest przypięty testami (§8): zbiór kodów, retryowalność, mapa kod →
endpoint → HTTP → `details`, pola sukcesu i pola health są porównywane z kodem.
Zmiana kodu bez zmiany tego dokumentu (albo odwrotnie) czerwieni `go test ./...`.

## 0. Konwencje

| Skrót | Żądanie | Auth | Od wersji |
|-------|---------|------|-----------|
| `print-jobs` | `POST /api/v1/print-jobs` | tak | v0.1.0 |
| `health` | `GET /api/v1/health` | **nie** | v0.1.0 |
| `printer-reset` | `POST /api/v1/admin/printer-reset` | tak | v0.4.0 |
| `update` | `POST /api/v1/admin/update` | tak | v0.1.0 |

- **Auth:** nagłówek `X-Print-Token` porównywany w stałym czasie. Brak →
  401 `MISSING_TOKEN`, zła wartość → 403 `FORBIDDEN`. Wyjątek: dokładna ścieżka
  `/api/v1/health` przechodzi bez tokenu. Auth działa **przed** routingiem, więc
  dowolna inna ścieżka bez tokenu (także nieistniejąca) daje kopertę 401.
- **JSON:** `Content-Type: application/json` (bez `charset`). Body to jeden
  obiekt JSON w zapisie zwartym (bez spacji), zakończony jednym `\n`
  (`json.Encoder`). Jedyny wyjątek: replay terminalny `print-jobs` (§1.1) — te
  same pola, ale **bez** końcowego `\n`.
- **Kolejność kluczy** nie należy do kontraktu (struktury: kolejność deklaracji,
  mapy: alfabetycznie). Klient porównuje zdekodowane obiekty, nie bajty.
- **Typy JSON** w tabelach: `string`, `number`, `boolean`, `null`,
  `string[]` (tablica stringów, może być pusta). `number` to w praktyce liczba
  całkowita.
- Nagłówek `Accept` jest ignorowany.

## 1. Kształty sukcesu

Tabele w tej sekcji wymieniają **wszystkie** pola odpowiedzi — innych nie ma.
Kolumna „Stała” to jedyna możliwa wartość pola (`—` = wartość zmienna).

### 1.1 `print-jobs` → 200

<!-- kontrakt:sukces print-jobs 200 -->
| Pole | Typ JSON | Stała | Inwariant |
|------|----------|-------|-----------|
| `status` | string | `"printed"` | jedyna wartość sukcesu (od v0.1.0) |
| `cups_job_id` | string | — | niepusty ciąg cyfr — id zadania CUPS |

200 przychodzi z trzech ścieżek, zawsze z tym samym kształtem:

1. świeży druk potwierdzony (§4) — fizycznie przez ~HS, a gdy drukarka
   odpowiada, ale nie mówi zrozumiałym ~HS — best-effort, po samym zakończeniu
   zadania w CUPS,
2. resume-by-key: ten sam `Idempotency-Key`, zadanie już wysłane do CUPS i bez
   faultu sprzętowego — agent nie wysyła go ponownie, tylko dokańcza
   potwierdzenie,
3. **replay terminalny**: ten sam `Idempotency-Key` po wcześniejszym sukcesie —
   agent oddaje zapisane bajty `{"status":"printed","cups_job_id":"<cyfry>"}`
   **bez końcowego `\n`**, niczego nie drukując. Rekord terminalny powstaje
   wyłącznie po sukcesie.

`Idempotency-Key` żyje `idempotency_ttl_days` (domyślnie 30 dni). Po tym czasie
klucz jest traktowany jak nowy, czyli kolejne żądanie to **świeży druk**.

### 1.2 `health` → 200 albo 503

`status: "ok"` z HTTP 200 albo `status: "degraded"` z HTTP 503. Body zawsze jest
JSON-em (również przy 503). 503 występuje, gdy zachodzi którykolwiek warunek:
`printer_online == false`, `cups_reachable == false`, albo ~HS sparsowane i
`paper_out`/`paused`/`head_open` == `true`.

<!-- kontrakt:health -->
| Pole | Typ JSON | Kiedy występuje | Od wersji |
|------|----------|-----------------|-----------|
| `version` | string | zawsze | v0.1.0 |
| `status` | string | zawsze — `"ok"` (200) albo `"degraded"` (503) | v0.1.0 |
| `printer_online` | boolean | zawsze — sonda TCP :9100 | v0.1.0 |
| `reach_error` | string | gdy sonda :9100 zwróciła błąd | v0.1.0 |
| `host_status` | string | zawsze — surowa linia 1 ~HS albo `"unavailable"` (sonda padła) albo `"unsupported"` (drukarka nie mówi ~HS) | v0.1.0 |
| `host_status_error` | string | gdy `host_status == "unavailable"` | v0.1.0 |
| `paper_out` | boolean | gdy ~HS sparsowane | v0.1.0 |
| `paused` | boolean | gdy ~HS sparsowane | v0.1.0 |
| `head_open` | boolean | gdy ~HS sparsowane | v0.2.0 |
| `queued_formats` | number | gdy ~HS sparsowane | v0.2.0 |
| `batch_remaining` | number | gdy ~HS sparsowane — surowe pole [8] linii 2 (bywa ~10^6: licznik mediów po restarcie drukarki) | v0.3.0 |
| `host_status_2` | string | gdy ~HS sparsowane — surowa linia 2, może być `""` | v0.2.0 |
| `cups_reasons` | string[] / null | gdy zapytanie IPP do CUPS się udało; `null`, gdy CUPS nie zwrócił atrybutu | v0.1.0 |
| `cups_error` | string | gdy zapytanie IPP do CUPS padło | v0.1.0 |
| `cups_reachable` | boolean | zawsze | v0.1.0 |
| `watchdog_auto_resets` | number | zawsze w binarce produkcyjnej (watchdog podłączony) | v0.4.0 |
| `watchdog_last_reset` | string | gdy watchdog wykonał auto-reset — RFC 3339 | v0.4.0 |

`version` to tag wydania **bez** `v` (`0.7.0`) albo `"dev"` dla binarki bez
ldflags. Zwarty zapis `"version":"X.Y.Z"` jest częścią kontraktu:
`deploy/update-bridge.sh` weryfikuje aktualizację grepem tych bajtów
(`grep -qF "\"version\":\"${TAG#v}\""`), także w odpowiedzi 503.

### 1.3 `printer-reset` → 200

<!-- kontrakt:sukces printer-reset 200 -->
| Pole | Typ JSON | Stała | Inwariant |
|------|----------|-------|-----------|
| `status` | string | `"reset_ok"` | |
| `panel_before` | string | — | stan panelu przed resetem; **może być `""`** (HTML panelu w nieznanym formacie) |
| `panel_after` | string | `"Ready"` | 200 tylko po powrocie panelu do Ready |
| `hs_ok` | boolean | — | best-effort ~HS po resecie; `false` NIE unieważnia resetu |

200 znaczy: `func=reset` wykonany, a panel wrócił do `Ready`. Reset ma skutek
fizyczny (restart print-servera). Typowo trwa ok. 30 s. Endpoint nie ma
własnego limitu czasu — w patologicznym przypadku może przekroczyć
`WriteTimeout` serwera i klient dostaje zerwane połączenie (§5, N7).

### 1.4 `update` → 202

<!-- kontrakt:sukces update 202 -->
| Pole | Typ JSON | Stała | Inwariant |
|------|----------|-------|-----------|
| `status` | string | `"updating"` | |
| `tag` | string | — | echo pola `tag` z żądania, **bez normalizacji** (z `v` albo bez) |

202 znaczy tylko „proces aktualizatora wystartował”, NIE „aktualizacja się
udała”. Wynik weryfikuje się przez `health.version` (aktualizator sam cofa
binarkę, gdy weryfikacja się nie powiedzie). Tag podawaj **z `v`**: `0.7.0`
przejdzie walidację (202), ale pobranie wydania padnie asynchronicznie
(tagi wydań to `v*`).

## 2. Koperta błędu

```json
{"code":"QUEUE_PAUSED","message":"job held by CUPS (pending-held); requires operator release","details":{"cups_job_id":"7","ipp_job_state":4}}
```

Inwarianty:

- `code` — string, zawsze jedna ze stałych z `internal/apierr/apierr.go`
  (zamknięty zbiór §2.1, nigdy pusty).
- `message` — string, niepusty tekst dla człowieka. **Nie do parsowania**: treść
  może się zmieniać i bywa techniczna (przy 422 na `update` zawiera np. lokalną
  ścieżkę instalacji).
- `details` — **albo nieobecne, albo niepusty obiekt** (nigdy `{}` ani `null`).
  Klucze i typy per kod: §2.3.
- Innych kluczy nie ma (status HTTP nie jest serializowany do body).

Klient mapuje po `code`. Kod nieznany klientowi (np. z nowszego agenta)
klasyfikuje po klasie HTTP — §6.

### 2.1 Kody

<!-- kontrakt:kody -->
| Kod | `Retryable()` (Go) | Retry automatyczny klienta | Znaczenie |
|-----|--------------------|----------------------------|-----------|
| `CUPS_UNAVAILABLE` | tak | tak | `lp`/IPP do cupsd padło albo CUPS przerwał zadanie (aborted) |
| `PRINTER_OFFLINE` | tak | tak | drukarka/panel nieosiągalne, głowica otwarta, fault po resecie |
| `PRINTER_OUT_OF_PAPER` | tak | tak | ~HS: brak papieru (fault sprzętowy — retry tym samym kluczem daje `PRINT_UNCONFIRMED`, §4) |
| `QUEUE_PAUSED` | tak | tak | kolejka CUPS wstrzymana, ~HS paused albo zadanie pending-held |
| `PRINT_TIMEOUT` | tak | tak | druk nie potwierdzony w budżecie czasu (etykiety mogą jeszcze wychodzić) |
| `BRIDGE_RESTARTING` | tak | tak | magazyn idempotencji niedostępny albo uszkodzony rekord pending |
| `INVALID_PDF` | nie | nie | render PDF → ZPL nie powiódł się |
| `INVALID_ZPL` | nie | nie | CUPS anulował zadanie |
| `UNSUPPORTED_FORMAT` | nie | nie | payload nie jest ani PDF, ani ZPL |
| `INVALID_REQUEST` | nie | nie | błędne żądanie (nagłówek, JSON, base64, tag) |
| `MISSING_TOKEN` | nie | nie | brak `X-Print-Token` |
| `FORBIDDEN` | nie | nie | zły `X-Print-Token` |
| `PRINTER_BUSY` | tak | **nie — ponów ręcznie** | reset odrzucony, bo trwa druk |
| `PRINT_UNCONFIRMED` | nie | **nie — decyzja człowieka** | wynik fizyczny po faulcie niepoznawalny (§4) |

`Retryable()` to klasyfikacja wewnątrz Go — **nie trafia na drut**. Klient
decyduje o automatycznym retry po klasie HTTP (§3).

### 2.2 Mapa emisji: kod → endpoint → HTTP → `details`

Jeden wiersz = jedna kombinacja, która naprawdę występuje. Ten sam kod może
przyjść z kilku endpointów, z różnym HTTP i raz z `details`, raz bez.

<!-- kontrakt:emisje -->
| Kod | Endpoint | HTTP | `details` | Od wersji | Kiedy |
|-----|----------|------|-----------|-----------|-------|
| `INVALID_REQUEST` | print-jobs | 400 | — | v0.1.0 | brak `Idempotency-Key`; body nie jest JSON-em albo > 20 MB; brak/zły base64 |
| `INVALID_REQUEST` | update | 400 | — | v0.1.0 | body nie jest JSON-em (także puste) |
| `INVALID_REQUEST` | update | **422** | — | v0.1.0 | aktualizator odrzucił żądanie: zły/brak `tag`, ale też błąd po stronie agenta (log aktualizatora, start procesu) |
| `BRIDGE_RESTARTING` | print-jobs | 503 | — | v0.1.0 | błąd odczytu magazynu idempotencji; rekord pending bez użytecznego `cups_job_id` |
| `PRINT_UNCONFIRMED` | print-jobs | 409 | `original_fault`: string, `cups_job_id`: string | v0.4.1 | retry kluczem, którego zadanie przerwał fault sprzętowy |
| `INVALID_PDF` | print-jobs | 422 | — | v0.1.0 | render nie powiódł się |
| `UNSUPPORTED_FORMAT` | print-jobs | 422 | — | v0.1.0 | payload ani PDF, ani ZPL |
| `INVALID_ZPL` | print-jobs | 422 | — | v0.1.0 | IPP: zadanie canceled |
| `PRINTER_OFFLINE` | print-jobs | 503 | — | v0.1.0 | :9100 nieosiągalny przed wysłaniem; głowica otwarta (~HS); brak odpowiedzi ~HS w całym budżecie weryfikacji |
| `PRINTER_OFFLINE` | printer-reset | 503 | — | v0.4.0 | panel niedostępny; `func=reset` nie powiódł się; panel nie wrócił do Ready w budżecie |
| `PRINTER_OFFLINE` | printer-reset | 503 | `panel_state`: string | v0.4.0 | po resecie panel raportuje fault |
| `QUEUE_PAUSED` | print-jobs | 503 | — | v0.1.0 | kolejka CUPS wstrzymana (przed wysłaniem); ~HS paused (po wysłaniu) |
| `QUEUE_PAUSED` | print-jobs | 503 | `ipp_job_state`: number, `cups_job_id`: string | v0.1.0 | IPP: zadanie pending-held (wymaga zwolnienia przez operatora) |
| `CUPS_UNAVAILABLE` | print-jobs | 503 | — | v0.1.0 | `lp` padło; zapytanie IPP padło; IPP: zadanie aborted |
| `PRINT_TIMEOUT` | print-jobs | 503 | — | v0.1.0 | budżet potwierdzenia wyczerpany (CUPS albo ~HS wciąż drenuje); kontekst anulowany (budżet czasu serwera albo rozłączenie klienta) |
| `PRINT_TIMEOUT` | printer-reset | 503 | — | v0.4.0 | anulowany kontekst w oczekiwaniu na panel — tylko po rozłączeniu klienta, więc w praktyce niewidoczny |
| `PRINTER_OUT_OF_PAPER` | print-jobs | 503 | — | v0.1.0 | ~HS: brak papieru po wysłaniu |
| `PRINTER_BUSY` | printer-reset | 409 | — | v0.4.0 | panel raportuje `Printing` |
| `MISSING_TOKEN` | print-jobs, printer-reset, update | 401 | — | v0.1.0 (printer-reset: v0.4.0) | brak `X-Print-Token` |
| `FORBIDDEN` | print-jobs, printer-reset, update | 403 | — | v0.1.0 (printer-reset: v0.4.0) | zły `X-Print-Token` |

Relacja kod → HTTP jest 1:1 z jednym wyjątkiem: `INVALID_REQUEST` ma 400
**albo** 422 (tylko `update`). `health` nie emituje koperty błędu.

### 2.3 `details` per kod

- `QUEUE_PAUSED` — `details` **tylko** przy IPP pending-held:
  `ipp_job_state` number (dziś zawsze `4` = pending-held),
  `cups_job_id` string (niepusty ciąg cyfr). Pozostałe emisje bez `details`.
- `PRINT_UNCONFIRMED` — `details` **zawsze**: `original_fault` string (dziś
  zawsze `"PRINTER_OUT_OF_PAPER"`), `cups_job_id` string (niepusty ciąg cyfr).
- `PRINTER_OFFLINE` — `details` **tylko** z `printer-reset`, gdy po resecie
  panel raportuje fault: `panel_state` string (tekst z panelu, np.
  `"Paper Jam"`; **może być `""`**). Pozostałe emisje bez `details`.
- Wszystkie inne kody: nigdy `details`.

`cups_job_id` w `details` jest zawsze stringiem, a `ipp_job_state` zawsze liczbą.
Klient nie może zakładać, że dany kod zawsze ma albo zawsze nie ma `details`.

## 3. Retry: automatyczny vs ręczny

- **Retry automatyczny klienta = każdy 5xx.** Dziś każdy 5xx agenta to 503 z
  kodem `Retryable() == true`. Ponawiaj **tym samym** `Idempotency-Key`: jeśli
  zadanie zostało już wysłane do CUPS, agent je wznawia (resume-by-key) zamiast
  wysyłać drugi raz, więc retry nie duplikuje etykiety.
- **4xx = trwałe dla tego żądania** — nie ponawiaj automatycznie.
- **`PRINTER_BUSY` (409, tylko `printer-reset`) — ponów ręcznie.** W Go
  `Retryable() == true` („spróbuj po zakończeniu druku”), ale reset ma skutek
  fizyczny i jest akcją operatora. Klient celowo **nie** ponawia go
  automatycznie: operator ponawia reset sam, gdy batch się skończy. To jedyny
  kod, w którym `Retryable()` i retry automatyczny klienta się różnią.
- **`PRINT_UNCONFIRMED` (409) — nigdy automatycznie**, decyzja człowieka (§4).

## 4. `printed` i `PRINT_UNCONFIRMED`

`printed` = `job-state=9` + `~HS` zdrowy + bufor/batch zdrenowany (flaga batcha
linii 2 ~HS = 0) — na szczęśliwej ścieżce oznacza to fizyczne wyjście ostatniej
etykiety (zweryfikowane na sprzęcie).

**Best-effort bez ~HS:** gdy drukarka odpowiada na ~HS, ale odpowiedzi nie da
się sparsować (dialekt bez ~HS), `printed` znaczy tylko „CUPS zakończył
zadanie” (`job-state=9` albo zadanie zniknęło z historii CUPS) — bez fizycznego
potwierdzenia wyjścia etykiety. Health pokazuje ten stan jako
`host_status: "unsupported"` (ta sama sonda ~HS). Na drucie oba przypadki
wyglądają tak samo.

**Wyjątek (nieobserwowalność po faulcie):** gdy job został przerwany faultem
sprzętowym (`PRINTER_OUT_OF_PAPER`), fizyczny wynik jest niepoznawalny — przy
recovery medium print-server potrafi ODRZUCIĆ zbuforowany format (zmierzone
2026-06-07), a w innych gałęziach (reset/wznowienie) ten sam sygnał oznacza
wydrukowanie. Retry tym samym `Idempotency-Key` zwraca wtedy **PRINT_UNCONFIRMED
(409, bez retry!)** z `details.original_fault` i `details.cups_job_id`. UI musi
zapytać człowieka: „etykieta wyszła?" → [potwierdź] (zamknij job po stronie
klienta) / [dodrukuj] (NOWY `Idempotency-Key`). Automatyczny retry ani resubmit
tym samym kluczem nigdy nie rozwiążą tego stanu.

`PRINT_UNCONFIRMED` istnieje od v0.4.1. Agent starszy niż v0.4.1 na retry po
braku papieru wznawiał zadanie i mógł zwrócić fałszywe `printed`.

## 5. Odpowiedzi bez koperty

Te odpowiedzi nie są kopertą JSON (albo nie mają body). Produkuje je biblioteka
standardowa Go (`http.ServeMux`, `http.Server`), a nie handlery agenta. Klient
musi traktować „body nie jest kopertą” jako osobny, jawnie obsłużony wynik:
5xx → przejściowy, 4xx → trwały. N6 i pośrednicy (proxy) są poza kontrolą
agenta, więc tej klasy nie da się usunąć po stronie Go.

| # | Przypadek | Odpowiedź | Przypięte testem |
|---|-----------|-----------|------------------|
| N1 | nieznana ścieżka **z poprawnym tokenem** (także agent < v0.4.0 dla `printer-reset`) | 404, `text/plain; charset=utf-8`, `X-Content-Type-Options: nosniff`, body `404 page not found\n` | tak |
| N1b | nieznana ścieżka **bez tokenu** | 401 koperta `MISSING_TOKEN` (auth przed routingiem) | tak |
| N2 | zła metoda na znanej ścieżce z tokenem (np. `GET print-jobs`) | 405, `text/plain; charset=utf-8`, `Allow: POST`, body `Method Not Allowed\n` | tak |
| N3 | zła metoda na `health` (np. `POST`) — **bez auth** | 405, `text/plain; charset=utf-8`, `Allow: GET, HEAD` | tak |
| N4 | ukośnik na końcu (`/api/v1/print-jobs/`) z tokenem | 404 jak N1 | tak |
| N5 | nieczysta ścieżka (`//api/v1/print-jobs`) z tokenem | 307, `Location: /api/v1/print-jobs`, puste body | tak |
| N6 | **plain HTTP na port TLS** (`base_url` z `http://`) — realna pomyłka konfiguracji | `HTTP/1.0 400 Bad Request` **bez `Content-Type`**, body `Client sent an HTTP request to an HTTPS server.\n` | nie (zachowanie `http.Server`; wymaga sieci) |
| N7 | panika w handlerze, przekroczony `WriteTimeout` (patologiczny reset, §1.3) | brak odpowiedzi — zerwane połączenie | nie (wymaga sieci) |
| N8 | zniekształcone żądanie, nagłówki > 1 MiB | 400 / 431 `text/plain`, `Connection: close` | nie (wymaga sieci) |

Uwaga: body > 20 MB to **nie** jest N8 — daje kopertę 400 `INVALID_REQUEST`
(§2.2).

## 6. Zasady zmian kontraktu

- **Zmiany tylko addytywne:** nowe pole w obiekcie sukcesu albo w health, nowy
  klucz w `details`, nowy kod błędu, `details` przy kodzie, który go dotąd nie
  miał.
- **Nowy kod błędu:** o zachowaniu klienta, który go jeszcze nie zna, decyduje
  klasa HTTP — **5xx = przejściowy** (retry automatyczny tym samym kluczem),
  **4xx = trwały**. HTTP nowego kodu musi więc odpowiadać jego semantyce. Nowy
  kod dopisz do `apierr.go`, do tabel §2.1 i §2.2 oraz do tabeli przypadków
  testu kontraktu (testy z §8 tego wymagają).
- **Zmiany łamiące** (wymagają skoordynowanego wydania z klientem): usunięcie
  albo zmiana nazwy pola sukcesu, zmiana typu pola (także w `details` i w
  health), zmiana wartości stałej (`"printed"`, `"reset_ok"`, `"Ready"`,
  `"updating"`), zmiana HTTP istniejącej emisji (np. 422 → 503 przenosi klienta
  z „trwały” do „przejściowy”), usunięcie kodu, usunięcie `details`, na którym
  klient polega (`PRINT_UNCONFIRMED`), zmiana zapisu `"version":"X.Y.Z"` w
  health (czyta go aktualizator).
- Zmiana `Retryable()` bez zmiany HTTP nie zmienia drutu — wymaga tylko
  aktualizacji §2.1.

## 7. Wersje

- Kontrakt na drucie: stały od **v0.4.1** do **v0.7.0** włącznie.
- Agent < v0.4.0: brak `printer-reset` (żądanie daje N1 — 404 `text/plain`),
  brak `PRINTER_BUSY`, `watchdog_*` w health i `details.panel_state`.
- Agent < v0.4.1: brak `PRINT_UNCONFIRMED` (§4).
- Wersję agenta na hoście podaje `health.version`.

## 8. Testy przypinające

- `internal/server/contract_test.go` — „golden” przez `Router()` z prawdziwym
  `TokenAuth` i prawdziwymi producentami błędów (`printer.Printer`,
  `printer.PrinterResetter` z `printer.WebPanel`, `update.SpawnUpdater`) nad
  atrapami CUPS/~HS/panelu: status, `Content-Type`, pełne body jako zdekodowana
  struktura z typami, bajty replayu, odpowiedzi bez koperty N1–N5. Tabela
  przypadków pokrywa każdy kod z `apierr.go`.
- `internal/server/contract_doc_test.go` — spójność tego dokumentu z kodem:
  §2.1 (zbiór kodów, `Retryable()`, retry automatyczny = 5xx), §2.2 (wiersze =
  emisje z tabeli przypadków), §1.1/§1.3/§1.4 (pola, typy, stałe).
- `cmd/print-bridge/contract_health_test.go` — health przez `Router()` z
  prawdziwym `makeHealth`: pełne body, bajty `"version":"X.Y.Z"` czytane przez
  `deploy/update-bridge.sh`, spójność z tabelą §1.2.
- `internal/apierr/apierr_test.go` — `Retryable()` dla wszystkich 14 kodów.
