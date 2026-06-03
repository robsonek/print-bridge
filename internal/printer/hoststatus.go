package printer

import (
	"context"
	"net"
	"strings"
	"time"
)

// HostStatus is the physical printer state derived from a ZPL ~HS response.
// This is the authoritative physical signal: a socket:9100 backend gives CUPS no
// back-channel, so CUPS printer-state-reasons are unreliable.
type HostStatus struct {
	PaperOut   bool
	Paused     bool
	HeadOpen   bool
	BufferFull bool
	Raw        string
}

func (h HostStatus) Healthy() bool {
	return !h.PaperOut && !h.Paused && !h.HeadOpen && !h.BufferFull
}

// ParseHostStatus parses the first string of a Zebra ~HS reply (comma-separated).
// Field index 1 = paper out, 2 = pause. Tolerant: ok=false when not parseable,
// which the caller maps to graceful degrade (best-effort printed).
func ParseHostStatus(line string) (HostStatus, bool) {
	line = strings.TrimSpace(strings.Trim(line, "\x02\x03\r\n"))
	fields := strings.Split(line, ",")
	if len(fields) < 3 {
		return HostStatus{}, false
	}
	return HostStatus{
		PaperOut: fields[1] == "1",
		Paused:   fields[2] == "1",
		Raw:      line,
	}, true
}

// QueryHostStatus opens a short bidirectional TCP connection to the printer,
// sends ~HS, and parses the first reply line.
func QueryHostStatus(ctx context.Context, addr string, timeout time.Duration) (HostStatus, bool, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return HostStatus{}, false, err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte("~HS")); err != nil {
		return HostStatus{}, false, err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return HostStatus{}, false, err
	}
	first := buf[:n]
	if idx := strings.IndexAny(string(first), "\r\n\x03"); idx > 0 {
		first = first[:idx]
	}
	hs, ok := ParseHostStatus(string(first))
	return hs, ok, nil
}
