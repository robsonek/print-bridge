package printer

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"time"
)

// HostStatus is the physical printer state derived from a ZPL ~HS response.
// This is the authoritative physical signal: a socket:9100 backend gives CUPS no
// back-channel, so CUPS printer-state-reasons are unreliable.
//
// Field map CONFIRMED on a real XP-423B (2026-06-06, recorded frames in
// hoststatus_test.go; closes spike Task 19 / MED #10):
//
//	string 1: aaa,b,c,dddd,eee,f,...  -> [1] paper out, [2] pause,
//	          [4] formats in receive buffer, [5] buffer full
//	string 2: mmm,n,o,...             -> [2] head open (flip 0->1 verified
//	          with the head physically open)
//
// String 2 field [8] ("labels remaining in batch" in the Zebra spec) is
// deliberately NOT parsed: on this clone firmware it holds junk at idle
// (recorded in one session: 00000000, then 1119879168, then a stable 1334273
// after a head open/close cycle). Gating Draining() on it would leave every
// job "draining" forever -> false PRINT_TIMEOUT. Raw2 keeps the raw string
// for diagnostics.
type HostStatus struct {
	PaperOut      bool
	Paused        bool
	HeadOpen      bool // ~HS string 2 field [2]
	BufferFull    bool // ~HS string 1 field [5]
	QueuedFormats int  // ~HS string 1 field [4]: formats waiting in the receive buffer
	Raw           string
	Raw2          string // ~HS string 2 ("" when the reply carried only string 1)
}

// Healthy reports physical readiness. PaperOut/Paused/HeadOpen are faults;
// HeadOpen entered the gate once the string-2 wiring was confirmed on hardware
// (it used to be excluded as a never-set field, #10). BufferFull and the drain
// counters are deliberately NOT gated: they signal "busy printing", not a fault.
func (h HostStatus) Healthy() bool {
	return !h.PaperOut && !h.Paused && !h.HeadOpen
}

// Draining reports that the printer still holds undone work: formats waiting
// in the receive buffer. With the paced LPD backend a CUPS job completes when
// the DATA is delivered (~3 s), while the engine keeps printing for N×~5 s —
// Draining()==false means at most the LAST format is still in the engine.
// (The batch counter from string 2 would close that gap, but it is junk on
// this firmware — see the HostStatus doc.)
func (h HostStatus) Draining() bool {
	return h.QueuedFormats > 0
}

// ParseHostStatus parses string 1 of a Zebra ~HS reply (comma-separated).
// Tolerant: ok=false when not parseable, which the caller maps to graceful
// degrade (best-effort printed).
func ParseHostStatus(line string) (HostStatus, bool) {
	line = strings.TrimSpace(strings.Trim(line, "\x02\x03\r\n"))
	fields := strings.Split(line, ",")
	if len(fields) < 3 {
		return HostStatus{}, false
	}
	hs := HostStatus{
		PaperOut: fields[1] == "1",
		Paused:   fields[2] == "1",
		Raw:      line,
	}
	if len(fields) > 4 {
		hs.QueuedFormats, _ = strconv.Atoi(fields[4])
	}
	if len(fields) > 5 {
		hs.BufferFull = fields[5] == "1"
	}
	return hs, true
}

// ParseHostStatusReply parses a full (possibly multi-string) ~HS reply. String 1
// is required (ok=false without it); string 2 is optional — when absent its
// fields stay zero, so a string-1-only dialect degrades gracefully instead of
// inventing faults.
func ParseHostStatusReply(reply string) (HostStatus, bool) {
	var lines []string
	for _, l := range strings.FieldsFunc(reply, func(r rune) bool {
		return r == '\x03' || r == '\r' || r == '\n'
	}) {
		l = strings.TrimSpace(strings.Trim(l, "\x02"))
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return HostStatus{}, false
	}
	hs, ok := ParseHostStatus(lines[0])
	if !ok {
		return HostStatus{}, false
	}
	if len(lines) > 1 {
		hs.Raw2 = lines[1]
		fields := strings.Split(lines[1], ",")
		if len(fields) > 2 {
			hs.HeadOpen = fields[2] == "1"
		}
	}
	return hs, true
}

// QueryHostStatus opens a short bidirectional TCP connection to the printer,
// sends ~HS, and parses the reply (strings 1 and 2).
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

	// #16: TCP is a stream — accumulate across reads. Stop once TWO framed
	// strings arrived (string 2 carries head-open + labels-remaining). After
	// string 1 is framed, shrink the read deadline: a dialect that sends only
	// one string must not hang the probe for the full timeout.
	const maxBuf = 4096
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	var readErr error
	sawFirst := false
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if bytes.Count(buf, []byte{0x03}) >= 2 {
				break
			}
			if !sawFirst {
				if idx := strings.IndexAny(string(buf), "\r\n\x03"); idx > 0 {
					sawFirst = true
					_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
				}
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

	// If nothing arrived at all, surface the transport error (verify() maps
	// this to busy-retry and ultimately PRINTER_OFFLINE).
	if len(buf) == 0 {
		return HostStatus{}, false, readErr
	}
	hs, ok := ParseHostStatusReply(string(buf))
	return hs, ok, nil
}
