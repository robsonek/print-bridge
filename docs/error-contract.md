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

`printed` = `job-state=9` + `~HS` zdrowy (lub best-effort gdy `~HS` niewspierane).
