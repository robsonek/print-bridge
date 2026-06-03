# Spike sprzętowy XP-423B (do wykonania przed produkcją)

Środowisko: VM/LXC Proxmox (Debian 13) + CUPS + XP-423B `socket://ip:9100`, raw queue, tryb ZPL.

## Do potwierdzenia (każde = TAK/NIE + notatka)
1. [ ] `~HS` wspierane — `printf '~HS' | nc <ip> 9100` zwraca 3 linie statusu Zebra?
       Jeśli NIE → agent działa w trybie degrade (job-state=9 best-effort).
2. [ ] Mapowanie pól `~HS` XP-423B == Zebra (field[1]=paper out, field[2]=pause)?
       Skoryguj `ParseHostStatus` jeśli inny dialekt.
3. [ ] `socket:9100` dwukierunkowy (odczyt po `~HS`)?
4. [ ] Realny rozmiar rolki (104×148 vs 100×150 mm) → ustaw `label_width_mm`/`height_mm`.
5. [ ] **Skanowalność barcode** — wydrukuj realną etykietę Allegro w ZPL (passthrough)
       ORAZ w PDF (render→^GF), zeskanuj kody czytnikiem. Oba czytelne?
6. [ ] Allegro A6→A4 (allegro-api#10120) — czy realny PDF ma MediaBox A6? Loguj wymiar.
7. [ ] Pełny cykl: submit ZPL → `printed`; wyłącz papier → `PRINTER_OUT_OF_PAPER`;
       włącz → health `down→ok`.
8. [ ] Idempotency: retransmisja tego samego `Idempotency-Key` → brak 2. wydruku.
9. [ ] Self-update: `POST /admin/update` z prawdziwym tagiem → restart + `/health` version bump.
10. [ ] `/codex:review` całego agenta przed pierwszym releasem.
