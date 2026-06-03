# Spike sprzętowy XP-423B (do wykonania przed produkcją)

Środowisko: VM/LXC Proxmox (Debian 13) + CUPS + XP-423B `socket://<ip>:9100`, **raw queue** (bez PPD), tryb ZPL.

## Potwierdzone z dokumentacji producenta (2026-06-03) — NIE do testowania, tło

Trzy artefakty producenta (manual PL „Instalacja XP-423B Windows", `Linux_SDK_2.0.4`, sterownik CUPS
`cupsdrv-2.4.0`) potwierdzają fundamenty designu:

- **Połączenie = surowy TCP `socket:9100`** — Windows: „Standard TCP/IP Port → RAW, Port 9100 → HP JetDirect,
  **SNMP: Nie**". SDK: `OpenPort("NET,<ip>")`. → brak kanału zwrotnego SNMP → status MUSI iść przez `~HS`,
  nie przez `printer-state-reasons` CUPS (potwierdza nasz pivot).
- **ZPL natywnie** — firmware „1.032 EZD", zakładka „Z" w Diagnostic Tool, dedykowane ZPL SDK. Prawdopodobna
  auto-detekcja języka ze strumienia (`^XA` → tryb ZPL); do potwierdzenia testem niżej.
- **`~HS` to oficjalny mechanizm statusu** — SDK `ZPL_HostStatusReturn()` = komenda `~HS`; `ZPL_GetPrinterStatus()`
  zwraca int-bitmask z `ParseStatus()`:
  | bit | znaczenie |   | bit | znaczenie |
  |---|---|---|---|---|
  | `0b0000001` | głowica otwarta | | `0b0010000` | pauza |
  | `0b0000010` | zacięcie (jam) |  | `0b0100000` | drukowanie |
  | `0b0000100` | brak papieru |    | `0b1000000` | pokrywa otwarta |
  | `0b0001000` | brak ribbonu |    | `0` | OK |
  To **kanoniczny cel** dla `hoststatus.go` (obecnie parsujemy tylko paper/pause).
- **203 dpi / 1-bit** — vendor PPD: `DefaultResolution 203dpi`, `cupsBitsPerColor 1`. Nasz render PDF→^GF
  @203dpi + threshold pasuje do natywnej rozdzielczości.
- **Etykieta 4×6" = 101,6×152,4 mm** (@203dpi ≈ **812×1218 dots**) — manual + PPD `PageSize w4h6`.
  Config zaktualizowany: `label_width_mm=102`, `label_height_mm=152`.
- **`NET` dwukierunkowy** — SDK czyta odpowiedź po `NET` → socket:9100 obsługuje odczyt (`~HS` wykonalne).

## Kroki setupu drukarki (jednorazowo, magazynier, Windows/USB)

1. Statyczny IP: **Diagnostic Tool** (`xprintertech.com/test-tool`, Windows, USB) → „Ethernet Setup" →
   Static IP (IP/maska/brama) → „Set IP". Konfiguracja sieci **przeżywa factory reset**.
2. Kalibracja czujnika: Diagnostic Tool → „Calibrate Sensor", Media Type = **Gap** (etykiety die-cut 4×6).
   Alternatywnie sprzętowo przyciskiem (manual, sekcja kalibracja).
3. Na VM: `lpadmin -p xp423b -E -v socket://<ip>:9100 -o raw` (raw queue, bez PPD — patrz `install-debian.sh`).

## Pozostałe EMPIRYCZNE niewiadome (do potwierdzenia na sprzęcie)

Tylko dwie realne — reszta ugruntowana wyżej.

### A. Dokładny format odpowiedzi `~HS` na TYM egzemplarzu (kalibracja parsera)
1. [ ] `printf '~HS' | nc <ip> 9100 | xxd` — zapisz surową odpowiedź (bajty/linie/ETX/CR-LF).
2. [ ] **Cross-check SDK (ground-truth):** zbuduj i odpal demo ZPL SDK na VM:
       `cd Linux_SDK_2.0.4/ZPL && g++ sample/*.cpp -Ilib -Llib -lPrinterSDK -o demozpl && LD_LIBRARY_PATH=lib ./demozpl`
       → wybierz `2.NET`, podaj IP, `1.Get Status`. Porównaj zdekodowany stan (głowica/jam/papier/ribbon/
       pauza/druk/pokrywa) z surowym `~HS` z kroku 1 → ustal mapping bajt→bit.
3. [ ] Skoryguj `internal/printer/hoststatus.go::ParseHostStatus` do realnego formatu i **rozszerz HostStatus
       o 7 stanów SDK** (HeadOpen/PaperJam/PaperOut/RibbonOut/Paused/Printing/CoverOpen); `Healthy()` =
       brak {HeadOpen, PaperJam, PaperOut, RibbonOut, CoverOpen} (pauza blokuje, printing=transient/ok).
       Zamyka MED #10 (obecnie HeadOpen/BufferFull niegate'owane bo nieparsowane).
4. [ ] Jeśli `~HS` NIE działa (mało prawdopodobne wg SDK) → agent zostaje w trybie degrade (job-state=9
       best-effort); udokumentuj.

### B. Skanowalność kodów kreskowych naszego renderu
5. [ ] Wydrukuj realną etykietę Allegro w **ZPL (passthrough)** ORAZ w **PDF (render→^GF @203dpi threshold)**.
       Zeskanuj kody czytnikiem. Oba czytelne?
6. [ ] Jeśli ^GF PDF zawodzi → **ostateczny fallback**: osobna kolejka CUPS z vendor PPD (`XP-420B.ppd`,
       cups-raster) dla ścieżki PDF. UWAGA: to właśnie ostrzegana ścieżka raster/dithering — tylko last-resort,
       sprawdź skanowalność zanim wdrożysz.
7. [ ] ZPL auto-detect: `printf '^XA^FO50,50^A0N,40,40^FDTEST^FS^XZ' | nc <ip> 9100` → drukuje etykietę
       „TEST" bez ręcznego przełączania trybu? (potwierdza, że EZD auto-rozpoznaje ZPL).
8. [ ] Allegro A6→A4 (allegro-api#10120) — `getLabel` w formacie PDF: czy realny MediaBox to A6 (≈297×420 pt)?
       Loguj wymiar; guard #15 odrzuca strony >1.4× szerokości (102mm) jako podejrzenie A4.

## Cykl operacyjny + idempotencja + self-update (potwierdzenie end-to-end)
9.  [ ] Submit ZPL → `printed`; wyłącz papier → `PRINTER_OUT_OF_PAPER`; włóż papier → health `down→ok`.
10. [ ] Retransmisja tego samego `Idempotency-Key` (`pj:{id}`) → **brak 2. wydruku** (resume-by-key/replay).
11. [ ] Multi-parcel: PDF/ZPL wielostronicowy → każda paczka osobna etykieta (render per strona).
12. [ ] Self-update: `POST /api/v1/admin/update` z prawdziwym tagiem → restart + `/health` version bump
        (sha256 obowiązkowe — fail-closed gdy brak/niezgodny).

## Przed pierwszym releasem
13. [ ] `/codex:review` całego agenta (po kalibracji `~HS`/rozmiaru z punktów A/B).
