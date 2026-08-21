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
	"slices"
	"sort"
	"strconv"
	"strings"
)

// RenderOptions controls PDF->ZPL rasterization. Defaults are calibrated on a
// real XP-423B (see hardware-spike-findings.md): render below the printhead width
// for a margin, darker threshold + ^MD + slow ^PR against faint thermal print.
type RenderOptions struct {
	WidthMM          int   // guard: reject pages wider than this in BOTH orientations (A4 on an A6 roll); landscape pages auto-rotate 90°
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

// Matches both per-page lines ("Page    2 size:  595.28 x 841.89 pts", emitted
// with -f/-l) and the single-page form ("Page size: ..." without them).
var (
	pageSizeRE  = regexp.MustCompile(`Page(?:\s+\d+)?\s+size:\s+([0-9.]+)\s+x\s+([0-9.]+)\s+pts`)
	pageRotRE   = regexp.MustCompile(`Page(?:\s+\d+)?\s+rot:\s+(-?\d+)`)
	pageCountRE = regexp.MustCompile(`(?m)^Pages:\s+(\d+)`)
)

// pageGeom is one page's raw MediaBox size plus its /Rotate flag as reported
// by pdfinfo. Viewers and pdftoppm render the page TURNED by rot, so sizing
// logic must key on displayDimsMM, never the raw box — e.g. macOS Preview
// "rotates" a PDF by flipping /Rotate alone, leaving the MediaBox landscape.
type pageGeom struct {
	wPt, hPt float64
	rot      int
}

// quarterTurned reports whether /Rotate swaps the page's axes (90°/270°).
func (g pageGeom) quarterTurned() bool {
	r := ((g.rot % 360) + 360) % 360
	return r == 90 || r == 270
}

// displayDimsMM is the page size in mm as rendered (after applying /Rotate).
func (g pageGeom) displayDimsMM() (w, h float64) {
	w, h = g.wPt/72.0*25.4, g.hPt/72.0*25.4
	if g.quarterTurned() {
		w, h = h, w
	}
	return w, h
}

// parsePageGeoms extracts per-page geometry from pdfinfo output, pairing each
// "Page N rot:" line with the preceding "Page N size:" line positionally.
func parsePageGeoms(info []byte) []pageGeom {
	var geoms []pageGeom
	for _, line := range strings.Split(string(info), "\n") {
		if m := pageSizeRE.FindStringSubmatch(line); m != nil {
			w, errW := strconv.ParseFloat(m[1], 64)
			h, errH := strconv.ParseFloat(m[2], 64)
			if errW != nil || errH != nil {
				continue
			}
			geoms = append(geoms, pageGeom{wPt: w, hPt: h})
			continue
		}
		if m := pageRotRE.FindStringSubmatch(line); m != nil && len(geoms) > 0 {
			if r, err := strconv.Atoi(m[1]); err == nil {
				geoms[len(geoms)-1].rot = r
			}
		}
	}
	return geoms
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

	// #15: guard against an A4 MediaBox on an A6 roll (allegro-api#10120), keyed
	// on pdfinfo geometry (doubles as the invalid-PDF gate — pdfinfo fails on
	// garbage). Sizing uses DISPLAY dimensions (MediaBox turned by /Rotate; see
	// pageGeom). A page too wide for the roll but fitting after a 90° turn
	// (physically-landscape GLS labels) is auto-rotated at raster time instead
	// of rejected; only pages fitting in NEITHER orientation are refused.
	var geoms []pageGeom
	var rotate []bool // per page: raster needs OUR extra 90° turn to lie across the roll
	if r.opt.WidthMM > 0 {
		// -f 1 -l -1: per-page "Page N size:" lines. The default output reports
		// only page 1, which let an "A6 cover + A4 body" PDF slip past the guard.
		info, err := exec.CommandContext(ctx, "pdfinfo", "-f", "1", "-l", "-1", in).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("pdfinfo failed (invalid PDF?): %v: %s", err, info)
		}
		geoms = parsePageGeoms(info)
		if len(geoms) == 0 {
			return nil, fmt.Errorf("pdfinfo: no Page size (invalid PDF?)")
		}
		// The per-page render below trusts this enumeration; a mismatch would
		// silently drop pages (#7), so refuse instead.
		if m := pageCountRE.FindSubmatch(info); m != nil {
			if n, _ := strconv.Atoi(string(m[1])); n != len(geoms) {
				return nil, fmt.Errorf("pdfinfo reports %d pages but enumerated %d (invalid PDF?)", n, len(geoms))
			}
		}
		limit := 1.4 * float64(r.opt.WidthMM)
		rotate = make([]bool, len(geoms))
		for i, g := range geoms {
			w, h := g.displayDimsMM()
			switch {
			case w <= limit: // lies across the roll as displayed (slight squash tolerated)
			case h <= limit: // landscape wider than the roll: turn 90°, print full-size
				rotate[i] = true
			default:
				return nil, fmt.Errorf("PDF page %d is %.0fx%.0fmm, exceeding the %dmm roll in both orientations — MediaBox likely A4 not A6 (allegro-api#10120)", i+1, w, h, r.opt.WidthMM)
			}
		}
	}

	// pdftoppm's -scale-to-x/-scale-to-y act on the page's RAW (pre-/Rotate)
	// axes while the emitted raster IS turned (verified empirically: a rot-90
	// page with -scale-to-x W comes out W dots TALL). The single-call fast path
	// scales every page with -scale-to-x, so it is only correct when no page is
	// quarter-turned — by flag or by our auto-rotation.
	needPerPage := slices.Contains(rotate, true)
	for _, g := range geoms {
		if g.quarterTurned() {
			needPerPage = true
			break
		}
	}

	type rasterPage struct {
		path   string
		turned bool
	}
	var pages []rasterPage
	if !needPerPage {
		outPrefix := filepath.Join(dir, "out")
		// -scale-to-x sets the target raster width in dots (kept below the printhead
		// so ^FO can add a left margin without clipping); -scale-to-y -1 preserves the
		// aspect ratio. #7: NO -singlefile — one PNG per page so a multi-parcel /
		// label+summary PDF emits one label each, never silently dropping pages.
		args := []string{"-png", "-scale-to-x", strconv.Itoa(r.opt.RenderWidthDots), "-scale-to-y", "-1", in, outPrefix}
		if out, err := exec.CommandContext(ctx, "pdftoppm", args...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("pdftoppm failed: %v: %s", err, out)
		}
		found, err := filepath.Glob(outPrefix + "-*.png")
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			return nil, fmt.Errorf("pdftoppm produced no png")
		}
		// Sort NUMERICALLY by trailing -N (pdftoppm zero-pads to page-count width);
		// a lexical sort would misorder once N >= 10.
		sort.Slice(found, func(i, j int) bool { return pageNum(found[i]) < pageNum(found[j]) })
		for _, p := range found {
			pages = append(pages, rasterPage{path: p})
		}
	} else {
		// Page-by-page rendering with a per-page scale axis. The final raster
		// must be RenderWidthDots WIDE after all turns (poppler's /Rotate + our
		// optional extra 90°), and the scale flags address RAW axes — so the
		// axis is -scale-to-x exactly when the two turns cancel out (both or
		// neither), -scale-to-y when exactly one applies.
		for i, turned := range rotate {
			prefix := filepath.Join(dir, fmt.Sprintf("p%d", i+1))
			page := strconv.Itoa(i + 1)
			args := []string{"-png", "-f", page, "-l", page}
			if turned == geoms[i].quarterTurned() {
				args = append(args, "-scale-to-x", strconv.Itoa(r.opt.RenderWidthDots), "-scale-to-y", "-1")
			} else {
				args = append(args, "-scale-to-y", strconv.Itoa(r.opt.RenderWidthDots), "-scale-to-x", "-1")
			}
			args = append(args, in, prefix)
			if out, err := exec.CommandContext(ctx, "pdftoppm", args...).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("pdftoppm failed on page %d: %v: %s", i+1, err, out)
			}
			found, err := filepath.Glob(prefix + "-*.png")
			if err != nil {
				return nil, err
			}
			if len(found) != 1 {
				return nil, fmt.Errorf("pdftoppm produced %d pngs for page %d, want 1", len(found), i+1)
			}
			pages = append(pages, rasterPage{path: found[0], turned: turned})
		}
	}

	var buf bytes.Buffer
	for _, p := range pages {
		b, err := os.ReadFile(p.path)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", filepath.Base(p.path), err)
		}
		if p.turned {
			img = rotate90CW(img)
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

// rotate90CW returns src turned a quarter turn clockwise — how a landscape
// label gets laid along the roll (the direction is arbitrary for printing).
func rotate90CW(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
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
