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
