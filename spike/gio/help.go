package main

import (
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/config"
)

// The help overlay (`?`, the desktop's help.ts): each key this window binds, with the
// keys [keys] gives it now and [keys]'s own words for it, then the [[keys.jump]].
// [keys.nav] up and down scroll it; help, back or close close it, as a click outside does.
// shortcut: one flat list in the window's action order, not the desktop's sections.

// helpRow is one line of the overlay: its keys, and what they do.
type helpRow struct{ keys, what string }

// helpView is the overlay while it is up.
type helpView struct {
	list  widget.List
	close widget.Clickable
	card  widget.Clickable // takes the card's clicks, so only one outside closes it
}

// helpRows are keys' rows for the window's actions, unbound ones left out.
func helpRows(keys config.Keys) []helpRow {
	var out []helpRow
	for _, a := range actions {
		binding, _ := keys.Binding(a.path)
		what, _ := config.KeyDoc(a.path)
		if binding == "" || binding == "-" || strings.HasPrefix(a.path, "picker.") {
			continue // the chooser's own, told by the chooser
		}
		out = append(out, helpRow{strings.Join(config.SplitKeys(binding), ", "), what})
	}
	for _, j := range keys.Jump {
		out = append(out, helpRow{j.Chord, "go to " + j.Target})
	}
	return out
}

// browseHelp is a [keys] action while the overlay is up; false for one it does not take.
func (u *ui) browseHelp(path string, count int) bool {
	l := &u.helping.list
	if to, ok := moved(path, count, l.Position.First, l.Position.Count, len(u.helpRows)); ok {
		l.ScrollTo(to)
		return true
	}
	switch path {
	case "help", "nav.back", "picker.close":
		u.helping = nil
		return true
	}
	return false
}

// layoutHelp draws the overlay as a card over the window.
func (u *ui) layoutHelp(gtx C) {
	h := u.helping
	if h.close.Clicked(gtx) {
		u.helping = nil
		return
	}
	shade := u.pal.background
	shade.A = 0x99
	paint.FillShape(gtx.Ops, shade, clip.Rect{Max: gtx.Constraints.Max}.Op())
	h.close.Layout(gtx, func(gtx C) D { return D{Size: gtx.Constraints.Max} })
	layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = min(gtx.Dp(640), gtx.Constraints.Max.X)
		gtx.Constraints.Max.X = gtx.Constraints.Min.X
		gtx.Constraints.Max.Y = gtx.Constraints.Max.Y * 4 / 5
		m := op.Record(gtx.Ops)
		dims := layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(material.H6(u.th, "Keys").Layout),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Flexed(1, func(gtx C) D {
					return material.List(u.th, &h.list).Layout(gtx, len(u.helpRows), func(gtx C, i int) D {
						r := u.helpRows[i]
						return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
							return layout.Flex{}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(170), gtx.Dp(170)
									k := material.Body2(u.th, r.keys)
									k.Color, k.Font.Typeface = u.pal.accent, "monospace"
									return k.Layout(gtx)
								}),
								layout.Flexed(1, material.Body2(u.th, r.what).Layout),
							)
						})
					})
				}),
			)
		})
		call := m.Stop()
		box := image.Rectangle{Max: dims.Size}
		paint.FillShape(gtx.Ops, u.pal.surface, clip.UniformRRect(box, gtx.Dp(8)).Op(gtx.Ops))
		u.outline(gtx, box)
		h.card.Layout(gtx, func(gtx C) D { return D{Size: dims.Size} })
		call.Add(gtx.Ops)
		return dims
	})
}
