package printer

import (
	"strings"
	"testing"
)

func TestEncodeGF(t *testing.T) {
	// 2 bytes/row, 2 rows: row0 = 0xFF 0x00, row1 = 0x00 0xFF
	bitmap := []byte{0xFF, 0x00, 0x00, 0xFF}
	got := EncodeGF(bitmap, 2, 2)
	want := "^GFA,4,4,2,FF0000FF"
	if got != want {
		t.Errorf("EncodeGF = %q, want %q", got, want)
	}
}

func TestWrapLabel(t *testing.T) {
	out := WrapLabel("^GFA,4,4,2,FF0000FF", 832, 1184)
	if !strings.HasPrefix(out, "^XA") || !strings.HasSuffix(out, "^XZ") {
		t.Errorf("label not wrapped in ^XA..^XZ: %q", out)
	}
	if !strings.Contains(out, "^PW832") || !strings.Contains(out, "^LL1184") {
		t.Errorf("label missing print width/length: %q", out)
	}
}
