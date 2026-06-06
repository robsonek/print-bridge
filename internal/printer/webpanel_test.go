package printer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Realny HTML z XP-423B Print Server (nagrany 2026-06-07): class BEZ
// cudzysłowów, stan w <TD class=greentext>Ready</TD>, a nagłówek <STYLE>
// zawiera definicje ".redtext {...}" które NIE mogą matchować parsera.
const realPanelHTML = `<HTML><HEAD><META http-equiv=Content-Type content=text/html; charset=windows-1252><STYLE type=text/css>.maintext {FONT-SIZE:12px}.redtext  {FONT-WEIGHT:bold;COLOR:red}.greentext{FONT-WEIGHT:bold;COLOR:green}</STYLE></HEAD><BODY><TABLE><TR><TD class=whitetext width=30%>Printer Status</TD></TR><TR><TD class=greentext>STAN</TD><TD><INPUT type=button value='Refresh'></TD></TR></TABLE></BODY></HTML>`

func panelServer(t *testing.T, state, class string, resetHits *int) *httptest.Server {
	t.Helper()
	html := strings.Replace(realPanelHTML, "class=greentext>STAN", "class="+class+">"+state, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status.cgi"):
			_, _ = w.Write([]byte(html))
		case strings.HasSuffix(r.URL.Path, "/function.cgi") && r.URL.Query().Get("func") == "reset":
			if resetHits != nil {
				*resetHits++
			}
			_, _ = w.Write([]byte("OK"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebPanelStatusReady(t *testing.T) {
	srv := panelServer(t, "Ready", "greentext", nil)
	p := &WebPanel{BaseURL: srv.URL}
	st, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != "Ready" || !st.Green || st.Fault() {
		t.Errorf("Ready: %+v", st)
	}
	if st.Printing() {
		t.Error("Ready nie jest Printing")
	}
}

func TestWebPanelStatusPrinting(t *testing.T) {
	srv := panelServer(t, "Printing", "greentext", nil)
	p := &WebPanel{BaseURL: srv.URL}
	st, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Printing() || st.Fault() {
		t.Errorf("Printing: %+v", st)
	}
}

// Reguła ze spike'a: KAŻDY redtext = fault (nie mapować stringów 1:1 —
// firmware może mieć ich więcej).
func TestWebPanelStatusRedtextIsFault(t *testing.T) {
	for _, state := range []string{"Paper Jam", "Carriage Open", "Cokolwiek Nowego"} {
		srv := panelServer(t, state, "redtext", nil)
		p := &WebPanel{BaseURL: srv.URL}
		st, err := p.Status(context.Background())
		if err != nil {
			t.Fatalf("Status(%s): %v", state, err)
		}
		if !st.Fault() || st.Green {
			t.Errorf("%s: musi być fault, got %+v", state, st)
		}
		if st.State != state {
			t.Errorf("State = %q, want %q (treść jako powód do Signala)", st.State, state)
		}
	}
}

// Nieparsowalny HTML -> unknown (Known=false), bez wymyślania faultów.
func TestWebPanelStatusUnparseable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>inny firmware</html>"))
	}))
	t.Cleanup(srv.Close)
	p := &WebPanel{BaseURL: srv.URL}
	st, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Known {
		t.Errorf("unparseable musi dać Known=false: %+v", st)
	}
	if st.Fault() {
		t.Error("unknown nie może być raportowany jako fault")
	}
}

func TestWebPanelStatusTransportError(t *testing.T) {
	p := &WebPanel{BaseURL: "http://127.0.0.1:1", HTTPC: &http.Client{Timeout: 500 * time.Millisecond}}
	if _, err := p.Status(context.Background()); err == nil {
		t.Fatal("martwy panel musi zwrócić błąd transportu")
	}
}

func TestWebPanelResetHitsFunctionCGI(t *testing.T) {
	hits := 0
	srv := panelServer(t, "Ready", "greentext", &hits)
	p := &WebPanel{BaseURL: srv.URL}
	if err := p.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if hits != 1 {
		t.Errorf("function.cgi?func=reset trafiony %d razy, want 1", hits)
	}
}
