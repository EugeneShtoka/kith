package main

import (
	"context"
	"image"
	"strconv"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message's history ([keys.timeline] history, the TUI's H, the desktop's history.ts),
// shown as a card over the window: every version it had, oldest first, with when and by
// how much it changed, and its deletion, as kithd's MessageHistory tells them. A
// message's "(edited)" opens it too. [keys.nav] up and down scroll it; history, back or
// close close it, as a click outside does.

// historyView is the card while it is up: its message, and what kithd said (read false
// while it is asked).
type historyView struct {
	msg       domain.Message
	read      bool
	revisions []domain.Revision
	deletion  domain.Deletion
	err       error
	list      widget.List
	close     widget.Clickable
	card      widget.Clickable // takes the card's clicks, so only one outside closes it
}

// historyRow is one line of the card: a heading (when, and what happened) over words,
// either of which may be "".
type historyRow struct{ head, body string }

// openHistory shows m's history, read from kithd now.
func (u *ui) openHistory(ctx context.Context, m domain.Message) {
	h := &historyView{msg: m, list: widget.List{List: layout.List{Axis: layout.Vertical}}}
	u.revisions = h
	room := u.timeline.room
	go func() {
		revisions, deletion, err := u.backend.MessageHistory(ctx, room, m.ID)
		u.mu.Lock()
		h.read, h.revisions, h.deletion, h.err = true, revisions, deletion, err
		u.mu.Unlock()
		u.w.Invalidate()
	}()
}

// historyRows are the card's lines: each version, then the deletion; or why none is
// listed. Where none was kept, a message never edited is its own words (a deleted one's,
// kept under [display.deleted] keep).
func historyRows(h *historyView) []historyRow {
	m := h.msg
	switch {
	case !h.read:
		return []historyRow{{head: "Reading its history…"}}
	case h.err != nil:
		return []historyRow{{head: "Could not read its history: " + h.err.Error()}}
	}
	var rows []historyRow
	for i, r := range h.revisions {
		what := "Sent"
		if i > 0 {
			what = "Edited" + changeNote(h.revisions[i-1].Body, r.Body)
		}
		rows = append(rows, historyRow{head: stamp(r.At) + what, body: r.Body})
	}
	switch {
	case len(rows) > 0:
	case m.Edited:
		rows = append(rows, historyRow{head: "It was edited before its versions were being kept, and the server has nothing left of them."})
	case m.Body != "":
		rows = append(rows, historyRow{head: stamp(m.Timestamp) + "Sent", body: m.Body})
	case m.Redacted:
		rows = append(rows, historyRow{head: "What it said was not kept ([display.deleted] keep is off)."})
	default:
		rows = append(rows, historyRow{head: "It has never been edited."})
	}
	if m.Redacted {
		by, reason, at := m.RedactedBy, m.RedactedReason, m.RedactedAt
		if d := h.deletion; d.By != "" {
			by, reason = d.By, d.Reason
		}
		if !h.deletion.At.IsZero() {
			at = h.deletion.At
		}
		line := stamp(at) + "Deleted"
		if by != "" && by != m.Sender {
			line += " by " + by
		}
		if reason != "" {
			line += ": " + reason
		}
		rows = append(rows, historyRow{head: line})
	}
	return rows
}

// stamp is a time as a heading's start, "" for none.
func stamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Local().Format("Mon 2 Jan 3:04 PM") + " · "
}

// changeNote is how much a version changed, in characters added or removed (history.ts).
func changeNote(before, after string) string {
	switch diff := len([]rune(after)) - len([]rune(before)); {
	case diff > 0:
		return " +" + strconv.Itoa(diff)
	case diff < 0:
		return " −" + strconv.Itoa(-diff)
	}
	return " reworded"
}

// browseHistory is a [keys] action while the card is up; false for one it does not take.
func (u *ui) browseHistory(path string, count int) bool {
	l := &u.revisions.list
	if to, ok := moved(path, count, l.Position.First, l.Position.Count, len(historyRows(u.revisions))); ok {
		l.ScrollTo(to)
		return true
	}
	switch path {
	case "timeline.history", "nav.back", "picker.close":
		u.revisions = nil
		return true
	}
	return false
}

// layoutHistory draws the card over the window.
func (u *ui) layoutHistory(gtx C) {
	h := u.revisions
	if h.close.Clicked(gtx) {
		u.revisions = nil
		return
	}
	rows := historyRows(h)
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
				layout.Rigid(material.H6(u.th, "History").Layout),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Flexed(1, func(gtx C) D {
					return material.List(u.th, &h.list).Layout(gtx, len(rows), func(gtx C, i int) D {
						return layout.Inset{Bottom: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
							head := material.Caption(u.th, rows[i].head)
							head.Color = u.pal.muted
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(head.Layout),
								layout.Rigid(material.Body1(u.th, rows[i].body).Layout),
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
