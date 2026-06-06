package printer

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// testRenderOpts mirrors the hardware-calibrated defaults (config.Default).
func testRenderOpts() RenderOptions {
	return RenderOptions{
		WidthMM: 104, Threshold: 160,
		RenderWidthDots: 800, PrintWidthDots: 832,
		Darkness: 14, PrintRate: 2, MarginX: 16, MarginY: 8,
	}
}

func TestRenderPDFToZPL(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed; verified in hardware spike")
	}
	pdf, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(testRenderOpts())
	zpl, perr := r.PDFToZPL(context.Background(), pdf)
	if perr != nil {
		t.Fatalf("PDFToZPL: %v", perr)
	}
	s := string(zpl)
	if !strings.HasPrefix(s, "^XA") || !strings.Contains(s, "^GFA,") || !strings.HasSuffix(s, "^XZ") {
		t.Errorf("output not a wrapped ^GF ZPL label: %.40q...", s)
	}
}

// PDFToZPL must inject the calibrated quality params (darkness, speed, margin,
// print width) into the label so faint/clipped print is fixed at the source.
func TestRenderInjectsQualityParams(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(testRenderOpts())
	zpl, perr := r.PDFToZPL(context.Background(), pdf)
	if perr != nil {
		t.Fatalf("PDFToZPL: %v", perr)
	}
	s := string(zpl)
	for _, want := range []string{"^MD14", "^PR2", "^FO16,8", "^PW832"} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered label missing %q: %.60q...", want, s)
		}
	}
}

func TestPDFToZPLRejectsGarbage(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	r := NewPDFRenderer(testRenderOpts())
	if _, err := r.PDFToZPL(context.Background(), []byte("not a pdf")); err == nil {
		t.Fatal("expected error for non-PDF input")
	}
}

// #7 regression: a multi-page PDF must emit ONE ^XA..^XZ per page.
func TestPDFToZPLMultiPageEmitsAllLabels(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed; verified in hardware spike")
	}
	pdf, err := os.ReadFile("testdata/sample-2page.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(testRenderOpts())
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

// #15 regression: an A4-MediaBox PDF on an A6 roll must be REJECTED. With
// -scale-to-x every page renders to the same dot-width, so the guard now keys on
// the real MediaBox (pdfinfo) instead of rasterized width.
func TestPDFToZPLRejectsOversizedA4(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := os.ReadFile("testdata/sample-a4.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(testRenderOpts()) // A6 roll (widthMM 104)
	_, perr := r.PDFToZPL(context.Background(), pdf)
	if perr == nil {
		t.Fatal("A4 PDF on an A6 roll must be rejected (suspect A4 not A6)")
	}
	if !strings.Contains(perr.Error(), "A4") {
		t.Errorf("rejection must hint at A4 suspicion, got: %v", perr)
	}
}

// #15 regression: a correctly-sized A6 page must NOT be rejected.
func TestPDFToZPLAcceptsA6WithinTolerance(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(testRenderOpts())
	if _, perr := r.PDFToZPL(context.Background(), pdf); perr != nil {
		t.Fatalf("A6-sized page must pass the size guard, got: %v", perr)
	}
}
