# print-bridge

Agent druku etykiet termicznych (Go) dla XPrinter XP-423B przez CUPS. Companion
dla `the marketplace orchestrator`. Przyjmuje ZPL (passthrough) lub PDF (render→ZPL `^GF`),
drukuje przez raw queue CUPS, weryfikuje fizyczny stan przez ZPL `~HS`.

## Endpointy
- `POST /api/v1/print-jobs` — `X-Print-Token` + `Idempotency-Key`; body `{label_base64|pdf_base64, copies, format?, external_reference?}`.
- `GET  /api/v1/health` — stan drukarki/CUPS + `~HS` + version (200 ok / 503 degraded).
- `POST /api/v1/admin/update` — self-update do tagu.

## Build
`CGO_ENABLED=0 go build ./cmd/print-bridge`

## Deploy (Debian 13)
`sudo ./install-debian.sh <printer_ip> <queue> <egress_cidr>` — patrz `deploy/`.

## Wymagania runtime
- CUPS + `cups-client` (raw queue `socket://ip:9100`)
- `poppler-utils` (`pdftoppm`) dla fallbacku PDF
- drukarka w trybie emulacji ZPL
