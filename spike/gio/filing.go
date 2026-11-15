package main

import (
	"context"
	"slices"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Filing the room under the room list's cursor into spaces and tags ([keys.rooms]
// spaces, the desktop's filing.ts): kithd offers the places in the TUI's order, ticked
// where they hold the room (FilingPlaces), and files it (FileRoom): it writes the tags,
// archives on the network and records releases; the rail follows when kithd says it
// changed. In the card [keys.nav] up and down move, [keys.picker] toggle or a click
// ticks, accept or File files, back or close closes, as a click outside does.

// filingView is the card while it is up: the room, kithd's places (nil while asked)
// with the ticks, the row under the cursor, and while kithd files it, busy.
type filingView struct {
	room        domain.Room
	name        string // as the rail names it
	places      []domain.FilingPlace
	ticked      []bool
	at          int
	busy        bool
	note        string
	btns        []widget.Clickable
	file        widget.Clickable
	close, card widget.Clickable
}

// openFiling asks kithd where room can be filed.
func (u *ui) openFiling(ctx context.Context, room domain.Room) {
	f := &filingView{room: room, name: u.roomName(room)}
	u.filing = f
	spaces := u.display.FilingSpaces
	go func() {
		places, matched, err := u.backend.FilingPlaces(ctx, room.ID, spaces)
		u.mu.Lock()
		defer u.mu.Unlock()
		defer u.w.Invalidate()
		switch {
		case err != nil:
			f.note = "Could not read where it can go: " + err.Error()
		case !matched:
			f.note = "Nothing in [display] filing_spaces matches a space of this profile."
		}
		f.places, f.ticked, f.btns = places, make([]bool, len(places)), make([]widget.Clickable, len(places))
		for i, p := range places {
			f.ticked[i] = p.In
		}
	}()
}

// asked is what filing f asks of kithd: each place ticked otherwise than it holds the room.
func (f *filingView) asked() (tags map[string]bool, spaces map[domain.SpaceID]bool) {
	tags, spaces = map[string]bool{}, map[domain.SpaceID]bool{}
	for i, p := range f.places {
		switch {
		case f.ticked[i] == p.In:
		case p.Space != "":
			spaces[p.Space] = f.ticked[i]
		default:
			tags[p.Tag] = f.ticked[i]
		}
	}
	return tags, spaces
}

// applyFiling files the room as the ticks say; refused, the card stays with them, saying why.
func (u *ui) applyFiling(ctx context.Context) {
	f := u.filing
	if f.busy || f.places == nil {
		return
	}
	tags, spaces := f.asked()
	if len(tags) == 0 && len(spaces) == 0 {
		u.filing = nil
		u.note, u.noteAt = "No change.", time.Now() // under mu, as the keys and clicks hold it
		return
	}
	f.busy, f.note = true, ""
	go func() {
		r, err := u.backend.FileRoom(ctx, f.room.ID, tags, spaces)
		if err != nil {
			u.mu.Lock()
			f.busy, f.note = false, "Not filed: "+err.Error()
			u.mu.Unlock()
			u.w.Invalidate()
			return
		}
		u.mu.Lock()
		if u.filing == f {
			u.filing = nil
		}
		u.mu.Unlock()
		u.say(f.told(tags, spaces, r))
	}()
}

// told says what came of a filing, as the desktop's told does.
func (f *filingView) told(tags map[string]bool, spaces map[domain.SpaceID]bool, r *v1.FileRoomResponse) string {
	var into, outOf []string
	for _, p := range f.places { // the picker's order
		if in, ok := tags[p.Tag]; ok && p.Space == "" {
			if in {
				into = append(into, p.Label)
			} else {
				outOf = append(outOf, p.Label)
			}
		}
	}
	var moved, parts []string
	if len(into) > 0 {
		moved = append(moved, "in "+strings.Join(into, ", "))
	}
	if len(outOf) > 0 {
		moved = append(moved, "out of "+strings.Join(outOf, ", "))
	}
	if len(moved) > 0 {
		parts = append(parts, f.name+" is "+strings.Join(moved, ", and "))
	}
	parts = append(parts, f.spacesTold(spaces, r.GetSpaces())...)
	if r.GetArchiveFailed() != "" {
		parts = append(parts, "its network did not archive or unarchive it: "+r.GetArchiveFailed())
	}
	if r.GetReleaseFailed() != "" {
		parts = append(parts, "out of a filter's tag here, but a filter may move it back elsewhere: "+r.GetReleaseFailed())
	}
	if len(parts) == 0 {
		return "No change."
	}
	return strings.Join(parts, " · ")
}

// spacesTold says what came of each space filing, grouped as the TUI's line groups them.
func (f *filingView) spacesTold(asked map[domain.SpaceID]bool, filed []*v1.SpaceFiled) []string {
	var added, removed, kept, refused []string
	for _, s := range filed {
		id := domain.SpaceID(s.GetSpaceId())
		name := string(id)
		if i := slices.IndexFunc(f.places, func(p domain.FilingPlace) bool { return p.Space == id }); i >= 0 {
			name = f.places[i].Label
		}
		switch s.GetOutcome() {
		case v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_FILED:
			if asked[id] {
				added = append(added, name)
			} else {
				removed = append(removed, name)
			}
		case v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_PARENT_KEPT:
			kept = append(kept, name)
		default:
			if s.GetError() != "" {
				name += " (" + s.GetError() + ")"
			}
			refused = append(refused, name)
		}
	}
	var out []string
	for _, g := range []struct {
		lead, tail string
		names      []string
	}{
		{"filed into ", "", added}, {"taken out of ", "", removed},
		{"moved in ", " but kept its own record of where it lives (no power there)", kept},
		{"refused by ", "", refused},
	} {
		if len(g.names) > 0 {
			out = append(out, g.lead+strings.Join(g.names, ", ")+g.tail)
		}
	}
	return out
}

// chooseFiling is a [keys] action while the card is up; false for one it does not take.
func (u *ui) chooseFiling(ctx context.Context, path string) bool {
	f := u.filing
	switch path {
	case "nav.up":
		f.at = max(0, f.at-1)
	case "nav.down":
		f.at = max(0, min(len(f.places)-1, f.at+1))
	case "picker.toggle":
		if !f.busy && f.at < len(f.places) {
			f.ticked[f.at] = !f.ticked[f.at]
		}
	case "picker.accept", "nav.open":
		u.applyFiling(ctx)
	case "rooms.spaces", "nav.back", "picker.close":
		u.filing = nil
	default:
		return false
	}
	return true
}

// layoutFiling draws the card over the window.
func (u *ui) layoutFiling(ctx context.Context, gtx C) {
	f := u.filing
	if f.close.Clicked(gtx) {
		u.filing = nil
		return
	}
	if f.file.Clicked(gtx) {
		u.applyFiling(ctx)
	}
	for i := range f.btns {
		if f.btns[i].Clicked(gtx) && !f.busy {
			f.at, f.ticked[i] = i, !f.ticked[i]
		}
	}
	u.layoutCard(gtx, &f.close, &f.card, func(gtx C) D {
		rows := []layout.FlexChild{
			layout.Rigid(material.H6(u.th, "File "+f.name).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		}
		if f.places == nil && f.note == "" {
			rows = append(rows, layout.Rigid(material.Body2(u.th, "Reading where it can go…").Layout))
		}
		for i, p := range f.places {
			mark := "☐ "
			if f.ticked[i] {
				mark = "☑ "
			}
			kind := "tag"
			if p.Space != "" {
				kind = "space"
			}
			rows = append(rows, layout.Rigid(func(gtx C) D {
				return u.cardRow(gtx, &f.btns[i], i == f.at, func(gtx C) D {
					l := material.Caption(u.th, "  "+kind)
					l.Color = u.pal.muted
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Flexed(1, material.Body1(u.th, mark+p.Label).Layout),
						layout.Rigid(l.Layout),
					)
				})
			}))
		}
		if f.note != "" {
			rows = append(rows, layout.Rigid(func(gtx C) D {
				l := material.Body2(u.th, f.note)
				l.Color = u.pal.muted
				return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, l.Layout)
			}))
		}
		if f.places != nil {
			label := "File"
			if f.busy {
				label = "Filing…"
			}
			rows = append(rows, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx C) D { return u.button(gtx, &f.file, label, !f.busy, false) })
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
}
