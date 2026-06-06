package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseDeviceURIFull(t *testing.T) {
	cfg, err := parseDeviceURI("lpdpaced://192.168.1.75/lp?rate=25000&chunk=1000&timeout=10")
	if err != nil {
		t.Fatalf("parseDeviceURI: %v", err)
	}
	if cfg.addr != "192.168.1.75:515" {
		t.Errorf("addr = %q, want 192.168.1.75:515", cfg.addr)
	}
	if cfg.queue != "lp" {
		t.Errorf("queue = %q, want lp", cfg.queue)
	}
	if cfg.rateBps != 25000 {
		t.Errorf("rateBps = %d, want 25000", cfg.rateBps)
	}
	if cfg.chunk != 1000 {
		t.Errorf("chunk = %d, want 1000", cfg.chunk)
	}
	if cfg.timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", cfg.timeout)
	}
}

func TestParseDeviceURIDefaults(t *testing.T) {
	cfg, err := parseDeviceURI("lpdpaced://10.0.0.5/lp")
	if err != nil {
		t.Fatalf("parseDeviceURI: %v", err)
	}
	if cfg.addr != "10.0.0.5:515" {
		t.Errorf("addr = %q, want 10.0.0.5:515", cfg.addr)
	}
	// Default = 20000 B/s: klif patologii print-servera leży między 40 a 60 KB/s
	// (pomiar 2026-06-06), 20 KB/s daje 2x margines.
	if cfg.rateBps != 20000 {
		t.Errorf("rateBps = %d, want default 20000", cfg.rateBps)
	}
	if cfg.chunk != 0 {
		t.Errorf("chunk = %d, want 0 (klient dobierze)", cfg.chunk)
	}
}

func TestParseDeviceURICustomPort(t *testing.T) {
	cfg, err := parseDeviceURI("lpdpaced://10.0.0.5:5515/raw")
	if err != nil {
		t.Fatalf("parseDeviceURI: %v", err)
	}
	if cfg.addr != "10.0.0.5:5515" {
		t.Errorf("addr = %q, want 10.0.0.5:5515", cfg.addr)
	}
	if cfg.queue != "raw" {
		t.Errorf("queue = %q, want raw", cfg.queue)
	}
}

func TestParseDeviceURIExplicitRateZeroDisablesPacing(t *testing.T) {
	cfg, err := parseDeviceURI("lpdpaced://10.0.0.5/lp?rate=0")
	if err != nil {
		t.Fatalf("parseDeviceURI: %v", err)
	}
	if cfg.rateBps != 0 {
		t.Errorf("rateBps = %d, want 0 (wyłączony pacing)", cfg.rateBps)
	}
}

func TestParseDeviceURIErrors(t *testing.T) {
	cases := []struct {
		name, uri string
	}{
		{"zły scheme", "lpd://10.0.0.5/lp"},
		{"brak hosta", "lpdpaced:///lp"},
		{"brak kolejki", "lpdpaced://10.0.0.5"},
		{"pusta kolejka", "lpdpaced://10.0.0.5/"},
		{"rate nieliczbowy", "lpdpaced://10.0.0.5/lp?rate=szybko"},
		{"rate ujemny", "lpdpaced://10.0.0.5/lp?rate=-5"},
		{"chunk nieliczbowy", "lpdpaced://10.0.0.5/lp?chunk=xl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDeviceURI(tc.uri)
			if err == nil {
				t.Fatalf("parseDeviceURI(%q): brak błędu", tc.uri)
			}
			if !errors.Is(err, errConfig) {
				t.Errorf("err = %v, want errConfig (operator musi poprawić device-uri)", err)
			}
		})
	}
}

func TestBuildPayloadCopies(t *testing.T) {
	data := []byte("^XA^XZ")
	if got := buildPayload(data, 1); !bytes.Equal(got, data) {
		t.Errorf("copies=1: payload zmieniony")
	}
	if got := buildPayload(data, 3); !bytes.Equal(got, bytes.Repeat(data, 3)) {
		t.Errorf("copies=3: payload = %q", got)
	}
	if got := buildPayload(data, 0); !bytes.Equal(got, data) {
		t.Errorf("copies=0: traktuj jak 1, payload = %q", got)
	}
}

func TestExitCodeFor(t *testing.T) {
	if got := exitCodeFor(nil); got != exitOK {
		t.Errorf("nil err -> %d, want %d", got, exitOK)
	}
	_, cfgErr := parseDeviceURI("lpd://x/y")
	if got := exitCodeFor(cfgErr); got != exitStopQueue {
		t.Errorf("config err -> %d, want %d (STOP: operator naprawia URI)", got, exitStopQueue)
	}
	if got := exitCodeFor(errors.New("lpd: dial: connection refused")); got != exitRetry {
		t.Errorf("net err -> %d, want %d (RETRY: cupsd ponowi)", got, exitRetry)
	}
}

func TestDiscoveryLine(t *testing.T) {
	line := discoveryLine()
	if !strings.HasPrefix(line, "network lpdpaced ") {
		t.Errorf("discovery = %q, want prefix 'network lpdpaced '", line)
	}
}
