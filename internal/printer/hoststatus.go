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
//
// HeadOpen/BufferFull are retained for a FUTURE ~HS dialect: on the XP-423B
// (Xprinter clone) the wiring is unconfirmed — head-up lives in the SECOND ~HS
// string (which QueryHostStatus does not yet read) and the field map must be
// validated on real hardware (spike Task 19 / hardware-spike.md item #2). Until
// then ParseHostStatus never sets them, so Healthy() must NOT gate on them (#10).
type HostStatus struct {
	PaperOut   bool
	Paused     bool
	HeadOpen   bool // not parsed yet (2nd ~HS string); see spike Task 19
	BufferFull bool // not parsed yet; see spike Task 19
	Raw        string
}

// Healthy reports physical readiness using ONLY the fields ParseHostStatus
// actually parses (PaperOut, Paused). #10: HeadOpen/BufferFull are intentionally
// excluded — they are never set yet, so gating on them would be a dead, always-
// false signal that lies in the printed/health contract. Wire them in once the
// ~HS dialect is confirmed on the XP-423B (spike Task 19).
func (h HostStatus) Healthy() bool {
	return !h.PaperOut && !h.Paused
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

	// #16: TCP is a stream — a single conn.Read can deliver only PART of ~HS
	// status string 1 (a slow/loaded printer may flush string-by-string, or a
	// segment may split it). Reading once and parsing would truncate the line to
	// <3 fields -> ok=false -> false "printed" on a real paper-out. So accumulate
	// across reads until string 1 is fully framed (ETX or CR/LF) or the deadline
	// fires / the buffer cap is hit. SetDeadline bounds the total wait.
	const maxBuf = 4096
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	var readErr error
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			// String 1 is terminated by ETX (\x03) or CR/LF. idx>0 (not >=0):
			// a real reply starts with STX, so a leading terminator is not a
			// valid end-of-string-1.
			if idx := strings.IndexAny(string(buf), "\r\n\x03"); idx > 0 {
				if hs, ok := ParseHostStatus(string(buf[:idx])); ok {
					return hs, ok, nil
				}
				// Terminator seen but <3 fields -> malformed string 1; reading
				// further won't recover it -> graceful degrade.
				return HostStatus{}, false, nil
			}
			if len(buf) >= maxBuf {
				break // chatty/garbage peer: stop growing, parse what we have
			}
		}
		if err != nil {
			readErr = err
			break // deadline / EOF / reset: parse whatever arrived
		}
	}

	// No complete first string before the deadline/EOF. If nothing arrived at
	// all, surface the transport error (verify() maps this to PRINTER_OFFLINE).
	if len(buf) == 0 {
		return HostStatus{}, false, readErr
	}
	first := buf
	if idx := strings.IndexAny(string(first), "\r\n\x03"); idx > 0 {
		first = first[:idx]
	}
	hs, ok := ParseHostStatus(string(first))
	return hs, ok, nil
}
