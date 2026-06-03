package printer

import "image"

// ToMonochrome converts an image to a packed 1-bit bitmap using a hard threshold
// (NOT dithering — dithering destroys barcode scannability at 203dpi). A pixel
// darker than threshold becomes a set bit (black dot). Bits are packed MSB-first.
func ToMonochrome(img image.Image, threshold uint8) (bitmap []byte, bytesPerRow, height int) {
	b := img.Bounds()
	w := b.Dx()
	height = b.Dy()
	bytesPerRow = (w + 7) / 8
	bitmap = make([]byte, bytesPerRow*height)

	for y := 0; y < height; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// Rec.601 luma; RGBA() returns 16-bit, shift to 8-bit.
			lum := (r*299 + g*587 + bl*114) / 1000 >> 8
			if uint8(lum) < threshold {
				bitmap[y*bytesPerRow+x/8] |= 1 << (7 - uint(x%8))
			}
		}
	}
	return bitmap, bytesPerRow, height
}
