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
