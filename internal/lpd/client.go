// Package lpd implements a minimal RFC 1179 (LPD) client with application-level
// send pacing.
//
// Pacing is the whole point of this package: the XP-423B print-server (Ethernut/
// Nut-OS 4.8, 10/100) drops segments when a GbE Linux sender bursts faster than
// ~40-60 KB/s; Linux then backs off retransmissions exponentially and a 66 KB
// two-label job takes 30-50 s to arrive (measured 2026-06-06, see
// docs/hardware-spike-findings.md). Trickling the data file at ~20 KB/s keeps the
// server's receive path clean: the same 66 KB arrives in ~3 s, while the print
// engine (~6.6 KB/s at 2 ips) never starves.
package lpd

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"time"
)

// Client submits raw jobs to an LPD print server.
type Client struct {
	Addr      string        // host:port print-servera (np. "192.168.1.75:515")
	Queue     string        // nazwa kolejki LPD (np. "lp")
	RateBps   int           // pacing wysyłki danych w B/s; 0 = bez pacingu
	ChunkSize int           // rozmiar chunka danych; 0 => 1448 (1 MSS)
	Timeout   time.Duration // deadline pojedynczej operacji I/O; 0 => 30 s
}

func (c *Client) chunkSize() int {
	if c.ChunkSize > 0 {
		return c.ChunkSize
	}
	return 1448
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 30 * time.Second
}

// Print sends one complete LPD job (control file + data file) and waits for the
// final data-file ACK. A nil return means the print server acknowledged receipt
// of the complete data file.
func (c *Client) Print(ctx context.Context, data []byte, jobID int, user, title string) error {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "print-bridge"
	}
	// RFC 1179 §6: trzycyfrowy numer joba w nazwach cf/df.
	seq := jobID % 1000
	cfName := fmt.Sprintf("cfA%03d%s", seq, host)
	dfName := fmt.Sprintf("dfA%03d%s", seq, host)

	var ctrl bytes.Buffer
	fmt.Fprintf(&ctrl, "H%s\n", host)
	fmt.Fprintf(&ctrl, "P%s\n", user)
	fmt.Fprintf(&ctrl, "J%s\n", title)
	fmt.Fprintf(&ctrl, "l%s\n", dfName) // 'l' = print leaving control chars (raw)

	d := net.Dialer{Timeout: c.timeout()}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return fmt.Errorf("lpd: dial %s: %w", c.Addr, err)
	}
	defer conn.Close()
	// Przerwij wiszące I/O natychmiast po anulowaniu kontekstu (nie czekaj na
	// per-op deadline) — SetDeadline w przeszłość budzi blokujący Read/Write.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	// 02 | queue LF -> ACK
	if err := c.send(ctx, conn, []byte("\x02"+c.Queue+"\n"), "receive-job"); err != nil {
		return err
	}
	// 02 | len SP cfName LF -> ACK ; ctrl NUL -> ACK
	hdr := fmt.Sprintf("\x02%d %s\n", ctrl.Len(), cfName)
	if err := c.send(ctx, conn, []byte(hdr), "control-file header"); err != nil {
		return err
	}
	if err := c.send(ctx, conn, append(ctrl.Bytes(), 0x00), "control-file"); err != nil {
		return err
	}
	// 03 | len SP dfName LF -> ACK ; data (paced) NUL -> ACK
	hdr = fmt.Sprintf("\x03%d %s\n", len(data), dfName)
	if err := c.send(ctx, conn, []byte(hdr), "data-file header"); err != nil {
		return err
	}
	if err := c.writePaced(ctx, conn, data); err != nil {
		return err
	}
	if err := c.send(ctx, conn, []byte{0x00}, "data-file"); err != nil {
		return err
	}
	return nil
}

// send writes payload and consumes the one-byte ACK that closes the phase.
func (c *Client) send(ctx context.Context, conn net.Conn, payload []byte, phase string) error {
	if err := c.write(ctx, conn, payload); err != nil {
		return fmt.Errorf("lpd: %s: %w", phase, err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(c.timeout()))
	ack := make([]byte, 1)
	if _, err := conn.Read(ack); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("lpd: %s: %w", phase, ctx.Err())
		}
		return fmt.Errorf("lpd: %s: reading ACK: %w", phase, err)
	}
	if ack[0] != 0x00 {
		return fmt.Errorf("lpd: %s: server NACK 0x%02x", phase, ack[0])
	}
	return nil
}

// writePaced trickles data at RateBps in ChunkSize chunks. RateBps == 0 writes
// everything at once.
func (c *Client) writePaced(ctx context.Context, conn net.Conn, data []byte) error {
	if c.RateBps <= 0 {
		if err := c.write(ctx, conn, data); err != nil {
			return fmt.Errorf("lpd: data-file body: %w", err)
		}
		return nil
	}
	chunk := c.chunkSize()
	interval := time.Duration(float64(chunk) / float64(c.RateBps) * float64(time.Second))
	for sent := 0; sent < len(data); sent += chunk {
		end := sent + chunk
		if end > len(data) {
			end = len(data)
		}
		if err := c.write(ctx, conn, data[sent:end]); err != nil {
			return fmt.Errorf("lpd: data-file body at %d/%d B: %w", sent, len(data), err)
		}
		if end == len(data) {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("lpd: data-file body at %d/%d B: %w", end, len(data), ctx.Err())
		case <-time.After(interval):
		}
	}
	return nil
}

func (c *Client) write(ctx context.Context, conn net.Conn, p []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(c.timeout()))
	if _, err := conn.Write(p); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}
