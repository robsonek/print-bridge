package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CUPS backend exit codes (cups/backend.h).
const (
	exitOK        = 0 // CUPS_BACKEND_OK
	exitFailed    = 1 // CUPS_BACKEND_FAILED
	exitStopQueue = 4 // CUPS_BACKEND_STOP
	exitRetry     = 6 // CUPS_BACKEND_RETRY
)

// errConfig marks operator-fixable configuration mistakes (bad device-uri).
// They map to CUPS_BACKEND_STOP: retrying a typo forever would silently hang the
// queue, stopping it surfaces the problem in lpstat immediately (fail-loud).
var errConfig = errors.New("config error")

type backendConfig struct {
	addr    string        // host:port print-servera
	queue   string        // kolejka LPD na print-serverze (np. "lp")
	rateBps int           // pacing danych w B/s; 0 = bez pacingu
	chunk   int           // chunk wysyłki; 0 = default klienta (1448)
	timeout time.Duration // per-I/O timeout; 0 = default klienta (30 s)
}

// parseDeviceURI parses lpdpaced://host[:port]/queue?rate=&chunk=&timeout=.
//
// rate defaults to 20000 B/s: the XP-423B print-server's pathology cliff sits
// between 40 and 60 KB/s (measured 2026-06-06, docs/hardware-spike-findings.md),
// so 20 KB/s keeps a 2x margin while still outrunning the ~6.6 KB/s print engine.
func parseDeviceURI(uri string) (backendConfig, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return backendConfig{}, fmt.Errorf("%w: device-uri %q: %v", errConfig, uri, err)
	}
	if u.Scheme != "lpdpaced" {
		return backendConfig{}, fmt.Errorf("%w: scheme %q, oczekiwany lpdpaced://", errConfig, u.Scheme)
	}
	if u.Hostname() == "" {
		return backendConfig{}, fmt.Errorf("%w: brak hosta w device-uri %q", errConfig, uri)
	}
	queue := strings.TrimPrefix(u.Path, "/")
	if queue == "" {
		return backendConfig{}, fmt.Errorf("%w: brak kolejki LPD w device-uri %q (oczekiwane lpdpaced://host/kolejka)", errConfig, uri)
	}
	port := u.Port()
	if port == "" {
		port = "515"
	}

	cfg := backendConfig{
		addr:    net.JoinHostPort(u.Hostname(), port),
		queue:   queue,
		rateBps: 20000,
	}
	q := u.Query()
	if v := q.Get("rate"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return backendConfig{}, fmt.Errorf("%w: rate=%q (B/s, >=0)", errConfig, v)
		}
		cfg.rateBps = n
	}
	if v := q.Get("chunk"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return backendConfig{}, fmt.Errorf("%w: chunk=%q (bajty, >0)", errConfig, v)
		}
		cfg.chunk = n
	}
	if v := q.Get("timeout"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return backendConfig{}, fmt.Errorf("%w: timeout=%q (sekundy, >0)", errConfig, v)
		}
		cfg.timeout = time.Duration(n) * time.Second
	}
	return cfg, nil
}

// buildPayload realizes copies by replicating the stream — same rationale as the
// agent's buildSubmitPayload (#14): each ^XA..^XZ is self-contained, raw ZPL has
// no honored quantity attribute on this path.
func buildPayload(data []byte, copies int) []byte {
	if copies <= 1 {
		return data
	}
	return bytes.Repeat(data, copies)
}

func exitCodeFor(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errConfig):
		return exitStopQueue
	default:
		// Sieć/protokół: niekompletny job LPD print-server odrzuca (zweryfikowane
		// na sprzęcie), więc ponowienie całości jest bezpieczne — cupsd ponowi.
		return exitRetry
	}
}

func discoveryLine() string {
	return `network lpdpaced "Unknown" "LPD z pacingiem (print-bridge)"`
}
