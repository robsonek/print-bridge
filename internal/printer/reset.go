package printer

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/robsonek/print-bridge/internal/apierr"
)

// PanelAPI is the WebPanel surface used by the resetter and the watchdog
// (interface for tests).
type PanelAPI interface {
	Status(context.Context) (PanelState, error)
	Reset(context.Context) error
}

// ResetOutcome is the wire-facing result of a printer reset.
type ResetOutcome struct {
	PanelBefore string `json:"panel_before"`
	PanelAfter  string `json:"panel_after"`
	HSOk        bool   `json:"hs_ok"`
}

// PrinterResetter performs the spike-proven recovery: function.cgi?func=reset
// clears latched faults (Paper Jam after a roll change does NOT auto-recover)
// and a wedged 9100 responder, and resumes a buffered pending job. Guard: never
// reset while the engine is printing — that would lose the active batch.
type PrinterResetter struct {
	Panel        PanelAPI
	Probe        Prober        // best-effort ~HS check po resecie
	PollInterval time.Duration // odstęp poll'a po resecie; 0 tylko w testach
	MaxPolls     int           // ile prób czekania aż panel wróci do Ready

	mu sync.Mutex // print-server jest jednowątkowy — serializuj resety
}

func (r *PrinterResetter) pollInterval() time.Duration {
	if r.PollInterval > 0 {
		return r.PollInterval
	}
	return 0
}

func (r *PrinterResetter) maxPolls() int {
	if r.MaxPolls > 0 {
		return r.MaxPolls
	}
	return 15
}

// Reset checks the panel, refuses while printing, triggers func=reset and
// waits until the panel reports Ready again. Transport errors right after the
// reset are part of the restart (~1 s HTTP blackout) and are tolerated within
// the poll budget.
func (r *PrinterResetter) Reset(ctx context.Context) (ResetOutcome, *apierr.Error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	before, err := r.Panel.Status(ctx)
	if err != nil {
		return ResetOutcome{}, apierr.New(apierr.CodePrinterOffline,
			"panel drukarki (status.cgi) niedostępny: "+err.Error(), 503)
	}
	if before.Printing() {
		return ResetOutcome{}, apierr.New(apierr.CodePrinterBusy,
			"druk w toku — reset przerwałby aktywny batch; spróbuj po zakończeniu", 409)
	}

	if err := r.Panel.Reset(ctx); err != nil {
		return ResetOutcome{}, apierr.New(apierr.CodePrinterOffline,
			"func=reset nie powiódł się: "+err.Error(), 503)
	}

	out := ResetOutcome{PanelBefore: before.State}
	for i := 0; i < r.maxPolls(); i++ {
		if r.pollInterval() > 0 {
			select {
			case <-ctx.Done():
				return out, apierr.New(apierr.CodePrintTimeout, "context canceled while waiting for panel", 503)
			case <-time.After(r.pollInterval()):
			}
		}
		st, err := r.Panel.Status(ctx)
		if err != nil {
			continue // restartowy blackout HTTP — czekaj dalej
		}
		if st.Ready() {
			out.PanelAfter = st.State
			// Best-effort: czy 9100/~HS też wróciło. Brak odpowiedzi NIE
			// unieważnia resetu — panel jest autorytatywny.
			if _, ok, err := r.Probe.HostStatus(ctx); err == nil && ok {
				out.HSOk = true
			}
			return out, nil
		}
		if st.Fault() {
			return out, apierr.New(apierr.CodePrinterOffline,
				"po resecie panel raportuje fault: "+st.State, 503).
				WithDetail("panel_state", st.State)
		}
	}
	return out, apierr.New(apierr.CodePrinterOffline,
		"panel nie wrócił do Ready w budżecie po resecie ("+strconv.Itoa(r.maxPolls())+" prób)", 503)
}
