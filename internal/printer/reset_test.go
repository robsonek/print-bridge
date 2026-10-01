package printer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// --- U3 (od v0.8.0): budżet czasu resetu i granice kontekstu ---

// manualCtx: kontekst, który test kończy w wybranym miejscu, z wybranym
// powodem (DeadlineExceeded = budżet, Canceled = rozłączenie klienta).
type manualCtx struct {
	context.Context
	done chan struct{}
	err  error
}

func newManualCtx() *manualCtx {
	return &manualCtx{Context: context.Background(), done: make(chan struct{})}
}
func (c *manualCtx) Done() <-chan struct{} { return c.done }
func (c *manualCtx) Err() error            { return c.err }
func (c *manualCtx) end(err error) {
	if c.err == nil {
		c.err = err
		close(c.done)
	}
}

// hookPanel: atrapa panelu z hakami na każde wywołanie (bez sleepów).
type hookPanel struct {
	status   func(call int) (PanelState, error)
	reset    func() error
	statuses int
	resets   int
}

func (p *hookPanel) Status(context.Context) (PanelState, error) {
	i := p.statuses
	p.statuses++
	return p.status(i)
}
func (p *hookPanel) Reset(context.Context) error {
	p.resets++
	if p.reset != nil {
		return p.reset()
	}
	return nil
}

func wantResetTimeout(t *testing.T, e *apierr.Error, sent bool, msgPart string) {
	t.Helper()
	if e == nil || e.Code != apierr.CodePrintTimeout || e.HTTPStatus != 503 {
		t.Fatalf("want 503 PRINT_TIMEOUT, got %v", e)
	}
	if got, ok := e.Details["reset_sent"].(bool); !ok || got != sent || len(e.Details) != 1 {
		t.Errorf("details = %v, want {reset_sent: %v}", e.Details, sent)
	}
	if !strings.Contains(e.Message, msgPart) {
		t.Errorf("message %q bez %q", e.Message, msgPart)
	}
}

// Czekanie na trwający reset (HTTP albo watchdoga) mieści się w budżecie: po
// końcu kontekstu → PRINT_TIMEOUT, func=reset NIE wysłany, panel nietknięty.
func TestResetWaitingForOtherResetEndsWithContext(t *testing.T) {
	for _, c := range []struct {
		name, msg string
		err       error
	}{
		{"budżet", "budżet czasu resetu wyczerpany", context.DeadlineExceeded},
		{"rozłączenie", "klient rozłączył się", context.Canceled},
	} {
		t.Run(c.name, func(t *testing.T) {
			entered, unblock := make(chan struct{}), make(chan struct{})
			p := &hookPanel{status: func(call int) (PanelState, error) {
				if call == 0 {
					close(entered)
					<-unblock // pierwszy reset trzyma semafor
				}
				return ready, nil
			}}
			r := newResetterFor(p)
			first := make(chan *apierr.Error, 1)
			go func() { _, e := r.Reset(context.Background()); first <- e }()
			<-entered

			ctx := newManualCtx()
			second := make(chan *apierr.Error, 1)
			go func() { _, e := r.Reset(ctx); second <- e }()
			ctx.end(c.err)
			wantResetTimeout(t, <-second, false, c.msg)
			if p.statuses != 1 {
				t.Errorf("drugi reset nie może dotknąć panelu (statuses=%d)", p.statuses)
			}
			close(unblock)
			if e := <-first; e != nil {
				t.Errorf("pierwszy reset: %v", e)
			}
		})
	}
}

// Martwy kontekst przy WOLNYM semaforze: semafor zajęty bez losowania select,
// sprawdzenie po zajęciu kończy operację, zanim cokolwiek pójdzie do panelu.
func TestResetDeadContextFreeSemaphoreTouchesNoPanel(t *testing.T) {
	p := &hookPanel{status: func(int) (PanelState, error) { return ready, nil }}
	r := newResetterFor(p)
	ctx := newManualCtx()
	ctx.end(context.DeadlineExceeded)
	for i := 0; i < 50; i++ {
		_, e := r.Reset(ctx)
		wantResetTimeout(t, e, false, "NIE został wysłany")
	}
	if p.statuses != 0 || p.resets != 0 {
		t.Errorf("panel dotknięty: statuses=%d resets=%d", p.statuses, p.resets)
	}
}

func TestResetContextEndsBeforeFuncReset(t *testing.T) {
	t.Run("błąd pierwszego statusu przy końcu kontekstu", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{status: func(int) (PanelState, error) {
			ctx.end(context.DeadlineExceeded)
			return PanelState{}, context.DeadlineExceeded
		}}
		_, e := newResetterFor(p).Reset(ctx)
		wantResetTimeout(t, e, false, "budżet czasu resetu wyczerpany")
		if p.resets != 0 {
			t.Errorf("func=reset wysłany (%d)", p.resets)
		}
	})
	t.Run("status odebrany, kontekst skończył się przed func=reset", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{status: func(int) (PanelState, error) {
			ctx.end(context.Canceled)
			return paperJam, nil
		}}
		_, e := newResetterFor(p).Reset(ctx)
		wantResetTimeout(t, e, false, "klient rozłączył się")
		if p.resets != 0 {
			t.Errorf("func=reset wysłany mimo martwego kontekstu (%d)", p.resets)
		}
	})
}

func TestResetContextEndsAfterFuncResetStarted(t *testing.T) {
	t.Run("w trakcie func=reset", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{
			status: func(int) (PanelState, error) { return ready, nil },
			reset:  func() error { ctx.end(context.DeadlineExceeded); return context.DeadlineExceeded },
		}
		_, e := newResetterFor(p).Reset(ctx)
		wantResetTimeout(t, e, true, "sprawdź panel przed ponowieniem")
	})
	t.Run("w select pętli", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{
			status: func(int) (PanelState, error) { return ready, nil },
			reset:  func() error { ctx.end(context.Canceled); return nil },
		}
		r := newResetterFor(p)
		r.PollInterval = time.Hour // select wybierze kontekst, nie timer
		_, e := r.Reset(ctx)
		wantResetTimeout(t, e, true, "klient rozłączył się")
	})
	t.Run("błąd statusu w pętli (PollInterval 0)", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{status: func(call int) (PanelState, error) {
			if call == 0 {
				return ready, nil
			}
			ctx.end(context.DeadlineExceeded)
			return PanelState{}, context.DeadlineExceeded
		}}
		_, e := newResetterFor(p).Reset(ctx)
		wantResetTimeout(t, e, true, "budżet czasu resetu wyczerpany")
		if p.statuses != 2 {
			t.Errorf("po końcu kontekstu pętla nie może dalej pytać panelu (statuses=%d)", p.statuses)
		}
	})
	t.Run("niekońcowy status w pętli (PollInterval 0)", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{status: func(call int) (PanelState, error) {
			if call == 0 {
				return ready, nil
			}
			ctx.end(context.DeadlineExceeded)
			return PanelState{State: "Initializing", Green: true, Known: true}, nil
		}}
		_, e := newResetterFor(p).Reset(ctx)
		wantResetTimeout(t, e, true, "budżet czasu resetu wyczerpany")
		if p.statuses != 2 {
			t.Errorf("statuses=%d, want 2", p.statuses)
		}
	})
}

// Pierwszeństwo odpowiedzi panelu: stan, który rozstrzyga wynik, wygrywa z
// kontekstem zakończonym tuż po odebraniu odpowiedzi (piny — bez zmiany
// względem v0.7.0).
func TestResetDecisivePanelStateWinsOverEndedContext(t *testing.T) {
	t.Run("Printing przed resetem → PRINTER_BUSY", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{status: func(int) (PanelState, error) { ctx.end(context.Canceled); return printing, nil }}
		if _, e := newResetterFor(p).Reset(ctx); e == nil || e.Code != apierr.CodePrinterBusy {
			t.Fatalf("want PRINTER_BUSY, got %v", e)
		}
	})
	t.Run("Ready po resecie → sukces", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{status: func(call int) (PanelState, error) {
			if call == 1 {
				ctx.end(context.DeadlineExceeded)
			}
			return ready, nil
		}}
		out, e := newResetterFor(p).Reset(ctx)
		if e != nil || out.PanelAfter != "Ready" {
			t.Fatalf("want sukces, got %+v %v", out, e)
		}
	})
	t.Run("fault po resecie → PRINTER_OFFLINE z panel_state", func(t *testing.T) {
		ctx := newManualCtx()
		p := &hookPanel{status: func(call int) (PanelState, error) {
			if call == 1 {
				ctx.end(context.DeadlineExceeded)
			}
			return paperJam, nil
		}}
		_, e := newResetterFor(p).Reset(ctx)
		if e == nil || e.Code != apierr.CodePrinterOffline || e.Details["panel_state"] != "Paper Jam" {
			t.Fatalf("want PRINTER_OFFLINE panel_state, got %v (%v)", e, e.Details)
		}
	})
}

func newResetterFor(p PanelAPI) *PrinterResetter {
	return &PrinterResetter{Panel: p, Probe: &fakeBackend{hsOK: true}, PollInterval: 0, MaxPolls: 5}
}
