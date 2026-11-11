package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// isMain is whether m shows in the main timeline: not a thread's reply.
func isMain(m domain.Message) bool { return m.ThreadRoot == "" || m.ThreadRoot == m.ID }

// --- Reactions ---

// react folds reactions into the open room's: added ones by their own ID, removed ones out.
func (t *timeline) react(rs []domain.Reaction, removed bool) {
	for _, r := range rs {
		held := t.reactions[r.Target]
		held = slices.DeleteFunc(held, func(h domain.Reaction) bool { return h.ID == r.ID })
		if !removed {
			held = append(held, r)
		}
		t.reactions[r.Target] = held
	}
}

func (u *ui) loadReactions(ctx context.Context, room domain.RoomID) {
	rs, err := u.backend.CachedReactions(ctx, room)
	if err != nil {
		log.Printf("reactions: %v", err)
		return
	}
	u.mu.Lock()
	if u.timeline.room == room {
		u.timeline.react(rs, false)
	}
	u.mu.Unlock()
	u.w.Invalidate()
}

// layoutReactions is a message's reactions, a pill for each key with its count.
func (u *ui) layoutReactions(gtx C, rs []domain.Reaction) D {
	tallies := domain.AggregateReactions(rs, "")
	children := make([]layout.FlexChild, 0, 2*len(tallies))
	for _, tally := range tallies {
		children = append(children,
			layout.Rigid(func(gtx C) D {
				m := op.Record(gtx.Ops)
				dims := layout.Inset{Left: unit.Dp(7), Right: unit.Dp(7), Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx,
					material.Caption(u.th, fmt.Sprintf("%s %d", tally.Key, tally.Count)).Layout)
				call := m.Stop()
				paint.FillShape(gtx.Ops, u.pal.raised, clip.UniformRRect(image.Rectangle{Max: dims.Size}, dims.Size.Y/2).Op(gtx.Ops))
				call.Add(gtx.Ops)
				return dims
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(4)}.Layout))
	}
	return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx C) D { return layout.Flex{}.Layout(gtx, children...) })
}

// --- Typing, and short notes ---

// layoutTyping is who is typing in the open room, or the last note (a copy) while fresh.
func (u *ui) layoutTyping(gtx C) D {
	line := ""
	if time.Since(u.noteAt) < 2*time.Second && u.note != "" {
		line = u.note
	} else if names := u.typing[u.current]; len(names) > 0 {
		shown := make([]string, len(names))
		for i, id := range names {
			shown[i] = u.nameOf(id)
		}
		verb := " is typing…"
		if len(shown) > 1 {
			verb = " are typing…"
		}
		line = strings.Join(shown, ", ") + verb
	}
	l := material.Caption(u.th, line)
	l.Color = u.pal.muted
	return layout.Inset{Left: unit.Dp(16), Top: unit.Dp(2)}.Layout(gtx, l.Layout)
}

// nameOf is id's name as the open room's messages give it, else id.
func (u *ui) nameOf(id string) string {
	for _, m := range slices.Backward(u.timeline.msgs) {
		if m.Sender == id && m.SenderName != "" {
			return m.SenderName
		}
	}
	return id
}

// --- Replies ---

// layoutReplyTo is the line over a reply naming what it answers; clicking it scrolls to
// that message when it is loaded and in the main timeline.
func (u *ui) layoutReplyTo(gtx C, t *timeline, st *msgState, m domain.Message) D {
	i := slices.IndexFunc(t.msgs, func(h domain.Message) bool { return h.ID == m.ReplyTo })
	what := "a message not loaded"
	if i >= 0 {
		first, _, _ := strings.Cut(text(t.msgs[i]), "\n")
		if r := []rune(first); len(r) > 80 {
			first = string(r[:80]) + "…"
		}
		what = sender(t.msgs[i]) + ": " + first
	}
	if st.reply.Clicked(gtx) && i >= 0 {
		if k := slices.Index(t.shown, i); k >= 0 {
			t.list.Position = layout.Position{First: k, BeforeEnd: true}
		}
	}
	return st.reply.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				size := image.Pt(gtx.Dp(2), gtx.Dp(16))
				paint.FillShape(gtx.Ops, u.pal.border, clip.Rect{Max: size}.Op())
				return D{Size: size}
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
			layout.Flexed(1, func(gtx C) D {
				l := material.Caption(u.th, "↳ "+what)
				l.Color, l.MaxLines = u.pal.muted, 1
				return l.Layout(gtx)
			}),
		)
	})
}

// --- Threads ---

// openThread shows a thread beside the timeline: what is cached at once, then its newest
// page from kithd.
func (u *ui) openThread(ctx context.Context, root domain.EventID) {
	t := &u.timeline
	t.thread = root
	t.threadList = widget.List{List: layout.List{Axis: layout.Vertical, ScrollToEnd: true}}
	u.focus = &u.threadComposer
	room := t.room
	go func() {
		page, err := u.backend.ThreadPage(ctx, room, root, "", pageSize)
		if err != nil {
			log.Printf("thread: %v", err)
			return
		}
		u.merge(ctx, room, page.Messages)
	}()
}

// threadIndexes is the open thread's root (when loaded) and replies, as indexes of msgs.
func (t *timeline) threadIndexes() []int {
	var out []int
	for i, m := range t.msgs {
		if m.ID == t.thread || m.ThreadRoot == t.thread {
			out = append(out, i)
		}
	}
	return out
}

func (u *ui) layoutThread(ctx context.Context, gtx C) D {
	t := &u.timeline
	if u.closeThread.Clicked(gtx) {
		t.thread = ""
		return D{}
	}
	shown := t.threadIndexes()
	fill(gtx, u.pal.surface)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(8), Top: unit.Dp(6), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						l := material.Subtitle2(u.th, "Thread")
						l.Font.Weight = font.Bold
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D { return u.button(gtx, &u.closeThread, "Close", true, false) }),
				)
			})
		}),
		layout.Flexed(1, func(gtx C) D {
			return material.List(u.th, &t.threadList).Layout(gtx, len(shown), func(gtx C, k int) D {
				return u.layoutMessage(ctx, gtx, t, shown, k, true)
			})
		}),
		layout.Rigid(func(gtx C) D {
			return u.layoutEditor(ctx, gtx, &u.threadComposer, nil, "Reply in thread", t.thread)
		}),
	)
}

// --- Image viewer ---

// layoutViewer covers the window with an image at the most that fits; a click or Esc
// closes it.
func (u *ui) layoutViewer(gtx C) D {
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameEscape},
			pointer.Filter{Target: &u.viewing, Kinds: pointer.Press},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.Event:
			if e.State == key.Press {
				u.viewing = nil
			}
		case pointer.Event:
			u.viewing = nil
		}
	}
	if u.viewing == nil {
		return D{}
	}
	shade := u.pal.background
	shade.A = 0xee
	paint.FillShape(gtx.Ops, shade, clip.Rect{Max: gtx.Constraints.Max}.Op())
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &u.viewing)
	return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx C) D {
		b := u.viewing.Size()
		w, h := fit(b.X, b.Y, gtx.Constraints.Max.X, gtx.Constraints.Max.Y)
		return layout.Center.Layout(gtx, func(gtx C) D {
			gtx.Constraints = layout.Exact(image.Pt(w, h))
			return widget.Image{Src: *u.viewing, Fit: widget.Contain}.Layout(gtx)
		})
	})
}

// --- Rooms in order ---

// bump moves room up the list for a message sent at, as kithd's last-message order has it.
func (u *ui) bump(room domain.RoomID, at time.Time) {
	if !at.After(u.last[room]) {
		return
	}
	u.last[room] = at
	byRecent(u.rooms, u.last)
}
