# print-bridge

Agent druku etykiet termicznych (Go) dla Xprinter XP-423B przez CUPS. Companion
dla `the marketplace orchestrator`. Przyjmuje ZPL (passthrough) lub PDF
(render→ZPL `^GFA` z kompresją RLE), drukuje przez raw queue CUPS z własnym
backendem `lpdpaced`, potwierdza FIZYCZNE zakończenie druku przez ZPL `~HS`.

## Endpointy
- `POST /api/v1/print-jobs` — `X-Print-Token` + `Idempotency-Key`; body
  `{label_base64|pdf_base64, copies, format?, external_reference?}`.
  `status:"printed"` = ostatnia etykieta fizycznie wyszła (drain-poll `~HS`);
  retry z tym samym kluczem wznawia istniejący job (zero duplikatów).
  **Wyjątek:** po faulcie sprzętowym (np. brak papieru) fizyczny wynik joba
  jest nieobserwowalny — retry zwraca `PRINT_UNCONFIRMED` (409, nie-retryable)
  i wymaga decyzji człowieka: potwierdź albo dodrukuj NOWYM kluczem
  (`docs/error-contract.md`).
- `GET  /api/v1/health` — drukarka/CUPS/`~HS`: m.in. `head_open`, `paper_out`,
  `paused`, `queued_formats`, `batch_remaining`, `host_status`/`host_status_2`,
  `watchdog_auto_resets` (200 ok / 503 degraded).
- `POST /api/v1/admin/printer-reset` — „wymieniłem papier — wznów": odwiesza
  latched `Paper Jam` i zawieszony responder 9100 (`function.cgi?func=reset`),
  zbuforowany job dokańcza się; 409 `PRINTER_BUSY` gdy trwa druk (retryable).
- `POST /api/v1/admin/update` — self-update do tagu release (sudo → transient
  unit systemd; log: `data/update.log`).

## Specyfika sprzętu (XP-423B, zmierzone na żywo)
- Print-server (10/100, Ethernut) gubi pakiety przy wysyłce >40-60 KB/s z GbE
  Linuxa — backend `lpdpaced` sączy dane ~20 KB/s
  (device-uri `lpdpaced://<ip>/lp?rate=20000`); bez tego multi-label job
  wlecze się 30-50 s („druga etykieta po minucie").
- Pole [8] linii 2 `~HS`: w trakcie druku flaga batcha (0/1, sygnał „ostatnia
  etykieta wyszła"), po boocie/cyklu głowicy wyciek licznika mediów z NVRAM —
  stąd guard wiarygodności `<10000`.
- Watchdog (tick 60 s) auto-resetuje zawieszony responder `~HS` (3 kolejne
  erry transportu + TCP żywe + panel `Ready`; rate-limit 15 min).
- Gałki ciemności (`^MD`, `~SD`, panelowa density — także po power-cycle) są
  MARTWE na tym firmware; działa za to `^PR` (prędkość → ciepło na punkt):
  produkcyjnie `^PR2` dla pełnego krycia kodów kreskowych. Część ramek w PDF-ach
  przewoźników jest rysowana jaśniejszą szarością (luma 166/183) — `render_threshold`
  domyślnie **190** (kod + `config.json.template`), żeby się drukowały; niżej
  (np. 160) gubi te kreski.

Pełne wyniki pomiarów: `docs/hardware-spike-findings.md`.

## Build
`CGO_ENABLED=0 go build ./cmd/print-bridge ./cmd/lpdpaced`

## Deploy (Debian 13)
`sudo ./install-debian.sh <printer_ip> <queue> <egress_cidr>` — instaluje
agenta (`/opt/print-bridge`, systemd), backend CUPS
(`/usr/lib/cups/backend/lpdpaced`), updater (`/usr/local/sbin/update-bridge.sh`
+ sudoers drop-in dla self-update) i kolejkę `lpdpaced://`. Przy świeżej
instalacji `config.json` jest seedowany automatycznie: `printer_ip` i
`cups_queue` z argumentów + wygenerowany `print_token` (wypisany na końcu —
przekaż go orchestratorowi); ponowna instalacja NIE rusza istniejącego
configu. Przed produkcją `ufw allow ssh && ufw enable`. Aktualizacja ręczna:
`sudo update-bridge.sh <tag>`. Patrz `deploy/`.

## Wymagania runtime
- CUPS + `cups-client` (raw queue przez backend `lpdpaced`)
- `poppler-utils` (`pdftoppm`/`pdfinfo`) — ścieżka PDF (preferowana dla rynku
  PL: wbudowany font emulacji ZPL nie ma polskich glifów)
- drukarka w emulacji ZPL (CEZD zatrzaskuje język po pierwszym `^XA`)
