# Kontrakt błędów print-bridge

Body: `{ "code": "...", "message": "...", "details": {...} }`. Klient mapuje po **kodzie**.

| Kod | Retry | HTTP |
|-----|-------|------|
| CUPS_UNAVAILABLE | tak | 503 |
| PRINTER_OFFLINE | tak | 503 |
| PRINTER_OUT_OF_PAPER | tak | 503 |
| QUEUE_PAUSED | tak | 503 |
| PRINT_TIMEOUT | tak | 503 |
| BRIDGE_RESTARTING | tak | 503 |
| INVALID_PDF | nie | 422 |
| INVALID_ZPL | nie | 422 |
| UNSUPPORTED_FORMAT | nie | 422 |
| INVALID_REQUEST | nie | 400 |
| MISSING_TOKEN | nie | 401 |
| FORBIDDEN | nie | 403 |
| PRINTER_BUSY | tak | 409 |
| PRINT_UNCONFIRMED | **nie** | 409 |

`printed` = `job-state=9` + `~HS` zdrowy + bufor/batch zdrenowany (flaga batcha
linii 2 ~HS = 0) — na szczęśliwej ścieżce oznacza to fizyczne wyjście ostatniej
etykiety (zweryfikowane na sprzęcie).

**Wyjątek (nieobserwowalność po faulcie):** gdy job został przerwany faultem
sprzętowym (`PRINTER_OUT_OF_PAPER`), fizyczny wynik jest niepoznawalny — przy
recovery medium print-server potrafi ODRZUCIĆ zbuforowany format (zmierzone
2026-06-07), a w innych gałęziach (reset/wznowienie) ten sam sygnał oznacza
wydrukowanie. Retry tym samym `Idempotency-Key` zwraca wtedy **PRINT_UNCONFIRMED
(409, bez retry!)** z `details.original_fault` i `details.cups_job_id`. UI musi
zapytać człowieka: „etykieta wyszła?" → [potwierdź] (zamknij job po stronie
klienta) / [dodrukuj] (NOWY `Idempotency-Key`). Automatyczny retry ani resubmit
tym samym kluczem nigdy nie rozwiążą tego stanu.

`PRINTER_BUSY` (409, retry tak): reset drukarki odrzucony, bo trwa druk —
ponów po zakończeniu batcha.
