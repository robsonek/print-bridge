package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/robsonek/print-bridge/internal/apierr"
	"github.com/robsonek/print-bridge/internal/printer"
)

type fakePrinter struct {
	res printer.Result
	err *apierr.Error

	// Counters for idempotency regression tests.
	submitCalls atomic.Int32
	resumeCalls atomic.Int32
}

func (f *fakePrinter) Print(context.Context, []byte, int) (printer.Result, *apierr.Error) {
	f.submitCalls.Add(1)
	return f.res, f.err
}
func (f *fakePrinter) ResumeJob(context.Context, int) (printer.Result, *apierr.Error) {
	f.resumeCalls.Add(1)
	return f.res, f.err
}

type memStore struct {
	mu       sync.Mutex
	terminal map[string]string
	pending  map[string]string
	getErr   error // when set, Get returns this error
}

func newMemStore() *memStore {
	return &memStore{terminal: map[string]string{}, pending: map[string]string{}}
}
func (m *memStore) Get(k string) (StoreRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return StoreRecord{}, false, m.getErr
	}
	if r, ok := m.terminal[k]; ok {
		return StoreRecord{ResponseJSON: r, Terminal: true}, true, nil
	}
	if j, ok := m.pending[k]; ok {
		return StoreRecord{CUPSJobID: j, Terminal: false}, true, nil
	}
	return StoreRecord{}, false, nil
}
func (m *memStore) SavePending(k, job string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[k] = job
	return nil
}
func (m *memStore) SaveTerminal(k, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminal[k] = body
	return nil
}
func (m *memStore) getPending(k string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.pending[k]
	return v, ok
}

func newHandlers(p PrintService, s Store) *Handlers {
	return &Handlers{
		Printer: p,
		Store:   s,
		KeyLock: NewKeyLock(),
		Health:  func(context.Context) (int, any) { return 200, map[string]string{"status": "ok"} },
	}
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

// Regression (#3,#5): two concurrent requests with the same Idempotency-Key
// must result in exactly ONE Submit (per-key lock + persist-then-resume).
func TestPrintJobConcurrentSameKeySubmitsOnce(t *testing.T) {
	store := newMemStore()
	fp := &fakePrinter{res: printer.Result{Status: "printed", CUPSJobID: "7"}}
	h := newHandlers(fp, store)
	body := `{"label_base64":"XlhBREFUQV5YWg==","copies":1}`

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest("POST", "/api/v1/print-jobs", strings.NewReader(body))
			req.Header.Set("Idempotency-Key", "pj:race")
			rec := httptest.NewRecorder()
			h.PrintJobs(rec, req)
		}()
	}
	close(start)
	wg.Wait()

	if got := fp.submitCalls.Load(); got != 1 {
		t.Errorf("Submit must run exactly once for one key, got %d (resumes=%d)", got, fp.resumeCalls.Load())
	}
}

// Regression (#4,#11): a post-submit error (e.g. PRINT_TIMEOUT) must still
// persist the cups_job_id so a retry resumes instead of resubmitting.
func TestPrintJobErrorPathSavesPending(t *testing.T) {
	store := newMemStore()
	fp := &fakePrinter{
		res: printer.Result{CUPSJobID: "7"},
		err: apierr.New(apierr.CodePrintTimeout, "did not complete", 503),
	}
	h := newHandlers(fp, store)
	body := `{"label_base64":"XlhBREFUQV5YWg==","copies":1}`
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "pj:timeout")
	rec := httptest.NewRecorder()
	h.PrintJobs(rec, req)

	if rec.Code != 503 {
		t.Fatalf("PRINT_TIMEOUT must surface as 503, got %d", rec.Code)
	}
	if job, ok := store.getPending("pj:timeout"); !ok || job != "7" {
		t.Errorf("error path must SavePending(key, cups_job_id); pending=%q ok=%v", job, ok)
	}
}

// Regression (#13): Store.Get returning an error must NOT trigger a fresh print;
// return a retryable error so Laravel retries instead of risking a duplicate.
func TestPrintJobGetErrorIsSafeRefusal(t *testing.T) {
	store := newMemStore()
	store.getErr = errors.New("database is locked")
	fp := &fakePrinter{res: printer.Result{Status: "printed", CUPSJobID: "7"}}
	h := newHandlers(fp, store)
	body := `{"label_base64":"XlhBREFUQV5YWg==","copies":1}`
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "pj:storeerr")
	rec := httptest.NewRecorder()
	h.PrintJobs(rec, req)

	if rec.Code != 503 {
		t.Fatalf("store Get error must yield retryable 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fp.submitCalls.Load() != 0 || fp.resumeCalls.Load() != 0 {
		t.Errorf("store error must NOT print: submits=%d resumes=%d", fp.submitCalls.Load(), fp.resumeCalls.Load())
	}
}

// Regression (#13): a pending record with an unusable cups_job_id must refuse
// (retryable) rather than resubmit.
func TestPrintJobPendingUnparsableJobIDRefuses(t *testing.T) {
	store := newMemStore()
	store.pending["pj:corrupt"] = "not-a-number"
	fp := &fakePrinter{res: printer.Result{Status: "printed", CUPSJobID: "7"}}
	h := newHandlers(fp, store)
	body := `{"label_base64":"XlhBREFUQV5YWg==","copies":1}`
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "pj:corrupt")
	rec := httptest.NewRecorder()
	h.PrintJobs(rec, req)

	if rec.Code != 503 {
		t.Fatalf("unparsable pending cups_job_id must yield retryable 503, got %d", rec.Code)
	}
	if fp.submitCalls.Load() != 0 {
		t.Errorf("must NOT resubmit on corrupt pending record, submits=%d", fp.submitCalls.Load())
	}
}

// Regression: a pending record with a valid cups_job_id resumes (no new Submit).
func TestPrintJobPendingResumes(t *testing.T) {
	store := newMemStore()
	store.pending["pj:resume"] = "42"
	fp := &fakePrinter{res: printer.Result{Status: "printed", CUPSJobID: "42"}}
	h := newHandlers(fp, store)
	body := `{"label_base64":"XlhBREFUQV5YWg==","copies":1}`
	req := httptest.NewRequest("POST", "/api/v1/print-jobs", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "pj:resume")
	rec := httptest.NewRecorder()
	h.PrintJobs(rec, req)

	if fp.resumeCalls.Load() != 1 {
		t.Errorf("pending record must ResumeJob once, resumes=%d", fp.resumeCalls.Load())
	}
	if fp.submitCalls.Load() != 0 {
		t.Errorf("pending record must NOT Submit, submits=%d", fp.submitCalls.Load())
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
