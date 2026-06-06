package printer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// WebPanel is a client for the XP-423B print-server's HTTP panel — the only
// channel that is independent of the 9100 ZPL responder (which can wedge, see
// hardware-spike-findings.md) and works regardless of the printer's language
// mode. Used for the reset recovery (function.cgi?func=reset) and its
// busy-guard (status.cgi).
type WebPanel struct {
	BaseURL string       // np. http://192.168.1.75
	HTTPC   *http.Client // nil => domyślny klient z timeoutem 8 s
}

// PanelState is the parsed status.cgi state. The spike-proven robust rule:
// greentext = OK/transient, ANY redtext = fault (the firmware may grow new
// strings — never map them 1:1), unparseable = unknown (no invented faults).
type PanelState struct {
	State string // np. "Ready", "Printing", "Paper Jam", "Carriage Open"
	Green bool
	Known bool // false = HTML nie pasuje do znanego formatu
}

func (s PanelState) Fault() bool    { return s.Known && !s.Green }
func (s PanelState) Printing() bool { return s.Known && s.Green && s.State == "Printing" }
func (s PanelState) Ready() bool    { return s.Known && s.Green && s.State == "Ready" }

// class bez cudzysłowów (realny firmware); definicje CSS ".redtext {" nie
// matchują, bo wymagamy "class=" i ">" wokół stanu.
var panelStateRE = regexp.MustCompile(`class="?(red|green)text"?>([^<]+)<`)

func (w *WebPanel) httpc() *http.Client {
	if w.HTTPC != nil {
		return w.HTTPC
	}
	return &http.Client{Timeout: 8 * time.Second}
}

func (w *WebPanel) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := w.httpc().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("panel %s: HTTP %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// Status fetches and parses status.cgi.
func (w *WebPanel) Status(ctx context.Context) (PanelState, error) {
	body, err := w.get(ctx, "/cgi-bin/status.cgi")
	if err != nil {
		return PanelState{}, err
	}
	m := panelStateRE.FindSubmatch(body)
	if m == nil {
		return PanelState{}, nil // alive, inny format -> unknown
	}
	return PanelState{
		State: strings.TrimSpace(string(m[2])),
		Green: string(m[1]) == "green",
		Known: true,
	}, nil
}

// Reset triggers the print-server reset (function.cgi?func=reset). The server
// briefly drops off HTTP (~1 s) while restarting; spike-verified to clear
// latched faults (Paper Jam) and a wedged 9100 responder, and to resume a
// buffered pending job. It does NOT print anything by itself.
func (w *WebPanel) Reset(ctx context.Context) error {
	_, err := w.get(ctx, "/admin/cgi-bin/function.cgi?func=reset")
	return err
}
