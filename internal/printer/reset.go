package printer

import (
	"context"
	"errors"
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

	// print-server jest jednowątkowy — serializuj resety. Semafor zamiast
	// mutexu (U3, od v0.8.0): czekanie na trwający reset (HTTP albo watchdoga)
	// kończy się razem z kontekstem, więc mieści się w budżecie handlera.
	semOnce sync.Once
	sem     chan struct{}
}

// acquire zajmuje semafor: wolny — zawsze (bez losowania select, więc martwy
// ctx deterministycznie trafia na sprawdzenie po zajęciu); zajęty — czeka do
// zwolnienia albo końca ctx (false).
func (r *PrinterResetter) acquire(ctx context.Context) bool {
	r.semOnce.Do(func() { r.sem = make(chan struct{}, 1) })
	select {
	case r.sem <- struct{}{}:
		return true
	default:
	}
	select {
	case r.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *PrinterResetter) release() { <-r.sem }

// timeoutError: kontekst skończył się, zanim panel rozstrzygnął wynik.
// details.reset_sent: true = żądanie func=reset zostało ROZPOCZĘTE, więc panel
// mógł je wykonać; false = na pewno nie wysłano.
func timeoutError(ctx context.Context, sent bool) *apierr.Error {
	why := "klient rozłączył się"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		why = "budżet czasu resetu wyczerpany"
	}
	what := "func=reset NIE został wysłany"
	if sent {
		what = "func=reset mógł zostać wykonany, a panel nie potwierdził Ready — sprawdź panel przed ponowieniem"
	}
	return apierr.New(apierr.CodePrintTimeout, why+" — "+what, 503).WithDetail("reset_sent", sent)
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
//
// U3 (od v0.8.0): koniec ctx (budżet handlera albo rozłączenie klienta) daje
// PRINT_TIMEOUT z details.reset_sent — ale tylko tam, gdzie wynik nie jest
// jeszcze rozstrzygnięty. Stan panelu, który rozstrzyga (Printing przed
// resetem, Ready albo fault po resecie), wygrywa z martwym ctx: to fakt o
// drukarce, odebrany zanim skończyliśmy.
func (r *PrinterResetter) Reset(ctx context.Context) (ResetOutcome, *apierr.Error) {
	if !r.acquire(ctx) {
		return ResetOutcome{}, timeoutError(ctx, false)
	}
	defer r.release()
	if ctx.Err() != nil {
		return ResetOutcome{}, timeoutError(ctx, false)
	}

	before, err := r.Panel.Status(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ResetOutcome{}, timeoutError(ctx, false)
		}
		return ResetOutcome{}, apierr.New(apierr.CodePrinterOffline,
			"panel drukarki (status.cgi) niedostępny: "+err.Error(), 503)
	}
	if before.Printing() {
		return ResetOutcome{}, apierr.New(apierr.CodePrinterBusy,
			"druk w toku — reset przerwałby aktywny batch; spróbuj po zakończeniu", 409)
	}
	if ctx.Err() != nil { // ostatnia chwila, by NIE wysłać func=reset
		return ResetOutcome{}, timeoutError(ctx, false)
	}

	if err := r.Panel.Reset(ctx); err != nil {
		if ctx.Err() != nil {
			return ResetOutcome{}, timeoutError(ctx, true)
		}
		return ResetOutcome{}, apierr.New(apierr.CodePrinterOffline,
			"func=reset nie powiódł się: "+err.Error(), 503)
	}

	out := ResetOutcome{PanelBefore: before.State}
	for i := 0; i < r.maxPolls(); i++ {
		if r.pollInterval() > 0 {
			select {
			case <-ctx.Done():
				return out, timeoutError(ctx, true)
			case <-time.After(r.pollInterval()):
			}
		}
		st, err := r.Panel.Status(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return out, timeoutError(ctx, true)
			}
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
		if ctx.Err() != nil { // niekońcowy stan, a czas minął
			return out, timeoutError(ctx, true)
		}
	}
	if ctx.Err() != nil {
		return out, timeoutError(ctx, true)
	}
	return out, apierr.New(apierr.CodePrinterOffline,
		"panel nie wrócił do Ready w budżecie po resecie ("+strconv.Itoa(r.maxPolls())+" prób)", 503)
}
