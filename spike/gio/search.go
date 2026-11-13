package main

import (
	"context"
	"fmt"
	"image"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/spike/gio/seltext"
)

// Search, as the desktop's (Find.tsx, session.search): the toolbar's field reads words
// with from:, since: and until: as the TUI does (domain.ParseSearch), in the scope the
// button beside it cycles (everywhere, this place, this room); [keys.search] all and room
// go to the field with that scope. Hits, newest first, take
// the room list's place, and the room list's keys move through them; a hit opens its room
// at its message (seek), a reply in a thread with its thread beside.

// searchLimit is how many hits a search lists, the desktop's.
const searchLimit = 200

// seekPage is how many older messages each read back to a sought message asks for.
const seekPage = 500

type searchScope int

const (
	scopeAll searchScope = iota
	scopeRow
	scopeRoom
)

var scopeNames = [...]string{scopeAll: "Everywhere", scopeRow: "This place", scopeRoom: "This room"}

// searchView is a search and its hits while they are listed.
type searchView struct {
	hits  []domain.SearchHit
	busy  bool
	note  string // said in place of a count: nothing to search for, an error
	at    int    // the hit under the room list's cursor
	btns  []widget.Clickable
	texts []seltext.State
	list  widget.List
	close widget.Clickable
}

// search asks kithd for what the toolbar's field finds in the scope chosen; only the
// last search asked is listed.
func (u *ui) runSearch(ctx context.Context) {
	filter := domain.ParseSearch(u.search.Text(), time.Now())
	s := &searchView{list: widget.List{List: layout.List{Axis: layout.Vertical}}}
	u.searching = s
	u.toPane(paneRooms) // the keys leave the field for the hits
	rooms := domain.EveryRoom()
	switch u.searchScope {
	case scopeRow:
		ids := slices.Collect(func(yield func(domain.RoomID) bool) {
			for _, r := range u.rooms {
				if u.inRow(r.ID) && !yield(r.ID) {
					return
				}
			}
		})
		rooms = domain.TheseRooms(ids)
	case scopeRoom:
		rooms = domain.TheseRooms([]domain.RoomID{u.current})
	}
	switch {
	case filter.SenderField != "":
		s.note = "from:" + filter.SenderField + " names a phonebook field: search it in the TUI"
		return
	case filter.Terms == "" && filter.Sender == "" && filter.Since.IsZero() && filter.Until.IsZero():
		s.note = "Nothing to search for"
		return
	case rooms.None():
		s.note = "No room to search"
		return
	}
	s.busy = true
	go func() {
		hits, err := u.backend.SearchMessages(ctx, domain.SearchRequest{Filter: filter, Rooms: rooms, Limit: searchLimit})
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.searching != s {
			return // closed, or another asked since
		}
		s.busy = false
		if err != nil {
			s.note = "Search failed: " + err.Error()
		}
		// A hit is listed only while its room is in the profile.
		held := map[domain.RoomID]bool{}
		for _, r := range u.rooms {
			held[r.ID] = true
		}
		for _, h := range hits {
			if held[h.RoomID] {
				s.hits = append(s.hits, h)
			}
		}
		s.btns, s.texts = make([]widget.Clickable, len(s.hits)), make([]seltext.State, len(s.hits))
		u.w.Invalidate()
	}()
}

// jumpTo opens room at event: the timeline reads back until it holds it (seek).
func (u *ui) jumpTo(ctx context.Context, room domain.RoomID, event domain.EventID) {
	if room != u.current {
		u.openLocked(ctx, room)
	}
	u.timeline.jump, u.roomAt = event, room
}

// browseHits is a [keys.nav] action on the hits in the room list's place; false for
// one it does not take.
func (u *ui) browseHits(ctx context.Context, path string, count int) bool {
	s := u.searching
	if to, ok := moved(path, count, s.at, s.list.Position.Count, len(s.hits)); ok && len(s.hits) > 0 {
		s.at = to
		reveal(&s.list.List, to, len(s.hits))
		return true
	}
	switch path {
	case "nav.open":
		if s.at >= len(s.hits) {
			return false
		}
		u.jumpTo(ctx, s.hits[s.at].RoomID, s.hits[s.at].EventID)
		u.toPane(paneTimeline)
	case "nav.back":
		u.searching = nil
	default:
		return false
	}
	return true
}

// seek moves the cursor to the message the timeline is to jump to, once read: a reply
// in a thread selects its root and opens the thread. Not read yet, it reads the page
// before, until the room's start says it is gone.
func (u *ui) seek(ctx context.Context) {
	t := &u.timeline
	i := slices.IndexFunc(t.msgs, func(m domain.Message) bool { return m.ID == t.jump })
	switch {
	case i >= 0:
		m := t.msgs[i]
		if !isMain(m) && m.ThreadRoot != "" {
			t.jump = m.ThreadRoot
			if t.thread != m.ThreadRoot {
				u.openThread(ctx, m.ThreadRoot)
			}
			return
		}
		if k := slices.Index(t.shown, i); k >= 0 {
			// Scrolled to whether laid out yet or not, two messages above it in view:
			// reveal only moves a list it has drawn, and this one may be pinned to its end.
			t.sel = m.ID
			t.list.ScrollTo(max(0, k-2))
		}
		t.jump = ""
		u.toPane(paneTimeline)
	case t.loading:
	case t.next != "":
		t.loading = true
		go u.older(ctx, t.room, t.next, seekPage)
	default:
		u.note, u.noteAt, t.jump = "That message is no longer cached", time.Now(), ""
	}
}

// layoutSearch lists the hits where the room list is.
func (u *ui) layoutSearch(ctx context.Context, gtx C) D {
	s := u.searching
	if s.close.Clicked(gtx) {
		u.searching = nil
		return D{}
	}
	for i := range s.btns {
		if s.btns[i].Clicked(gtx) {
			s.at = i
			u.jumpTo(ctx, s.hits[i].RoomID, s.hits[i].EventID)
		}
	}
	said := s.note
	switch {
	case s.busy:
		said = "Searching"
	case said == "" && len(s.hits) == 1:
		said = "1 message"
	case said == "":
		said = fmt.Sprintf("%d messages", len(s.hits))
	}
	names := map[domain.RoomID]string{}
	for _, r := range u.rooms {
		names[r.ID] = r.DisplayName()
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						l := material.Body2(u.th, said)
						l.Color = u.pal.muted
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D { return u.button(gtx, &s.close, "Close", true, false) }),
				)
			})
		}),
		layout.Flexed(1, func(gtx C) D {
			return material.List(u.th, &s.list).Layout(gtx, len(s.hits), func(gtx C, i int) D {
				return u.layoutHit(gtx, s, i, names[s.hits[i].RoomID])
			})
		}),
	)
}

// layoutHit is one hit: its room, sender and time over its excerpt, matched words marked.
func (u *ui) layoutHit(gtx C, s *searchView, i int, room string) D {
	h := s.hits[i]
	if room == "" {
		room = string(h.RoomID)
	}
	who := h.SenderKnownAs
	if who == "" {
		who = h.SenderName
	}
	if who == "" {
		who = h.Sender
	}
	return s.btns[i].Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		dims := layout.Inset{Left: unit.Dp(10), Right: unit.Dp(12), Top: unit.Dp(6), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					l := material.Caption(u.th, room+" · "+who+" · "+h.Timestamp.Local().Format("Jan 2, 2006 15:04"))
					l.Color, l.MaxLines = u.pal.muted, 1
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					excerpt := h.Snippet
					if h.FileName != "" {
						excerpt += " (" + h.FileName + ")"
					}
					return seltext.Text(&s.texts[i], u.th.Shaper, u.snippet(excerpt)...).Layout(gtx)
				}),
			)
		})
		call := m.Stop()
		r := image.Rectangle{Min: image.Pt(gtx.Dp(4), 0), Max: image.Pt(dims.Size.X-gtx.Dp(4), dims.Size.Y)}
		if u.pane == paneRooms && i == s.at {
			paint.FillShape(gtx.Ops, u.pal.selection, clip.UniformRRect(r, gtx.Dp(6)).Op(gtx.Ops))
		}
		call.Add(gtx.Ops)
		return dims
	})
}

// snippet styles a hit's excerpt: each matched run (between domain.HighlightStart and
// HighlightEnd) bold, in the accent.
func (u *ui) snippet(text string) []seltext.SpanStyle {
	plain := seltext.SpanStyle{Font: font.Font{Typeface: u.th.Face}, Size: u.th.TextSize * 0.93, Color: u.pal.text}
	var out []seltext.SpanStyle
	for i, part := range strings.Split(text, domain.HighlightStart) {
		rest := part
		if i > 0 {
			hit, after, _ := strings.Cut(part, domain.HighlightEnd)
			marked := plain
			marked.Content, marked.Font.Weight, marked.Color = hit, font.Bold, u.pal.accent
			out, rest = append(out, marked), after
		}
		if rest != "" {
			p := plain
			p.Content = rest
			out = append(out, p)
		}
	}
	return out
}
