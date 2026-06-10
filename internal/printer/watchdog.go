package printer

import (
	"context"
	"log"
	"sync"
	"time"
)

// Watchdog auto-recovers the wedged-~HS state observed on hardware
// (2026-06-07): the 9100 ZPL responder stops answering (~HS transport errors)
// while TCP still connects and the engine keeps printing fine. The only cure
// is the panel reset (function.cgi?func=reset) — no human intervention is
// needed, so the agent does it itself, guarded by:
//
//   - FailThreshold consecutive ~HS transport failures (a single busy probe
//     during a print is normal),
//   - TCP :9100 still connecting (printer powered — a reset of an unplugged
//     printer is pointless),
//   - panel reporting Ready, NOT Printing (never kill an active batch),
//   - MinGap rate-limit between auto-resets (a genuinely broken printer must
//     not be reset in a loop).
type Watchdog struct {
	Probe   Prober
	Reach   Reachability
	Panel   PanelAPI
	ResetFn func(context.Context) (ResetOutcome, error)

	Interval      time.Duration // odstęp ticków pętli Run (default 60 s)
	FailThreshold int           // ile kolejnych transport-errorów ~HS uznaje za zawieszenie
	MinGap        time.Duration // minimalny odstęp między auto-resetami

	mu          sync.Mutex
	consecFails int
	autoResets  int
	lastReset   time.Time
}

// WatchdogStats is exposed via /health for observability.
type WatchdogStats struct {
	AutoResets    int    `json:"auto_resets"`
	LastAutoReset string `json:"last_auto_reset,omitempty"` // RFC3339, "" gdy nigdy
}

func (w *Watchdog) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return time.Minute
}

// Run polls until ctx is canceled. Meant for a goroutine in main.
func (w *Watchdog) Run(ctx context.Context) {
	t := time.NewTicker(w.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
		}
	}
}

// Tick performs one watchdog evaluation (exported for tests).
//
// mu guards ONLY the counters. The I/O below it — Reachable, Panel.Status and
// especially ResetFn (up to MaxPolls x PollInterval ≈ 30 s) — runs unlocked,
// so Stats() (the /health endpoint) never hangs behind an in-flight reset.
// Run drives Tick from a single goroutine, so unlocked I/O cannot interleave
// two resets.
func (w *Watchdog) Tick(ctx context.Context) {
	_, _, err := w.Probe.HostStatus(ctx)

	w.mu.Lock()
	if err == nil {
		w.consecFails = 0 // odpowiedź (nawet niesparsowalna) = responder żyje
		w.mu.Unlock()
		return
	}
	w.consecFails++
	fails := w.consecFails
	if fails < w.FailThreshold || time.Since(w.lastReset) < w.MinGap {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	// Sygnatura zawieszenia: ~HS martwe, ale TCP wstaje.
	if ok, _ := w.Reach.Reachable(ctx); !ok {
		return // drukarka wyłączona/odpięta — reset nic nie da
	}
	st, perr := w.Panel.Status(ctx)
	if perr != nil || !st.Ready() {
		return // panel martwy / Printing / fault — nie ruszaj automatem
	}

	log.Printf("watchdog: ~HS martwe od %d sond, panel Ready — auto-reset print-servera", fails)
	out, rerr := w.ResetFn(ctx)

	w.mu.Lock()
	w.lastReset = time.Now()
	w.consecFails = 0
	if rerr == nil {
		w.autoResets++
	}
	w.mu.Unlock()

	if rerr != nil {
		log.Printf("watchdog: auto-reset nieudany: %v", rerr)
		return
	}
	log.Printf("watchdog: auto-reset OK (panel=%s hs_ok=%v)", out.PanelAfter, out.HSOk)
}

func (w *Watchdog) Stats() WatchdogStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := WatchdogStats{AutoResets: w.autoResets}
	if !w.lastReset.IsZero() {
		s.LastAutoReset = w.lastReset.Format(time.RFC3339)
	}
	return s
}
