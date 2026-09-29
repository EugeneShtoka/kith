package tui

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/media"
)

// mediaState is everything about putting an attachment on screen: the mode, the
// policy a room's rule can override, and what has already been drawn for the open room.
type mediaState struct {
	mode     string
	graphics graphics
	blocks   blockMode
	// cache holds attachment bytes and per-box drawings; nil just redoes the work.
	cache *media.Cache
	// auto and caching are the defaults that rules override per place.
	auto, caching bool
	rules         []domain.MediaRule
	// speed is the default voice-note speed.
	speed float64
	// imageRows caches rendered rows by event ID; a present nil value means "attempted,
	// nothing to show" (not retried). imageLoading dedupes in-flight loads. Both reset on
	// room change.
	imageRows    map[domain.EventID][]string
	imageLoading map[domain.EventID]bool
}

// withNoImages forgets every picture drawn so far, keeping the settings.
func (s mediaState) withNoImages() mediaState {
	s.imageRows = map[domain.EventID][]string{}
	s.imageLoading = map[domain.EventID]bool{}
	return s
}

// rowsFor is the drawn rows for one message's picture, empty when there are none.
func (s mediaState) rowsFor(id domain.EventID) []string { return s.imageRows[id] }

// settled reports whether this message's picture is drawn, known empty, or loading.
func (s mediaState) settled(id domain.EventID) bool {
	if _, done := s.imageRows[id]; done {
		return true
	}
	return s.imageLoading[id]
}

// full reports whether the room carries its cap of inline pictures, in flight included.
func (s mediaState) full(limit int) bool {
	return len(s.imageRows)+len(s.imageLoading) >= limit
}

// loading marks a picture as in flight.
func (s mediaState) loading(id domain.EventID) mediaState {
	s.imageLoading = withEntry(s.imageLoading, id, true)
	return s
}

// loaded records what a load produced; nil rows stop it being retried.
func (s mediaState) loaded(id domain.EventID, rows []string) mediaState {
	s.imageLoading = withoutEntry(s.imageLoading, id)
	s.imageRows = withEntry(s.imageRows, id, rows)
	return s
}

// Media display modes (config [display.media] mode).
const (
	mediaPlaceholder = config.MediaPlaceholder // a text chip only (default)
	mediaInline      = config.MediaInline      // drawn in the timeline with half-blocks
)

// graphics is how a picture is drawn. Only text-based drawing is supported: Kitty
// graphics-protocol pixels are not part of the frame and get painted over.
type graphics int

const (
	graphicsNone graphics = iota
	graphicsBlocks
)

// resolveBlocks turns the configured detail into its block mode (sextants by default).
func resolveBlocks(detail string) blockMode {
	if detail == config.MediaHalf {
		return halfBlocks
	}
	return sextants
}

// resolveGraphics decides how pictures are drawn from the configured mode.
func resolveGraphics(mode string) graphics {
	if mode == mediaInline {
		return graphicsBlocks
	}
	return graphicsNone
}

// Bounds for an automatically sized picture; a configured max_height overrides the
// automatic row ceiling.
const (
	minImageCells    = 8
	maxAutoImageRows = 24
)

// blockMode is how much of a picture one character carries: half-blocks give 1x2 exact
// pixels, sextants 2x3 pixels quantized to two shades. The zero value draws with
// half-blocks, so a missed assignment cannot nil-call.
type blockMode struct {
	px, py int
	// glyph maps a bitmap of lit pixels (bit i is pixel (i%px, i/px)) to its character.
	glyph func(bits int) rune
}

// pixels is px, py, treating an unset mode as half-blocks.
func (b blockMode) pixels() (px, py int) {
	if b.glyph == nil || b.px < 1 || b.py < 1 {
		return halfBlocks.px, halfBlocks.py
	}
	return b.px, b.py
}

// draw is the character for a bitmap, treating an unset mode as half-blocks.
func (b blockMode) draw(bits int) rune {
	if b.glyph == nil {
		return halfBlocks.glyph(bits)
	}
	return b.glyph(bits)
}

// halfBlocks splits a cell into top and bottom pixels.
var halfBlocks = blockMode{px: 1, py: 2, glyph: func(bits int) rune {
	return [...]rune{' ', '▀', '▄', '█'}[bits&0b11]
}}

// sextants split it into a 2x3 grid (Unicode 13).
var sextants = blockMode{px: 2, py: 3, glyph: sextantGlyph}

// sextantGlyph is the character for a 2x3 bitmap. The U+1FB00 run is in bitmap order
// but skips the four patterns that already had characters (blank, left, right, full).
func sextantGlyph(bits int) rune {
	const (
		blank = 0b000000
		left  = 0b010101 // positions 1, 3, 5
		right = 0b101010 // positions 2, 4, 6
		full  = 0b111111
	)
	switch bits & full {
	case blank:
		return ' '
	case left:
		return '▌'
	case right:
		return '▐'
	case full:
		return '█'
	}
	at := bits & full
	offset := at - 1
	if at > left {
		offset--
	}
	if at > right {
		offset--
	}
	return rune(0x1FB00 + offset)
}

// blockRows is how many terminal rows a width-w picture occupies. Independent of the
// drawing mode: size is about cells (about twice as tall as wide), not sampling.
func blockRows(w, imgW, imgH int) int {
	if imgW <= 0 || imgH <= 0 {
		return 1
	}
	return max((w*imgH+imgW-1)/imgW/2, 1)
}

// blockImage draws img as rows of styled text: each cell samples a px-by-py patch,
// splits it into a darker and lighter group, and lights the lighter ones.
func blockImage(mode blockMode, img image.Image, targetW, maxH int) []string {
	if img == nil || targetW < 1 {
		return nil
	}
	b := img.Bounds()
	iw, ih := b.Dx(), b.Dy()
	if iw <= 0 || ih <= 0 {
		return nil
	}
	targetH := blockRows(targetW, iw, ih)
	if maxH > 0 && targetH > maxH {
		// Shrink the width with the height to keep the aspect ratio (a cell is 1x2).
		targetH = maxH
		targetW = max(2*targetH*iw/ih, minImageCells)
	}
	px, py := mode.pixels()
	gridW, gridH := targetW*px, targetH*py

	out := make([]string, 0, targetH)
	patch := make([]color.RGBA, px*py)
	for row := range targetH {
		var sb strings.Builder
		for col := range targetW {
			for y := range py {
				for x := range px {
					r, g, bl := sampleRegion(img, b, col*px+x, row*py+y, gridW, gridH, iw, ih)
					patch[y*px+x] = color.RGBA{R: r, G: g, B: bl, A: 255}
				}
			}
			fg, bg, bits := split(patch)
			fmt.Fprintf(&sb, "\x1b[48;2;%d;%d;%dm\x1b[38;2;%d;%d;%dm%c",
				bg.R, bg.G, bg.B, fg.R, fg.G, fg.B, mode.draw(bits))
		}
		sb.WriteString("\x1b[0m")
		out = append(out, sb.String())
	}
	return out
}

// split reduces a cell's samples to two colors, split at the brightness midpoint, and
// the bitmap of pixels taking the lighter one.
func split(patch []color.RGBA) (fg, bg color.RGBA, bits int) {
	lo, hi := 1<<30, -1
	for _, c := range patch {
		l := luma(c)
		lo, hi = min(lo, l), max(hi, l)
	}
	mid := (lo + hi) / 2

	var light, dark [4]int // r, g, b, count
	for i, c := range patch {
		side := &dark
		if luma(c) > mid {
			side = &light
			bits |= 1 << i
		}
		side[0] += int(c.R)
		side[1] += int(c.G)
		side[2] += int(c.B)
		side[3]++
	}
	// A single-shade cell takes the other side's color.
	if light[3] == 0 {
		light = dark
	}
	if dark[3] == 0 {
		dark = light
	}
	return mean(light), mean(dark), bits
}

// luma is perceived brightness.
func luma(c color.RGBA) int { return 299*int(c.R) + 587*int(c.G) + 114*int(c.B) }

// mean is the average color of one side of a split.
func mean(side [4]int) color.RGBA {
	if side[3] == 0 {
		return color.RGBA{A: 255}
	}
	// #nosec G115 -- each channel is a sum of bytes divided by how many there were
	return color.RGBA{
		R: uint8(side[0] / side[3]), G: uint8(side[1] / side[3]), B: uint8(side[2] / side[3]), A: 255,
	}
}

// sampleRegion averages the source pixels mapping to one destination cell at
// (dstCol, dstRow) in a dstW×dstH grid, box-downsampling the image.
func sampleRegion(img image.Image, bounds image.Rectangle, dstCol, dstRow, dstW, dstH, srcW, srcH int) (r, g, b uint8) {
	x0 := dstCol * srcW / dstW
	x1 := min((dstCol+1)*srcW/dstW+1, srcW)
	y0 := dstRow * srcH / dstH
	y1 := min((dstRow+1)*srcH/dstH+1, srcH)
	var rSum, gSum, bSum, n uint64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			rv, gv, bv, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			rSum += uint64(rv >> 8)
			gSum += uint64(gv >> 8)
			bSum += uint64(bv >> 8)
			n++
		}
	}
	if n == 0 {
		return 0, 0, 0
	}
	return uint8(rSum / n), uint8(gSum / n), uint8(bSum / n) // #nosec G115 -- RGBA()>>8 ≤ 255; sum/n ≤ 255 fits uint8
}
