package printer

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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

// Guard A4 musi widzieć KAŻDĄ stronę: pdfinfo bez -f/-l raportuje tylko
// stronę 1, więc PDF "A6-okładka + A4-treść" prześlizgiwał się przez guard
// i drukował A4 ściśnięte na rolce A6. Parser musi też parować "Page N rot:"
// z poprzedzającą linią "Page N size:" — flaga /Rotate decyduje o orientacji.
func TestParsePageGeomsParsesPerPageLines(t *testing.T) {
	info := []byte(`Pages:           2
Page    1 size:  453.54 x 286.3 pts
Page    1 rot:   90
Page    2 size:  595.28 x 841.89 pts (A4)
Page    2 rot:   0
File size:       1029 bytes`)
	geoms := parsePageGeoms(info)
	if len(geoms) != 2 {
		t.Fatalf("parsePageGeoms: %d stron, want 2 (%v)", len(geoms), geoms)
	}
	if geoms[0].wPt != 453.54 || geoms[0].hPt != 286.3 || geoms[0].rot != 90 {
		t.Errorf("geoms[0] = %+v, want {453.54 286.3 90}", geoms[0])
	}
	if geoms[1].wPt != 595.28 || geoms[1].hPt != 841.89 || geoms[1].rot != 0 {
		t.Errorf("geoms[1] = %+v, want {595.28 841.89 0}", geoms[1])
	}
}

// Stary format (pdfinfo bez -f/-l) dalej musi się parsować.
func TestParsePageGeomsParsesPlainPageSizeLine(t *testing.T) {
	info := []byte("Pages:           1\nPage size:       295 x 420 pts\nPage rot:        270\nFile size:       1028 bytes")
	geoms := parsePageGeoms(info)
	if len(geoms) != 1 || geoms[0].wPt != 295 || geoms[0].hPt != 420 || geoms[0].rot != 270 {
		t.Fatalf("parsePageGeoms = %+v, want [{295 420 270}]", geoms)
	}
}

// displayDimsMM: /Rotate 90/270 zamienia szerokość z wysokością (tak stronę
// renderuje pdftoppm i tak widzi ją użytkownik); 0/180 — bez zmian.
func TestDisplayDimsMMHonorsRotate(t *testing.T) {
	g := pageGeom{wPt: 453.54, hPt: 286.3, rot: 90} // 160×101mm + /Rotate 90
	w, h := g.displayDimsMM()
	if w > 102 || h < 159 {
		t.Errorf("displayDimsMM(rot 90) = %.1fx%.1fmm, want ~101x160mm", w, h)
	}
	g.rot = 180
	if w, _ := g.displayDimsMM(); w < 159 {
		t.Errorf("displayDimsMM(rot 180) width = %.1fmm, want ~160mm", w)
	}
}

// --- helper: minimalny poprawny PDF o sterowalnym MediaBox i /Rotate ---

type pdfPage struct {
	w, h float64 // MediaBox w punktach
	rot  int     // /Rotate
}

// genPDF buduje od zera minimalny, poprawny strukturalnie PDF (z xref) —
// pdfinfo/pdftoppm wymagają prawidłowych offsetów.
func genPDF(t *testing.T, pages []pdfPage) []byte {
	t.Helper()
	var buf bytes.Buffer
	var offsets []int
	writeObj := func(body string) {
		offsets = append(offsets, buf.Len())
		buf.WriteString(body)
	}
	buf.WriteString("%PDF-1.4\n")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	writeObj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	writeObj(fmt.Sprintf("2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n",
		strings.Join(kids, " "), len(pages)))
	for i, p := range pages {
		content := "0 0 0 rg 10 10 60 40 re f\n"
		writeObj(fmt.Sprintf("%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Rotate %d /Resources << >> /Contents %d 0 R >>\nendobj\n",
			3+2*i, p.w, p.h, p.rot, 4+2*i))
		writeObj(fmt.Sprintf("%d 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n",
			4+2*i, len(content), content))
	}
	xrefAt := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xrefAt)
	return buf.Bytes()
}

var llRE = regexp.MustCompile(`\^LL(\d+)`)

// labelLengths wyciąga wartości ^LL (długość etykiety w dotach) z ZPL.
func labelLengths(t *testing.T, zpl string) []int {
	t.Helper()
	var lls []int
	for _, m := range llRE.FindAllStringSubmatch(zpl, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("nieparsowalne ^LL w %q", m[0])
		}
		lls = append(lls, n)
	}
	return lls
}

// Regresja (etykieta GLS 2026-08-21): strona 160×101mm z flagą /Rotate 90 jest
// WIZUALNIE pionowa (101mm szerokości — mieści się na rolce; tak ją renderuje
// pdftoppm), a guard czytał surowy MediaBox (160mm) i odrzucał jako "A4".
// macOS Preview obraca właśnie flagą, nie zmienia MediaBox.
func TestPDFToZPLAcceptsRotateFlaggedLandscape(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf := genPDF(t, []pdfPage{{w: 453.54, h: 286.3, rot: 90}}) // 160×101mm
	r := NewPDFRenderer(testRenderOpts())
	zpl, perr := r.PDFToZPL(context.Background(), pdf)
	if perr != nil {
		t.Fatalf("strona z /Rotate 90 mieszcząca się na rolce musi przejść, got: %v", perr)
	}
	s := string(zpl)
	if n := strings.Count(s, "^XA"); n != 1 {
		t.Errorf("want 1 etykieta, got %d", n)
	}
	// Wysoki raster (~1267 dotów przy 800 szerokości) = wydruk pionowy
	// pełnowymiarowy; ścieśniony poziomy miałby ^LL ~521.
	if lls := labelLengths(t, s); len(lls) != 1 || lls[0] < 1000 {
		t.Errorf("^LL = %v, want jedna wartość > 1000 (pionowy pełnowymiarowy render)", lls)
	}
}

// Etykieta fizycznie pozioma (160×101mm BEZ flagi /Rotate — oryginał z panelu
// GLS) nie mieści się na rolce na szerokość, ale mieści się po obrocie o 90° —
// agent musi ją SAM obrócić i wydrukować pełnowymiarowo, nie odrzucić.
func TestPDFToZPLAutoRotatesPhysicalLandscape(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf := genPDF(t, []pdfPage{{w: 453.54, h: 286.3, rot: 0}})
	r := NewPDFRenderer(testRenderOpts())
	zpl, perr := r.PDFToZPL(context.Background(), pdf)
	if perr != nil {
		t.Fatalf("pozioma etykieta mieszcząca się po obrocie musi przejść, got: %v", perr)
	}
	s := string(zpl)
	if n := strings.Count(s, "^XA"); n != 1 {
		t.Errorf("want 1 etykieta, got %d", n)
	}
	if lls := labelLengths(t, s); len(lls) != 1 || lls[0] < 1000 {
		t.Errorf("^LL = %v, want jedna wartość > 1000 (raster obrócony o 90°, nie ścieśniony)", lls)
	}
}

// A4 pozostaje odrzucane niezależnie od flagi /Rotate — nie mieści się na
// rolce w ŻADNEJ orientacji (sedno guardu #15 / allegro-api#10120).
func TestPDFToZPLRejectsA4EvenRotated(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf := genPDF(t, []pdfPage{{w: 595.28, h: 841.89, rot: 90}})
	r := NewPDFRenderer(testRenderOpts())
	_, perr := r.PDFToZPL(context.Background(), pdf)
	if perr == nil {
		t.Fatal("A4 z /Rotate 90 nadal musi być odrzucone")
	}
	if !strings.Contains(perr.Error(), "A4") {
		t.Errorf("odrzucenie musi wskazywać podejrzenie A4, got: %v", perr)
	}
}

// Dokument mieszany (pionowa A6 + pozioma GLS) — obie strony muszą wyjść jako
// osobne, pełnowymiarowe etykiety we właściwej kolejności (ścieżka per-page).
func TestPDFToZPLMixedOrientationsEmitFullSizeLabels(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf := genPDF(t, []pdfPage{{w: 295, h: 420, rot: 0}, {w: 453.54, h: 286.3, rot: 0}})
	r := NewPDFRenderer(testRenderOpts())
	zpl, perr := r.PDFToZPL(context.Background(), pdf)
	if perr != nil {
		t.Fatalf("PDFToZPL: %v", perr)
	}
	s := string(zpl)
	if n := strings.Count(s, "^XA"); n != 2 {
		t.Errorf("want 2 etykiety, got %d", n)
	}
	lls := labelLengths(t, s)
	if len(lls) != 2 {
		t.Fatalf("^LL count = %d, want 2 (%v)", len(lls), lls)
	}
	// p1: 800*420/295 ≈ 1139 (+2*8 marginesu); p2 po obrocie: 800*453.54/286.3 ≈ 1267 (+16).
	if lls[0] < 1000 || lls[1] < 1000 {
		t.Errorf("^LL = %v, want obie > 1000 (pełnowymiarowe, nie ścieśnione)", lls)
	}
	if lls[1] < lls[0] {
		t.Errorf("^LL = %v, strona 2 (dłuższa) musi wyjść jako druga — kolejność stron zaburzona?", lls)
	}
}

// rotate90CW: piksel (0,0) źródła ląduje w prawym górnym rogu; wymiary zamienione.
func TestRotate90CW(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 2, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			src.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	src.SetGray(0, 0, color.Gray{Y: 0}) // marker w lewym górnym
	dst := rotate90CW(src)
	if b := dst.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("bounds po obrocie = %v, want 3x2", b)
	}
	if r, g, b, _ := dst.At(2, 0).RGBA(); r != 0 || g != 0 || b != 0 {
		t.Errorf("marker (0,0) po obrocie CW musi być w (2,0), tam jest %v", dst.At(2, 0))
	}
	if r, _, _, _ := dst.At(0, 0).RGBA(); r == 0 {
		t.Errorf("(0,0) po obrocie powinno być białe")
	}
}

// Regresja: mieszany PDF (strona 1 = A6, strona 2 = A4) musi być ODRZUCONY —
// wcześniej guard patrzył tylko na stronę 1.
func TestPDFToZPLRejectsMixedA6A4(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := os.ReadFile("testdata/sample-mixed.pdf")
	if err != nil {
		t.Fatal(err)
	}
	r := NewPDFRenderer(testRenderOpts()) // rolka A6 (widthMM 104)
	_, perr := r.PDFToZPL(context.Background(), pdf)
	if perr == nil {
		t.Fatal("PDF z A4 na stronie 2 musi być odrzucony (guard widzi tylko stronę 1?)")
	}
	if !strings.Contains(perr.Error(), "A4") {
		t.Errorf("odrzucenie musi wskazywać podejrzenie A4, got: %v", perr)
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
