package printer

import (
	"context"
	"errors"
	"testing"

	"github.com/robsonek/print-bridge/internal/apierr"
)

type hsResp struct {
	hs  HostStatus
	ok  bool
	err error
}

type fakeBackend struct {
	reachable   bool
	paused      bool
	states      []int // returned in sequence by JobState
	idx         int
	hs          HostStatus
	hsOK        bool
	hsErr       error
	hsSeq       []hsResp // optional scripted ~HS sequence (drain tests); last entry sticky
	hsCalls     int      // number of HostStatus invocations
	submitErr   error
	rendered    []byte
	renderErr   error
	submitted   []byte
	jobStateErr error // when set, JobState returns this error instead of a state
	pollCalls   int   // number of JobState invocations (for fast-return assertions)
}

func (f *fakeBackend) Reachable(context.Context) (bool, error)   { return f.reachable, nil }
func (f *fakeBackend) QueuePaused(context.Context) (bool, error) { return f.paused, nil }
func (f *fakeBackend) Submit(_ context.Context, d []byte, _ int) (int, error) {
	f.submitted = d
	return 7, f.submitErr
}
func (f *fakeBackend) JobState(context.Context, int) (int, error) {
	f.pollCalls++
	if f.jobStateErr != nil {
		return 0, f.jobStateErr
	}
	s := f.states[f.idx]
	if f.idx < len(f.states)-1 {
		f.idx++
	}
	return s, nil
}
func (f *fakeBackend) HostStatus(context.Context) (HostStatus, bool, error) {
	f.hsCalls++
	if len(f.hsSeq) > 0 {
		r := f.hsSeq[0]
		if len(f.hsSeq) > 1 {
			f.hsSeq = f.hsSeq[1:]
		}
		return r.hs, r.ok, r.err
	}
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

// A multi-label job (multi-parcel PDF/ZPL) prints serially on the single-threaded
// print-server and needs proportionally longer to confirm. The confirm budget must
// scale with the label count (^XA), else a 2-parcel job false-times-out at 30s
// while still printing (observed on hardware with a 2-page DPD PDF).
func TestPrintConfirmTimeoutScalesWithLabelCount(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobProcessing}, hsOK: true}
	p := newPrinter(f) // ConfirmTimeoutPolls=5
	_, e := p.Print(context.Background(), []byte("^XAa^XZ^XAb^XZ"), 1)
	if e == nil || e.Code != apierr.CodePrintTimeout {
		t.Fatalf("want PRINT_TIMEOUT, got %v", e)
	}
	if f.pollCalls != 10 { // base 5 * 2 labels
		t.Errorf("2-label job must poll base*2=10 times before timeout, got %d", f.pollCalls)
	}
}

func TestPrintSingleLabelUsesBaseTimeout(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobProcessing}, hsOK: true}
	p := newPrinter(f) // ConfirmTimeoutPolls=5
	_, _ = p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if f.pollCalls != 5 { // base 5 * 1 label
		t.Errorf("1-label job must poll base=5 times, got %d", f.pollCalls)
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
	// #2: ~HS reachable but reply unparseable (ok=false, err=nil) -> the printer
	// IS alive, it just doesn't speak ~HS intelligibly -> graceful best-effort
	// "printed". This is the ONLY case that may degrade.
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hsOK: false, hsErr: nil}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e != nil {
		t.Fatalf("degrade should yield printed, got err %v", e)
	}
	if res.Status != "printed" {
		t.Errorf("status = %q, want printed (best-effort)", res.Status)
	}
}

// #2 regression: transport-error at verify time (dial/write/read fail) means the
// printer became UNREACHABLE between CUPS JobCompleted and the ~HS probe. The
// physical print is UNKNOWN -> must NOT degrade to "printed". Return retryable
// PRINTER_OFFLINE so resume-by-key re-verifies (without reprinting) once back.
func TestPrintHSTransportErrorIsPrinterOffline(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobCompleted}, hsOK: false, hsErr: errors.New("dial timeout")}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodePrinterOffline {
		t.Fatalf("want PRINTER_OFFLINE on ~HS transport error, got err=%v", e)
	}
	if res.Status == "printed" {
		t.Errorf("must NOT claim printed on transport error, got %+v", res)
	}
	if res.CUPSJobID != "7" {
		t.Errorf("offline Result must carry submitted job id 7, got %q", res.CUPSJobID)
	}
}

// #1 regression: JobProcessingStopped (IPP state 6) is NOT terminal (RFC 8011).
// Printer halted (paper/jam/pause); CUPS resumes to processing->completed once
// the fault clears. A [6,6,9] sequence must keep polling and end as "printed",
// NOT abort with CUPS_UNAVAILABLE.
func TestPrintProcessingStoppedKeepsPolling(t *testing.T) {
	f := &fakeBackend{
		reachable: true,
		states:    []int{JobProcessingStopped, JobProcessingStopped, JobCompleted},
		hsOK:      true,
	}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e != nil {
		t.Fatalf("state 6 must not abort; want printed, got err %v", e)
	}
	if res.Status != "printed" {
		t.Errorf("status = %q, want printed after stop->completed", res.Status)
	}
}

// #9 regression: JobPendingHeld (IPP state 4) needs an explicit operator release
// and will NOT resume on its own. pollAndVerify must return FAST with a descriptive
// QUEUE_PAUSED (not poll out the whole budget into a misleading PRINT_TIMEOUT),
// carrying the IPP state + cups job id as actionable details.
func TestPrintPendingHeldReturnsQueuePausedFast(t *testing.T) {
	f := &fakeBackend{
		reachable: true,
		states:    []int{JobPendingHeld},
		hsOK:      true,
	}
	p := newPrinter(f) // ConfirmTimeoutPolls=5
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodeQueuePaused {
		t.Fatalf("want QUEUE_PAUSED on pending-held, got err=%v", e)
	}
	// Fast return: a single poll surfacing state 4 must short-circuit, NOT consume
	// the whole ConfirmTimeoutPolls budget (which would yield PRINT_TIMEOUT).
	if f.pollCalls != 1 {
		t.Errorf("pending-held must return after 1 poll, got %d polls", f.pollCalls)
	}
	if res.CUPSJobID != "7" {
		t.Errorf("held Result must carry submitted job id 7, got %q", res.CUPSJobID)
	}
	if e.Details["ipp_job_state"] != JobPendingHeld {
		t.Errorf("want details.ipp_job_state=%d, got %v", JobPendingHeld, e.Details["ipp_job_state"])
	}
	if e.Details["cups_job_id"] != "7" {
		t.Errorf("want details.cups_job_id=7, got %v", e.Details["cups_job_id"])
	}
}

// #6 regression (eviction edge): when JobState reports ErrJobGone (CUPS purged the
// job from history), pollAndVerify must NOT return a hard CUPS_UNAVAILABLE. The
// job we submitted is gone => almost certainly completed => fall through to the
// ~HS verify(). With a healthy printer that yields "printed" (no false re-queue /
// double print on the resume-by-key recovery path).
func TestPollEvictedJobFallsThroughToVerify(t *testing.T) {
	f := &fakeBackend{
		reachable:   true,
		jobStateErr: ErrJobGone,
		hsOK:        true, // healthy ~HS reply
	}
	p := newPrinter(f)
	res, e := p.ResumeJob(context.Background(), 7)
	if e != nil {
		t.Fatalf("ErrJobGone must route to verify, not fail; got err %v", e)
	}
	if res.Status != "printed" {
		t.Errorf("evicted+healthy printer => printed, got %+v", res)
	}
}

// #6 regression: ErrJobGone routed to verify() must still honor the hardware
// truth. If ~HS reports media-empty, the result is the real OUT_OF_PAPER fault,
// NOT a falsely-degraded "printed".
func TestPollEvictedJobReportsHardwareFault(t *testing.T) {
	f := &fakeBackend{
		reachable:   true,
		jobStateErr: ErrJobGone,
		hs:          HostStatus{PaperOut: true},
		hsOK:        true,
	}
	p := newPrinter(f)
	_, e := p.ResumeJob(context.Background(), 7)
	if e == nil || e.Code != apierr.CodeOutOfPaper {
		t.Fatalf("evicted job with paper-out ~HS must report OUT_OF_PAPER, got %v", e)
	}
}

// #6 regression: a non-eviction JobState error (e.g. a real CUPS/transport
// failure, NOT ErrJobGone) must still map to the hard retryable CUPS_UNAVAILABLE
// — the ErrJobGone fast-path must not swallow genuine failures.
func TestPollGenericJobStateErrorStaysCUPSUnavailable(t *testing.T) {
	f := &fakeBackend{
		reachable:   true,
		jobStateErr: errors.New("connection refused"),
		hsOK:        true,
	}
	p := newPrinter(f)
	_, e := p.ResumeJob(context.Background(), 7)
	if e == nil || e.Code != apierr.CodeCUPSUnavailable {
		t.Fatalf("non-gone JobState error must be CUPS_UNAVAILABLE, got %v", e)
	}
}

// #1: a job stuck in processing-stopped for the whole confirm window must NOT
// abort -> it exhausts the poll budget and returns retryable PRINT_TIMEOUT
// (resume-by-key protects against a duplicate on retry).
func TestPrintProcessingStoppedExhaustsToTimeout(t *testing.T) {
	f := &fakeBackend{reachable: true, states: []int{JobProcessingStopped}, hsOK: true}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodePrintTimeout {
		t.Fatalf("want PRINT_TIMEOUT when stop persists, got err=%v", e)
	}
	if res.CUPSJobID != "7" {
		t.Errorf("timeout Result must carry submitted job id 7, got %q", res.CUPSJobID)
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

// --- Drain-poll w verify(): po lpdpaced CUPS-owe "completed" = dane dostarczone
// (~3 s), a silnik drukuje jeszcze N×~5 s. verify() polluje ~HS aż bufor i batch
// się opróżni (QueuedFormats==0) — dopiero wtedy "printed"
// znaczy FIZYCZNIE wydrukowane. Pola potwierdzone na sprzęcie 2026-06-06.

func TestVerifyWaitsForPhysicalDrain(t *testing.T) {
	f := &fakeBackend{
		reachable: true,
		states:    []int{JobCompleted},
		hsSeq: []hsResp{
			{hs: HostStatus{QueuedFormats: 2}, ok: true},
			{hs: HostStatus{QueuedFormats: 1}, ok: true},
			{hs: HostStatus{}, ok: true}, // opróżnione
		},
	}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e != nil {
		t.Fatalf("drain ending idle must yield printed, got err %v", e)
	}
	if res.Status != "printed" {
		t.Errorf("status = %q, want printed", res.Status)
	}
	if f.hsCalls != 3 {
		t.Errorf("verify must poll ~HS until drained: hsCalls = %d, want 3", f.hsCalls)
	}
}

// Zmiana kontraktu #2: błąd transportu ~HS w trakcie drenażu = print-server
// zajęty drukiem (jednowątkowy — findings: "timeout-w-trakcie-druku to busy,
// nie down"), więc RETRY w ramach budżetu zamiast natychmiastowego
// PRINTER_OFFLINE. Dopiero budżet wyczerpany bez ŻADNEJ odpowiedzi -> OFFLINE.
func TestVerifyTransportErrorDuringDrainIsBusyNotOffline(t *testing.T) {
	f := &fakeBackend{
		reachable: true,
		states:    []int{JobCompleted},
		hsSeq: []hsResp{
			{err: errors.New("dial timeout")},
			{err: errors.New("dial timeout")},
			{hs: HostStatus{}, ok: true},
		},
	}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e != nil {
		t.Fatalf("transient ~HS errors during drain must retry, got err %v", e)
	}
	if res.Status != "printed" {
		t.Errorf("status = %q, want printed after probe recovers", res.Status)
	}
}

func TestVerifyStillDrainingAtBudgetIsPrintTimeout(t *testing.T) {
	f := &fakeBackend{
		reachable: true,
		states:    []int{JobCompleted},
		// sticky: drukarka bez końca raportuje niedokończony batch
		hsSeq: []hsResp{{hs: HostStatus{QueuedFormats: 1}, ok: true}},
	}
	p := newPrinter(f)
	res, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodePrintTimeout {
		t.Fatalf("still-draining at budget end must be PRINT_TIMEOUT (retryable, resume re-verifies), got %v", e)
	}
	if res.CUPSJobID != "7" {
		t.Errorf("timeout Result must carry job id 7 for resume-by-key, got %q", res.CUPSJobID)
	}
}

// MED #10 domknięte: otwarta głowica (linia 2 ~HS) musi być faultem w verify,
// nie cichym "printed".
func TestVerifyHeadOpenFailsFast(t *testing.T) {
	f := &fakeBackend{
		reachable: true,
		states:    []int{JobCompleted},
		hs:        HostStatus{HeadOpen: true},
		hsOK:      true,
	}
	p := newPrinter(f)
	_, e := p.Print(context.Background(), []byte("^XA^XZ"), 1)
	if e == nil || e.Code != apierr.CodePrinterOffline {
		t.Fatalf("head-open must be a fault (PRINTER_OFFLINE), got %v", e)
	}
	if f.hsCalls != 1 {
		t.Errorf("fault must fail fast: hsCalls = %d, want 1", f.hsCalls)
	}
}
