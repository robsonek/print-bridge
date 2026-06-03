package printer

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// EncodeGF wraps a 1-bit bitmap into a ZPL ^GF (Graphic Field) command using the
// ASCII-hex (A) variant. bytesPerRow*height must equal len(bitmap). A set bit = black dot.
func EncodeGF(bitmap []byte, bytesPerRow, height int) string {
	total := bytesPerRow * height
	return fmt.Sprintf("^GFA,%d,%d,%d,%s", total, total, bytesPerRow, strings.ToUpper(hex.EncodeToString(bitmap)))
}

// WrapLabel builds a full ZPL label around a graphic field, sized in dots (px @203dpi).
func WrapLabel(gf string, widthDots, heightDots int) string {
	return fmt.Sprintf("^XA^PW%d^LL%d^FO0,0%s^FS^XZ", widthDots, heightDots, gf)
}
