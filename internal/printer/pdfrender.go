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
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// RenderOptions controls PDF->ZPL rasterization. Defaults are calibrated on a
// real XP-423B (see hardware-spike-findings.md): render below the printhead width
// for a margin, darker threshold + ^MD + slow ^PR against faint thermal print.
type RenderOptions struct {
	WidthMM          int   // guard: reject pages whose MediaBox width >> this (A4 on an A6 roll)
	Threshold        uint8 // grayscale->1-bit cutoff (higher = darker/heavier)
	RenderWidthDots  int   // pdftoppm -scale-to-x target width
	PrintWidthDots   int   // ^PW printhead width
	Darkness         int   // ^MD
	PrintRate        int   // ^PR ips
	MarginX, MarginY int   // ^FO left/top margin
}

// PDFRenderer rasterizes a PDF to ZPL ^GF via poppler's pdftoppm. Native Go PDF
// rendering would require CGO; exec keeps the binary CGO-free.
type PDFRenderer struct {
	opt RenderOptions
}

func NewPDFRenderer(opt RenderOptions) *PDFRenderer {
	return &PDFRenderer{opt: opt}
}

var pageSizeRE = regexp.MustCompile(`Page size:\s+([0-9.]+)\s+x\s+([0-9.]+)\s+pts`)

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

	// #15: guard against an A4 MediaBox on an A6 roll (allegro-api#10120). With
	// -scale-to-x every page rasterizes to the same dot-width, so the old
	// rasterized-width guard can no longer see A4 (A4/A6 share an aspect ratio).
	// Key on the real MediaBox via pdfinfo instead. Also doubles as the
	// invalid-PDF gate (pdfinfo fails on garbage).
	if r.opt.WidthMM > 0 {
		info, err := exec.CommandContext(ctx, "pdfinfo", in).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("pdfinfo failed (invalid PDF?): %v: %s", err, info)
		}
		m := pageSizeRE.FindSubmatch(info)
		if m == nil {
			return nil, fmt.Errorf("pdfinfo: no Page size (invalid PDF?)")
		}
		wpt, _ := strconv.ParseFloat(string(m[1]), 64)
		wmm := wpt / 72.0 * 25.4
		if wmm > 1.4*float64(r.opt.WidthMM) {
			return nil, fmt.Errorf("PDF page is %.0fmm wide, far exceeding the %dmm roll — MediaBox likely A4 not A6 (allegro-api#10120)", wmm, r.opt.WidthMM)
		}
	}

	outPrefix := filepath.Join(dir, "out")
	// -scale-to-x sets the target raster width in dots (kept below the printhead
	// so ^FO can add a left margin without clipping); -scale-to-y -1 preserves the
	// aspect ratio. #7: NO -singlefile — one PNG per page so a multi-parcel /
	// label+summary PDF emits one label each, never silently dropping pages.
	args := []string{"-png", "-scale-to-x", strconv.Itoa(r.opt.RenderWidthDots), "-scale-to-y", "-1", in, outPrefix}
	if out, err := exec.CommandContext(ctx, "pdftoppm", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %v: %s", err, out)
	}

	pages, err := filepath.Glob(outPrefix + "-*.png")
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("pdftoppm produced no png")
	}
	// Sort NUMERICALLY by trailing -N (pdftoppm zero-pads to page-count width);
	// a lexical sort would misorder once N >= 10.
	sort.Slice(pages, func(i, j int) bool { return pageNum(pages[i]) < pageNum(pages[j]) })

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
		bitmap, bytesPerRow, height := ToMonochrome(img, r.opt.Threshold)
		gf := EncodeGF(bitmap, bytesPerRow, height)
		// #7: one ^XA..^XZ per page; concatenated, the printer feeds N labels.
		buf.WriteString(WrapLabel(gf, LabelOptions{
			PrintWidthDots:   r.opt.PrintWidthDots,
			RasterHeightDots: height,
			Darkness:         r.opt.Darkness,
			PrintRate:        r.opt.PrintRate,
			OffsetX:          r.opt.MarginX,
			OffsetY:          r.opt.MarginY,
		}))
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
