// lpdpaced is a CUPS backend: LPD (RFC 1179) with application-level send pacing.
//
// Why it exists: the XP-423B print-server (Ethernut/Nut-OS 4.8, 10/100 Mbps)
// drops TCP segments when a GbE Linux host bursts data faster than ~40-60 KB/s.
// Linux retransmits with exponential backoff and a 66 KB two-label ZPL job takes
// 30-50 s to arrive — the print engine starves between labels, which surfaced as
// "the second label prints a minute late" (docs/hardware-spike-findings.md,
// 2026-06-06). The stock socket:// backend additionally loses the buffer on its
// early FIN, and the stock lpd:// backend cannot pace. Trickling the data file at
// ~20 KB/s delivers the same job in ~3 s with zero retransmissions.
//
// Install: /usr/lib/cups/backend/lpdpaced (root:root 0755, own binary — cupsd's
// AppArmor profile may not allow executing from /opt).
// Device URI: lpdpaced://<printer-ip>/lp?rate=20000
//
// CUPS invokes backends as:
//
//	lpdpaced job-id user title copies options [file]
//
// with the device URI in $DEVICE_URI (argv[0] as fallback), and with NO
// arguments for device discovery.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/robsonek/print-bridge/internal/lpd"
)

func main() {
	os.Exit(run())
}

func run() int {
	return runWith(os.Args, os.Stdin, os.Stderr)
}

func runWith(args []string, stdin io.Reader, stderr io.Writer) int {
	if len(args) == 1 {
		fmt.Println(discoveryLine())
		return exitOK
	}
	if len(args) < 6 || len(args) > 7 {
		fmt.Fprintf(stderr, "ERROR: użycie: %s job-id user title copies options [file]\n", args[0])
		return exitFailed
	}

	uri := os.Getenv("DEVICE_URI")
	if uri == "" {
		uri = args[0]
	}
	cfg, err := parseDeviceURI(uri)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return exitCodeFor(err)
	}

	jobID, _ := strconv.Atoi(args[1])
	user := args[2]
	title := args[3]
	copies := parseCopies(args[4], stderr)

	var data []byte
	if len(args) == 7 {
		data, err = os.ReadFile(args[6])
	} else {
		data, err = io.ReadAll(stdin)
		copies = 1 // stdin: cupsd dostarcza strumień dokładnie raz
	}
	// Pusty/nieczytelny spool to błąd TRWAŁY: retry nic nie zmieni, a exitFailed
	// oddaje decyzję error-policy (stop-printer potrafi zatrzymać kolejkę).
	// CUPS_BACKEND_CANCEL kasuje job bezwarunkowo i kolejka żyje dalej.
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: czytanie danych joba: %v\n", err)
		return exitCancel
	}
	if len(data) == 0 {
		fmt.Fprintln(stderr, "ERROR: pusty job")
		return exitCancel
	}
	payload := buildPayload(data, copies)

	// Budżet całego transferu: czas wynikający z pacingu + minuta zapasu na
	// dial/ACK; bez pacingu sam zapas.
	budget := time.Minute
	if cfg.rateBps > 0 {
		budget += time.Duration(float64(len(payload))/float64(cfg.rateBps)) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	client := &lpd.Client{
		Addr:      cfg.addr,
		Queue:     cfg.queue,
		RateBps:   cfg.rateBps,
		ChunkSize: cfg.chunk,
		Timeout:   cfg.timeout,
	}
	fmt.Fprintf(stderr, "INFO: wysyłam %d B do %s/%s (pacing %d B/s)\n",
		len(payload), cfg.addr, cfg.queue, cfg.rateBps)
	if err := client.Print(ctx, payload, jobID, user, title); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return exitCodeFor(err)
	}
	fmt.Fprintf(stderr, "INFO: print-server potwierdził odbiór %d B\n", len(payload))
	return exitOK
}

// parseCopies parses CUPS argv[4]; garbage is announced and treated as one
// copy instead of being silently swallowed as 0.
func parseCopies(s string, stderr io.Writer) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		fmt.Fprintf(stderr, "WARNING: copies=%q nieparsowalne — drukuję 1 kopię\n", s)
		return 1
	}
	if n < 1 {
		return 1
	}
	return n
}
