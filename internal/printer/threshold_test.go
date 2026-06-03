package printer

import (
	"image"
	"image/color"
	"testing"
)

func TestToMonochrome(t *testing.T) {
	// 8x1 image: left 4 px black, right 4 px white.
	img := image.NewRGBA(image.Rect(0, 0, 8, 1))
	for x := 0; x < 8; x++ {
		if x < 4 {
			img.Set(x, 0, color.Black)
		} else {
			img.Set(x, 0, color.White)
		}
	}
	bitmap, bytesPerRow, height := ToMonochrome(img, 128)
	if bytesPerRow != 1 || height != 1 {
		t.Fatalf("bytesPerRow=%d height=%d, want 1,1", bytesPerRow, height)
	}
	// Black left 4 bits set (MSB first) => 1111 0000 = 0xF0
	if bitmap[0] != 0xF0 {
		t.Errorf("bitmap[0] = %08b, want 11110000", bitmap[0])
	}
}
