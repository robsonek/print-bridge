package server

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robsonek/print-bridge/internal/printer"
	"github.com/robsonek/print-bridge/internal/update"
)

// Od v0.9.0 koperta błędu na drucie nie niesie wyjścia narzędzi ani tekstu
// systemów zewnętrznych (spec 2026-10-03-message-bez-wyjscia-narzedzi, §5):
// surowe body odpowiedzi Router() bez znacznika, kod i HTTP bez zmian,
// znacznik w logu agenta. Uzupełnia testy miejsc w internal/printer.

func captureServerLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

// assertWireLeak: W1 na drucie — status, pełna koperta (code + message, bez
// details), znacznik ani w surowym body, ani nieobecny w logu.
func assertWireLeak(t *testing.T, w *contractWorld, c contractCase, status int, code, msg, marker string, logs *bytes.Buffer) {
	t.Helper()
	rec := w.do(w.router(), c)
	if rec.Code != status {
		t.Errorf("HTTP %d, want %d", rec.Code, status)
	}
	if strings.Contains(rec.Body.String(), marker) {
		t.Errorf("znacznik %q w body: %s", marker, rec.Body.String())
	}
	got := decodeContractBody(t, rec.Body.Bytes())
	if want := envelope(code, msg); jsonString(got) != jsonString(want) {
		t.Errorf("koperta = %s\n     want %s", jsonString(got), jsonString(want))
	}
	if !strings.Contains(logs.String(), marker) {
		t.Errorf("znacznika %q brak w logu agenta: %q", marker, logs.String())
	}
}

// T18: 422 update — message to tekst SENTINELA, nie err (opakowanie niesie
// tekst spoza listy stałych).
func TestLeakUpdateRejectedWrappedSentinel(t *testing.T) {
	for _, sentinel := range []error{update.ErrInvalidTag, update.ErrInvalidInstance} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			logs := captureServerLog(t)
			w := newContractWorld(t)
			w.updater = func(string) error { return fmt.Errorf("SEKRET-TAG /opt/print-bridge: %w", sentinel) }
			assertWireLeak(t, w, updateCase("update-opakowany-sentinel", `{"tag":"v1.2.3"}`),
				http.StatusUnprocessableEntity, "INVALID_REQUEST", sentinel.Error(), "SEKRET-TAG", logs)
		})
	}
}

// sekretPDF: PDF, którego słownik /Encrypt wskazuje nieznany handler — pdfinfo
// cytuje jego nazwę („Couldn't find the 'SEKRET-BODY' security handler”).
func sekretPDF() []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 288 432] >>",
		"<< /Filter /SEKRET-BODY /V 1 /R 2 /O (x) /U (x) /P -4 >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Encrypt 4 0 R /ID [<00><00>] >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

// T19: przypadek z sondy Codexa na PRAWDZIWYM pdfinfo (CI instaluje
// poppler-utils).
func TestLeakRealPDFInfoSecurityHandler(t *testing.T) {
	if _, err := exec.LookPath("pdfinfo"); err != nil {
		t.Skip("pdfinfo niedostępne")
	}
	logs := captureServerLog(t)
	w := newContractWorld(t)
	w.render = printer.NewPDFRenderer(printer.RenderOptions{WidthMM: 102, Threshold: 190, RenderWidthDots: 832, PrintWidthDots: 832})
	c := printCase("pdf-sekret-body")
	c.body = `{"pdf_base64":"` + base64.StdEncoding.EncodeToString(sekretPDF()) + `"}`
	assertWireLeak(t, w, c, http.StatusUnprocessableEntity, "INVALID_PDF", "PDF render failed: pdfinfo exited with code 1", "SEKRET-BODY", logs)
}

// T20: prawdziwy CUPSClient.Submit z fałszywym lp jako jedynym wpisem PATH.
func TestLeakLPOutputOnWire(t *testing.T) {
	logs := captureServerLog(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lp"), []byte("#!/bin/sh\nprintf 'lp: SEKRET-LP scheduler not responding\\n' >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	w := newContractWorld(t)
	w.sub = printer.NewCUPSClient("q")
	assertWireLeak(t, w, printCase("lp-sekret"), http.StatusServiceUnavailable, "CUPS_UNAVAILABLE", "lp submit failed: lp exited with code 2", "SEKRET-LP", logs)
}

// T21 (W3): stan panelu po resecie zostaje w details.panel_state (decyzja A),
// znika z message.
func TestLeakPanelFaultStateOnWire(t *testing.T) {
	logs := captureServerLog(t)
	w := newContractWorld(t)
	w.panel.status = []panelPage{panelHTML("greentext", "Ready"), panelHTML("redtext", "SEKRET-STATE")}
	rec := w.do(w.router(), resetCase("reset-fault-sekret"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("HTTP %d, want 503", rec.Code)
	}
	got := decodeContractBody(t, rec.Body.Bytes())
	want := envelopeDetails("PRINTER_OFFLINE", panelFaultMsg, map[string]any{"panel_state": "SEKRET-STATE"})
	if jsonString(got) != jsonString(want) {
		t.Errorf("koperta = %s\n     want %s", jsonString(got), jsonString(want))
	}
	if !strings.Contains(logs.String(), "SEKRET-STATE") {
		t.Errorf("stanu panelu brak w logu agenta: %q", logs.String())
	}
}
