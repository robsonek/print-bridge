package printer

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeResetFn zlicza wywołania auto-resetu.
type fakeResetFn struct {
	calls int
	err   *ResetOutcome
}

func newWatchdog(hs *fakeBackend, panel *fakePanel, rf *fakeResetFn) *Watchdog {
	return &Watchdog{
		Probe: hs,
		Reach: hs,
		Panel: panel,
		ResetFn: func(ctx context.Context) (ResetOutcome, error) {
			rf.calls++
			return ResetOutcome{PanelAfter: "Ready", HSOk: true}, nil
		},
		FailThreshold: 3,
		MinGap:        15 * time.Minute,
	}
}

func TestWatchdogResetsAfterConsecutiveHSFailures(t *testing.T) {
	hs := &fakeBackend{reachable: true, hsErr: errors.New("i/o timeout")} // ~HS trwale martwe
	panel := &fakePanel{states: []PanelState{ready}}
	rf := &fakeResetFn{}
	w := newWatchdog(hs, panel, rf)

	for i := 0; i < 3; i++ {
		w.Tick(context.Background())
	}
	if rf.calls != 1 {
		t.Fatalf("po %d nieudanych sondach auto-reset musi odpalić raz, calls=%d", 3, rf.calls)
	}
	st := w.Stats()
	if st.AutoResets != 1 {
		t.Errorf("Stats().AutoResets = %d, want 1", st.AutoResets)
	}
}

func TestWatchdogNoResetBelowThreshold(t *testing.T) {
	hs := &fakeBackend{reachable: true, hsErr: errors.New("timeout")}
	rf := &fakeResetFn{}
	w := newWatchdog(hs, &fakePanel{states: []PanelState{ready}}, rf)

	w.Tick(context.Background())
	w.Tick(context.Background())
	if rf.calls != 0 {
		t.Errorf("2 < threshold 3: reset nie może odpalić, calls=%d", rf.calls)
	}
}

func TestWatchdogHealthyHSResetsCounter(t *testing.T) {
	hs := &fakeBackend{reachable: true, hsSeq: []hsResp{
		{err: errors.New("timeout")},
		{err: errors.New("timeout")},
		{hs: HostStatus{}, ok: true}, // wraca do życia
		{err: errors.New("timeout")},
		{err: errors.New("timeout")},
	}}
	rf := &fakeResetFn{}
	w := newWatchdog(hs, &fakePanel{states: []PanelState{ready}}, rf)

	for i := 0; i < 5; i++ {
		w.Tick(context.Background())
	}
	if rf.calls != 0 {
		t.Errorf("licznik musi się zerować po zdrowej sondzie; calls=%d", rf.calls)
	}
}

// Sygnatura zawieszenia to "~HS martwe, ale TCP żyje". Gdy NIE łączy się
// nawet TCP, drukarka jest wyłączona/odpięta — reset nic nie da.
func TestWatchdogNoResetWhenTCPDown(t *testing.T) {
	hs := &fakeBackend{reachable: false, hsErr: errors.New("timeout")}
	rf := &fakeResetFn{}
	w := newWatchdog(hs, &fakePanel{states: []PanelState{ready}}, rf)

	for i := 0; i < 5; i++ {
		w.Tick(context.Background())
	}
	if rf.calls != 0 {
		t.Errorf("drukarka offline: auto-reset bez sensu, calls=%d", rf.calls)
	}
}

// Nigdy nie resetuj w trakcie druku — panel Printing blokuje (sam ResetFn też
// ma guard, ale watchdog nie powinien nawet próbować).
func TestWatchdogNoResetWhilePrinting(t *testing.T) {
	hs := &fakeBackend{reachable: true, hsErr: errors.New("timeout")}
	rf := &fakeResetFn{}
	w := newWatchdog(hs, &fakePanel{states: []PanelState{printing}}, rf)

	for i := 0; i < 5; i++ {
		w.Tick(context.Background())
	}
	if rf.calls != 0 {
		t.Errorf("Printing: auto-reset zabroniony, calls=%d", rf.calls)
	}
}

// Auto-reset trwa do ~30 s (MaxPolls x PollInterval). Tick nie może trzymać
// przez ten czas mutexa współdzielonego ze Stats() — /health wisiałby przez
// cały reset, czyli dokładnie wtedy, gdy monitoring najbardziej chce wiedzieć,
// co się dzieje.
func TestWatchdogStatsNotBlockedByInFlightReset(t *testing.T) {
	hs := &fakeBackend{reachable: true, hsErr: errors.New("timeout")}
	started := make(chan struct{})
	block := make(chan struct{})
	w := &Watchdog{
		Probe: hs, Reach: hs, Panel: &fakePanel{states: []PanelState{ready}},
		ResetFn: func(ctx context.Context) (ResetOutcome, error) {
			close(started)
			<-block // reset "w toku"
			return ResetOutcome{PanelAfter: "Ready", HSOk: true}, nil
		},
		FailThreshold: 1, MinGap: 15 * time.Minute,
	}

	tickDone := make(chan struct{})
	go func() {
		w.Tick(context.Background())
		close(tickDone)
	}()
	<-started

	statsDone := make(chan WatchdogStats, 1)
	go func() { statsDone <- w.Stats() }()
	select {
	case <-statsDone:
		// Stats wróciło mimo wiszącego resetu — o to chodzi.
	case <-time.After(2 * time.Second):
		t.Fatal("Stats() zablokowane przez trwający auto-reset (mutex trzymany przez I/O)")
	}

	close(block)
	<-tickDone
	if st := w.Stats(); st.AutoResets != 1 {
		t.Errorf("po dokończonym resecie AutoResets = %d, want 1", st.AutoResets)
	}
}

func TestWatchdogRateLimitsResets(t *testing.T) {
	hs := &fakeBackend{reachable: true, hsErr: errors.New("timeout")} // martwe na zawsze
	rf := &fakeResetFn{}
	w := newWatchdog(hs, &fakePanel{states: []PanelState{ready}}, rf)

	for i := 0; i < 10; i++ {
		w.Tick(context.Background())
	}
	if rf.calls != 1 {
		t.Errorf("rate-limit MinGap: w jednym oknie tylko 1 auto-reset, calls=%d", rf.calls)
	}
}
