package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/robsonek/print-bridge/internal/printer"
	"github.com/robsonek/print-bridge/internal/server"
	"github.com/robsonek/print-bridge/internal/version"
)

// Golden kontraktu health (docs/error-contract.md §1.2): GET /api/v1/health
// przez server.Router() z prawdziwym TokenAuth (health bez tokenu) i
// PRAWDZIWYM makeHealth nad atrapami :9100, ~HS, IPP i watchdoga.

// recordedIdleReply: ramka ~HS nagrana na XP-423B w stanie idle (ta sama co
// w internal/printer/hoststatus_test.go).
const recordedIdleReply = "\x02150,0,0,1219,000,0,0,0,000,0,0,0\x03\r\n" +
	"\x02000,0,0,0,0,2,0,0,00000000,1,000\x03\r\n" +
	"\x028888,0\x03\r\n"

type healthCase struct {
	name    string
	reach   fakeReach
	hs      fakeHS
	reasons fakeReasons
	wd      printer.WatchdogStats
	status  int
	want    map[string]any
}

func parsedHS(t *testing.T, reply string) printer.HostStatus {
	t.Helper()
	hs, ok := printer.ParseHostStatusReply(reply)
	if !ok {
		t.Fatalf("ramka ~HS się nie parsuje: %q", reply)
	}
	return hs
}

func healthCases(t *testing.T) []healthCase {
	idle := parsedHS(t, recordedIdleReply)
	paperOutLine1Only := parsedHS(t, "\x02150,1,0,1219,000,0,0,0,000,0,0,0\x03\r\n")
	return []healthCase{
		{
			name:    "ok-hs-sparsowane",
			reach:   fakeReach{online: true},
			hs:      fakeHS{hs: idle, ok: true},
			reasons: fakeReasons{reasons: []string{"none"}},
			status:  http.StatusOK,
			want: map[string]any{
				"version":              "1.2.3",
				"status":               "ok",
				"watchdog_auto_resets": float64(0),
				"printer_online":       true,
				"paper_out":            false,
				"paused":               false,
				"head_open":            false,
				"queued_formats":       float64(0),
				"batch_remaining":      float64(0),
				"host_status":          "150,0,0,1219,000,0,0,0,000,0,0,0",
				"host_status_2":        "000,0,0,0,0,2,0,0,00000000,1,000",
				"cups_reasons":         []any{"none"},
				"cups_reachable":       true,
			},
		},
		{
			name:    "ok-hs-nieobslugiwane-pusta-lista-reasons",
			reach:   fakeReach{online: true},
			hs:      fakeHS{ok: false},
			reasons: fakeReasons{reasons: []string{}},
			wd:      printer.WatchdogStats{AutoResets: 2, LastAutoReset: "2026-06-07T12:00:00Z"},
			status:  http.StatusOK,
			want: map[string]any{
				"version":              "1.2.3",
				"status":               "ok",
				"watchdog_auto_resets": float64(2),
				"watchdog_last_reset":  "2026-06-07T12:00:00Z",
				"printer_online":       true,
				"host_status":          "unsupported",
				"cups_reasons":         []any{},
				"cups_reachable":       true,
			},
		},
		{
			name:    "degraded-offline-hs-i-cups-padly",
			reach:   fakeReach{online: false, err: errors.New("dial tcp: i/o timeout")},
			hs:      fakeHS{err: errors.New("read: connection reset by peer")},
			reasons: fakeReasons{err: errors.New("ipp: connection refused")},
			status:  http.StatusServiceUnavailable,
			want: map[string]any{
				"version":              "1.2.3",
				"status":               "degraded",
				"watchdog_auto_resets": float64(0),
				"printer_online":       false,
				"reach_error":          "dial tcp: i/o timeout",
				"host_status":          "unavailable",
				"host_status_error":    "read: connection reset by peer",
				"cups_error":           "ipp: connection refused",
				"cups_reachable":       false,
			},
		},
		{
			// nil slice z IPP (CUPS nie zwrócił atrybutu) → "cups_reasons": null;
			// odpowiedź tylko z linią 1 → host_status_2 "".
			name:    "degraded-brak-papieru-reasons-null",
			reach:   fakeReach{online: true},
			hs:      fakeHS{hs: paperOutLine1Only, ok: true},
			reasons: fakeReasons{reasons: nil},
			status:  http.StatusServiceUnavailable,
			want: map[string]any{
				"version":              "1.2.3",
				"status":               "degraded",
				"watchdog_auto_resets": float64(0),
				"printer_online":       true,
				"paper_out":            true,
				"paused":               false,
				"head_open":            false,
				"queued_formats":       float64(0),
				"batch_remaining":      float64(0),
				"host_status":          "150,1,0,1219,000,0,0,0,000,0,0,0",
				"host_status_2":        "",
				"cups_reasons":         nil,
				"cups_reachable":       true,
			},
		},
	}
}

func serveHealth(t *testing.T, c healthCase) *httptest.ResponseRecorder {
	t.Helper()
	wd := c.wd
	h := &server.Handlers{Health: makeHealth(c.reach, c.hs, c.reasons, func() printer.WatchdogStats { return wd })}
	rec := httptest.NewRecorder()
	server.Router(h, "kontrakt-test-token").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	return rec
}

// healthVersionNeedle odtwarza grep z deploy/update-bridge.sh:
// grep -qF "\"version\":\"${TAG#v}\"" — ${TAG#v} = tag bez wiodącego v.
func healthVersionNeedle(t *testing.T, tag string) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "deploy", "update-bridge.sh"))
	if err != nil {
		t.Fatalf("read update-bridge.sh: %v", err)
	}
	if !strings.Contains(string(script), `grep -qF "\"version\":\"${TAG#v}\""`) {
		t.Fatal(`update-bridge.sh nie weryfikuje już health przez grep -qF "\"version\":\"${TAG#v}\"" — zaktualizuj ten test i §1.2 dokumentu`)
	}
	return `"version":"` + strings.TrimPrefix(tag, "v") + `"`
}

func TestContractHealthGolden(t *testing.T) {
	orig := version.Version
	version.Version = "1.2.3" // jak ldflags wydania v1.2.3 (tag bez v)
	t.Cleanup(func() { version.Version = orig })
	needle := healthVersionNeedle(t, "v1.2.3")

	for _, c := range healthCases(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := serveHealth(t, c)
			raw := rec.Body.Bytes()
			if rec.Code != c.status {
				t.Fatalf("HTTP %d, want %d; body=%s", rec.Code, c.status, raw)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var got map[string]any
			dec := json.NewDecoder(bytes.NewReader(raw))
			if err := dec.Decode(&got); err != nil || dec.More() {
				t.Fatalf("body nie jest jednym obiektem JSON (%v): %q", err, raw)
			}
			if !reflect.DeepEqual(got, c.want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(c.want)
				t.Errorf("body:\n got  %s\n want %s", g, w)
			}
			// Zwarty JSON + "\n" — aktualizator czyta surowe bajty, także przy 503.
			var compact bytes.Buffer
			payload := bytes.TrimSuffix(raw, []byte("\n"))
			if !bytes.HasSuffix(raw, []byte("}\n")) || json.Compact(&compact, payload) != nil || !bytes.Equal(compact.Bytes(), payload) {
				t.Errorf("body nie jest zwartym JSON-em z jednym \\n: %q", raw)
			}
			if !bytes.Contains(raw, []byte(needle)) {
				t.Errorf("body bez %s — update-bridge.sh uznałby update za nieudany i cofnął binarkę: %q", needle, raw)
			}
		})
	}
}

// §1.2 ↔ makeHealth: tabela kontrakt:health wymienia dokładnie pola, które
// health emituje, z ich typami; pole „zawsze” jest w każdej odpowiedzi.
func TestContractDocHealthFieldsMatch(t *testing.T) {
	rows := healthDocRows(t)
	cases := healthCases(t)

	observed := map[string]map[string]bool{} // pole → typy JSON
	present := map[string]int{}
	for _, c := range cases {
		rec := serveHealth(t, c)
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for k, v := range got {
			if observed[k] == nil {
				observed[k] = map[string]bool{}
			}
			observed[k][healthJSONType(v)] = true
			present[k]++
		}
	}

	for name, row := range rows {
		types, ok := observed[name]
		if !ok {
			t.Errorf("pole %q z §1.2 nie występuje w żadnym przypadku healthCases()", name)
			continue
		}
		var docTypes []string
		for _, typ := range strings.Split(row["Typ JSON"], "/") {
			docTypes = append(docTypes, strings.TrimSpace(typ))
		}
		sort.Strings(docTypes)
		var gotTypes []string
		for typ := range types {
			gotTypes = append(gotTypes, typ)
		}
		sort.Strings(gotTypes)
		if !reflect.DeepEqual(docTypes, gotTypes) {
			t.Errorf("pole %q: dokument %v, emitowane %v", name, docTypes, gotTypes)
		}
		if strings.HasPrefix(row["Kiedy występuje"], "zawsze") && present[name] != len(cases) {
			t.Errorf("pole %q opisane jako „zawsze”, a jest w %d/%d odpowiedziach", name, present[name], len(cases))
		}
	}
	for name := range observed {
		if _, ok := rows[name]; !ok {
			t.Errorf("pole %q emitowane przez makeHealth, nieopisane w §1.2", name)
		}
	}
}

func healthJSONType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		for _, e := range x {
			if _, ok := e.(string); !ok {
				return "array"
			}
		}
		return "string[]"
	}
	return "?"
}

// healthDocRows: wiersze tabeli po znaczniku <!-- kontrakt:health --> (parser
// jak w internal/server/contract_doc_test.go — pakiety testowe nie współdzielą
// kodu), kluczem jest nazwa pola.
func healthDocRows(t *testing.T) map[string]map[string]string {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "error-contract.md")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	cell := strings.NewReplacer("`", "", "**", "")
	split := func(line string) []string {
		line = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "|"), "|")
		cells := strings.Split(line, "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cell.Replace(cells[i]))
		}
		return cells
	}
	marker := regexp.MustCompile(`^<!-- kontrakt:health -->$`)

	var header []string
	rows := map[string]map[string]string{}
	state := 0 // 0 szukam znacznika, 1 nagłówek, 2 separator, 3 wiersze
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch state {
		case 0:
			if marker.MatchString(strings.TrimSpace(line)) {
				state = 1
			}
		case 1:
			if !strings.HasPrefix(line, "|") {
				t.Fatalf("§1.2: po znaczniku kontrakt:health brak tabeli: %q", line)
			}
			header, state = split(line), 2
		case 2:
			state = 3
		case 3:
			if !strings.HasPrefix(line, "|") {
				state = 4
				continue
			}
			cells := split(line)
			if len(cells) != len(header) {
				t.Fatalf("§1.2: %d komórek, nagłówek ma %d: %q", len(cells), len(header), line)
			}
			row := map[string]string{}
			for i, h := range header {
				row[h] = cells[i]
			}
			rows[row["Pole"]] = row
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s: brak tabeli po <!-- kontrakt:health -->", path)
	}
	return rows
}
