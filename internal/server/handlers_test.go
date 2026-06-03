package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/robsonek/print-bridge/internal/apierr"
	"github.com/robsonek/print-bridge/internal/printer"
)

type fakePrinter struct {
	res printer.Result
	err *apierr.Error
}

func (f *fakePrinter) Print(context.Context, []byte, int) (printer.Result, *apierr.Error) {
	return f.res, f.err
}
func (f *fakePrinter) ResumeJob(context.Context, int) (printer.Result, *apierr.Error) {
	return f.res, f.err
}

type memStore struct {
	terminal map[string]string
	pending  map[string]string
}

func newMemStore() *memStore {
	return &memStore{terminal: map[string]string{}, pending: map[string]string{}}
}
func (m *memStore) Get(k string) (StoreRecord, bool, error) {
	if r, ok := m.terminal[k]; ok {
		return StoreRecord{ResponseJSON: r, Terminal: true}, true, nil
	}
	if j, ok := m.pending[k]; ok {
		return StoreRecord{CUPSJobID: j, Terminal: false}, true, nil
	}
	return StoreRecord{}, false, nil
}
func (m *memStore) SavePending(k, job string) error  { m.pending[k] = job; return nil }
func (m *memStore) SaveTerminal(k, body string) error { m.terminal[k] = body; return nil }

func newHandlers(p PrintService, s Store) *Handlers {
	return &Handlers{Printer: p, Store: s, Health: func(context.Context) (int, any) { return 200, map[string]string{"status": "ok"} }}
}

func TestPrintJobNewSuccess(t *testing.T) {
	h := newHandlers(&fakePrinter{res: printer.Result{Status: "printed", CUPSJobID: "7"}}, newMemStore())
	body := `{"label_base64":"XlhBREFUQV5YWg==","copies":1}` // base64("^XADATA^XZ")? not exact, decode-tolerant test
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "pj:1")
	rec := httptest.NewRecorder()
	h.PrintJobs(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"printed"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPrintJobMissingIdempotencyKey(t *testing.T) {
	h := newHandlers(&fakePrinter{}, newMemStore())
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", strings.NewReader(`{"label_base64":"QQ=="}`))
	rec := httptest.NewRecorder()
	h.PrintJobs(rec, req)
	if rec.Code != 400 {
		t.Errorf("missing Idempotency-Key => 400, got %d", rec.Code)
	}
}

func TestPrintJobReplaysTerminal(t *testing.T) {
	store := newMemStore()
	store.terminal["pj:9"] = `{"status":"printed","cups_job_id":"42"}`
	called := false
	h := newHandlers(&fakePrinter{res: printer.Result{Status: "SHOULD-NOT-RUN"}}, store)
	_ = called
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", strings.NewReader(`{"label_base64":"QQ=="}`))
	req.Header.Set("Idempotency-Key", "pj:9")
	rec := httptest.NewRecorder()
	h.PrintJobs(rec, req)
	if !strings.Contains(rec.Body.String(), `"42"`) {
		t.Errorf("must replay stored terminal body, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "SHOULD-NOT-RUN") {
		t.Error("terminal key must NOT re-run Print")
	}
}

func TestHealthEndpoint(t *testing.T) {
	h := newHandlers(&fakePrinter{}, newMemStore())
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	h.HealthHandler(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("health = %d %s", rec.Code, rec.Body.String())
	}
}
