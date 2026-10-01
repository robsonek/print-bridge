package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/robsonek/print-bridge/internal/apierr"
)

// Spójność docs/error-contract.md ↔ kod. Źródła prawdy: zbiór kodów i
// Retryable() — internal/apierr/apierr.go; HTTP, `details` i pola sukcesu —
// tabela przypadków contractCases() (rzeczywiste emisje przez Router()).
// Tabele dokumentu są oznaczone komentarzem <!-- kontrakt:<nazwa> [arg...] -->.

var contractMarkerRE = regexp.MustCompile(`^<!-- kontrakt:(\S+)((?: \S+)*) -->$`)

type docTable struct {
	args []string
	rows []map[string]string // nagłówek → komórka (bez ` i **)
}

func docCell(s string) string {
	return strings.TrimSpace(strings.NewReplacer("`", "", "**", "").Replace(s))
}

func docRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = docCell(cells[i])
	}
	return cells
}

// readContractTables: tabele markdown poprzedzone znacznikiem, per nazwa.
func readContractTables(t *testing.T, path string) map[string][]docTable {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	tables := map[string][]docTable{}
	for i := 0; i < len(lines); i++ {
		m := contractMarkerRE.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			continue
		}
		name, tbl := m[1], docTable{args: strings.Fields(m[2])}
		j := i + 1
		if j+1 >= len(lines) || !strings.HasPrefix(lines[j], "|") || !strings.HasPrefix(lines[j+1], "|") {
			t.Fatalf("%s:%d: po znaczniku kontrakt:%s brak tabeli", path, i+1, name)
		}
		header := docRow(lines[j])
		for j += 2; j < len(lines) && strings.HasPrefix(lines[j], "|"); j++ {
			cells := docRow(lines[j])
			if len(cells) != len(header) {
				t.Fatalf("%s:%d: %d komórek, nagłówek ma %d", path, j+1, len(cells), len(header))
			}
			row := map[string]string{}
			for k, h := range header {
				row[h] = cells[k]
			}
			tbl.rows = append(tbl.rows, row)
		}
		tables[name] = append(tables[name], tbl)
		i = j - 1
	}
	return tables
}

func contractDoc(t *testing.T) map[string][]docTable {
	return readContractTables(t, filepath.Join("..", "..", "docs", "error-contract.md"))
}

func docCol(t *testing.T, row map[string]string, col string) string {
	t.Helper()
	v, ok := row[col]
	if !ok {
		t.Fatalf("tabela bez kolumny %q (wiersz %v)", col, row)
	}
	return v
}

// docYesNo: pierwsze słowo komórki „tak”/„nie”.
func docYesNo(t *testing.T, cell string) bool {
	t.Helper()
	switch strings.Fields(cell + " ")[0] {
	case "tak":
		return true
	case "nie":
		return false
	}
	t.Fatalf("komórka %q: oczekiwano tak/nie", cell)
	return false
}

// detailsSig normalizuje `details` do "klucz: typ, ..." (posortowane);
// "" = brak details. Wspólna postać dla dokumentu i zdekodowanego body.
func detailsSig(d any) string {
	obj, _ := d.(map[string]any)
	var parts []string
	for k, v := range obj {
		parts = append(parts, k+": "+jsonType(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func docDetailsSig(t *testing.T, cell string) string {
	t.Helper()
	if cell == "—" {
		return ""
	}
	var parts []string
	for _, p := range strings.Split(cell, ",") {
		k, typ, ok := strings.Cut(p, ":")
		if !ok {
			t.Fatalf("details %q: oczekiwano „klucz: typ”", cell)
		}
		parts = append(parts, strings.TrimSpace(k)+": "+strings.TrimSpace(typ))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// §2.1: zbiór kodów = stałe apierr.go; kolumna Retryable() = Code.Retryable();
// „retry automatyczny klienta” ⇔ kod ma emisję na print-jobs i każda jego
// emisja na print-jobs to 5xx (od v0.8.0: mutacje admin nigdy nie są ponawiane
// automatycznie, więc kod tylko z resetu/update to zawsze „nie”). Jedynym
// kodem, w którym Retryable() i retry automatyczny się różnią, jest
// PRINTER_BUSY (409 z resetu — operator ponawia ręcznie); nowy taki kod
// wymaga świadomej decyzji i wpisu w §3.
func TestContractDocCodesMatchAPIErr(t *testing.T) {
	tables := contractDoc(t)["kody"]
	if len(tables) != 1 {
		t.Fatalf("oczekiwano jednej tabeli kontrakt:kody, jest %d", len(tables))
	}
	statuses := map[string][]int{} // tylko emisje na print-jobs
	for _, c := range contractCases() {
		if c.status >= 400 && contractEndpoints[c.path] == "print-jobs" {
			code, _ := c.want["code"].(string)
			statuses[code] = append(statuses[code], c.status)
		}
	}

	inDoc := map[string]bool{}
	for _, row := range tables[0].rows {
		code := docCol(t, row, "Kod")
		if inDoc[code] {
			t.Errorf("kod %s dwa razy w §2.1", code)
		}
		inDoc[code] = true

		goRetry := docYesNo(t, docCol(t, row, "Retryable() (Go)"))
		if got := apierr.Code(code).Retryable(); got != goRetry {
			t.Errorf("%s: dokument Retryable()=%v, apierr.go %v", code, goRetry, got)
		}
		auto := docYesNo(t, docCol(t, row, "Retry automatyczny klienta"))
		all5xx := len(statuses[code]) > 0
		for _, s := range statuses[code] {
			all5xx = all5xx && s >= 500
		}
		if auto != all5xx {
			t.Errorf("%s: retry automatyczny=%v, a emisje na print-jobs mają HTTP %v (retry automatyczny ⇔ emisje na print-jobs i wszystkie 5xx)", code, auto, statuses[code])
		}
		if goRetry != auto && code != string(apierr.CodePrinterBusy) {
			t.Errorf("%s: Retryable()=%v ≠ retry automatyczny=%v — dziś dozwolone tylko dla PRINTER_BUSY", code, goRetry, auto)
		}
	}

	want := map[string]bool{}
	for _, c := range apierrCodes(t) {
		want[c] = true
		if !inDoc[c] {
			t.Errorf("kod %s z apierr.go nieopisany w §2.1", c)
		}
	}
	for c := range inDoc {
		if !want[c] {
			t.Errorf("kod %s z §2.1 nie istnieje w apierr.go", c)
		}
	}
}

// §2.2: wiersze (kod, endpoint, HTTP, details) = dokładnie zbiór emisji z
// contractCases() — w obie strony.
func TestContractDocEmissionsMatchCases(t *testing.T) {
	tables := contractDoc(t)["emisje"]
	if len(tables) != 1 {
		t.Fatalf("oczekiwano jednej tabeli kontrakt:emisje, jest %d", len(tables))
	}
	doc := map[string]bool{}
	for _, row := range tables[0].rows {
		status, err := strconv.Atoi(docCol(t, row, "HTTP"))
		if err != nil {
			t.Fatalf("HTTP %q: %v", row["HTTP"], err)
		}
		sig := docDetailsSig(t, docCol(t, row, "details"))
		for _, ep := range strings.Split(docCol(t, row, "Endpoint"), ",") {
			k := docCol(t, row, "Kod") + " | " + strings.TrimSpace(ep) + " | " + strconv.Itoa(status) + " | " + sig
			if doc[k] {
				t.Errorf("emisja %q dwa razy w §2.2", k)
			}
			doc[k] = true
		}
	}

	got := map[string]bool{}
	for _, c := range contractCases() {
		if c.status < 400 {
			continue
		}
		code, _ := c.want["code"].(string)
		got[code+" | "+contractEndpoints[c.path]+" | "+strconv.Itoa(c.status)+" | "+detailsSig(c.want["details"])] = true
	}
	for _, k := range sortedKeys(got) {
		if !doc[k] {
			t.Errorf("emisja z kodu nieopisana w §2.2: %s", k)
		}
	}
	for _, k := range sortedKeys(doc) {
		if !got[k] {
			t.Errorf("wiersz §2.2 bez przypadku w contractCases(): %s", k)
		}
	}
}

// §1.1/§1.3/§1.4: każde body sukcesu ma DOKŁADNIE pola z tabeli, z jej typem;
// kolumna „Stała” (jeśli nie „—”) to jedyna dopuszczalna wartość.
func TestContractDocSuccessFieldsMatchCases(t *testing.T) {
	type field struct{ typ, constant string }
	doc := map[string]map[string]field{} // "endpoint HTTP" → pole → opis
	for _, tbl := range contractDoc(t)["sukces"] {
		if len(tbl.args) != 2 {
			t.Fatalf("znacznik kontrakt:sukces wymaga argumentów <endpoint> <HTTP>, jest %v", tbl.args)
		}
		id := strings.Join(tbl.args, " ")
		doc[id] = map[string]field{}
		for _, row := range tbl.rows {
			doc[id][docCol(t, row, "Pole")] = field{docCol(t, row, "Typ JSON"), docCol(t, row, "Stała")}
		}
	}

	seen := map[string]bool{}
	for _, c := range contractCases() {
		if c.status >= 400 {
			continue
		}
		id := contractEndpoints[c.path] + " " + strconv.Itoa(c.status)
		seen[id] = true
		fields, ok := doc[id]
		if !ok {
			t.Errorf("%s: sukces %s nieopisany (brak tabeli kontrakt:sukces %s)", c.name, id, id)
			continue
		}
		for k, v := range c.want {
			f, ok := fields[k]
			if !ok {
				t.Errorf("%s: pole %q nieopisane w tabeli %s", c.name, k, id)
				continue
			}
			if jsonType(v) != f.typ {
				t.Errorf("%s: pole %q ma typ %s, dokument %s", c.name, k, jsonType(v), f.typ)
			}
			if f.constant != "—" {
				var want any
				if err := json.Unmarshal([]byte(f.constant), &want); err != nil {
					t.Fatalf("tabela %s, pole %q: stała %q nie jest JSON-em: %v", id, k, f.constant, err)
				}
				if v != want {
					t.Errorf("%s: pole %q = %#v, stała w dokumencie %#v", c.name, k, v, want)
				}
			}
		}
		for k := range fields {
			if _, ok := c.want[k]; !ok {
				t.Errorf("%s: brak pola %q z tabeli %s", c.name, k, id)
			}
		}
	}
	for id := range doc {
		if !seen[id] {
			t.Errorf("tabela kontrakt:sukces %s bez przypadku w contractCases()", id)
		}
	}
}
