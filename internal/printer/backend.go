package printer

import (
	"context"
	"net"
	"strings"
	"time"
)

// SocketReachability checks the printer TCP port and (optionally) CUPS queue pause.
type SocketReachability struct {
	Addr    string // <printer_ip>:9100
	Timeout time.Duration
	Reasons func(context.Context) ([]string, error) // optional: CUPS PrinterReasons
}

func (s *SocketReachability) Reachable(ctx context.Context) (bool, error) {
	d := net.Dialer{Timeout: s.Timeout}
	conn, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return false, nil
	}
	_ = conn.Close()
	return true, nil
}

// QueuePaused is a DELIBERATE best-effort, fail-open precheck (#21 accepted risk).
// It returns (false, nil) both when Reasons is unwired (unit tests; prod always
// wires cups.PrinterReasons) and when the IPP PrinterReasons query errors. This
// fail-open is intentional and has NO correctness impact: it is only a fast-fail
// hint. The AUTHORITATIVE physical signal is the ~HS probe in verify(); a queue
// that is genuinely paused/disabled is still caught downstream — a disabled queue
// never reaches JobCompleted -> pollAndVerify returns PRINT_TIMEOUT, and a
// rejecting queue fails Submit -> CUPS_UNAVAILABLE. So a missed pause here can
// never produce a false "printed". (socket:9100 gives CUPS no back-channel, so
// printer-state-reasons are advisory only — see hoststatus.go.)
func (s *SocketReachability) QueuePaused(ctx context.Context) (bool, error) {
	if s.Reasons == nil {
		return false, nil
	}
	reasons, err := s.Reasons(ctx)
	if err != nil {
		return false, nil // unknown -> don't block; ~HS/job-state will catch real faults
	}
	for _, r := range reasons {
		if strings.Contains(r, "paused") || strings.Contains(r, "stopped") {
			return true, nil
		}
	}
	return false, nil
}

// HSProbe adapts QueryHostStatus to the Prober interface.
type HSProbe struct {
	Addr    string // <printer_ip>:9100
	Timeout time.Duration
}

func (h *HSProbe) HostStatus(ctx context.Context) (HostStatus, bool, error) {
	return QueryHostStatus(ctx, h.Addr, h.Timeout)
}
