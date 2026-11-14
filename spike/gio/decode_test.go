package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
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

// An image whose turn to load comes after it left the screen is not read (the backend
// here is nil: reading would panic), and is forgotten so that drawing it again reads it.
func TestAnImageScrolledPastIsNotRead(t *testing.T) {
	u := &ui{imageSlots: make(chan struct{}, 1), frame: 7}
	p := &picture{drawn: 6}
	u.timeline = timeline{room: "matrix:!r:x", images: map[domain.EventID]*picture{"$a": p}}
	u.loadImage(context.Background(), "matrix:!r:x", "$a", p, 100, 100)
	if _, ok := u.timeline.images["$a"]; ok {
		t.Error("the image scrolled past is still held, so it never loads when drawn again")
	}
}
