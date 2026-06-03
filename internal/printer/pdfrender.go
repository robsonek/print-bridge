package printer

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
)

// PDFRenderer rasterizes a PDF to ZPL ^GF via poppler's pdftoppm. Native Go PDF
// rendering would require CGO; exec keeps the binary CGO-free.
type PDFRenderer struct {
	dpi       int
	widthMM   int
	heightMM  int
	threshold uint8
}

func NewPDFRenderer(dpi, widthMM, heightMM int) *PDFRenderer {
	return &PDFRenderer{dpi: dpi, widthMM: widthMM, heightMM: heightMM, threshold: 128}
}

func (r *PDFRenderer) PDFToZPL(ctx context.Context, pdf []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pb-pdf-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, pdf, 0o600); err != nil {
		return nil, err
	}
	outPrefix := filepath.Join(dir, "out")

	// -singlefile -> out.png (first page only). -r sets DPI.
	cmd := exec.CommandContext(ctx, "pdftoppm", "-png", "-singlefile", "-r", fmt.Sprintf("%d", r.dpi), in, outPrefix)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %v: %s", err, combined)
	}

	pngBytes, err := os.ReadFile(outPrefix + ".png")
	if err != nil {
		return nil, fmt.Errorf("pdftoppm produced no png: %w", err)
	}
	img, _, err := image.Decode(bytesReader(pngBytes))
	if err != nil {
		return nil, err
	}

	bitmap, bytesPerRow, height := ToMonochrome(img, r.threshold)
	gf := EncodeGF(bitmap, bytesPerRow, height)
	widthDots := bytesPerRow * 8
	return []byte(WrapLabel(gf, widthDots, height)), nil
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
