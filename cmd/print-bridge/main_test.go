package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/robsonek/print-bridge/internal/printer"
)

type fakeReach struct {
	online bool
	err    error
}

func (f fakeReach) Reachable(context.Context) (bool, error) { return f.online, f.err }

type fakeHS struct {
	hs  printer.HostStatus
	ok  bool
	err error
}

func (f fakeHS) HostStatus(context.Context) (printer.HostStatus, bool, error) {
	return f.hs, f.ok, f.err
}

type fakeReasons struct {
	reasons []string
	err     error
}

func (f fakeReasons) PrinterReasons(context.Context) ([]string, error) {
	return f.reasons, f.err
}

func TestMakeHealth(t *testing.T) {
	cases := []struct {
		name       string
		reach      fakeReach
		hs         fakeHS
		reasons    fakeReasons
		wantStatus int
		wantState  string
	}{
		{
			name:       "all healthy",
			reach:      fakeReach{online: true},
			hs:         fakeHS{hs: printer.HostStatus{}, ok: true},
			reasons:    fakeReasons{reasons: []string{"none"}},
			wantStatus: http.StatusOK,
			wantState:  "ok",
		},
		{
			// #20 regression: cupsd down (IPP transport error), :9100 up, ~HS
			// unsupported -> must degrade (was wrongly "ok" before the fix).
			name:       "cups unreachable degrades",
			reach:      fakeReach{online: true},
			hs:         fakeHS{ok: false}, // printer doesn't speak ~HS
			reasons:    fakeReasons{err: errors.New("dial tcp localhost:631: connection refused")},
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "degraded",
		},
		{
			name:       "printer offline degrades",
			reach:      fakeReach{online: false},
			hs:         fakeHS{ok: false},
			reasons:    fakeReasons{reasons: []string{"none"}},
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "degraded",
		},
		{
			name:       "hs unhealthy degrades",
			reach:      fakeReach{online: true},
			hs:         fakeHS{hs: printer.HostStatus{PaperOut: true}, ok: true},
			reasons:    fakeReasons{reasons: []string{"none"}},
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "degraded",
		},
		{
			// ~HS unsupported but cupsd reachable and printer online -> still ok.
			name:       "hs unsupported but reachable stays ok",
			reach:      fakeReach{online: true},
			hs:         fakeHS{ok: false},
			reasons:    fakeReasons{reasons: []string{"none"}},
			wantStatus: http.StatusOK,
			wantState:  "ok",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn := makeHealth(tc.reach, tc.hs, tc.reasons)
			status, body := fn(context.Background())
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			m, ok := body.(map[string]any)
			if !ok {
				t.Fatalf("body is not a map: %T", body)
			}
			if m["status"] != tc.wantState {
				t.Errorf("status field = %v, want %q", m["status"], tc.wantState)
			}
		})
	}
}

// #20: on an IPP error the body must carry cups_error and cups_reachable=false
// so the 5-min health poll/Signal alert has a diagnosable reason.
func TestMakeHealthExposesCupsError(t *testing.T) {
	fn := makeHealth(
		fakeReach{online: true},
		fakeHS{ok: false},
		fakeReasons{err: errors.New("connection refused")},
	)
	_, body := fn(context.Background())
	m := body.(map[string]any)
	if m["cups_reachable"] != false {
		t.Errorf("cups_reachable = %v, want false", m["cups_reachable"])
	}
	if _, ok := m["cups_error"]; !ok {
		t.Error("body must include cups_error when PrinterReasons fails")
	}
}

// MED #10 domknięte: /health musi raportować head_open (z linii 2 ~HS) oraz
// liczniki drenażu (queued_formats, labels_remaining) i degradować się przy
// otwartej głowicy — wcześniej zwracał "ok" z fizycznie otwartą pokrywą
// (zweryfikowane na sprzęcie w spike'u).
func TestMakeHealthHeadOpenDegradesAndIsExposed(t *testing.T) {
	fn := makeHealth(
		fakeReach{online: true},
		fakeHS{hs: printer.HostStatus{HeadOpen: true, QueuedFormats: 2, BatchRemaining: 1334273, Raw2: "000,0,1,0,0,2,0,0,01334273,1,000"}, ok: true},
		fakeReasons{reasons: []string{"none"}},
	)
	status, body := fn(context.Background())
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (head open is a fault)", status)
	}
	m := body.(map[string]any)
	if m["head_open"] != true {
		t.Errorf("head_open = %v, want true", m["head_open"])
	}
	if m["queued_formats"] != 2 {
		t.Errorf("queued_formats = %v, want 2", m["queued_formats"])
	}
	if m["host_status_2"] != "000,0,1,0,0,2,0,0,01334273,1,000" {
		t.Errorf("host_status_2 = %v, want raw line 2 for diagnostics", m["host_status_2"])
	}
	if m["batch_remaining"] != 1334273 {
		t.Errorf("batch_remaining = %v, want raw 1334273 (diagnostyka, surowa wartość)", m["batch_remaining"])
	}
}
