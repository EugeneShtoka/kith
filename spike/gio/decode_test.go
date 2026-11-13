package main

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// An image is kept at the size it is drawn, never enlarged.
func TestDecodeScalesDownToFit(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 400, 200))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ maxW, maxH, w, h int }{
		{100, 100, 100, 50},
		{400, 50, 100, 50},
		{1000, 1000, 400, 200},
	} {
		img, err := decode(data.Bytes(), tc.maxW, tc.maxH)
		if err != nil {
			t.Fatal(err)
		}
		if got := img.Size(); got != image.Pt(tc.w, tc.h) {
			t.Errorf("decode in %d×%d = %v, want %d×%d", tc.maxW, tc.maxH, got, tc.w, tc.h)
		}
	}
}
