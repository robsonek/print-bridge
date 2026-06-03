package printer

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRenderPDFToZPL(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed; verified in hardware spike (Task 19)")
	}
	pdf, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(203, 104, 148)
	zpl, perr := r.PDFToZPL(context.Background(), pdf)
	if perr != nil {
		t.Fatalf("PDFToZPL: %v", perr)
	}
	s := string(zpl)
	if !strings.HasPrefix(s, "^XA") || !strings.Contains(s, "^GFA,") || !strings.HasSuffix(s, "^XZ") {
		t.Errorf("output not a wrapped ^GF ZPL label: %.40q...", s)
	}
}

func TestPDFToZPLRejectsGarbage(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	r := NewPDFRenderer(203, 104, 148)
	if _, err := r.PDFToZPL(context.Background(), []byte("not a pdf")); err == nil {
		t.Fatal("expected error for non-PDF input")
	}
}

// #7 regression: a multi-page PDF (multi-parcel label, or label+summary) must
// emit ONE ^XA..^XZ block per page, not silently drop pages 2..N. pdftoppm
// without -singlefile produces out-1.png, out-2.png, ...; each is rendered and
// the labels are concatenated into one ZPL stream so the printer feeds N labels.
func TestPDFToZPLMultiPageEmitsAllLabels(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed; verified in hardware spike (Task 19)")
	}
	pdf, err := os.ReadFile("testdata/sample-2page.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(203, 104, 148)
	zpl, perr := r.PDFToZPL(context.Background(), pdf)
	if perr != nil {
		t.Fatalf("PDFToZPL: %v", perr)
	}
	s := string(zpl)
	if n := strings.Count(s, "^XA"); n != 2 {
		t.Errorf("2-page PDF must emit 2 labels (^XA count), got %d: %.60q...", n, s)
	}
	if n := strings.Count(s, "^XZ"); n != 2 {
		t.Errorf("2-page PDF must emit 2 label terminators (^XZ count), got %d", n)
	}
	if !strings.HasPrefix(s, "^XA") || !strings.HasSuffix(s, "^XZ") {
		t.Errorf("combined stream must start with ^XA and end with ^XZ: %.40q...", s)
	}
}

// #15 regression: configured label size (widthMM/heightMM) must be a live guard.
// An A4-MediaBox PDF rasterizes ~2x wider than the configured A6 roll (allegro
// returns A4 in some cases, allegro-api#10120). PDFToZPL must REJECT it (the
// orchestrator maps the error to INVALID_PDF) instead of silently rendering a
// clipped/mis-scaled label under a false "printed".
func TestPDFToZPLRejectsOversizedA4(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := os.ReadFile("testdata/sample-a4.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(203, 104, 148) // A6 roll
	_, perr := r.PDFToZPL(context.Background(), pdf)
	if perr == nil {
		t.Fatal("A4 PDF on an A6 roll must be rejected (suspect A4 not A6)")
	}
	if !strings.Contains(perr.Error(), "A4") {
		t.Errorf("rejection must hint at A4 suspicion, got: %v", perr)
	}
}

// #15 regression: a correctly-sized A6 page must NOT be rejected by the size
// guard (small rendering tolerance honored).
func TestPDFToZPLAcceptsA6WithinTolerance(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(203, 104, 148)
	if _, perr := r.PDFToZPL(context.Background(), pdf); perr != nil {
		t.Fatalf("A6-sized page must pass the size guard, got: %v", perr)
	}
}
