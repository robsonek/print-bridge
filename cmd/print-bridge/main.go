package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/robsonek/print-bridge/internal/config"
	"github.com/robsonek/print-bridge/internal/idempotency"
	"github.com/robsonek/print-bridge/internal/printer"
	"github.com/robsonek/print-bridge/internal/server"
	"github.com/robsonek/print-bridge/internal/tlsgen"
	"github.com/robsonek/print-bridge/internal/update"
	"github.com/robsonek/print-bridge/internal/version"
)

func main() {
	exeDir := executableDir()
	cfgPath := filepath.Join(exeDir, "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config invalid: %v", err)
	}

	certPath := absUnder(exeDir, cfg.TLSCertPath)
	keyPath := absUnder(exeDir, cfg.TLSKeyPath)
	if err := tlsgen.EnsureCert(certPath, keyPath, []string{"localhost", "127.0.0.1", cfg.PrinterIP}); err != nil {
		log.Fatalf("tls: %v", err)
	}

	store, err := idempotency.Open(absUnder(exeDir, cfg.IdempotencyDB), cfg.IdempotencyTTLDays)
	if err != nil {
		log.Fatalf("idempotency: %v", err)
	}
	// NOTE: store.Close() is NOT deferred — a deferred close never runs under the
	// SIGTERM the agent actually receives (systemctl stop / self-update). It is
	// called explicitly after graceful shutdown below so the WAL is checkpointed
	// cleanly on the routine restart path (#8).
	go cleanupLoop(store)

	printerAddr := fmt.Sprintf("%s:9100", cfg.PrinterIP)
	cups := printer.NewCUPSClient(cfg.CUPSQueue)
	reach := &printer.SocketReachability{Addr: printerAddr, Timeout: 5 * time.Second, Reasons: cups.PrinterReasons}
	probe := &printer.HSProbe{Addr: printerAddr, Timeout: 5 * time.Second}
	render := printer.NewPDFRenderer(cfg.RenderDPI, cfg.LabelWidthMM, cfg.LabelHeightMM)

	p := &printer.Printer{
		Reach: reach, Sub: cups, Poll: cups, Probe: probe, Render: render,
		PollInterval: time.Second, ConfirmTimeoutPolls: cfg.ConfirmTimeoutSec,
	}

	h := &server.Handlers{
		Printer: p,
		Store:   server.NewStoreAdapter(store),
		KeyLock: server.NewKeyLock(),
		Health:  makeHealth(reach, probe, cups),
		Updater: func(tag string) error {
			return update.SpawnUpdater(absUnder(exeDir, "update-bridge.sh"), tag)
		},
		// #27: server-side upper bound for the whole print op (exec lp + every
		// JobState IPP round-trip + verify), so a hung cupsd `lp` held open by a
		// non-timing-out client cannot pin the handler forever. Sized to sit just
		// UNDER the HTTP WriteTimeout (ConfirmTimeoutSec+60) so the handler returns
		// its own clean retryable error before the raw connection is guillotined,
		// while still exceeding the healthy poll loop's nominal ConfirmTimeoutSec
		// (×1s PollInterval) budget so a slow-but-healthy print is never cut off.
		ConfirmTimeout: time.Duration(cfg.ConfirmTimeoutSec+55) * time.Second,
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", cfg.ListenPort),
		Handler: server.Router(h, cfg.PrintToken),
		// #18: bounded HTTP timeouts (defense-in-depth on top of the ufw egress-CIDR
		// restriction). ReadHeaderTimeout kills slowloris BEFORE auth/handler runs.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,  // whole request incl. base64 PDF body
		IdleTimeout:       120 * time.Second, // keep-alive reaping
		// WriteTimeout MUST exceed the print confirm budget: PrintJobs long-polls up
		// to ConfirmTimeoutSec (x 1s PollInterval). +60s slack covers render/submit/
		// verify around the loop so a legitimate long print is never cut off.
		WriteTimeout: time.Duration(cfg.ConfirmTimeoutSec+60) * time.Second,
	}

	// #8: graceful shutdown. The agent is restarted via SIGTERM (systemctl stop /
	// self-update); without a handler Go exits immediately, no defers run, and a
	// SIGTERM mid-poll leaves no persisted state. Run the server in a goroutine and
	// drain on SIGINT/SIGTERM, then close the store explicitly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srvErr := make(chan error, 1)
	go func() {
		log.Printf("print-bridge %s listening on %s (queue=%s printer=%s)", version.Version, srv.Addr, cfg.CUPSQueue, printerAddr)
		if err := srv.ListenAndServeTLS(certPath, keyPath); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
	}()

	select {
	case err := <-srvErr:
		// Startup/listen failure: close the store before exiting.
		_ = store.Close()
		log.Fatalf("server: %v", err)
	case <-ctx.Done():
		stop() // restore default signal handling so a second signal force-quits
		log.Printf("shutdown signal received, draining in-flight requests...")
	}

	// Give in-flight polls (up to ConfirmTimeoutSec) time to reach SaveTerminal
	// before the DB closes. srv.Shutdown waits for active handlers without
	// cancelling their contexts, so a confirming print finishes cleanly.
	shutCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ConfirmTimeoutSec+5)*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("graceful shutdown timed out: %v", err)
	}
	if err := store.Close(); err != nil {
		log.Printf("store close: %v", err)
	}
	log.Printf("print-bridge stopped")
}

// Small interfaces over the concrete probes so makeHealth is unit-testable (#20).
type reachabilityProbe interface {
	Reachable(context.Context) (bool, error)
}
type hostStatusProbe interface {
	HostStatus(context.Context) (printer.HostStatus, bool, error)
}
type cupsReasonsProbe interface {
	PrinterReasons(context.Context) ([]string, error)
}

func makeHealth(reach reachabilityProbe, probe hostStatusProbe, cups cupsReasonsProbe) func(context.Context) (int, any) {
	return func(ctx context.Context) (int, any) {
		body := map[string]any{"version": version.Version}

		online, reachErr := reach.Reachable(ctx)
		body["printer_online"] = online
		if reachErr != nil {
			body["reach_error"] = reachErr.Error()
		}

		hs, ok, hsErr := probe.HostStatus(ctx)
		if ok {
			body["paper_out"] = hs.PaperOut
			body["paused"] = hs.Paused
			body["host_status"] = hs.Raw
		} else if hsErr != nil {
			// Distinguish a probe transport failure/timeout from "printer doesn't
			// speak ~HS" (alive but unparseable).
			body["host_status"] = "unavailable"
			body["host_status_error"] = hsErr.Error()
		} else {
			body["host_status"] = "unsupported"
		}

		// #20: an IPP TRANSPORT error means cupsd is unreachable -> the real print
		// path (lp -> cupsd) will fail with CUPS_UNAVAILABLE. That is a hard signal
		// (unlike the CONTENT of printer-state-reasons, which is unreliable over a
		// socket:9100 backend), so degrade health on it. Previously a CUPS error was
		// silently ignored and health could report "ok" with cupsd down.
		cupsReachable := true
		if reasons, err := cups.PrinterReasons(ctx); err == nil {
			body["cups_reasons"] = reasons
		} else {
			cupsReachable = false
			body["cups_error"] = err.Error()
		}
		body["cups_reachable"] = cupsReachable

		status := http.StatusOK
		if !online || !cupsReachable || (ok && !hs.Healthy()) {
			status = http.StatusServiceUnavailable
			body["status"] = "degraded"
		} else {
			body["status"] = "ok"
		}
		return status, body
	}
}

func cleanupLoop(store *idempotency.Store) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for range t.C {
		if n, err := store.Cleanup(time.Now()); err == nil && n > 0 {
			log.Printf("idempotency cleanup: removed %d expired rows", n)
		}
	}
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func absUnder(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
