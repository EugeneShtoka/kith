package tui

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHalfBlockImage(t *testing.T) {
	t.Parallel()

	// A 4×4 solid red image.
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}

	rows := blockImage(halfBlocks, img, 4, 16)
	// halfBlockRows(4,4,4) = ceil(16/4)/2 = 2 rows; each row is 4 half-block cells.
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Four cells wide.
	if n := ansi.StringWidth(rows[0]); n != 4 {
		t.Errorf("cells per row = %d, want 4", n)
	}
	if !strings.Contains(rows[0], "48;2;255;0;0") || !strings.Contains(rows[0], "38;2;255;0;0") {
		t.Errorf("row missing truecolor red: %q", rows[0])
	}

	// The height cap is honored.
	tall := image.NewRGBA(image.Rect(0, 0, 4, 400))
	if rows := blockImage(halfBlocks, tall, 4, 5); len(rows) != 5 {
		t.Errorf("capped height rows = %d, want 5", len(rows))
	}
	if blockImage(halfBlocks, nil, 4, 16) != nil {
		t.Error("nil image should render no rows")
	}
}

// A picture that hits the row cap keeps its shape: the width comes down with the
// height.
func TestHalfBlocksKeepTheirShapeAtTheRowCap(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		imgW, imgH int
	}{
		{"landscape", 1200, 900},
		{"portrait", 900, 1600},
		{"square", 1000, 1000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const targetW, maxH = 118, 22
			rows := blockImage(halfBlocks, solidImage(tt.imgW, tt.imgH), targetW, maxH)
			if len(rows) == 0 {
				t.Fatal("nothing was drawn")
			}
			if len(rows) > maxH {
				t.Fatalf("drew %d rows, past the cap of %d", len(rows), maxH)
			}
			// One cell is one pixel wide and two tall, so the drawn aspect is cols :
			// 2*rows.
			cols := ansi.StringWidth(rows[0])
			want := float64(tt.imgW) / float64(tt.imgH)
			got := float64(cols) / float64(2*len(rows))
			if got < want*0.85 || got > want*1.15 {
				t.Errorf("a %dx%d picture drew %d x %d cells — aspect %.2f, want about %.2f",
					tt.imgW, tt.imgH, cols, len(rows), got, want)
			}
		})
	}
}

// solidImage is a plain image of the given size; only its proportions matter here.
func solidImage(w, h int) image.Image {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.SetRGBA(x, y, color.RGBA{R: 128, G: 128, B: 128, A: 255})
		}
	}
	return m
}

// Every 2x3 bitmap has its own character, and the four that Unicode had already are the
// ones it had already.
func TestSextantGlyphs(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		bits int
		want rune
	}{
		{"nothing lit", 0b000000, ' '},
		{"the left column", 0b010101, '▌'},
		{"the right column", 0b101010, '▐'},
		{"everything lit", 0b111111, '█'},
		{"top left only", 0b000001, '\U0001FB00'},
		{"top right only", 0b000010, '\U0001FB01'},
		{"both of the top", 0b000011, '\U0001FB02'},
		{"the last one", 0b111110, '\U0001FB3B'},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := sextantGlyph(tt.bits); got != tt.want {
				t.Errorf("sextantGlyph(%06b) = %q (U+%04X), want %q", tt.bits, got, got, tt.want)
			}
		})
	}

	// All sixty-four patterns, all different, all inside the block Unicode set aside.
	seen := map[rune]int{}
	for bits := range 64 {
		g := sextantGlyph(bits)
		if prev, dup := seen[g]; dup {
			t.Fatalf("patterns %06b and %06b both draw %q", prev, bits, g)
		}
		seen[g] = bits
		if g >= 0x1FB00 && g > 0x1FB3B {
			t.Errorf("pattern %06b maps to U+%04X, past the end of the sextants", bits, g)
		}
	}
}

// Sextants carry more of the picture at the same size on screen.
func TestSextantsCarryMoreAtTheSameSize(t *testing.T) {
	t.Parallel()

	img := solidImage(1200, 900)
	const targetW, maxH = 40, 40

	half := blockImage(halfBlocks, img, targetW, maxH)
	sext := blockImage(sextants, img, targetW, maxH)
	if len(half) == 0 || len(sext) == 0 {
		t.Fatal("nothing was drawn")
	}
	if len(half) != len(sext) {
		t.Errorf("half-blocks drew %d rows and sextants %d; want the same picture the same size",
			len(half), len(sext))
	}
	if ansi.StringWidth(half[0]) != ansi.StringWidth(sext[0]) {
		t.Errorf("half-blocks drew %d columns and sextants %d; want the same",
			ansi.StringWidth(half[0]), ansi.StringWidth(sext[0]))
	}
}

// A drawing is made of drawable cells, whatever is in the picture: a flat picture needs
// no glyph at all and is simply the background color.
func TestAFlatPictureDrawsAsItsOwnColor(t *testing.T) {
	t.Parallel()

	rows := blockImage(sextants, solidImage(400, 300), 20, 20)
	if len(rows) == 0 {
		t.Fatal("nothing was drawn")
	}
	if got := ansi.StringWidth(rows[0]); got != 20 {
		t.Errorf("a row is %d cells wide, want 20", got)
	}
	if !strings.Contains(rows[0], "48;2;128;128;128") {
		t.Errorf("a flat grey picture is not drawn in its own color: %q", rows[0])
	}
}
