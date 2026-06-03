package printer

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestParseHostStatusPaperOut(t *testing.T) {
	// Zebra ~HS line 1: aaa,b,c,... where field[1]=paper out (1), field[2]=pause (1).
	hs, ok := ParseHostStatus("030,1,0,1234,000,0,0,0,000,0,0,0")
	if !ok {
		t.Fatal("parse should succeed for a well-formed line")
	}
	if !hs.PaperOut {
		t.Error("PaperOut should be true (field[1]=1)")
	}
	if hs.Paused {
		t.Error("Paused should be false (field[2]=0)")
	}
}

func TestParseHostStatusHealthy(t *testing.T) {
	hs, ok := ParseHostStatus("030,0,0,1234,000,0,0,0,000,0,0,0")
	if !ok || hs.PaperOut || hs.Paused {
		t.Errorf("healthy parse wrong: %+v ok=%v", hs, ok)
	}
	if !hs.Healthy() {
		t.Error("Healthy() should be true when no fault flags set")
	}
}

func TestParseHostStatusUnparseable(t *testing.T) {
	if _, ok := ParseHostStatus("garbage"); ok {
		t.Error("unparseable input must report ok=false (triggers graceful degrade)")
	}
}

// #10 regression: Healthy() must reflect ONLY fields ParseHostStatus actually
// parses (PaperOut, Paused). HeadOpen/BufferFull are kept in the struct for a
// future ~HS dialect spike but are never set, so gating Healthy() on them would
// be a dead/false signal. A status with PaperOut=false, Paused=false must be
// Healthy even if a (currently unparsed) HeadOpen field were set.
func TestHealthyOnlyConsidersPaperOutAndPaused(t *testing.T) {
	if !(HostStatus{}).Healthy() {
		t.Error("zero-value HostStatus must be Healthy")
	}
	// HeadOpen/BufferFull are not gated -> still Healthy when only they are set.
	if !(HostStatus{HeadOpen: true}).Healthy() {
		t.Error("HeadOpen must NOT gate Healthy() (unparsed, future-only field)")
	}
	if !(HostStatus{BufferFull: true}).Healthy() {
		t.Error("BufferFull must NOT gate Healthy() (unparsed, future-only field)")
	}
	if (HostStatus{PaperOut: true}).Healthy() {
		t.Error("PaperOut must gate Healthy()")
	}
	if (HostStatus{Paused: true}).Healthy() {
		t.Error("Paused must gate Healthy()")
	}
}

// #16 regression: QueryHostStatus must accumulate across multiple TCP reads.
// TCP is a stream: a slow/loaded printer may flush the ~HS reply in pieces, so a
// single conn.Read can truncate status string 1 (-> ok=false -> false "printed"
// on a real paper-out). A reply delivered in two chunks must still parse fully.
func TestQueryHostStatusReassemblesChunkedReply(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Paper-out reply (field[1]=1) split mid-line across two writes.
	const part1 = "030,1"
	const part2 = ",0,1234,000,0,0,0,000,0,0,0\x03\r\n"

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 8)
		_, _ = conn.Read(buf) // consume the ~HS request
		_, _ = conn.Write([]byte(part1))
		time.Sleep(20 * time.Millisecond) // force a separate TCP segment
		_, _ = conn.Write([]byte(part2))
	}()

	hs, ok, err := QueryHostStatus(context.Background(), ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("QueryHostStatus: %v", err)
	}
	if !ok {
		t.Fatal("chunked reply must parse to ok=true after reassembly")
	}
	if !hs.PaperOut {
		t.Errorf("paper-out (field[1]=1) must survive chunked read, got %+v", hs)
	}
}
