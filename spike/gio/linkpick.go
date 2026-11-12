package main

import (
	"context"
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Which of a message's links a link key ([keys.timeline] copy_url, open_url,
// open_url_focus) takes, where it holds several, as the desktop's chooser asks
// (Linking.tsx): [keys.nav] up and down move, [keys.picker] accept takes, close closes,
// and a click takes one.

// linkChoice is the chooser over the window: the key that asked, the links, the one
// under the cursor.
type linkChoice struct {
	copy  bool
	links []string
	at    int
	btns  []widget.Clickable
	close widget.Clickable
	card  widget.Clickable // takes the card's clicks, so only one outside closes it
}

// messageLinks are m's links as domain.MessageLinks finds them: its web links, then its
// formatted links', then the chat links kith follows.
func messageLinks(m domain.Message) []string {
	_, spans := drawn(m, text(m))
	var hrefs []string
	for _, s := range spans {
		if s.Link && s.Href != "" {
			hrefs = append(hrefs, s.Href)
		}
	}
	return domain.MessageLinks(m.Body, hrefs)
}

// takeLink copies or opens one of m's links: its only one, or the one picked; false when
// it has none.
func (u *ui) takeLink(ctx context.Context, gtx C, m domain.Message, copyIt bool) bool {
	links := messageLinks(m)
	switch len(links) {
	case 0:
		return false
	case 1:
		u.useLink(ctx, gtx, copyIt, links[0])
	default:
		u.linking = &linkChoice{copy: copyIt, links: links, btns: make([]widget.Clickable, len(links))}
	}
	return true
}

// useLink copies link, or hands it to kithd (Follow), whose link rules pick the window
// that follows it, this one or another; a web link kithd does not follow is the
// browser's.
func (u *ui) useLink(ctx context.Context, gtx C, copyIt bool, link string) {
	if copyIt {
		u.copy(gtx, link, "Copied the link")
		return
	}
	go func() {
		web, err := u.backend.FollowLink(ctx, link)
		switch {
		case err != nil:
			u.say("Could not follow the link: " + err.Error())
		case web:
			openLink(link)
		}
	}()
}

// chooseLink is a [keys] action while the chooser is up; false for one it does not take.
func (u *ui) chooseLink(ctx context.Context, gtx C, path string) bool {
	l := u.linking
	switch path {
	case "nav.up":
		l.at = max(0, l.at-1)
	case "nav.down":
		l.at = min(len(l.links)-1, l.at+1)
	case "picker.accept":
		u.linking = nil
		u.useLink(ctx, gtx, l.copy, l.links[l.at])
	case "picker.close":
		u.linking = nil
	default:
		return false
	}
	return true
}

// layoutLinking draws the chooser as a card over the window.
func (u *ui) layoutLinking(ctx context.Context, gtx C) {
	l := u.linking
	if l.close.Clicked(gtx) {
		u.linking = nil
		return
	}
	for i := range l.btns {
		if l.btns[i].Clicked(gtx) {
			u.linking = nil
			u.useLink(ctx, gtx, l.copy, l.links[i])
			return
		}
	}
	title := "Open which link?"
	if l.copy {
		title = "Copy which link?"
	}
	shade := u.pal.background
	shade.A = 0x99
	paint.FillShape(gtx.Ops, shade, clip.Rect{Max: gtx.Constraints.Max}.Op())
	l.close.Layout(gtx, func(gtx C) D { return D{Size: gtx.Constraints.Max} })
	layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = min(gtx.Dp(520), gtx.Constraints.Max.X)
		gtx.Constraints.Max.X = gtx.Constraints.Min.X
		m := op.Record(gtx.Ops)
		dims := layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
			rows := []layout.FlexChild{
				layout.Rigid(material.H6(u.th, title).Layout),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			}
			for i, link := range l.links {
				rows = append(rows, layout.Rigid(func(gtx C) D {
					return l.btns[i].Layout(gtx, func(gtx C) D {
						m := op.Record(gtx.Ops)
						dims := layout.UniformInset(unit.Dp(6)).Layout(gtx, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							t := material.Body1(u.th, link)
							t.MaxLines = 1
							return t.Layout(gtx)
						})
						call := m.Stop()
						if i == l.at {
							paint.FillShape(gtx.Ops, u.pal.selection, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(6)).Op(gtx.Ops))
						}
						call.Add(gtx.Ops)
						return dims
					})
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
		call := m.Stop()
		box := image.Rectangle{Max: dims.Size}
		paint.FillShape(gtx.Ops, u.pal.surface, clip.UniformRRect(box, gtx.Dp(8)).Op(gtx.Ops))
		u.outline(gtx, box)
		l.card.Layout(gtx, func(gtx C) D { return D{Size: dims.Size} })
		call.Add(gtx.Ops)
		return dims
	})
}
