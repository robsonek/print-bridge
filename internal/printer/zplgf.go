package printer

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
)

// labelCount counts the ^XA label starts in a ZPL stream (minimum 1). The confirm
// timeout scales with it: a multi-parcel job prints serially on the single-threaded
// print-server and needs proportionally longer to finish.
func labelCount(zpl []byte) int {
	if n := bytes.Count(zpl, []byte("^XA")); n > 1 {
		return n
	}
	return 1
}

// EncodeGF wraps a 1-bit bitmap into a ZPL ^GF (Graphic Field) command using the
// ASCII-hex (A) variant with ZPL run-length compression. bytesPerRow*height must
// equal len(bitmap). A set bit = black dot.
//
// Compression is REQUIRED on the XP-423B print-server: an uncompressed full-page
// ^GFA (~250 KB for an A6 label) overruns the server's receive buffer and stalls
// the LPD spool mid-transfer (verified on hardware). RLE shrinks a mostly-white
// label ~7x (e.g. 249 KB -> 33 KB), which transfers and prints cleanly. The
// decoded raster is byte-identical (lossless), so print quality is unchanged.
//
// ZPL ASCII compression codes (per row of 2*bytesPerRow hex nibbles):
//   - repeat count precedes the nibble: G-Y = 1-19, g-z = 20,40,..,400
//   - ","  fill the rest of the row with 0x00 (zero) nibbles
//   - "!"  fill the rest of the row with 0xFF (F) nibbles
//   - ":"  this row is identical to the previous row
func EncodeGF(bitmap []byte, bytesPerRow, height int) string {
	var data strings.Builder
	var prev string
	for y := 0; y < height; y++ {
		row := bitmap[y*bytesPerRow : (y+1)*bytesPerRow]
		h := strings.ToUpper(hex.EncodeToString(row))
		if h == prev {
			data.WriteByte(':')
			prev = h
			continue
		}
		data.WriteString(compressRow(h))
		prev = h
	}
	total := bytesPerRow * height
	return fmt.Sprintf("^GFA,%d,%d,%d,%s", total, total, bytesPerRow, data.String())
}

// compressRow applies ZPL ASCII RLE to one row of hex nibbles.
func compressRow(h string) string {
	var out strings.Builder
	n := len(h)
	for i := 0; i < n; {
		c := h[i]
		run := 1
		for i+run < n && h[i+run] == c {
			run++
		}
		// A run that reaches the end of the row of all-0 or all-F collapses to a
		// single fill code regardless of length.
		if i+run == n && c == '0' {
			out.WriteByte(',')
			break
		}
		if i+run == n && c == 'F' {
			out.WriteByte('!')
			break
		}
		if run > 1 {
			out.WriteString(repeatCode(run))
		}
		out.WriteByte(c)
		i += run
	}
	return out.String()
}

// repeatCode encodes a repeat count using ZPL letters: g-z carry multiples of 20
// (g=20 .. z=400), G-Y carry the remainder 1-19.
//
// A run never exceeds the row length (2*bytesPerRow nibbles). For a 4" printhead
// (XP-423B, ~832 dots = 208 nibbles) the count stays well under 400, so a single
// g-z code always suffices; the loop guards larger heads defensively.
func repeatCode(n int) string {
	var s strings.Builder
	for n >= 400 {
		s.WriteByte('z') // z = 400
		n -= 400
	}
	if n >= 20 {
		hi := n / 20
		s.WriteByte(byte('g' + hi - 1))
		n -= hi * 20
	}
	if n > 0 {
		s.WriteByte(byte('G' + n - 1))
	}
	return s.String()
}

// LabelOptions controls the ZPL label wrapper around a graphic field. All sizes
// are in dots (px @203dpi). Darkness/PrintRate of 0 leave the printer default.
type LabelOptions struct {
	PrintWidthDots   int // ^PW — printable width (printhead, e.g. 832 for a 4" head)
	RasterHeightDots int // graphic height; ^LL is derived to leave a top+bottom margin
	Darkness         int // ^MD darkness boost against faint print (0 = printer default)
	PrintRate        int // ^PR print speed in ips; slower = darker (0 = printer default)
	OffsetX, OffsetY int // ^FO position of the graphic (left/top margin)
}

// WrapLabel builds a full ZPL label around a graphic field.
func WrapLabel(gf string, opt LabelOptions) string {
	var b strings.Builder
	b.WriteString("^XA")
	if opt.Darkness > 0 {
		fmt.Fprintf(&b, "^MD%d", opt.Darkness)
	}
	if opt.PrintRate > 0 {
		fmt.Fprintf(&b, "^PR%d", opt.PrintRate)
	}
	// ^LL spans the graphic plus a symmetric top+bottom margin (OffsetY each).
	ll := opt.RasterHeightDots + 2*opt.OffsetY
	fmt.Fprintf(&b, "^PW%d^LL%d^FO%d,%d%s^FS^XZ", opt.PrintWidthDots, ll, opt.OffsetX, opt.OffsetY, gf)
	return b.String()
}
