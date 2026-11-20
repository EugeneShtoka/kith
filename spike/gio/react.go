package main

import (
	"cmp"
	"context"
	"image"
	"log"
	"maps"
	"slices"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/emoji"
)

// picker is the reaction picker over the window: a name to search by, and the emoji it
// offers, the ones this person uses most first.
type picker struct {
	target domain.EventID
	query  widget.Editor
	scores map[string]int // nil until kithd answers
	asked  bool
	btns   map[string]*widget.Clickable
	close  widget.Clickable
	card   widget.Clickable // takes the card's clicks, so only one outside closes it
}

// common are offered after the ones this person has used, before any search.
var common = []string{"👍", "❤️", "😂", "😮", "😢", "🙏", "🎉", "👀", "✅", "🔥", "👎", "🤔"}

// pickerSize is how many emoji the picker shows at most.
const pickerSize = 40

// offered is what the picker shows for its query: without one, the most used and then
// the common ones; with one, every emoji whose name holds it, names starting with it
// first.
func (p *picker) offered() []string {
	q := strings.ToLower(strings.TrimSpace(p.query.Text()))
	if q == "" {
		used := slices.Collect(maps.Keys(p.scores))
		slices.SortFunc(used, func(a, b string) int { return cmp.Or(p.scores[b]-p.scores[a], strings.Compare(a, b)) })
		return firstN(slices.Compact(append(used, common...)), pickerSize)
	}
	return searchEmoji(q, pickerSize)
}

// searchEmoji is at most n emoji whose name holds q, names starting with it first,
// shorter names before longer.
func searchEmoji(q string, n int) []string {
	type hit struct {
		name, glyph string
		prefix      bool
	}
	seen := map[string]bool{}
	var hits []hit
	for _, set := range []map[string]string{emoji.Curated, emoji.Standard} {
		for name, glyph := range set {
			if strings.Contains(name, q) && !seen[glyph] {
				seen[glyph] = true
				hits = append(hits, hit{name, glyph, strings.HasPrefix(name, q)})
			}
		}
	}
	slices.SortFunc(hits, func(a, b hit) int {
		if a.prefix != b.prefix {
			if a.prefix {
				return -1
			}
			return 1
		}
		return cmp.Or(len(a.name)-len(b.name), strings.Compare(a.name, b.name))
	})
	out := make([]string, 0, min(len(hits), n))
	for _, h := range firstN(hits, n) {
		out = append(out, h.glyph)
	}
	return out
}

func firstN[T any](s []T, n int) []T {
	return s[:min(len(s), n)]
}

// react sends glyph as a reaction to the picker's message and closes it.
func (u *ui) react(ctx context.Context, glyph string) {
	room, target := u.timeline.room, u.picking.target
	u.picking = nil
	u.toPane(paneTimeline)
	go func() {
		if err := u.backend.SendReaction(ctx, room, target, glyph); err != nil {
			u.say("Not sent: " + err.Error())
			return
		}
		if err := u.backend.RecordEmoji(ctx, domain.EmojiReaction, room, glyph); err != nil {
			log.Printf("record emoji: %v", err)
		}
	}()
}

// layoutPicker is the picker as a card over the window: Enter takes the first emoji on
// offer, Esc or a click outside closes it.
func (u *ui) layoutPicker(ctx context.Context, gtx C) {
	p := u.picking
	if !p.asked {
		p.asked, p.btns = true, map[string]*widget.Clickable{}
		p.query.SingleLine, p.query.Submit = true, true
		u.focus = &p.query
		room := u.timeline.room
		go func() {
			scores, err := u.backend.EmojiScores(ctx, domain.EmojiReaction, room, nil, "")
			if err != nil {
				log.Printf("emoji scores: %v", err)
			}
			u.mu.Lock()
			if u.picking == p {
				p.scores = scores
			}
			u.mu.Unlock()
			u.w.Invalidate()
		}()
	}
	offered := p.offered()
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &p.query, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			u.picking = nil
			u.toPane(paneTimeline)
			return
		}
	}
	for {
		ev, ok := p.query.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok && len(offered) > 0 {
			u.react(ctx, offered[0])
			return
		}
	}
	if p.close.Clicked(gtx) {
		u.picking = nil
		u.toPane(paneTimeline)
		return
	}
	for glyph, btn := range p.btns {
		if btn.Clicked(gtx) {
			u.react(ctx, glyph)
			return
		}
	}
	shade := u.pal.background
	shade.A = 0x99
	paint.FillShape(gtx.Ops, shade, clip.Rect{Max: gtx.Constraints.Max}.Op())
	p.close.Layout(gtx, func(gtx C) D { return D{Size: gtx.Constraints.Max} })
	layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = min(gtx.Dp(420), gtx.Constraints.Max.X)
		gtx.Constraints.Max.X = gtx.Constraints.Min.X
		m := op.Record(gtx.Ops)
		dims := layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
			const perRow = 10
			rows := []layout.FlexChild{
				layout.Rigid(func(gtx C) D { return u.field(gtx, u.editor(&p.query, "React with: a name, or pick one").Layout) }),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			}
			for start := 0; start < len(offered); start += perRow {
				row := offered[start:min(start+perRow, len(offered))]
				rows = append(rows, layout.Rigid(func(gtx C) D {
					cells := make([]layout.FlexChild, 0, len(row))
					for _, glyph := range row {
						btn := p.btns[glyph]
						if btn == nil {
							btn = new(widget.Clickable)
							p.btns[glyph] = btn
						}
						cells = append(cells, layout.Rigid(func(gtx C) D {
							return btn.Layout(gtx, func(gtx C) D {
								return layout.UniformInset(unit.Dp(4)).Layout(gtx, material.H6(u.th, glyph).Layout)
							})
						}))
					}
					return layout.Flex{}.Layout(gtx, cells...)
				}))
			}
			if len(offered) == 0 {
				rows = append(rows, layout.Rigid(func(gtx C) D {
					l := material.Body2(u.th, "No emoji by that name")
					l.Color = u.pal.muted
					return l.Layout(gtx)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
		call := m.Stop()
		box := image.Rectangle{Max: dims.Size}
		paint.FillShape(gtx.Ops, u.pal.surface, clip.UniformRRect(box, gtx.Dp(8)).Op(gtx.Ops))
		u.outline(gtx, box)
		p.card.Layout(gtx, func(gtx C) D { return D{Size: dims.Size} })
		call.Add(gtx.Ops)
		return dims
	})
}
