package printer

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// T22: mechanizm tekstu publicznego (publicerr.go). Klasy toolOutcome
// sprawdzane na prawdziwych procesach (sonda §2.5 specu).
func TestToolOutcomeClasses(t *testing.T) {
	fakeTools(t, map[string]string{
		"narzedzie-exit3":  `printf 'SEKRET-OUT\n' >&2; exit 3`,
		"narzedzie-zabite": `kill -9 $$`,
	})
	run := func(ctx context.Context, name string) error {
		return exec.CommandContext(ctx, name).Run()
	}
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"kod-wyjscia", run(context.Background(), "narzedzie-exit3"), "narzedzie exited with code 3"},
		{"sygnal", run(context.Background(), "narzedzie-zabite"), "narzedzie terminated by signal"},
		{"martwy-ctx", run(dead, "narzedzie-exit3"), "narzedzie not started (request context ended)"},
		{"brak-binarki", run(context.Background(), "narzedzia-nie-ma"), "narzedzie could not be started"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toolOutcome("narzedzie", c.err); got != c.want {
				t.Errorf("toolOutcome(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

func TestPublicMessage(t *testing.T) {
	src := errors.New("pełny błąd SEKRET-SRC")
	pub := withPublic("lp exited with code 2", src)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"publiczny", pub, "lp submit failed: lp exited with code 2"},
		{"publiczny-opakowany", fmt.Errorf("opakowanie SEKRET-WRAP: %w", pub), "lp submit failed: lp exited with code 2"},
		{"bez-publicznego", errors.New("SEKRET-RAW"), "lp submit failed"},
		{"wlasny-literal", ownErrorf("pdfinfo reports %d pages but enumerated %d (invalid PDF?)", 2, 1),
			"lp submit failed: pdfinfo reports 2 pages but enumerated 1 (invalid PDF?)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := publicMessage("lp submit failed", c.err)
			if got != c.want {
				t.Errorf("publicMessage = %q, want %q", got, c.want)
			}
			if got == "" || strings.Contains(got, "SEKRET") {
				t.Errorf("publicMessage = %q: pusty albo z tekstem źródła", got)
			}
		})
	}
}

// Error() publicError = tekst źródła (log agenta, pola health bez zmian), a
// łańcuch błędów zostaje zachowany.
func TestPublicErrorKeepsSourceErrorAndChain(t *testing.T) {
	src := errors.New("pełny błąd SEKRET-SRC")
	pub := withPublic("klasa", src)
	if pub.Error() != src.Error() {
		t.Errorf("Error() = %q, want %q", pub.Error(), src.Error())
	}
	if !errors.Is(pub, src) {
		t.Error("errors.Is(withPublic(x, src), src) = false — łańcuch przerwany")
	}
	own := ownErrorf("pdftoppm produced %d pngs for page %d, want 1", 2, 1)
	if want := "pdftoppm produced 2 pngs for page 1, want 1"; own.Error() != want {
		t.Errorf("ownErrorf Error() = %q, want %q", own.Error(), want)
	}
}
