package printer

import (
	"strings"
	"testing"
)

func TestEncodeGF(t *testing.T) {
	// EncodeGF emits ^GFA with ASCII run-length compression (verified on XP-423B:
	// uncompressed ^GFA hex overruns the print-server buffer and stalls the spool).
	// Compression codes: G-Y = run 1-19, g-z = run 20-400, "," = rest of row is
	// 0x00, "!" = rest of row is 0xFF, ":" = row identical to the previous row.
	cases := []struct {
		name      string
		bitmap    []byte
		bpr, h    int
		want      string
	}{
		{
			// row0 "FF00": 2xF -> "HF" (H=run 2), then rest zeros -> ","
			// row1 "00FF": 2x0 -> "H0", then rest F -> "!"
			name: "runs and trailing fills", bitmap: []byte{0xFF, 0x00, 0x00, 0xFF}, bpr: 2, h: 2,
			want: "^GFA,4,4,2,HF,H0!",
		},
		{
			name: "all-zero row -> comma", bitmap: []byte{0x00, 0x00}, bpr: 2, h: 1,
			want: "^GFA,2,2,2,,",
		},
		{
			name: "all-ones row -> bang", bitmap: []byte{0xFF, 0xFF}, bpr: 2, h: 1,
			want: "^GFA,2,2,2,!",
		},
		{
			// row0 "AB" -> "AB" (singletons), row1 identical -> ":"
			name: "repeated row -> colon", bitmap: []byte{0xAB, 0xAB}, bpr: 1, h: 2,
			want: "^GFA,2,2,1,AB:",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EncodeGF(c.bitmap, c.bpr, c.h); got != c.want {
				t.Errorf("EncodeGF = %q, want %q", got, c.want)
			}
		})
	}
}

func TestLabelCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"^XAa^XZ", 1},
		{"^XAa^XZ^XAb^XZ", 2},
		{"^XA^XA^XA", 3},
		{"", 1},        // min 1 (degenerate)
		{"garbage", 1}, // min 1 (no ^XA)
	}
	for _, c := range cases {
		if got := labelCount([]byte(c.in)); got != c.want {
			t.Errorf("labelCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestWrapLabel(t *testing.T) {
	out := WrapLabel("^GFA,4,4,2,FF0000FF", LabelOptions{
		PrintWidthDots: 832, RasterHeightDots: 1100,
		Darkness: 14, PrintRate: 2, OffsetX: 16, OffsetY: 8,
	})
	if !strings.HasPrefix(out, "^XA") || !strings.HasSuffix(out, "^XZ") {
		t.Errorf("label not wrapped in ^XA..^XZ: %q", out)
	}
	for _, want := range []string{"^MD14", "^PR2", "^PW832", "^FO16,8", "^LL1116"} {
		if !strings.Contains(out, want) {
			t.Errorf("label missing %q: %q", want, out)
		}
	}
}

func TestWrapLabelOmitsZeroDarknessAndRate(t *testing.T) {
	// Darkness/PrintRate of 0 mean "leave the printer default" -> no ^MD/^PR emitted.
	out := WrapLabel("^GFA,4,4,2,FF0000FF", LabelOptions{
		PrintWidthDots: 832, RasterHeightDots: 1184,
	})
	if strings.Contains(out, "^MD") || strings.Contains(out, "^PR") {
		t.Errorf("zero darkness/rate must not emit ^MD/^PR: %q", out)
	}
	if !strings.Contains(out, "^FO0,0") {
		t.Errorf("zero offset should be ^FO0,0: %q", out)
	}
}
