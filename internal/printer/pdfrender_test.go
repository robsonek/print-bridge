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
