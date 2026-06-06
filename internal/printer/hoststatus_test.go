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

// Healthy(): PaperOut/Paused/HeadOpen są faultami. HeadOpen wszedł do gate'a po
// POTWIERDZENIU dialektu na sprzęcie (spike: linia2[2] flip 0→1 przy otwartej
// głowicy; pomiar idle 2026-06-06: linia2 obecna i parsowalna) — wcześniejszy
// guard #10 (nie gate'ować nigdy-nieustawianych pól) przestał obowiązywać dla
// HeadOpen. BufferFull NADAL nie gate'uje: to sygnał przepływu (busy przy dużym
// jobie), nie awaria.
func TestHealthyGatesPaperOutPausedHeadOpen(t *testing.T) {
	if !(HostStatus{}).Healthy() {
		t.Error("zero-value HostStatus must be Healthy")
	}
	if (HostStatus{HeadOpen: true}).Healthy() {
		t.Error("HeadOpen must gate Healthy() (dialect confirmed on hardware)")
	}
	if !(HostStatus{BufferFull: true}).Healthy() {
		t.Error("BufferFull must NOT gate Healthy() (busy/flow signal, not a fault)")
	}
	if (HostStatus{PaperOut: true}).Healthy() {
		t.Error("PaperOut must gate Healthy()")
	}
	if (HostStatus{Paused: true}).Healthy() {
		t.Error("Paused must gate Healthy()")
	}
}

// Ramka nagrana z realnego XP-423B w spoczynku (2026-06-06, port 9100, ~HS):
// 3 linie STX..ETX CR LF. Linia 1[4]=formaty w buforze odbiorczym, [5]=buffer
// full; linia 2[2]=head-open. UWAGA: pole [8] linii 2 ("labels remaining" wg
// spec Zebry) klon używa jako licznika mediów wpisywanego po cyklu głowicy
// (reprodukcja 2x: idle 00000000 -> cykl głowicy -> stabilne 01334273;
// delta dzień-do-dnia = dokładnie 1 etykieta+gap, 1235 = ^LL 1219 + 16).
// Niezerowe przy idle po każdej wymianie rolki -> NIE parsować semantycznie;
// tylko surowe Raw2 do diagnostyki.
const recordedIdleHS = "\x02150,0,0,1219,000,0,0,0,000,0,0,0\x03\r\n" +
	"\x02000,0,0,0,0,2,0,0,00000000,1,000\x03\r\n" +
	"\x028888,0\x03\r\n"

func TestParseHostStatusReplyRecordedIdleFrame(t *testing.T) {
	hs, ok := ParseHostStatusReply(recordedIdleHS)
	if !ok {
		t.Fatal("recorded idle frame must parse")
	}
	if hs.PaperOut || hs.Paused || hs.HeadOpen || hs.BufferFull {
		t.Errorf("idle frame must carry no faults: %+v", hs)
	}
	if hs.QueuedFormats != 0 {
		t.Errorf("idle frame: queued=%d, want 0", hs.QueuedFormats)
	}
	if hs.Draining() {
		t.Error("idle frame must not be Draining()")
	}
	if !hs.Healthy() {
		t.Error("idle frame must be Healthy()")
	}
	if hs.Raw != "150,0,0,1219,000,0,0,0,000,0,0,0" {
		t.Errorf("Raw must stay line 1, got %q", hs.Raw)
	}
	if hs.Raw2 != "000,0,0,0,0,2,0,0,00000000,1,000" {
		t.Errorf("Raw2 must carry line 2 for diagnostics, got %q", hs.Raw2)
	}
}

// Regresja klonowego firmware: pole [8] linii 2 jest niezerowe przy IDLE po
// każdym cyklu głowicy/wymianie rolki (nagrane na sprzęcie: 01334273 —
// licznik mediów, nie "labels remaining"). Draining() NIE może na nim polegać
// — inaczej każdy druk po wymianie rolki kończyłby się wiecznym drenażem
// i fałszywym PRINT_TIMEOUT.
func TestParseHostStatusReplyIgnoresJunkLine2Counter(t *testing.T) {
	reply := "\x02150,0,0,1219,000,0,0,0,000,0,0,0\x03\r\n" +
		"\x02000,0,0,0,0,2,0,0,1334273,1,000\x03\r\n" +
		"\x028888,0\x03\r\n"
	hs, ok := ParseHostStatusReply(reply)
	if !ok {
		t.Fatal("junk-counter frame must parse")
	}
	if hs.Draining() {
		t.Error("junk in line2[8] must NOT make the status Draining()")
	}
	if !hs.Healthy() {
		t.Error("junk in line2[8] must NOT make the status unhealthy")
	}
}

// Spike (2026-06-06): otwarta głowica → linia 2 pole [2] przechodzi 0→1,
// linia 1 BEZ zmian — to jest luka MED #10 (agent ślepy na otwartą głowicę).
func TestParseHostStatusReplyHeadOpen(t *testing.T) {
	reply := "\x02150,0,0,1219,000,0,0,0,000,0,0,0\x03\r\n" +
		"\x02000,0,1,0,0,2,0,0,00000000,1,000\x03\r\n" +
		"\x028888,0\x03\r\n"
	hs, ok := ParseHostStatusReply(reply)
	if !ok {
		t.Fatal("head-open frame must parse")
	}
	if !hs.HeadOpen {
		t.Error("HeadOpen must be true (line2[2]=1)")
	}
	if hs.Healthy() {
		t.Error("head-open must not be Healthy() (MED #10)")
	}
}

// Druk w toku: linia1[4]=formaty czekające w buforze odbiorczym. To sygnał
// „jeszcze drukuje", NIE fault.
func TestParseHostStatusReplyDraining(t *testing.T) {
	reply := "\x02150,0,0,1219,002,0,0,0,000,0,0,0\x03\r\n" +
		"\x02000,0,0,0,0,2,0,0,00000003,1,000\x03\r\n" +
		"\x028888,0\x03\r\n"
	hs, ok := ParseHostStatusReply(reply)
	if !ok {
		t.Fatal("draining frame must parse")
	}
	if hs.QueuedFormats != 2 {
		t.Errorf("QueuedFormats = %d, want 2 (line1[4]=002)", hs.QueuedFormats)
	}
	if !hs.Draining() {
		t.Error("must be Draining()")
	}
	if !hs.Healthy() {
		t.Error("draining is busy, not a fault — must stay Healthy()")
	}
}

func TestParseHostStatusReplyBufferFull(t *testing.T) {
	reply := "\x02150,0,0,1219,005,1,0,0,000,0,0,0\x03\r\n" +
		"\x02000,0,0,0,0,2,0,0,00000001,1,000\x03\r\n"
	hs, ok := ParseHostStatusReply(reply)
	if !ok {
		t.Fatal("buffer-full frame must parse")
	}
	if !hs.BufferFull {
		t.Error("BufferFull must be true (line1[5]=1)")
	}
	if !hs.Healthy() {
		t.Error("BufferFull alone must stay Healthy() (flow signal)")
	}
}

// Inny dialekt / ucięta odpowiedź: sama linia 1 nadal parsuje się ok=true,
// pola z linii 2 zostają zerowe (graceful degrade — bez fałszywych faultów).
func TestParseHostStatusReplyLine2Missing(t *testing.T) {
	hs, ok := ParseHostStatusReply("\x02150,1,0,1219,000,0,0,0,000,0,0,0\x03\r\n")
	if !ok {
		t.Fatal("line-1-only reply must still parse")
	}
	if !hs.PaperOut {
		t.Error("PaperOut from line 1 must be set")
	}
	if hs.HeadOpen {
		t.Errorf("missing line 2 must leave its fields zero: %+v", hs)
	}
}

// QueryHostStatus musi doczytać i sparsować linię 2 (head-open), nie kończyć
// na pierwszym terminatorze linii 1.
func TestQueryHostStatusParsesSecondLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 8)
		_, _ = conn.Read(buf) // ~HS request
		_, _ = conn.Write([]byte("\x02150,0,0,1219,000,0,0,0,000,0,0,0\x03\r\n"))
		time.Sleep(20 * time.Millisecond)
		_, _ = conn.Write([]byte("\x02000,0,1,0,0,2,0,0,00000000,1,000\x03\r\n\x028888,0\x03\r\n"))
	}()

	hs, ok, err := QueryHostStatus(context.Background(), ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("QueryHostStatus: %v", err)
	}
	if !ok {
		t.Fatal("must parse")
	}
	if !hs.HeadOpen {
		t.Errorf("HeadOpen from delayed line 2 must be parsed, got %+v", hs)
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
