package printer

import (
	"context"
	"errors"
	"testing"

	"github.com/robsonek/print-bridge/internal/apierr"
)

type fakeBackend struct {
	reachable  bool
	paused     bool
	states     []int // returned in sequence by JobState
	idx        int
	hs         HostStatus
	hsOK       bool
	hsErr      error
	submitErr  error
	rendered   []byte
	renderErr  error
	submitted  []byte
}

func (f *fakeBackend) Reachable(context.Context) (bool, error)   { return f.reachable, nil }
func (f *fakeBackend) QueuePaused(context.Context) (bool, error) { return f.paused, nil }
func (f *fakeBackend) Submit(_ context.Context, d []byte, _ int) (int, error) {
	f.submitted = d
	return 7, f.submitErr
}
func (f *fakeBackend) JobState(context.Context, int) (int, error) {
	s := f.states[f.idx]
	if f.idx < len(f.states)-1 {
		f.idx++
	}
	return s, nil
}
func (f *fakeBackend) HostStatus(context.Context) (HostStatus, bool, error) {
	return f.hs, f.hsOK, f.hsErr
}
func (f *fakeBackend) PDFToZPL(context.Context, []byte) ([]byte, error) {
	return f.rendered, f.renderErr
}

func newPrinter(f *fakeBackend) *Printer {
	return &Printer{Reach: f, Sub: f, Poll: f, Probe: f, Render: f, PollInterval: 0, ConfirmTimeoutPolls: 5}
}

func TestPrintZPLHappyPath(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobProcessing, JobCompleted}, hs: HostStatus{}, hsOK: true}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XADATA^XZ"), 1)
	if e != nil {
		t.Fatalf("unexpected error: %v", e)
	}
	if res.Status != "printed" || res.CUPSJobID != "7" {
		t.Errorf("result = %+v", res)
	}
	if string(f.submitted) != "^XADATA^XZ" {
		t.Errorf("ZPL must be passed through unchanged, got %q", f.submitted)
	}
}

func TestPrintPDFRenders(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hsOK: true, rendered: []byte("^XAGF^XZ")}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("%PDF-1.4 ..."), 1)
	if e != nil {
		t.Fatalf("err: %v", e)
	}
	if string(f.submitted) != "^XAGF^XZ" {
		t.Errorf("PDF must be rendered before submit, got %q", f.submitted)
	}
	if res.Status != "printed" {
		t.Errorf("status = %q", res.Status)
	}
}

func TestPrintUnsupportedFormat(t *testing.T) {
	p := newPrinter(&fakeBackend{reachable: true})
	_, e := p.Print(context.Background(), []byte("garbage"), 1)
	if e == nil || e.Code != apierr.CodeUnsupportedFormat {
		t.Fatalf("want UNSUPPORTED_FORMAT, got %v", e)
	}
}

func TestPrintOffline(t *testing.T) {
	p := newPrinter(&fakeBackend{reachable: false})
	_, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodePrinterOffline {
		t.Fatalf("want PRINTER_OFFLINE, got %v", e)
	}
}

func TestPrintQueuePaused(t *testing.T) {
	p := newPrinter(&fakeBackend{reachable: true, paused: true})
	_, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodeQueuePaused {
		t.Fatalf("want QUEUE_PAUSED, got %v", e)
	}
}

func TestPrintTimeout(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobProcessing}, hsOK: true}
	p := newPrinter(f)
	_, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodePrintTimeout {
		t.Fatalf("want PRINT_TIMEOUT, got %v", e)
	}
}

func TestPrintPaperOutAfterCompletion(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hs: HostStatus{PaperOut: true}, hsOK: true}
	p := newPrinter(f)
	_, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodeOutOfPaper {
		t.Fatalf("want PRINTER_OUT_OF_PAPER, got %v", e)
	}
}

func TestPrintHSUnsupportedDegradesToPrinted(t *testing.T) {
	// ~HS returns ok=false (printer doesn't speak it) -> graceful degrade.
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hsOK: false, hsErr: errors.New("no reply")}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e != nil {
		t.Fatalf("degrade should yield printed, got err %v", e)
	}
	if res.Status != "printed" {
		t.Errorf("status = %q, want printed (best-effort)", res.Status)
	}
}

func TestPrintAborted(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobAborted}, hsOK: true}
	p := newPrinter(f)
	_, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodeCUPSUnavailable {
		t.Fatalf("want CUPS_UNAVAILABLE on abort, got %v", e)
	}
}

// Regression (#4,#11): after a successful Submit, every post-submit error path
// must carry the CUPS job id in Result so the handler can persist it for
// resume-by-key. Without it a retry resubmits => duplicate physical label.
func TestPrintTimeoutCarriesCUPSJobID(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobProcessing}, hsOK: true}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodePrintTimeout {
		t.Fatalf("want PRINT_TIMEOUT, got %v", e)
	}
	if res.CUPSJobID != "7" {
		t.Errorf("timeout Result must carry submitted job id 7, got %q", res.CUPSJobID)
	}
}

func TestPrintAbortCarriesCUPSJobID(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobAborted}, hsOK: true}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodeCUPSUnavailable {
		t.Fatalf("want CUPS_UNAVAILABLE, got %v", e)
	}
	if res.CUPSJobID != "7" {
		t.Errorf("abort Result must carry submitted job id 7, got %q", res.CUPSJobID)
	}
}

func TestPrintPaperOutCarriesCUPSJobID(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hs: HostStatus{PaperOut: true}, hsOK: true}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodeOutOfPaper {
		t.Fatalf("want PRINTER_OUT_OF_PAPER, got %v", e)
	}
	if res.CUPSJobID != "7" {
		t.Errorf("paper-out Result must carry submitted job id 7, got %q", res.CUPSJobID)
	}
}

// Submit itself failing must NOT carry a job id (no physical job exists ->
// resubmit is correct, no duplicate).
func TestPrintSubmitFailureNoCUPSJobID(t *testing.T) {
	f := &fakeBackend{reachable: true, submitErr: errors.New("lp boom")}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodeCUPSUnavailable {
		t.Fatalf("want CUPS_UNAVAILABLE on submit failure, got %v", e)
	}
	if res.CUPSJobID != "" {
		t.Errorf("submit failure must NOT carry a job id, got %q", res.CUPSJobID)
	}
}
