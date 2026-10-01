package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robsonek/print-bridge/internal/printer"
	"github.com/robsonek/print-bridge/internal/update"
)

// Testy „golden” kontraktu HTTP (docs/error-contract.md). Każde żądanie idzie
// przez Router() z prawdziwym TokenAuth, a odpowiedzi produkują PRAWDZIWE
// printer.Printer, printer.PrinterResetter (z printer.WebPanel) i
// update.SpawnUpdater nad atrapami CUPS, ~HS i HTTP panelu — nigdy ręcznie
// złożony apierr.Error. Dzięki temu golden przypina status i `details`, które
// agent naprawdę emituje. Porównanie: status, Content-Type i CAŁE body po
// zdekodowaniu (number = float64), nie podciągi.

const contractToken = "kontrakt-test-token"

// panelBaseURL: adres z RFC 5737. Żądania nie wychodzą z procesu, bo
// panelTransport podmienia RoundTripper klienta panelu.
const panelBaseURL = "http://192.0.2.10"

// contractEndpoints: ścieżka → skrót z docs/error-contract.md §0.
var contractEndpoints = map[string]string{
	"/api/v1/print-jobs":          "print-jobs",
	"/api/v1/health":              "health",
	"/api/v1/admin/printer-reset": "printer-reset",
	"/api/v1/admin/update":        "update",
}

type hsReply struct {
	hs  printer.HostStatus
	ok  bool
	err error
}

// contractBackend udaje sondę :9100, CUPS (lp + IPP), ~HS i renderer PDF.
// Sekwencje states/hs: kolejne wywołania, ostatni element się powtarza.
type contractBackend struct {
	unreachable bool
	queuePaused bool
	submitErr   error
	renderErr   error
	states      []int
	stateErr    error
	hs          []hsReply
	submits     int
}

func (b *contractBackend) Reachable(context.Context) (bool, error) { return !b.unreachable, nil }
func (b *contractBackend) QueuePaused(context.Context) (bool, error) {
	return b.queuePaused, nil
}
func (b *contractBackend) Submit(context.Context, []byte, int) (int, error) {
	b.submits++
	if b.submitErr != nil {
		return 0, b.submitErr
	}
	return 7, nil
}
func (b *contractBackend) JobState(context.Context, int) (int, error) {
	if b.stateErr != nil {
		return 0, b.stateErr
	}
	s := b.states[0]
	if len(b.states) > 1 {
		b.states = b.states[1:]
	}
	return s, nil
}
func (b *contractBackend) HostStatus(context.Context) (printer.HostStatus, bool, error) {
	r := b.hs[0]
	if len(b.hs) > 1 {
		b.hs = b.hs[1:]
	}
	return r.hs, r.ok, r.err
}
func (b *contractBackend) PDFToZPL(context.Context, []byte) ([]byte, error) {
	if b.renderErr != nil {
		return nil, b.renderErr
	}
	return []byte("^XA^XZ"), nil
}

type panelPage struct {
	code int // 0 = 200
	html string
}

// panelHTML: kształt status.cgi z XP-423B (class bez cudzysłowów, definicje
// CSS ".redtext {" w nagłówku nie mogą matchować parsera).
func panelHTML(class, state string) panelPage {
	return panelPage{html: `<HTML><HEAD><STYLE type=text/css>.redtext  {COLOR:red}.greentext{COLOR:green}</STYLE></HEAD>` +
		`<BODY><TABLE><TR><TD class=` + class + `>` + state + `</TD></TR></TABLE></BODY></HTML>`}
}

// panelTransport udaje HTTP panel print-servera bez sieci: printer.WebPanel
// dostaje odpowiedzi status.cgi i function.cgi i parsuje je naprawdę.
type panelTransport struct {
	status    []panelPage // kolejne odpowiedzi status.cgi; ostatnia się powtarza
	resetCode int         // HTTP dla func=reset; 0 = 200
	onReset   func()      // wołane przy func=reset (np. rozłączenie klienta)
	resets    int
}

func (p *panelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	code, body := http.StatusOK, "OK"
	switch r.URL.Path {
	case "/cgi-bin/status.cgi":
		pg := p.status[0]
		if len(p.status) > 1 {
			p.status = p.status[1:]
		}
		code, body = pg.code, pg.html
		if code == 0 {
			code = http.StatusOK
		}
	case "/admin/cgi-bin/function.cgi":
		p.resets++
		if p.onReset != nil {
			p.onReset()
		}
		if p.resetCode != 0 {
			code = p.resetCode
		}
	default:
		code, body = http.StatusNotFound, ""
	}
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// contractWorld składa agenta jak main.go, tylko z atrapami na brzegach.
type contractWorld struct {
	be         *contractBackend
	store      *memStore
	panel      *panelTransport
	resetProbe *contractBackend
	resetPoll  time.Duration
	updater    func(tag string) error
	spawned    []string

	ctx    context.Context
	cancel context.CancelFunc
}

func newContractWorld() *contractWorld {
	w := &contractWorld{
		be:         &contractBackend{states: []int{printer.JobCompleted}, hs: []hsReply{{ok: true}}},
		store:      newMemStore(),
		panel:      &panelTransport{status: []panelPage{panelHTML("greentext", "Ready")}},
		resetProbe: &contractBackend{hs: []hsReply{{ok: true}}},
	}
	// Domyślnie aktualizator „startuje” (prawdziwy SpawnUpdater wołałby sudo);
	// przypadki 422 podmieniają go na prawdziwy update.SpawnUpdater.
	w.updater = func(tag string) error {
		w.spawned = append(w.spawned, tag)
		return nil
	}
	w.ctx, w.cancel = context.WithCancel(context.Background())
	return w
}

func (w *contractWorld) router() http.Handler {
	p := &printer.Printer{
		Reach: w.be, Sub: w.be, Poll: w.be, Probe: w.be, Render: w.be,
		ConfirmTimeoutPolls: 3,
	}
	rs := &printer.PrinterResetter{
		Panel:        &printer.WebPanel{BaseURL: panelBaseURL, HTTPC: &http.Client{Transport: w.panel}},
		Probe:        w.resetProbe,
		PollInterval: w.resetPoll,
		MaxPolls:     3,
	}
	h := &Handlers{
		Printer: p,
		Store:   w.store,
		KeyLock: NewKeyLock(),
		// health ma własny golden z prawdziwym makeHealth w cmd/print-bridge
		Health:         func(context.Context) (int, any) { return http.StatusOK, map[string]any{"status": "ok"} },
		Resetter:       rs.Reset,
		Updater:        func(tag string) error { return w.updater(tag) },
		ConfirmTimeout: 90 * time.Second,
	}
	return Router(h, contractToken)
}

// realSpawnUpdater: produkcyjny update.SpawnUpdater. logPath w nieistniejącym
// katalogu — jeśli walidacja przejdzie, SpawnUpdater pada na otwarciu logu
// (update.go), zanim cokolwiek uruchomi.
func realSpawnUpdater(w *contractWorld) {
	w.updater = func(tag string) error {
		return update.SpawnUpdater("/nonexistent/update-bridge.sh", "/nonexistent/data/update.log", tag, "")
	}
}

type contractCase struct {
	name   string
	method string
	path   string
	token  string // "" = bez X-Print-Token
	key    string // "" = bez Idempotency-Key
	body   string

	arrange func(w *contractWorld)
	// retry: najpierw to samo żądanie raz (np. fault albo sukces), potem
	// właściwe — tym samym kluczem. beforeRetry zmienia świat między nimi.
	retry       bool
	beforeRetry func(w *contractWorld)

	status int
	want   map[string]any
	// replay: body to zapisane bajty rekordu terminalnego (bez "\n").
	replay bool
	check  func(t *testing.T, w *contractWorld)
}

// oversizedBody: poprawny kształt JSON większy niż maxBodyBytes (budowany raz).
var oversizedBody = sync.OnceValue(func() string {
	return `{"label_base64":"` + strings.Repeat("A", maxBodyBytes+1024) + `"}`
})

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func labelBody(payload string) string { return `{"label_base64":"` + b64(payload) + `"}` }

const zpl = "^XA^XZ"

func printCase(name string) contractCase {
	return contractCase{name: name, method: http.MethodPost, path: "/api/v1/print-jobs", token: contractToken, key: "pj:" + name, body: labelBody(zpl)}
}

func resetCase(name string) contractCase {
	return contractCase{name: name, method: http.MethodPost, path: "/api/v1/admin/printer-reset", token: contractToken}
}

func updateCase(name, body string) contractCase {
	return contractCase{name: name, method: http.MethodPost, path: "/api/v1/admin/update", token: contractToken, body: body}
}

func envelope(code, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}

func envelopeDetails(code, msg string, details map[string]any) map[string]any {
	m := envelope(code, msg)
	m["details"] = details
	return m
}

func with(c contractCase, f func(*contractCase)) contractCase {
	f(&c)
	return c
}

// contractCases: tabela wszystkich odpowiedzi czterech endpointów (health:
// cmd/print-bridge). Źródło prawdy dla docs/error-contract.md §1 i §2.2 —
// sprawdza to contract_doc_test.go.
func contractCases() []contractCase {
	const (
		unconfirmedMsg = "job przerwany faultem sprzętowym — fizyczny wynik niepotwierdzalny; potwierdź wydruk albo dodrukuj nowym Idempotency-Key"
		badTagMsg      = "invalid release tag (expected semver like v1.2.3)"
	)
	printed := map[string]any{"status": "printed", "cups_job_id": "7"}
	cs := []contractCase{
		// --- print-jobs: sukces ---
		with(printCase("swiezy-zpl"), func(c *contractCase) {
			c.status, c.want = 200, printed
			c.check = func(t *testing.T, w *contractWorld) {
				if w.be.submits != 1 {
					t.Errorf("submits = %d, want 1", w.be.submits)
				}
			}
		}),
		with(printCase("swiezy-pdf"), func(c *contractCase) {
			c.body = labelBody("%PDF-1.4 kontrakt")
			c.status, c.want = 200, printed
		}),
		with(printCase("hs-nieobslugiwane-degraduje-do-printed"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.hs = []hsReply{{ok: false}} }
			c.status, c.want = 200, printed
		}),
		with(printCase("resume-po-timeout"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.states = []int{printer.JobProcessing} }
			c.retry = true
			c.beforeRetry = func(w *contractWorld) { w.be.states = []int{printer.JobCompleted} }
			c.status, c.want = 200, printed
			c.check = func(t *testing.T, w *contractWorld) {
				if w.be.submits != 1 {
					t.Errorf("resume nie może wysłać drugi raz: submits = %d", w.be.submits)
				}
			}
		}),
		with(printCase("replay-terminalny"), func(c *contractCase) {
			c.retry = true
			c.status, c.want, c.replay = 200, printed, true
			c.check = func(t *testing.T, w *contractWorld) {
				if w.be.submits != 1 {
					t.Errorf("replay nie może drukować: submits = %d", w.be.submits)
				}
			}
		}),

		// --- print-jobs: INVALID_REQUEST 400 ---
		with(printCase("brak-idempotency-key"), func(c *contractCase) {
			c.key = ""
			c.status, c.want = 400, envelope("INVALID_REQUEST", "Idempotency-Key header required")
		}),
		with(printCase("zly-json"), func(c *contractCase) {
			c.body = "nope"
			c.status, c.want = 400, envelope("INVALID_REQUEST", "invalid JSON body")
		}),
		with(printCase("puste-body"), func(c *contractCase) {
			c.body = ""
			c.status, c.want = 400, envelope("INVALID_REQUEST", "invalid JSON body")
		}),
		with(printCase("body-ponad-20mb"), func(c *contractCase) {
			c.body = oversizedBody()
			c.status, c.want = 400, envelope("INVALID_REQUEST", "invalid JSON body")
		}),
		with(printCase("brak-base64"), func(c *contractCase) {
			c.body = `{}`
			c.status, c.want = 400, envelope("INVALID_REQUEST", "label_base64/pdf_base64 missing or not base64")
		}),
		with(printCase("zly-base64"), func(c *contractCase) {
			c.body = `{"label_base64":"!!"}`
			c.status, c.want = 400, envelope("INVALID_REQUEST", "label_base64/pdf_base64 missing or not base64")
		}),

		// --- print-jobs: BRIDGE_RESTARTING 503 ---
		with(printCase("blad-magazynu"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.store.getErr = errors.New("database is locked") }
			c.status, c.want = 503, envelope("BRIDGE_RESTARTING", "idempotency store unavailable, retry")
		}),
		with(printCase("pending-bez-job-id"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.store.pending[c.key] = pendingRec{job: "not-a-number"} }
			c.status, c.want = 503, envelope("BRIDGE_RESTARTING", "pending job has no resumable cups_job_id")
		}),

		// --- print-jobs: PRINT_UNCONFIRMED 409 (pierwsza próba: brak papieru) ---
		with(printCase("retry-po-braku-papieru"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.hs = []hsReply{{hs: printer.HostStatus{PaperOut: true}, ok: true}} }
			c.retry = true
			c.beforeRetry = func(w *contractWorld) { w.be.hs = []hsReply{{ok: true}} } // zdrowa drukarka niczego nie dowodzi
			c.status, c.want = 409, envelopeDetails("PRINT_UNCONFIRMED", unconfirmedMsg,
				map[string]any{"original_fault": "PRINTER_OUT_OF_PAPER", "cups_job_id": "7"})
		}),

		// --- print-jobs: 422 ---
		with(printCase("pdf-nie-renderuje"), func(c *contractCase) {
			c.body = labelBody("%PDF-1.4 uszkodzony")
			c.arrange = func(w *contractWorld) { w.be.renderErr = errors.New("pdftoppm: exit status 1") }
			c.status, c.want = 422, envelope("INVALID_PDF", "PDF render failed: pdftoppm: exit status 1")
		}),
		with(printCase("ani-pdf-ani-zpl"), func(c *contractCase) {
			c.body = labelBody("garbage")
			c.status, c.want = 422, envelope("UNSUPPORTED_FORMAT", "payload is neither PDF nor ZPL")
		}),
		with(printCase("cups-anulowal"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.states = []int{printer.JobCanceled} }
			c.status, c.want = 422, envelope("INVALID_ZPL", "job canceled by CUPS")
		}),

		// --- print-jobs: PRINTER_OFFLINE 503 ---
		with(printCase("9100-nieosiagalny"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.unreachable = true }
			c.status, c.want = 503, envelope("PRINTER_OFFLINE", "printer not reachable on socket :9100")
		}),
		with(printCase("glowica-otwarta"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.hs = []hsReply{{hs: printer.HostStatus{HeadOpen: true}, ok: true}} }
			c.status, c.want = 503, envelope("PRINTER_OFFLINE", "printer head/cover open (~HS)")
		}),
		with(printCase("hs-milczy-w-budzecie"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.hs = []hsReply{{err: errors.New("i/o timeout")}} }
			c.status, c.want = 503, envelope("PRINTER_OFFLINE", "printer unreachable during ~HS verification: i/o timeout")
		}),

		// --- print-jobs: QUEUE_PAUSED 503 (bez i z details) ---
		with(printCase("kolejka-cups-wstrzymana"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.queuePaused = true }
			c.status, c.want = 503, envelope("QUEUE_PAUSED", "CUPS queue is paused/disabled")
		}),
		with(printCase("hs-paused"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.hs = []hsReply{{hs: printer.HostStatus{Paused: true}, ok: true}} }
			c.status, c.want = 503, envelope("QUEUE_PAUSED", "printer paused (~HS)")
		}),
		with(printCase("pending-held"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.states = []int{printer.JobPendingHeld} }
			c.status, c.want = 503, envelopeDetails("QUEUE_PAUSED", "job held by CUPS (pending-held); requires operator release",
				map[string]any{"ipp_job_state": float64(printer.JobPendingHeld), "cups_job_id": "7"})
		}),

		// --- print-jobs: CUPS_UNAVAILABLE 503 ---
		with(printCase("lp-padlo"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.submitErr = errors.New("lp: scheduler not responding") }
			c.status, c.want = 503, envelope("CUPS_UNAVAILABLE", "lp submit failed: lp: scheduler not responding")
		}),
		with(printCase("ipp-padlo"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.stateErr = errors.New("ipp: connection refused") }
			c.status, c.want = 503, envelope("CUPS_UNAVAILABLE", "job poll failed: ipp: connection refused")
		}),
		with(printCase("cups-przerwal"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.states = []int{printer.JobAborted} }
			c.status, c.want = 503, envelope("CUPS_UNAVAILABLE", "job aborted by CUPS")
		}),

		// --- print-jobs: PRINT_TIMEOUT 503 ---
		with(printCase("cups-nie-skonczyl"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.states = []int{printer.JobProcessing} }
			c.status, c.want = 503, envelope("PRINT_TIMEOUT", "job did not complete within confirm timeout")
		}),
		with(printCase("hs-wciaz-drenuje"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.hs = []hsReply{{hs: printer.HostStatus{QueuedFormats: 1}, ok: true}} }
			c.status, c.want = 503, envelope("PRINT_TIMEOUT",
				"labels still printing (~HS draining) at confirm budget; retry with the same Idempotency-Key re-verifies")
		}),

		// --- print-jobs: PRINTER_OUT_OF_PAPER 503 ---
		with(printCase("brak-papieru"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.be.hs = []hsReply{{hs: printer.HostStatus{PaperOut: true}, ok: true}} }
			c.status, c.want = 503, envelope("PRINTER_OUT_OF_PAPER", "printer reports media-empty (~HS)")
		}),

		// --- printer-reset: sukces ---
		with(resetCase("reset-z-paper-jam"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) {
				w.panel.status = []panelPage{panelHTML("redtext", "Paper Jam"), panelHTML("greentext", "Ready")}
			}
			c.status = 200
			c.want = map[string]any{"status": "reset_ok", "panel_before": "Paper Jam", "panel_after": "Ready", "hs_ok": true}
			c.check = func(t *testing.T, w *contractWorld) {
				if w.panel.resets != 1 {
					t.Errorf("func=reset wywołany %d razy, want 1", w.panel.resets)
				}
			}
		}),
		with(resetCase("reset-panel-nieznany-format-hs-milczy"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) {
				w.panel.status = []panelPage{{html: "<HTML><BODY>maintenance</BODY></HTML>"}, panelHTML("greentext", "Ready")}
				w.resetProbe.hs = []hsReply{{err: errors.New("i/o timeout")}}
			}
			c.status = 200
			c.want = map[string]any{"status": "reset_ok", "panel_before": "", "panel_after": "Ready", "hs_ok": false}
		}),

		// --- printer-reset: PRINTER_BUSY 409 ---
		with(resetCase("reset-w-trakcie-druku"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.panel.status = []panelPage{panelHTML("greentext", "Printing")} }
			c.status, c.want = 409, envelope("PRINTER_BUSY", "druk w toku — reset przerwałby aktywny batch; spróbuj po zakończeniu")
			c.check = func(t *testing.T, w *contractWorld) {
				if w.panel.resets != 0 {
					t.Errorf("reset w trakcie druku nie może wywołać func=reset (%d)", w.panel.resets)
				}
			}
		}),

		// --- printer-reset: PRINTER_OFFLINE 503 (bez i z details) ---
		with(resetCase("reset-panel-niedostepny"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.panel.status = []panelPage{{code: http.StatusServiceUnavailable}} }
			c.status, c.want = 503, envelope("PRINTER_OFFLINE", "panel drukarki (status.cgi) niedostępny: panel /cgi-bin/status.cgi: HTTP 503")
		}),
		with(resetCase("reset-func-reset-padl"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.panel.resetCode = http.StatusInternalServerError }
			c.status, c.want = 503, envelope("PRINTER_OFFLINE", "func=reset nie powiódł się: panel /admin/cgi-bin/function.cgi?func=reset: HTTP 500")
		}),
		with(resetCase("reset-panel-nie-wrocil-do-ready"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) {
				w.panel.status = []panelPage{panelHTML("greentext", "Ready"), panelHTML("greentext", "Initializing")}
			}
			c.status, c.want = 503, envelope("PRINTER_OFFLINE", "panel nie wrócił do Ready w budżecie po resecie (3 prób)")
		}),
		with(resetCase("reset-fault-po-resecie"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) { w.panel.status = []panelPage{panelHTML("redtext", "Paper Jam")} }
			c.status, c.want = 503, envelopeDetails("PRINTER_OFFLINE", "po resecie panel raportuje fault: Paper Jam",
				map[string]any{"panel_state": "Paper Jam"})
		}),
		with(resetCase("reset-fault-pusty-stan"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) {
				w.panel.status = []panelPage{panelHTML("greentext", "Ready"), panelHTML("redtext", " ")}
			}
			c.status, c.want = 503, envelopeDetails("PRINTER_OFFLINE", "po resecie panel raportuje fault: ",
				map[string]any{"panel_state": ""})
		}),

		// --- printer-reset: PRINT_TIMEOUT 503 (klient rozłączył się w trakcie) ---
		with(resetCase("reset-kontekst-anulowany"), func(c *contractCase) {
			c.arrange = func(w *contractWorld) {
				w.resetPoll = time.Hour // select wybierze anulowany kontekst, nie timer
				w.panel.onReset = w.cancel
			}
			c.status, c.want = 503, envelope("PRINT_TIMEOUT", "context canceled while waiting for panel")
		}),

		// --- update: sukces 202 ---
		with(updateCase("update-tag-z-v", `{"tag":"v1.2.3"}`), func(c *contractCase) {
			c.status, c.want = 202, map[string]any{"status": "updating", "tag": "v1.2.3"}
			c.check = func(t *testing.T, w *contractWorld) {
				if !reflect.DeepEqual(w.spawned, []string{"v1.2.3"}) {
					t.Errorf("aktualizator dostał %q, want [v1.2.3]", w.spawned)
				}
			}
		}),
		with(updateCase("update-tag-bez-v-echo", `{"tag":"1.2.3"}`), func(c *contractCase) {
			c.status, c.want = 202, map[string]any{"status": "updating", "tag": "1.2.3"}
		}),

		// --- update: INVALID_REQUEST 400 i 422 ---
		with(updateCase("update-puste-body", ``), func(c *contractCase) {
			c.status, c.want = 400, envelope("INVALID_REQUEST", "invalid JSON")
		}),
		with(updateCase("update-zly-json", `nope`), func(c *contractCase) {
			c.status, c.want = 400, envelope("INVALID_REQUEST", "invalid JSON")
		}),
		with(updateCase("update-zly-tag", `{"tag":"latest"}`), func(c *contractCase) {
			c.arrange = realSpawnUpdater
			c.status, c.want = 422, envelope("INVALID_REQUEST", badTagMsg)
		}),
		with(updateCase("update-brak-tagu", `{}`), func(c *contractCase) {
			c.arrange = realSpawnUpdater
			c.status, c.want = 422, envelope("INVALID_REQUEST", badTagMsg)
		}),
		with(updateCase("update-blad-logu-aktualizatora", `{"tag":"v1.2.3"}`), func(c *contractCase) {
			// błąd po stronie agenta też wychodzi jako 422 INVALID_REQUEST
			c.arrange = realSpawnUpdater
			c.status, c.want = 422, envelope("INVALID_REQUEST",
				"updater log /nonexistent/data/update.log: open /nonexistent/data/update.log: no such file or directory")
		}),
	}

	// --- auth: każdy chroniony endpoint ---
	for _, base := range []contractCase{printCase("auth"), resetCase("auth"), updateCase("auth", `{"tag":"v1.2.3"}`)} {
		ep := contractEndpoints[base.path]
		cs = append(cs,
			with(base, func(c *contractCase) {
				c.name, c.token = ep+"-brak-tokenu", ""
				c.status, c.want = 401, envelope("MISSING_TOKEN", "X-Print-Token header required")
			}),
			with(base, func(c *contractCase) {
				c.name, c.token = ep+"-zly-token", "nope"
				c.status, c.want = 403, envelope("FORBIDDEN", "invalid token")
			}),
		)
	}
	return cs
}

func (w *contractWorld) do(h http.Handler, c contractCase) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)).WithContext(w.ctx)
	if c.token != "" {
		req.Header.Set("X-Print-Token", c.token)
	}
	if c.key != "" {
		req.Header.Set("Idempotency-Key", c.key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func runContractCase(c contractCase) (*httptest.ResponseRecorder, *contractWorld) {
	w := newContractWorld()
	if c.arrange != nil {
		c.arrange(w)
	}
	h := w.router()
	if c.retry {
		w.do(h, c)
		if c.beforeRetry != nil {
			c.beforeRetry(w)
		}
	}
	return w.do(h, c), w
}

// decodeContractBody: body musi być DOKŁADNIE jednym obiektem JSON.
func decodeContractBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	var got map[string]any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("body nie jest obiektem JSON: %v; body=%q", err, raw)
	}
	if dec.More() {
		t.Fatalf("body ma więcej niż jedną wartość JSON: %q", raw)
	}
	return got
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonType: nazwa typu JSON jak w tabelach docs/error-contract.md.
func jsonType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		for _, e := range x {
			if _, ok := e.(string); !ok {
				return "array"
			}
		}
		return "string[]"
	}
	return "?"
}

// checkEnvelopeInvariants: §2 dokumentu — code (stała z apierr.go), message
// (niepusty tekst), details nieobecne albo NIEPUSTY obiekt, nic więcej.
func checkEnvelopeInvariants(t *testing.T, m map[string]any, codes map[string]bool) {
	t.Helper()
	code, _ := m["code"].(string)
	if !codes[code] {
		t.Errorf("code %v spoza stałych apierr.go", m["code"])
	}
	if msg, ok := m["message"].(string); !ok || msg == "" {
		t.Errorf("message musi być niepustym stringiem, got %#v", m["message"])
	}
	if d, present := m["details"]; present {
		if obj, ok := d.(map[string]any); !ok || len(obj) == 0 {
			t.Errorf("details musi być nieobecne albo niepustym obiektem, got %#v", d)
		}
	}
	for k := range m {
		if k != "code" && k != "message" && k != "details" {
			t.Errorf("nadmiarowy klucz koperty %q", k)
		}
	}
}

func TestContractGolden(t *testing.T) {
	codes := map[string]bool{}
	for _, c := range apierrCodes(t) {
		codes[c] = true
	}
	for _, c := range contractCases() {
		t.Run(contractEndpoints[c.path]+"/"+c.name, func(t *testing.T) {
			rec, w := runContractCase(c)
			raw := rec.Body.Bytes()
			if rec.Code != c.status {
				t.Fatalf("HTTP %d, want %d; body=%s", rec.Code, c.status, raw)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			got := decodeContractBody(t, raw)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("body:\n got  %s\n want %s", jsonString(got), jsonString(c.want))
			}
			if c.status >= 400 {
				checkEnvelopeInvariants(t, got, codes)
			}

			// Ramka: zwarty JSON; json.Encoder dokleja "\n", replay terminalny nie.
			payload := raw
			if !c.replay {
				if !bytes.HasSuffix(raw, []byte("}\n")) {
					t.Errorf("body musi kończyć się jednym \\n (json.Encoder): %q", raw)
				}
				payload = bytes.TrimSuffix(raw, []byte("\n"))
			} else if want := `{"status":"printed","cups_job_id":"7"}`; string(raw) != want {
				t.Errorf("bajty replayu = %q, want %q", raw, want)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, payload); err != nil || !bytes.Equal(compact.Bytes(), payload) {
				t.Errorf("body nie jest zwartym JSON-em: %q", raw)
			}
			if c.check != nil {
				c.check(t, w)
			}
		})
	}
}

// G3: każda stała z apierr.go ma ≥ 1 przypadek w tabeli, a tabela nie zna
// kodów spoza apierr.go.
func TestContractCasesCoverEveryAPIErrCode(t *testing.T) {
	codes := apierrCodes(t)
	known := map[string]bool{}
	for _, c := range codes {
		known[c] = true
	}
	covered := map[string]int{}
	for _, c := range contractCases() {
		if c.status < 400 {
			continue
		}
		code, _ := c.want["code"].(string)
		if !known[code] {
			t.Errorf("przypadek %q: kod %q spoza apierr.go", c.name, code)
		}
		covered[code]++
	}
	for _, code := range codes {
		if covered[code] == 0 {
			t.Errorf("kod %s nie ma ani jednego przypadku w contractCases()", code)
		}
	}
}

// Odpowiedzi, które NIE są kopertą (docs/error-contract.md §5, N1–N5) —
// produkuje je http.ServeMux, nie handlery. Dla pewności żadna nie drukuje.
func TestContractNonEnvelopeResponses(t *testing.T) {
	type tc struct {
		name, method, path, token string
		status                    int
		contentType, allow, loc   string
		body                      string
	}
	cases := []tc{
		{name: "N1 nieznana sciezka z tokenem", method: "POST", path: "/api/v1/nope", token: contractToken,
			status: 404, contentType: "text/plain; charset=utf-8", body: "404 page not found\n"},
		{name: "N2 GET print-jobs z tokenem", method: "GET", path: "/api/v1/print-jobs", token: contractToken,
			status: 405, contentType: "text/plain; charset=utf-8", allow: "POST", body: "Method Not Allowed\n"},
		{name: "N2 GET printer-reset z tokenem", method: "GET", path: "/api/v1/admin/printer-reset", token: contractToken,
			status: 405, contentType: "text/plain; charset=utf-8", allow: "POST", body: "Method Not Allowed\n"},
		{name: "N2 GET update z tokenem", method: "GET", path: "/api/v1/admin/update", token: contractToken,
			status: 405, contentType: "text/plain; charset=utf-8", allow: "POST", body: "Method Not Allowed\n"},
		{name: "N3 POST health bez auth", method: "POST", path: "/api/v1/health",
			status: 405, contentType: "text/plain; charset=utf-8", allow: "GET, HEAD", body: "Method Not Allowed\n"},
		{name: "N4 ukosnik na koncu", method: "POST", path: "/api/v1/print-jobs/", token: contractToken,
			status: 404, contentType: "text/plain; charset=utf-8", body: "404 page not found\n"},
		{name: "N5 nieczysta sciezka", method: "POST", path: "//api/v1/print-jobs", token: contractToken,
			status: 307, loc: "/api/v1/print-jobs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newContractWorld()
			rec := w.do(w.router(), contractCase{method: c.method, path: c.path, token: c.token, key: "pj:n", body: labelBody(zpl)})
			if rec.Code != c.status {
				t.Fatalf("HTTP %d, want %d; body=%q", rec.Code, c.status, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != c.contentType {
				t.Errorf("Content-Type = %q, want %q", got, c.contentType)
			}
			if c.contentType != "" && rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", rec.Header().Get("X-Content-Type-Options"))
			}
			if got := rec.Header().Get("Allow"); got != c.allow {
				t.Errorf("Allow = %q, want %q", got, c.allow)
			}
			if got := rec.Header().Get("Location"); got != c.loc {
				t.Errorf("Location = %q, want %q", got, c.loc)
			}
			if got := rec.Body.String(); got != c.body {
				t.Errorf("body = %q, want %q", got, c.body)
			}
			if w.be.submits != 0 {
				t.Errorf("odpowiedź bez koperty nie może drukować: submits = %d", w.be.submits)
			}
		})
	}

	// N1b: auth działa przed routingiem — nieznana ścieżka bez tokenu to
	// koperta 401, nie 404.
	t.Run("N1b nieznana sciezka bez tokenu", func(t *testing.T) {
		w := newContractWorld()
		rec := w.do(w.router(), contractCase{method: "POST", path: "/api/v1/nope"})
		if rec.Code != 401 || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("HTTP %d %q, want 401 application/json", rec.Code, rec.Header().Get("Content-Type"))
		}
		want := envelope("MISSING_TOKEN", "X-Print-Token header required")
		if got := decodeContractBody(t, rec.Body.Bytes()); !reflect.DeepEqual(got, want) {
			t.Errorf("body:\n got  %s\n want %s", jsonString(got), jsonString(want))
		}
	})
}

// apierrCodes: wartości WSZYSTKICH stałych typu Code z internal/apierr/apierr.go
// (parsowanie źródła — pakiet nie eksportuje listy kodów). go test uruchamia
// się w katalogu pakietu, stąd ścieżka względna.
func apierrCodes(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "apierr", "apierr.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var codes []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Code" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("stała Code %v nie jest literałem stringa", vs.Names)
				}
				code, _ := strconv.Unquote(lit.Value)
				codes = append(codes, code)
			}
		}
	}
	if len(codes) == 0 {
		t.Fatalf("brak stałych typu Code w %s", path)
	}
	sort.Strings(codes)
	return codes
}
