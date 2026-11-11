package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"strings"
	"unicode"
	"unicode/utf16"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// hueOf is id's initials hue, as the desktop hashes it (faces.ts hueOf: FNV-1a over
// UTF-16 units).
func hueOf(id string, hues int) int {
	h := uint32(0x811c9dc5)
	for _, unit := range utf16.Encode([]rune(id)) {
		h ^= uint32(unit)
		h *= 0x01000193
	}
	return int(h % uint32(hues))
}

// initials stand in for a name's picture, as faces.ts has them: the first letter of its
// first word and of its last; "#" for a name with no letter, "?" for an empty one.
func initials(name string) string {
	if !strings.ContainsFunc(name, unicode.IsLetter) {
		if strings.TrimSpace(name) == "" {
			return "?"
		}
		return "#"
	}
	if strings.HasPrefix(name, "@") && strings.Contains(name, ":") && !strings.ContainsAny(name, " \t") {
		name = name[1:strings.Index(name, ":")] // a Matrix ID is named by its local part
	}
	var words []string
	for _, w := range strings.Fields(name) {
		if i := strings.IndexFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }); i >= 0 {
			r := []rune(w[i:])[0]
			words = append(words, string(r))
		}
	}
	if len(words) == 0 {
		return "?"
	}
	out := words[0]
	if len(words) > 1 {
		out += words[len(words)-1]
	}
	return strings.ToUpper(out)
}

// face is a person's or a room's picture: person "" for the room's own.
type face struct {
	room   domain.RoomID
	person string
}

// layoutFace draws a face size dp square: its picture once fetched (version is the one
// kithd names, "" for none), else its initials on its hue.
func (u *ui) layoutFace(ctx context.Context, gtx C, f face, version, id, name string, size unit.Dp) D {
	px := gtx.Dp(size)
	box := image.Pt(px, px)
	radius := gtx.Dp(6)
	if version != "" && u.pal.pictures {
		p := u.faces[f]
		if p == nil {
			p = new(picture)
			u.faces[f] = p
			go u.loadFace(ctx, f, p)
		}
		if p.ok {
			defer clip.UniformRRect(image.Rectangle{Max: box}, radius).Push(gtx.Ops).Pop()
			gtx.Constraints = layout.Exact(box)
			return widget.Image{Src: p.op, Fit: widget.Cover}.Layout(gtx)
		}
	}
	hue := u.pal.hues[hueOf(id, len(u.pal.hues))]
	paint.FillShape(gtx.Ops, hue, clip.UniformRRect(image.Rectangle{Max: box}, radius).Op(gtx.Ops))
	l := material.Label(u.th, unit.Sp(float32(size)*0.38), initials(name))
	l.Font.Weight, l.Color = font.Bold, color.NRGBA{R: 0x11, G: 0x12, B: 0x14, A: 0xff}
	gtx.Constraints = layout.Exact(box)
	layout.Center.Layout(gtx, l.Layout)
	return D{Size: box}
}

func (u *ui) loadFace(ctx context.Context, f face, p *picture) {
	data, err := u.backend.LoadPicture(ctx, f.room, f.person)
	var img image.Image
	if err == nil && len(data) > 0 {
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	u.mu.Lock()
	if err == nil && img != nil {
		p.op, p.ok = paint.NewImageOp(img), true
	}
	p.err = err
	u.mu.Unlock()
	u.w.Invalidate()
}
