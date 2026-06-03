package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/robsonek/print-bridge/internal/apierr"
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
	defer store.Close()
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
		Health:  makeHealth(reach, probe, cups),
		Updater: func(tag string) error {
			return update.SpawnUpdater(absUnder(exeDir, "update-bridge.sh"), tag)
		},
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", cfg.ListenPort),
		Handler: server.Router(h, cfg.PrintToken),
	}
	log.Printf("print-bridge %s listening on %s (queue=%s printer=%s)", version.Version, srv.Addr, cfg.CUPSQueue, printerAddr)
	if err := srv.ListenAndServeTLS(certPath, keyPath); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func makeHealth(reach *printer.SocketReachability, probe *printer.HSProbe, cups *printer.CUPSClient) func(context.Context) (int, any) {
	return func(ctx context.Context) (int, any) {
		body := map[string]any{"version": version.Version}
		online, _ := reach.Reachable(ctx)
		body["printer_online"] = online
		hs, ok, _ := probe.HostStatus(ctx)
		if ok {
			body["paper_out"] = hs.PaperOut
			body["paused"] = hs.Paused
			body["host_status"] = hs.Raw
		}
		if reasons, err := cups.PrinterReasons(ctx); err == nil {
			body["cups_reasons"] = reasons
		}
		status := http.StatusOK
		if !online || (ok && !hs.Healthy()) {
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

var _ = apierr.CodeForbidden // keep apierr import if unused after refactor
