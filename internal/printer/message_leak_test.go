package printer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenPrinting/goipp"
	"github.com/robsonek/print-bridge/internal/apierr"
)

// Od v0.9.0 message koperty nie niesie wyjścia narzędzi (pdfinfo, pdftoppm,
// lp) ani tekstu systemów zewnętrznych (IPP, sieć, panel, ~HS) — pełny błąd
// idzie tylko do logu agenta (spec 2026-10-03-message-bez-wyjscia-narzedzi,
// §5). Każdy test wstrzykuje tekst z zewnątrz przez PRAWDZIWE źródło
// (fałszywe narzędzie w PATH, httptest, RoundTripper) i sprawdza jeden z
// wariantów asercji:
//   - W1: znacznika nie ma w kopercie (JSON = to, co pisze writeError), kod i
//     HTTP bez zmian, message równy literałowi, znacznik jest w logu;
//   - W2: tekstu z zewnątrz nie da się oznaczyć — log zawiera DOKŁADNY Error()
//     źródła;
//   - W3: details.panel_state zostaje (decyzja lidera A) — znacznik tylko w
//     details, nie w message.

// captureLog przechwytuje globalny log na czas testu (żaden test repo nie
// używa t.Parallel).
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

// fakeTools podstawia katalog ze skryptami jako JEDYNY wpis PATH — prawdziwe
// pdfinfo/pdftoppm/lp hosta nie są osiągalne. Skrypty używają wyłącznie
// wbudowanych poleceń sh (printf, exit, for), bo PATH nie ma /bin.
func fakeTools(t *testing.T, scripts map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// lastArg: ostatni argument skryptu (prefiks wyjścia pdftoppm).
const lastArg = `for a; do last=$a; done; `

const (
	pdfinfoOnePage   = `printf 'Pages:          1\nPage    1 size: 288 x 432 pts\nPage    1 rot:  0\n'`
	pdfinfoOnePageQT = `printf 'Pages:          1\nPage    1 size: 288 x 432 pts\nPage    1 rot:  90\n'`
)

type errTransport struct{ err error }

func (e errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

func leakRenderer() *PDFRenderer {
	return NewPDFRenderer(RenderOptions{WidthMM: 102, Threshold: 190, RenderWidthDots: 832, PrintWidthDots: 832})
}

func printPDF(t *testing.T, r Renderer) *apierr.Error {
	t.Helper()
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hsOK: true}
	p := &Printer{Reach: f, Sub: f, Poll: f, Probe: f, Render: r, ConfirmTimeoutPolls: 3}
	_, e := p.Print(context.Background(), []byte("%PDF-1.4 etykieta"), 1)
	return e
}

func wantEnvelope(t *testing.T, e *apierr.Error, code apierr.Code, status int, msg string) {
	t.Helper()
	if e == nil {
		t.Fatalf("want %s %d, got nil", code, status)
	}
	if e.Code != code || e.HTTPStatus != status {
		t.Errorf("koperta = %s %d, want %s %d", e.Code, e.HTTPStatus, code, status)
	}
	if e.Message != msg {
		t.Errorf("message = %q\n      want %q", e.Message, msg)
	}
}

func envelopeJSON(t *testing.T, e *apierr.Error) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertW1: znacznik wstrzyknięty w tekst z zewnątrz.
func assertW1(t *testing.T, e *apierr.Error, logs *bytes.Buffer, code apierr.Code, status int, msg, marker string) {
	t.Helper()
	wantEnvelope(t, e, code, status, msg)
	if body := envelopeJSON(t, e); strings.Contains(body, marker) {
		t.Errorf("znacznik %q w kopercie: %s", marker, body)
	}
	if !strings.Contains(logs.String(), marker) {
		t.Errorf("znacznika %q brak w logu agenta: %q", marker, logs.String())
	}
}

// assertW2: log zawiera dokładny Error() źródła (cała reszta linii logu).
func assertW2(t *testing.T, e *apierr.Error, logs *bytes.Buffer, code apierr.Code, status int, msg, wantErr string) {
	t.Helper()
	wantEnvelope(t, e, code, status, msg)
	if !strings.Contains(logs.String(), ": "+wantErr+"\n") {
		t.Errorf("log agenta bez pełnego błędu źródła %q: %q", wantErr, logs.String())
	}
}

// --- E1: render PDF (print.go, pdfrender.go) ---

func TestLeakPDFInfoOutput(t *testing.T) { // T1
	logs := captureLog(t)
	fakeTools(t, map[string]string{"pdfinfo": `printf "Syntax Error: Couldn't find the 'SEKRET-BODY' security handler\n" >&2; exit 1`})
	assertW1(t, printPDF(t, leakRenderer()), logs, apierr.CodeInvalidPDF, 422,
		"PDF render failed: pdfinfo exited with code 1", "SEKRET-BODY")
}

func TestLeakPDFToPPMOutput(t *testing.T) { // T2
	logs := captureLog(t)
	fakeTools(t, map[string]string{
		"pdfinfo":  pdfinfoOnePage,
		"pdftoppm": `printf 'SEKRET-PPM\n' >&2; exit 99`,
	})
	assertW1(t, printPDF(t, leakRenderer()), logs, apierr.CodeInvalidPDF, 422,
		"PDF render failed: pdftoppm exited with code 99", "SEKRET-PPM")
}

func TestLeakPDFToPPMPerPageOutput(t *testing.T) { // T3
	logs := captureLog(t)
	fakeTools(t, map[string]string{
		"pdfinfo":  pdfinfoOnePageQT,
		"pdftoppm": `printf 'SEKRET-PPMPAGE\n' >&2; exit 99`,
	})
	assertW1(t, printPDF(t, leakRenderer()), logs, apierr.CodeInvalidPDF, 422,
		"PDF render failed: pdftoppm exited with code 99 (page 1)", "SEKRET-PPMPAGE")
}

// T4, T4a–T4e: literały agenta (z liczbami) zostają w message; W2.
func TestLeakRenderOwnLiterals(t *testing.T) {
	cases := []struct {
		name, test string
		tools      map[string]string
		msg        string
		wantErr    string
	}{
		{"mediabox-a4", "T4", map[string]string{"pdfinfo": `printf 'Pages:          1\nPage    1 size: 595 x 842 pts\nPage    1 rot:  0\n'`},
			"PDF render failed: PDF page 1 is 210x297mm, exceeding the 102mm roll in both orientations — MediaBox likely A4 not A6 (allegro-api#10120)",
			"PDF page 1 is 210x297mm, exceeding the 102mm roll in both orientations — MediaBox likely A4 not A6 (allegro-api#10120)"},
		{"brak-page-size", "T4a", map[string]string{"pdfinfo": `printf 'Pages:          1\n'`},
			"PDF render failed: pdfinfo: no Page size (invalid PDF?)", "pdfinfo: no Page size (invalid PDF?)"},
		{"liczba-stron", "T4b", map[string]string{"pdfinfo": `printf 'Pages:          2\nPage    1 size: 288 x 432 pts\n'`},
			"PDF render failed: pdfinfo reports 2 pages but enumerated 1 (invalid PDF?)", "pdfinfo reports 2 pages but enumerated 1 (invalid PDF?)"},
		{"brak-png", "T4c", map[string]string{"pdfinfo": pdfinfoOnePage, "pdftoppm": `exit 0`},
			"PDF render failed: pdftoppm produced no png", "pdftoppm produced no png"},
		{"dwa-png-na-strone", "T4d", map[string]string{"pdfinfo": pdfinfoOnePageQT,
			"pdftoppm": lastArg + `printf x > "$last-1.png"; printf x > "$last-2.png"; exit 0`},
			"PDF render failed: pdftoppm produced 2 pngs for page 1, want 1", "pdftoppm produced 2 pngs for page 1, want 1"},
		{"raster-nie-png", "T4e", map[string]string{"pdfinfo": pdfinfoOnePage,
			"pdftoppm": lastArg + `printf 'SEKRET-RASTER' > "$last-1.png"; exit 0`},
			"PDF render failed: raster decode failed", "decode out-1.png: image: unknown format"},
	}
	for _, c := range cases {
		t.Run(c.test+"-"+c.name, func(t *testing.T) {
			logs := captureLog(t)
			fakeTools(t, c.tools)
			assertW2(t, printPDF(t, leakRenderer()), logs, apierr.CodeInvalidPDF, 422, c.msg, c.wantErr)
		})
	}
}

// T4f: fallback na PRAWDZIWYM źródle — os.MkdirTemp z TMPDIR ze znacznikiem.
func TestLeakRenderTempDirFallback(t *testing.T) {
	logs := captureLog(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "SEKRET-TMP", "nie-ma"))
	assertW1(t, printPDF(t, leakRenderer()), logs, apierr.CodeInvalidPDF, 422, "PDF render failed", "SEKRET-TMP")
}

// T5: fallback — błąd bez tekstu publicznego (atrapa renderera).
func TestLeakRenderUnknownErrorFallback(t *testing.T) {
	logs := captureLog(t)
	assertW1(t, printPDF(t, &fakeBackend{renderErr: errors.New("SEKRET-RENDER")}), logs,
		apierr.CodeInvalidPDF, 422, "PDF render failed", "SEKRET-RENDER")
}

// --- E2: lp (print.go, cups.go) ---

func submitViaLP(t *testing.T) *apierr.Error {
	t.Helper()
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hsOK: true}
	p := &Printer{Reach: f, Sub: NewCUPSClient("q"), Poll: f, Probe: f, Render: f, ConfirmTimeoutPolls: 3}
	_, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	return e
}

func TestLeakLPOutput(t *testing.T) { // T6
	logs := captureLog(t)
	fakeTools(t, map[string]string{"lp": `printf 'lp: SEKRET-LP scheduler not responding\n' >&2; exit 2`})
	assertW1(t, submitViaLP(t), logs, apierr.CodeCUPSUnavailable, 503, "lp submit failed: lp exited with code 2", "SEKRET-LP")
}

func TestLeakLPOutputWithoutJobID(t *testing.T) { // T7
	logs := captureLog(t)
	fakeTools(t, map[string]string{"lp": `printf 'SEKRET-LPOUT\n'; exit 0`})
	assertW1(t, submitViaLP(t), logs, apierr.CodeCUPSUnavailable, 503, "lp submit failed: no job id in lp output", "SEKRET-LPOUT")
}

// T7a: fallback na prawdziwym źródle — strconv.Atoi cytuje cyfry z wyjścia lp.
func TestLeakLPJobIDOverflowFallback(t *testing.T) {
	logs := captureLog(t)
	const digits = "91827364550918273645509182736455"
	fakeTools(t, map[string]string{"lp": `printf 'request id is q-` + digits + ` (0 file(s))\n'; exit 0`})
	assertW1(t, submitViaLP(t), logs, apierr.CodeCUPSUnavailable, 503, "lp submit failed", digits)
}

// --- E3: IPP (print.go, cups.go) ---

func pollVia(t *testing.T, c *CUPSClient) *apierr.Error {
	t.Helper()
	f := &fakeBackend{reachable: true, hsOK: true}
	p := &Printer{Reach: f, Sub: f, Poll: c, Probe: f, Render: f, ConfirmTimeoutPolls: 3}
	_, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	return e
}

// T8: znacznik TYLKO w przyczynie błędu transportu (URL neutralny).
func TestLeakIPPTransportCause(t *testing.T) {
	logs := captureLog(t)
	c := &CUPSClient{queue: "q", ippURL: "http://cups.test/printers/q", httpc: &http.Client{Transport: errTransport{errors.New("SEKRET-DIAL")}}}
	assertW1(t, pollVia(t, c), logs, apierr.CodeCUPSUnavailable, 503, "job poll failed: IPP transport error", "SEKRET-DIAL")
}

// T8b: znacznik TYLKO w URL (url.Error cytuje URL żądania).
func TestLeakIPPTransportURL(t *testing.T) {
	logs := captureLog(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c := &CUPSClient{queue: "q", ippURL: srv.URL + "/printers/SEKRET-Q", httpc: &http.Client{Timeout: 2 * time.Second}}
	assertW1(t, pollVia(t, c), logs, apierr.CodeCUPSUnavailable, 503, "job poll failed: IPP transport error", "SEKRET-Q")
}

// T8a: fallback na prawdziwym źródle — NewRequestWithContext cytuje zły URL.
func TestLeakIPPBadURLFallback(t *testing.T) {
	logs := captureLog(t)
	c := &CUPSClient{queue: "q", ippURL: "http://cups.test/printers/SEKRET-URL\x7f", httpc: &http.Client{Timeout: 2 * time.Second}}
	assertW1(t, pollVia(t, c), logs, apierr.CodeCUPSUnavailable, 503, "job poll failed", "SEKRET-URL")
}

// T9: dekoder goipp — body bez kształtu IPP.
func TestLeakIPPDecode(t *testing.T) {
	logs := captureLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SEKRET-IPP <html>nie IPP</html>"))
	}))
	t.Cleanup(srv.Close)
	c := &CUPSClient{queue: "q", ippURL: srv.URL, httpc: &http.Client{Timeout: 2 * time.Second}}
	e := pollVia(t, c)
	var m goipp.Message
	decErr := m.DecodeBytes([]byte("SEKRET-IPP <html>nie IPP</html>"))
	if decErr == nil {
		t.Fatal("sonda: dekoder goipp przyjął śmieci")
	}
	assertW2(t, e, logs, apierr.CodeCUPSUnavailable, 503, "job poll failed: invalid IPP response", decErr.Error())
}

func TestLeakIPPStatus(t *testing.T) { // T10
	logs := captureLog(t)
	assertW2(t, pollVia(t, ippStatusServer(t, goipp.StatusErrorForbidden)), logs, apierr.CodeCUPSUnavailable, 503,
		"job poll failed: IPP error 0x0401", "IPP error 0x0401 client-error-forbidden")
}

func TestLeakIPPNoJobState(t *testing.T) { // T10a
	logs := captureLog(t)
	assertW2(t, pollVia(t, ippStatusServer(t, goipp.StatusOk)), logs, apierr.CodeCUPSUnavailable, 503,
		"job poll failed: job-state not found in IPP response", "job-state not found in IPP response")
}

// --- E5: brak odpowiedzi ~HS w budżecie (print.go) ---

func TestLeakHSProbeError(t *testing.T) { // T12
	logs := captureLog(t)
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hsErr: errors.New("dial tcp 192.0.2.75:9100: SEKRET-HSERR")}
	_, e := newPrinter(f).Print(context.Background(), []byte("^XA^XZ"), 1)
	assertW1(t, e, logs, apierr.CodePrinterOffline, 503, "printer unreachable during ~HS verification", "SEKRET-HSERR")
}

// --- E6/E7/E8: panel (reset.go, webpanel.go) ---

func resetVia(t *testing.T, panel PanelAPI) *apierr.Error {
	t.Helper()
	r := &PrinterResetter{Panel: panel, Probe: &fakeBackend{hsOK: true}, MaxPolls: 3}
	_, e := r.Reset(context.Background())
	return e
}

func panelPageHTML(class, state string) string {
	return `<HTML><BODY><TABLE><TR><TD class=` + class + `>` + state + `</TD></TR></TABLE></BODY></HTML>`
}

// T13: znacznik TYLKO w przyczynie błędu transportu panelu (URL neutralny).
func TestLeakPanelTransportCause(t *testing.T) {
	logs := captureLog(t)
	p := &WebPanel{BaseURL: "http://panel.test", HTTPC: &http.Client{Transport: errTransport{errors.New("SEKRET-PANELDIAL")}}}
	assertW1(t, resetVia(t, p), logs, apierr.CodePrinterOffline, 503,
		"panel drukarki (status.cgi) niedostępny: brak połączenia z panelem", "SEKRET-PANELDIAL")
}

// T13b: znacznik TYLKO w URL panelu.
func TestLeakPanelTransportURL(t *testing.T) {
	logs := captureLog(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	p := &WebPanel{BaseURL: srv.URL + "/SEKRET-PANEL", HTTPC: &http.Client{Timeout: 2 * time.Second}}
	assertW1(t, resetVia(t, p), logs, apierr.CodePrinterOffline, 503,
		"panel drukarki (status.cgi) niedostępny: brak połączenia z panelem", "SEKRET-PANEL")
}

// T13a: fallback na prawdziwym źródle — zły BaseURL cytowany przez parser URL.
func TestLeakPanelBadURLFallback(t *testing.T) {
	logs := captureLog(t)
	p := &WebPanel{BaseURL: "http://panel.test/SEKRET-BASEURL\x7f"}
	assertW1(t, resetVia(t, p), logs, apierr.CodePrinterOffline, 503, "panel drukarki (status.cgi) niedostępny", "SEKRET-BASEURL")
}

func TestLeakPanelHTTPStatus(t *testing.T) { // T14
	logs := captureLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "SEKRET-503", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	e := resetVia(t, &WebPanel{BaseURL: srv.URL})
	assertW2(t, e, logs, apierr.CodePrinterOffline, 503, "panel drukarki (status.cgi) niedostępny: HTTP 503", "panel /cgi-bin/status.cgi: HTTP 503")
	if body := envelopeJSON(t, e); strings.Contains(body, "SEKRET-503") {
		t.Errorf("body panelu w kopercie: %s", body)
	}
}

func TestLeakPanelBodyRead(t *testing.T) { // T14a
	logs := captureLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("0123456789"))
	}))
	t.Cleanup(srv.Close)
	assertW2(t, resetVia(t, &WebPanel{BaseURL: srv.URL}), logs, apierr.CodePrinterOffline, 503,
		"panel drukarki (status.cgi) niedostępny: przerwany odczyt odpowiedzi panelu", "unexpected EOF")
}

// T15: fallback — błąd panelu bez tekstu publicznego (atrapa).
func TestLeakPanelUnknownErrorFallback(t *testing.T) {
	logs := captureLog(t)
	p := &hookPanel{status: func(int) (PanelState, error) { return PanelState{}, errors.New("SEKRET-PANELERR") }}
	assertW1(t, resetVia(t, p), logs, apierr.CodePrinterOffline, 503, "panel drukarki (status.cgi) niedostępny", "SEKRET-PANELERR")
}

// funcResetTransport: status.cgi → Ready, function.cgi → błąd transportu.
type funcResetTransport struct{ err error }

func (f funcResetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/admin/cgi-bin/function.cgi" {
		return nil, f.err
	}
	body := panelPageHTML("greentext", "Ready")
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestLeakFuncResetTransport(t *testing.T) { // T16
	logs := captureLog(t)
	p := &WebPanel{BaseURL: "http://panel.test", HTTPC: &http.Client{Transport: funcResetTransport{errors.New("SEKRET-RESET")}}}
	assertW1(t, resetVia(t, p), logs, apierr.CodePrinterOffline, 503,
		"func=reset nie powiódł się: brak połączenia z panelem", "SEKRET-RESET")
}

// T17 (W3): stan panelu po resecie zostaje w details.panel_state (decyzja A),
// ale znika z message.
func TestLeakPanelFaultStateOnlyInDetails(t *testing.T) {
	logs := captureLog(t)
	p := &hookPanel{status: func(call int) (PanelState, error) {
		if call == 0 {
			return PanelState{State: "Ready", Green: true, Known: true}, nil
		}
		return PanelState{State: "SEKRET-STATE", Known: true}, nil
	}}
	e := resetVia(t, p)
	wantEnvelope(t, e, apierr.CodePrinterOffline, 503, "po resecie panel raportuje fault (stan w details.panel_state)")
	if strings.Contains(e.Message, "SEKRET-STATE") {
		t.Errorf("stan panelu w message: %q", e.Message)
	}
	if len(e.Details) != 1 || e.Details["panel_state"] != "SEKRET-STATE" {
		t.Errorf("details = %v, want {panel_state: SEKRET-STATE}", e.Details)
	}
	if !strings.Contains(logs.String(), "SEKRET-STATE") {
		t.Errorf("stanu panelu brak w logu agenta: %q", logs.String())
	}
}

// --- T23: ciągłość Error() źródeł (log agenta, pola health) — pełna równość ---

func TestSourceErrorTextUnchanged(t *testing.T) {
	ctx := context.Background()

	_, err := ippStatusServer(t, goipp.StatusErrorForbidden).PrinterReasons(ctx)
	if got, want := errText(err), "IPP error 0x0401 client-error-forbidden"; got != want {
		t.Errorf("IPP status: Error() = %q, want %q", got, want)
	}

	ipp := &CUPSClient{queue: "q", ippURL: "http://cups.test/printers/q", httpc: &http.Client{Transport: errTransport{errors.New("SEKRET-DIAL")}}}
	_, err = ipp.PrinterReasons(ctx)
	if got, want := errText(err), `Post "http://cups.test/printers/q": SEKRET-DIAL`; got != want {
		t.Errorf("IPP transport: Error() = %q, want %q", got, want)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	_, err = (&WebPanel{BaseURL: srv.URL}).Status(ctx)
	if got, want := errText(err), "panel /cgi-bin/status.cgi: HTTP 503"; got != want {
		t.Errorf("panel HTTP: Error() = %q, want %q", got, want)
	}

	panel := &WebPanel{BaseURL: "http://panel.test", HTTPC: &http.Client{Transport: errTransport{errors.New("SEKRET-PANELDIAL")}}}
	_, err = panel.Status(ctx)
	if got, want := errText(err), `Get "http://panel.test/cgi-bin/status.cgi": SEKRET-PANELDIAL`; got != want {
		t.Errorf("panel transport: Error() = %q, want %q", got, want)
	}
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
