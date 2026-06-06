package printer

import (
	"context"
	"errors"
	"testing"

	"github.com/robsonek/print-bridge/internal/apierr"
)

type fakePanel struct {
	states    []PanelState // zwracane sekwencyjnie przez Status (ostatni sticky)
	statusErr []error      // równoległa sekwencja błędów (nil = ok)
	idx       int
	resets    int
	resetErr  error
}

func (f *fakePanel) Status(context.Context) (PanelState, error) {
	i := f.idx
	if f.idx < len(f.states)-1 {
		f.idx++
	}
	var err error
	if i < len(f.statusErr) {
		err = f.statusErr[i]
	}
	if i < len(f.states) {
		return f.states[i], err
	}
	return PanelState{}, err
}

func (f *fakePanel) Reset(context.Context) error {
	f.resets++
	return f.resetErr
}

var (
	ready    = PanelState{State: "Ready", Green: true, Known: true}
	printing = PanelState{State: "Printing", Green: true, Known: true}
	paperJam = PanelState{State: "Paper Jam", Green: false, Known: true}
)

func newResetter(p *fakePanel, hs *fakeBackend) *PrinterResetter {
	return &PrinterResetter{Panel: p, Probe: hs, PollInterval: 0, MaxPolls: 5}
}

func TestResetHappyPathFromLatchedFault(t *testing.T) {
	// Scenariusz merchanta: latched Paper Jam po wymianie rolki -> reset ->
	// panel wraca do Ready -> ~HS żywe.
	p := &fakePanel{states: []PanelState{paperJam, ready}}
	hs := &fakeBackend{hsOK: true}
	r := newResetter(p, hs)

	out, e := r.Reset(context.Background())
	if e != nil {
		t.Fatalf("Reset: %v", e)
	}
	if p.resets != 1 {
		t.Errorf("func=reset wywołany %d razy, want 1", p.resets)
	}
	if out.PanelBefore != "Paper Jam" || out.PanelAfter != "Ready" || !out.HSOk {
		t.Errorf("outcome = %+v", out)
	}
}

// Guard: NIE resetować w trakcie druku — przerwanie aktywnego batcha to
// utrata etykiet. PRINTER_BUSY jest retryable (spróbuj po zakończeniu druku).
func TestResetRefusesWhilePrinting(t *testing.T) {
	p := &fakePanel{states: []PanelState{printing}}
	r := newResetter(p, &fakeBackend{hsOK: true})

	_, e := r.Reset(context.Background())
	if e == nil || e.Code != apierr.CodePrinterBusy {
		t.Fatalf("want PRINTER_BUSY, got %v", e)
	}
	if p.resets != 0 {
		t.Errorf("reset NIE może być wywołany w trakcie druku (resets=%d)", p.resets)
	}
	if !e.Code.Retryable() {
		t.Error("PRINTER_BUSY musi być retryable")
	}
}

func TestResetPanelUnreachableIsPrinterOffline(t *testing.T) {
	p := &fakePanel{states: []PanelState{{}}, statusErr: []error{errors.New("dial tcp: refused")}}
	r := newResetter(p, &fakeBackend{hsOK: true})

	_, e := r.Reset(context.Background())
	if e == nil || e.Code != apierr.CodePrinterOffline {
		t.Fatalf("want PRINTER_OFFLINE gdy panel martwy, got %v", e)
	}
}

// Po func=reset print-server na ~1 s znika z HTTP — błędy w trakcie poll'a to
// normalna część restartu, nie porażka.
func TestResetToleratesPanelBlipDuringRestart(t *testing.T) {
	p := &fakePanel{
		states:    []PanelState{ready, {}, {}, ready},
		statusErr: []error{nil, errors.New("connection refused"), errors.New("timeout"), nil},
	}
	r := newResetter(p, &fakeBackend{hsOK: true})

	out, e := r.Reset(context.Background())
	if e != nil {
		t.Fatalf("Reset: %v", e)
	}
	if out.PanelAfter != "Ready" {
		t.Errorf("PanelAfter = %q, want Ready po przejściowym blipie", out.PanelAfter)
	}
}

func TestResetPanelNeverComesBackIsError(t *testing.T) {
	p := &fakePanel{
		states:    []PanelState{ready, {}},
		statusErr: []error{nil, errors.New("connection refused")},
	}
	r := newResetter(p, &fakeBackend{hsOK: true}) // MaxPolls=5, sticky błąd

	_, e := r.Reset(context.Background())
	if e == nil || e.Code != apierr.CodePrinterOffline {
		t.Fatalf("panel nie wrócił po resecie -> PRINTER_OFFLINE, got %v", e)
	}
}

// ~HS martwe po resecie nie unieważnia resetu (HSOk=false w odpowiedzi —
// panel to autorytatywne źródło, ~HS bywa zawieszone niezależnie).
func TestResetReportsHSStateBestEffort(t *testing.T) {
	p := &fakePanel{states: []PanelState{ready, ready}}
	hs := &fakeBackend{hsErr: errors.New("timeout")}
	r := newResetter(p, hs)

	out, e := r.Reset(context.Background())
	if e != nil {
		t.Fatalf("Reset: %v", e)
	}
	if out.HSOk {
		t.Error("HSOk musi być false gdy ~HS nie odpowiada")
	}
}
