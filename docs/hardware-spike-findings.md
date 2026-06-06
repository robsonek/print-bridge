# Hardware Spike — wyniki na realnym XP-423B (2026-06-06)

Egzemplarz testowany lokalnie w LAN. Mac testera `192.168.1.217`, drukarka
`192.168.1.75`, VM Debian 13 `192.168.1.120`. Ten dokument = **empiryczne wyniki**
do `hardware-spike.md` (plan). Skraca/zmienia kilka założeń designu — patrz „Implikacje".

## TL;DR

- ✅ **Fundament designu DZIAŁA:** silnik rozumie **ZPL natywnie** (CEZD auto-detekcja `^XA`),
  passthrough przez port 9100 drukuje, barcode czytelny wizualnie (skan czytnikiem — TODO).
- 🔴🇵🇱 **PIVOT FORMATU: ZPL passthrough GUBI POLSKIE ZNAKI → dla PL używaj PDF render.**
  Porównanie tej samej etykiety DPD (PDF vs ZPL, 2026-06-06): natywny ZPL ma ostrzejszy QR,
  ALE wbudowany font emulacji ZPL XP-423B **nie ma polskich glifów** mimo `^CI28` UTF-8 →
  „Kpino" zamiast „Kępino" = **BŁĘDNY ADRES** (paczka może nie dojść). **PDF→`^GF` rasteryzuje
  glify → polskie znaki POPRAWNE, a QR/barcode z renderu SKANUJĄ SIĘ.** Wniosek odwraca
  „ZPL-first": dla polskich etykiet **PDF preferowany** (poprawny adres > wizualna ostrość QR).
  ZPL passthrough OK tylko gdy adres jest grafiką (`^GF`) lub bez diakrytyków. → Laravel L1
  (per-magazyn `label_format`): **default PDF dla rynku PL**, nie ZPL.
- ✅ Silnik rozumie też **TSPL** (natywny język Xprinter) — nieużywany, ale potwierdza żywotność.
- 🔄 **`~HS` — KOREKTA wstępnego wniosku „niedostępny" (był BŁĘDNY).** Pierwsze 4 sondy
  (drukarka fabryczna, milage 0.007 km, NIGDY nie dostała ZPL) milczały → przedwcześnie
  uznałem „9100 jednokierunkowy". **Po pierwszym druku ZPL `~HS` odpowiada** pełnym 3-liniowym
  formatem Zebra (82 B) i **przeżywa power-cycle** (hipoteza: CEZD zatrzaskuje język ZPL w NVRAM
  po pierwszym `^XA`). Wymaga **świeżego połączenia** (reused-socket po druku bywa 0 B).
  → Decyzja G żywotna, ale `~HS` to kanał **uzupełniający**, NIE autorytatywny.
- ✅ **Status autorytatywny: web panel `status.cgi`** (HTTP) — **mode-independent** (działa
  niezależnie od trybu języka, więc i na świeżej drukarce przed pierwszym ZPL), rozróżnia stany
  bogato. `~HS` uzupełnia (paper-out/pause z linii 1) gdy drukarka w ZPL. Patrz „Decyzja architektury".
- 🔴 **Port 9100 (CUPS `socket://`) NIE DZIAŁA: „completed" bez druku.** CUPS socket backend
  robi half-close (FIN) zaraz po wysłaniu danych; ten print-server **gubi bufor przy każdym FIN**.
  Job kończy się `job-state=9 completed`, ale **nic nie drukuje** → job-state KŁAMIE.
- ✅ **FIX: backend `lpd://ip/lp` (port 515) zamiast `socket://ip:9100`.** Protokół LPD ma
  ramkowanie po liczbie bajtów + ACK serwera → print-server odbiera całość zanim połączenie
  się zamknie → **drukuje**. Zero zmian w kodzie Go — tylko `device-uri` kolejki CUPS.
- ⚠️ **Print-server jednowątkowy** — health-poll może kolidować z aktywnym drukiem; traktować
  timeout-w-trakcie-druku jako „busy", nie „down".

## Urządzenie (potwierdzone u źródła)

- Web panel: **„XP-423B Print Server"**, serwer **Ethernut/Nut-OS 4.8.7.0** (firmware print-servera).
- Drukarka: **XP-423B Version 1.038.2370 CEZD**, Milage **0.007 Km** (praktycznie dziewicza).
  Suffix `CEZD` = multi-emulacja (auto-detekcja języka ze strumienia).
- MAC `00:1b:82:3f:db:42`, TTL 64. Otwarte porty: **21 FTP, 23 telnet, 80 HTTP, 515 LPD, 9100 RAW**.
- Panel WWW (`/menu.htm` → `/cgi-bin/*.cgi`): General, Adjust, Media, Calibration, Serial,
  Network, Mail, Clock, Password + Tools: **Function, Upgrade, Status, File**.
- Media (z `media.cgi`): width 3.94" (100 mm), height 5.85" (~148 mm), gap 0.12" (3 mm),
  **sensor = Gap** (die-cut). Zgodne z 4×6"/100×148 ZPL.

## Co przetestowano i wynik

| # | Test | Wynik |
|---|------|-------|
| 1 | TCP do drukarki/VM | drukarka `:9100` open; VM `:22` open; VM `:631` (CUPS) zamknięty (jeszcze nie postawiony) |
| 2 | `~HS` / `~HQES` przez socket 9100 (przed i po primingu ZPL) | **0 bajtów zawsze** — brak kanału zwrotnego |
| 3 | ZPL `^XA…^XZ` na 9100, **natychmiastowy close** | **nie drukuje** (dane zgubione) |
| 4 | TSPL na 9100, natychmiastowy close | **nie drukuje** |
| 5 | „Print Configuration" (`func=report`, HTTP) | ✅ **drukuje** stronę config → silnik+papier+kalibracja+link OK |
| 6 | „Send File to Printer" (POST multipart) z TSPL | ✅ **drukuje** „SEND-FILE TSPL" + barcode |
| 7 | TSPL na 9100 z **przytrzymaniem socketu 7 s** | ✅ **drukuje** „RAW9100 HOLD" |
| 8 | ZPL na 9100 z przytrzymaniem 7 s | ✅ **drukuje** „ZPL RAW9100" + barcode — **fundament potwierdzony** |
| 9 | `status.cgi` baseline | `Ready` (greentext) |
| 10 | `status.cgi` przy otwartej głowicy | **`Carriage Open` (redtext)** |
| 11 | `status.cgi` brak papieru w spoczynku | `Ready` — **brak papieru NIE wykrywany w spoczynku** |
| 12 | job bez papieru → poll `status.cgi` | **`Paper Jam` (redtext)**, latched, poll szybki (0.0 s, brak kolizji bo job od razu failuje) |
| 13 | włożenie papieru bez resetu | nadal `Paper Jam` (latched, brak auto-recovery) |
| 14 | `func=reset` (HTTP) | ✅ recovery: krótka niedostępność print-servera → `Printing` → **`Ready`**; zaległy job z bufora się wykonał |
| 15 | skan barcode ZPL realnym czytnikiem | ✅ **odczytał `123456789`** — niewiadoma B (ZPL) zamknięta pozytywnie |
| 16 | VM: CUPS raw queue **`socket://192.168.1.75:9100`** + `lp -o raw` ZPL | 🔴 `job-state=9 completed` ale **etykieta NIE wyszła** (FIN gubi dane) |
| 17 | VM: CUPS raw queue **`lpd://192.168.1.75/lp`** + `lp -o raw` ZPL | ✅ „now printing… Connected to printer" → **drukuje** (kod 555555) |
| 18 | E2E: realna etykieta Allegro **A6 PDF** (104.8×148.2 mm) → agent `/print-jobs` | render OK, ale `^GFA` = **249 KB** → CUPS spooling **utknął na 79%**, deadlock, `PRINT_TIMEOUT`; **NIE wydrukowała** |
| 19 | agent `/health` (~HS) — odpowiada stabilnie | ✅ `status:ok`, host_status=linia1 ~HS (patrz mapa ~HS) |
| 20 | mały `^GFA` (100×100, 2.7 KB) przez LPD na CZYSTYM stanie | ✅ drukuje (status.cgi `Printing`) — **format `^GF` OK; problem dużego = rozmiar/bufor** |
| 21 | mały `^GFA` zaraz po zacięciu dużego (bez resetu) | 🔴 utknął na 0% — **zacięty duży `^GF` BLOKUJE LPD dla kolejnych jobów aż do `func=reset`** |
| 22 | idempotency: 2× POST ten sam `pj:idem-A` (ZPL) | ✅ oba `cups_job_id:7`, **BEZ 2. joba** — resume-by-key działa (replay) |

## Mapa statusów (`status.cgi`, parsuj `class=(red|green)text>TEKST`)

| String | Kolor | Znaczenie | Traktowanie |
|--------|-------|-----------|-------------|
| `Ready` | green | gotowa | OK |
| `Printing` | green | aktywny druk | transient/OK (nie blokuje) |
| `Carriage Open` | red | głowica/pokrywa otwarta | fault → Signal |
| `Paper Jam` | red | brak papieru / zacięcie (latched) | fault → Signal; recovery `func=reset` |

**Reguła odporna:** `greentext` = OK/transient; **każdy `redtext` = fault** (treść = powód do
Signala). NIE mapować stringów 1:1 — firmware może mieć ich więcej; nieparsowalne → `unknown`.

## Mapa `~HS` (Zebra Host Status, 3 linie `STX…ETX CR LF`, świeże połączenie)

Format (zaobserwowany, 82 B): `\x02<linia1>\x03\r\n\x02<linia2>\x03\r\n\x02<linia3>\x03\r\n`

| Linia | Przykład (OK) | Kluczowe pola (pozycja, 0-indeks po przecinkach) |
|-------|---------------|--------------------------------------------------|
| 1 | `150,0,0,1219,000,0,0,0,000,0,0,0` | poz1 comm; **poz2 = paper-out** (0/1); **poz3 = pause** (0/1); poz4 = label length (1219 dots) |
| 2 | `000,0,0,0,0,2,0,0,01335508,1,000` | **poz3 = head-up** (0=zamkn., 1=otwarta); poz4 ribbon; poz6 print mode; poz9 licznik |
| 3 | `8888,0` | password, static RAM |

**Zweryfikowane empirycznie:** głowica otwarta → linia 2 poz3 `0→1` (linia 1 bez zmian!).
Brak papieru w SPOCZYNKU → `~HS` NIE ustawia paper-out (poz2 zostaje 0) — jak status.cgi (oba ślepe).
`~HS` przeżywa power-cycle (odpowiada przed pierwszym drukiem, gdy drukarka raz weszła w ZPL).

**⚠️ Luka agenta v0.1.0 (MED #10):** agent zwraca `host_status` = **tylko linia 1** i parsuje
paper-out/pause. **head-open (linia 2 poz3) jest NIEWIDOCZNY** → agent /health = „ok" przy otwartej
głowicy (zweryfikowane). status.cgi to łapie (`Carriage Open`).

## Implikacje dla agenta `print-bridge` (do wdrożenia w kodzie)

1. **Decyzja architektury statusu: `status.cgi` AUTORYTATYWNY + `~HS` UZUPEŁNIAJĄCY.**
   - **`status.cgi` (HTTP) = główne źródło zdrowia** — mode-independent (działa na świeżej
     drukarce przed pierwszym ZPL, gdzie `~HS` milczy), łapie `Carriage Open` którego agent
     przez `~HS` nie widzi. Dodać klienta HTTP `status.cgi` (parser `class=(red|green)text`,
     `Healthy()` = green Ready/Printing, redtext = fault z treścią jako powód do Signala,
     nieparsowalne → `unknown`). Config: **adres HTTP print-servera** (ten sam IP, port 80).
   - **`~HS` zostaje jako uzupełnienie** (paper-out/pause z linii 1, gdy drukarka w ZPL).
     Opcjonalnie rozszerzyć parser o **linię 2 poz3 (head-open)** — zamyka MED #10 — ale skoro
     `status.cgi` już to łapie, priorytet niski. NIE robić `~HS` jedynym źródłem (milczy na
     świeżej/po factory-reset drukarce → fałszywy down przy pierwszym pollu).
   - Druk: kolejka CUPS przez **`lpd://`** (pkt 3), NIE `socket://`.
2. **Recovery (filar 3):** awarie są **latched**, brak auto-recovery po fizycznym fixie.
   Agent może wywołać `GET /admin/cgi-bin/function.cgi?func=reset` po wykryciu fault+fix.
   **Uwaga:** reset na chwilę ubija print-server (HTTP URLError ~1 s) → poll z retry.
3. **Timing 9100 — ROZSTRZYGNIĘTE: użyć backendu `lpd://`, NIE `socket://`.** `socket://`
   na tym print-serverze daje „completed" bez druku (FIN gubi bufor). `lpd://ip/lp` drukuje
   niezawodnie. **`install-debian.sh`: `lpadmin -p <queue> -E -v lpd://<ip>/lp -o raw`** (było
   `socket://<ip>:9100`). Spec agenta (decyzja C/E) — device-uri = `lpd://`. Kod Go bez zmian
   (agent woła `lp -o raw -d <queue>`, nie tworzy socketu sam).
   - **Konsekwencja dla decyzji H/F:** `job-state=9` z backendu `socket://` jest BEZWARTOŚCIOWY
     jako dowód druku. `lpd://` jest wiarygodniejszy (ACK = print-server odebrał całość), ale
     „odebrał" ≠ „silnik wydrukował" → realna weryfikacja przez `status.cgi` (pkt 1) i tak konieczna.
4. **Jednowątkowość print-servera:** health-poll i druk dzielą jedno urządzenie. Timeout
   `status.cgi` PODCZAS aktywnego druku = „busy"/„unknown", **nie** „down" (inaczej fałszywe alerty).
5. **Bufor print-servera:** joby są buforowane i wykonują się gdy drukarka wróci do Ready
   (zaległy job wykonał się po reset) — uwaga na idempotency przy retry/timeout.
6b. ✅ **PDF→`^GF` ROZWIĄZANE kompresją RLE — zweryfikowane na sprzęcie (etykieta JaFoti A6).**
   `^GFA` z kompresją ASCII RLE (`,`=reszta wiersza 0, `!`=reszta F, `:`=powtórz wiersz, G-Y/g-z=liczniki)
   zmniejszył 249 KB → **33 KB (13%)** → **drukuje bez zacięcia (idle ~9-12 s), barcode skanowalny**.
   To dowodzi: problem był **WIRE (transfer do print-servera), NIE RAM silnika** (124 KB raster po dekompresji
   silnik drukuje OK). **Zatwierdzone parametry kalibracyjne (do portu w Go):**
   `pdftoppm -gray -scale-to-x 800` (margines, brak ucięcia na 832-dot głowicy 4″), threshold **160**,
   offset `^FO16,8`, **`^MD14`** (darkness, przeciw bladości), **`^PR2`** (2 ips — wolno = ciemniej),
   `^PW832`, RLE compression. Standalone generator: `print-bridge` testy → `/tmp/gen_gf_rle.py` (referencja portu).
6. 🔴 **PDF→`^GF` (oryginalnie) ZACINA SIĘ — `^GFA` (ASCII hex) za duży dla bufora print-servera.**
   Realna etykieta A6 (89 KB PDF) → `^GFA` **249 KB** → CUPS spooling utknął na 79% (deadlock
   TCP backpressure: print-server nie odbiera dalej, silnik nie startuje bez kompletu). `zplgf.go`
   robi `^GFA,...,hex` (2 znaki/bajt). **FIX wymagany przed użyciem ścieżki PDF:** kompresja `^GF`
   — **`:Z64:` (zlib+base64)** lub ZPL-RLE (etykieta = głównie biel → kompresja drastyczna,
   spodziewane <20 KB). Dopóki tego nie ma, **PDF→`^GF` niefunkcjonalny na tym sprzęcie**;
   ZPL passthrough (natywne komendy, małe) działa bez zarzutu.
   - Wtórnie: `confirm_timeout` 30 s i tak za krótki dla rastra; po kompresji zweryfikować ponownie.

## Pozostaje (otwarte)

**ZAIMPLEMENTOWANE 2026-06-06 (TDD, kod agenta, zweryfikowane E2E na sprzęcie):**
- [x] ~~Kompresja `^GF` RLE~~ — `EncodeGF` (zplgf.go), 249 KB→33 KB, etykieta JaFoti drukuje w 5.2 s.
- [x] ~~Parametry jakości~~ — `WrapLabel`+`LabelOptions` (`^MD`/`^PR`/`^FO`), `RenderOptions` (scale-to-x/
      threshold), 7 pól config + defaults (160/14/2/16/8/832/800), `main.go` przekazanie. Guard A4 → MediaBox (pdfinfo).
- [x] ~~`install-debian.sh`: `socket://` → `lpd://ip/lp`~~ + `config.json.template` nowe pola.
- [x] ~~VM raw queue~~ / ~~skan barcode ZPL~~ / ~~idempotency resume-by-key~~ — ✅ (wyżej).

**POZOSTAJE (osobny cykl):**
- [ ] 🔴🔴 **NIEROZWIĄZANE — druga etykieta multi-label `^GF` drukuje z ~MINUTOWYM OPÓŹNIENIEM.**
      Zweryfikowane 2026-06-06 na 2-paczkowym DPD PDF: **1. etykieta wychodzi OD RAZU, 2. dopiero po ~60 s**
      (pojedynczy PDF JaFoti drukuje w 5.2 s — więc to NIE czas druku, lecz przerwa MIĘDZY etykietami).
      Skalowanie confirm_timeout (30 s→60 s/2-label, ZAIMPLEMENTOWANE) złagodziło, ale 60 s wciąż za mało
      → `PRINT_TIMEOUT` (druk i tak kończy się, idempotency+retry chroni). **Przyczyna NIEZNANA** —
      hipotezy do zbadania w nowej sesji: print-server Ethernut buforuje/przetwarza drugi `^GF` z opóźnieniem;
      LPD job z 2×`^XA` — drugi label czeka; może `^XB`/`^PQ`/separator między etykietami; może trzeba
      SŁAĆ KAŻDĄ ETYKIETĘ OSOBNYM jobem (lp per label) zamiast jednego strumienia 2×`^XA`. **To główny
      problem do rozwiązania.** Workaround tymczasowy: większy `confirm_timeout_sec` (np. 60→120/label) +
      klient Laravel timeout > agent.
- [ ] 🇵🇱 **Laravel L1: default `label_format = PDF` dla rynku PL** (ZPL gubi diakrytyki → błędny adres).
- [ ] **Status: klient `status.cgi`** jako autorytatywne health (pkt 1); `~HS` uzupełnia (head-open linia 2).
- [ ] Recovery filar 3: integracja `func=reset` (pkt 2) po fault+fix.
- [ ] Agent E2E: self-update (`/admin/update`) — niesprawdzone.
- [ ] `/codex:review` agenta po zmianach.
