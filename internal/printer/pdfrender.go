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
	"sort"
	"strconv"
	"strings"
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

	// #7: NO -singlefile. pdftoppm emits one PNG per page, zero-padded:
	// out-1.png, out-2.png, ... (a multi-parcel shipment / label+summary PDF is
	// multi-page). -singlefile would rasterize page 1 ONLY and silently drop the
	// rest under a false "printed". -r sets DPI.
	cmd := exec.CommandContext(ctx, "pdftoppm", "-png", "-r", fmt.Sprintf("%d", r.dpi), in, outPrefix)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %v: %s", err, combined)
	}

	pages, err := filepath.Glob(outPrefix + "-*.png")
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("pdftoppm produced no png")
	}
	// Sort NUMERICALLY by the trailing -N, not lexically: pdftoppm zero-pads to
	// the page-count width, so "out-10.png" must follow "out-9.png" — a lexical
	// sort would misorder pages once N >= 10.
	sort.Slice(pages, func(i, j int) bool { return pageNum(pages[i]) < pageNum(pages[j]) })

	// #15: the configured roll size (widthMM/heightMM) is the expected raster
	// width in dots. Allegro sometimes returns an A4 MediaBox instead of A6
	// (allegro-api#10120) which rasterizes ~2x wider; rendering it on an A6 roll
	// clips/mis-positions the label. Reject grossly-oversized pages (the
	// orchestrator maps the error to INVALID_PDF) rather than silently printing a
	// broken label under "printed". Small deviations are tolerated (continue).
	expectedWidthDots := int(float64(r.widthMM) * float64(r.dpi) / 25.4)

	var buf bytes.Buffer
	for _, p := range pages {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", filepath.Base(p), err)
		}
		bitmap, bytesPerRow, height := ToMonochrome(img, r.threshold)
		widthDots := bytesPerRow * 8
		if expectedWidthDots > 0 && float64(widthDots) > 1.4*float64(expectedWidthDots) {
			return nil, fmt.Errorf("rasterized page %s is %d dots wide, far exceeding the configured %dmm roll (~%d dots) — MediaBox likely A4 not A6 (allegro-api#10120)",
				filepath.Base(p), widthDots, r.widthMM, expectedWidthDots)
		}
		gf := EncodeGF(bitmap, bytesPerRow, height)
		// #7: one ^XA..^XZ per page; concatenated, the printer feeds N labels.
		buf.WriteString(WrapLabel(gf, widthDots, height))
	}
	return buf.Bytes(), nil
}

// pageNum extracts the trailing -N from "out-7.png" for numeric page ordering;
// fail-safe to 0 if absent/unparseable.
func pageNum(path string) int {
	base := strings.TrimSuffix(filepath.Base(path), ".png")
	if i := strings.LastIndexByte(base, '-'); i >= 0 {
		n, _ := strconv.Atoi(base[i+1:])
		return n
	}
	return 0
}
