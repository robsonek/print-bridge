package lpd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeLPD is a minimal in-process RFC 1179 receiver. It records the protocol
// frames it sees and can be told to NACK a given phase or drop the connection
// mid-transfer.
type fakeLPD struct {
	ln net.Listener

	nackPhase string // "receive-job" | "ctrl-hdr" | "ctrl-file" | "data-hdr" | "data-final"
	dropAfter string // "data-hdr" => close conn right after ACKing the data header

	// recorded state (read after <-done)
	queueLine string
	ctrlHdr   string
	ctrlBody  string
	dataHdr   string
	data      []byte
	chunkGaps []time.Duration // gaps between successive data reads >1 byte

	done chan error
}

func newFakeLPD(t *testing.T) *fakeLPD {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeLPD{ln: ln, done: make(chan error, 1)}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeLPD) addr() string { return f.ln.Addr().String() }

func (f *fakeLPD) ack(conn net.Conn, phase string) bool {
	if f.nackPhase == phase {
		_, _ = conn.Write([]byte{0x01})
		return false
	}
	_, _ = conn.Write([]byte{0x00})
	if f.dropAfter == phase {
		_ = conn.Close()
		return false
	}
	return true
}

func (f *fakeLPD) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		f.done <- err
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReader(conn)

	line, err := r.ReadString('\n')
	if err != nil {
		f.done <- fmt.Errorf("queue line: %w", err)
		return
	}
	f.queueLine = line
	if !f.ack(conn, "receive-job") {
		f.done <- nil
		return
	}

	line, err = r.ReadString('\n')
	if err != nil {
		f.done <- fmt.Errorf("ctrl hdr: %w", err)
		return
	}
	f.ctrlHdr = line
	if !f.ack(conn, "ctrl-hdr") {
		f.done <- nil
		return
	}
	n, err := sizeOf(line)
	if err != nil {
		f.done <- err
		return
	}
	body := make([]byte, n+1) // + NUL
	if _, err := io.ReadFull(r, body); err != nil {
		f.done <- fmt.Errorf("ctrl body: %w", err)
		return
	}
	f.ctrlBody = string(body[:n])
	if !f.ack(conn, "ctrl-file") {
		f.done <- nil
		return
	}

	line, err = r.ReadString('\n')
	if err != nil {
		f.done <- fmt.Errorf("data hdr: %w", err)
		return
	}
	f.dataHdr = line
	if !f.ack(conn, "data-hdr") {
		f.done <- nil
		return
	}
	n, err = sizeOf(line)
	if err != nil {
		f.done <- err
		return
	}
	// Read the data file recording inter-read gaps (pacing observability).
	f.data = make([]byte, 0, n)
	buf := make([]byte, 64*1024)
	last := time.Now()
	for len(f.data) < n {
		limit := n - len(f.data) // nie wciągnij trailing NUL do danych
		if limit > len(buf) {
			limit = len(buf)
		}
		m, err := r.Read(buf[:limit])
		if err != nil {
			f.done <- fmt.Errorf("data body at %d/%d: %w", len(f.data), n, err)
			return
		}
		now := time.Now()
		f.chunkGaps = append(f.chunkGaps, now.Sub(last))
		last = now
		f.data = append(f.data, buf[:m]...)
	}
	nul := make([]byte, 1)
	if _, err := io.ReadFull(r, nul); err != nil {
		f.done <- fmt.Errorf("data NUL: %w", err)
		return
	}
	if !f.ack(conn, "data-final") {
		f.done <- nil
		return
	}
	f.done <- nil
}

// sizeOf parses the byte count out of "\x02<len> cfA001host\n" / "\x03<len> ...".
func sizeOf(hdr string) (int, error) {
	s := strings.TrimLeft(hdr, "\x02\x03")
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return 0, fmt.Errorf("malformed header %q", hdr)
	}
	return strconv.Atoi(fields[0])
}

func (f *fakeLPD) wait(t *testing.T) {
	t.Helper()
	select {
	case err := <-f.done:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("fake server: timeout")
	}
}

func TestPrintSendsProtocolFrames(t *testing.T) {
	f := newFakeLPD(t)
	c := &Client{Addr: f.addr(), Queue: "lp"}
	data := []byte("^XA^FDtest^FS^XZ")

	if err := c.Print(context.Background(), data, 7, "robson", "etykieta"); err != nil {
		t.Fatalf("Print: %v", err)
	}
	f.wait(t)

	if f.queueLine != "\x02lp\n" {
		t.Errorf("queue line = %q, want \\x02lp\\n", f.queueLine)
	}
	if !strings.Contains(f.ctrlHdr, " cfA007") {
		t.Errorf("ctrl hdr = %q, want cfA007<host>", f.ctrlHdr)
	}
	for _, want := range []string{"Probson\n", "Jetykieta\n", "ldfA007"} {
		if !strings.Contains(f.ctrlBody, want) {
			t.Errorf("ctrl body %q missing %q", f.ctrlBody, want)
		}
	}
	if !strings.HasPrefix(f.dataHdr, "\x03"+strconv.Itoa(len(data))+" dfA007") {
		t.Errorf("data hdr = %q, want \\x03%d dfA007<host>", f.dataHdr, len(data))
	}
	if !bytes.Equal(f.data, data) {
		t.Errorf("data = %q, want %q", f.data, data)
	}
}

func TestPrintJobIDWrapsAtThreeDigits(t *testing.T) {
	f := newFakeLPD(t)
	c := &Client{Addr: f.addr(), Queue: "lp"}

	if err := c.Print(context.Background(), []byte("x"), 12345, "u", "t"); err != nil {
		t.Fatalf("Print: %v", err)
	}
	f.wait(t)

	if !strings.Contains(f.ctrlHdr, " cfA345") {
		t.Errorf("ctrl hdr = %q, want cfA345 (12345 mod 1000)", f.ctrlHdr)
	}
}

func TestPrintPacesDataAtRate(t *testing.T) {
	f := newFakeLPD(t)
	// 10000 B przy 20000 B/s i chunku 1000 B => 10 chunków, 9 przerw po 50 ms
	// => minimum ~450 ms. Bez pacingu fake odbiera wszystko w pojedyncze ms.
	c := &Client{Addr: f.addr(), Queue: "lp", RateBps: 20000, ChunkSize: 1000}
	data := bytes.Repeat([]byte("A"), 10000)

	start := time.Now()
	if err := c.Print(context.Background(), data, 1, "u", "t"); err != nil {
		t.Fatalf("Print: %v", err)
	}
	elapsed := time.Since(start)
	f.wait(t)

	if !bytes.Equal(f.data, data) {
		t.Fatalf("data corrupted: got %d B, want %d B", len(f.data), len(data))
	}
	if elapsed < 400*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 400ms (pacing not applied)", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %v, want < 5s (pacing too slow)", elapsed)
	}
}

func TestPrintNoRateMeansNoPacing(t *testing.T) {
	f := newFakeLPD(t)
	c := &Client{Addr: f.addr(), Queue: "lp"} // RateBps == 0
	data := bytes.Repeat([]byte("A"), 64*1024)

	start := time.Now()
	if err := c.Print(context.Background(), data, 1, "u", "t"); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("elapsed = %v, want fast path without pacing", elapsed)
	}
	f.wait(t)
}

func TestPrintNackOnReceiveJob(t *testing.T) {
	f := newFakeLPD(t)
	f.nackPhase = "receive-job"
	c := &Client{Addr: f.addr(), Queue: "lp"}

	err := c.Print(context.Background(), []byte("x"), 1, "u", "t")
	if err == nil || !strings.Contains(err.Error(), "receive-job") {
		t.Fatalf("err = %v, want NACK mentioning receive-job", err)
	}
}

func TestPrintNackOnFinalDataAck(t *testing.T) {
	f := newFakeLPD(t)
	f.nackPhase = "data-final"
	c := &Client{Addr: f.addr(), Queue: "lp"}

	err := c.Print(context.Background(), []byte("x"), 1, "u", "t")
	if err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("err = %v, want NACK mentioning data file", err)
	}
}

func TestPrintServerDropsMidData(t *testing.T) {
	f := newFakeLPD(t)
	f.dropAfter = "data-hdr"
	c := &Client{Addr: f.addr(), Queue: "lp", Timeout: 2 * time.Second}

	start := time.Now()
	err := c.Print(context.Background(), bytes.Repeat([]byte("A"), 256*1024), 1, "u", "t")
	if err == nil {
		t.Fatal("err = nil, want error after server dropped connection")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %v, want prompt failure (no hang)", elapsed)
	}
}

func TestPrintContextCancelDuringPacing(t *testing.T) {
	f := newFakeLPD(t)
	// 1 MB przy 20 KB/s trwałby ~50 s — cancel po 150 ms musi przerwać szybko.
	c := &Client{Addr: f.addr(), Queue: "lp", RateBps: 20000, ChunkSize: 1448}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := c.Print(ctx, bytes.Repeat([]byte("A"), 1<<20), 1, "u", "t")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancel took %v, want < 3s", elapsed)
	}
}
