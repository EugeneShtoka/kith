package main

import (
	"context"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// pane is where the keys act: the rail, the room list, the timeline, or a composer
// (the composer while one of the window's fields has the focus).
type pane int

const (
	paneRail pane = iota
	paneRooms
	paneTimeline
	paneComposer
)

// actions are the [keys] paths the window takes, in priority order: of two bound to one
// key, the first that applies to the pane acts ("enter" opens a room in the list, and
// goes to what a reply answers in the timeline).
var actions = []action{
	{"jump_to", true}, {"jump_back", true}, {"jump_forward", true},
	{"focus_rail", true}, {"focus_rooms", true}, {"focus_timeline", true}, {"focus_composer", true},
	{"focus_next", false}, {"focus_prev", false},
	{"insert.send", true}, {"insert.attach", true}, {"insert.cancel", true},
	{"rail.mark_read", false},
	{"rooms.mark_read", false}, {"rooms.mark_all_read", false}, {"rooms.mark_unread", false},
	{"timeline.insert", false}, {"timeline.reply", false}, {"timeline.go_reply", false},
	{"timeline.react", false}, {"timeline.redact", false}, {"timeline.edit", false},
	{"timeline.next_mention", false}, {"timeline.prev_mention", false},
	{"timeline.next_attachment", false}, {"timeline.prev_attachment", false},
	{"timeline.prev_day", false}, {"timeline.next_day", false},
	{"timeline.copy_text", false}, {"timeline.copy_url", false},
	{"timeline.open_url", false}, {"timeline.open_url_focus", false},
	{"timeline.download", false}, {"timeline.view_media", false}, {"timeline.play", false},
	{"timeline.open_thread", false}, {"timeline.start_thread", false},
	{"nav.open", false}, {"nav.back", false}, {"nav.up", false}, {"nav.down", false},
	{"nav.page_up", false}, {"nav.page_down", false}, {"nav.half_page_up", false}, {"nav.half_page_down", false},
	{"nav.select_oldest", false}, {"nav.select_newest", false}, {"nav.scroll_oldest", false}, {"nav.scroll_newest", false},
	{"picker.accept", false}, {"picker.close", false}, // the link chooser's (chooseLink)
}

// confirmation is a question the next press answers: y or enter does it, anything else
// drops it.
type confirmation struct {
	question string
	yes      func()
}

// keys reads every press the window's widgets leave, so it runs last in the frame: Gio
// gives a key to the first reader whose filter matches, and a filter of no name matches
// them all (Tab too once named, which Gio otherwise keeps for moving the focus). What
// it changes shows on the next frame.
func (u *ui) keys(ctx context.Context, gtx C) {
	u.typingIn = nil
	fields := []any{&u.composer, &u.threadComposer, &u.filter, &u.search}
	if u.picking != nil {
		fields = append(fields, &u.picking.query)
	}
	for _, field := range fields {
		if gtx.Focused(field) {
			u.typingIn = field
		}
	}
	typing := u.typingIn != nil
	if typing {
		u.pane = paneComposer
	} else if u.pane == paneComposer {
		u.pane = paneTimeline
	}
	const mods = key.ModCtrl | key.ModShift | key.ModAlt | key.ModSuper
	for {
		ev, ok := gtx.Event(key.Filter{Optional: mods}, key.Filter{Name: key.NameTab, Optional: key.ModShift})
		if !ok {
			return
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		name := keyName(e)
		if name == "" {
			continue
		}
		if c := u.confirm; c != nil {
			u.confirm = nil
			if name == "y" || name == "enter" {
				c.yes()
			}
			continue
		}
		if u.linking != nil {
			for _, path := range u.keymap.press(name, false) {
				if u.chooseLink(ctx, gtx, path) {
					break
				}
			}
			gtx.Execute(op.InvalidateCmd{})
			continue
		}
		for _, path := range u.keymap.press(name, typing) {
			if u.do(ctx, gtx, path, u.keymap.count) {
				break
			}
		}
		gtx.Execute(op.InvalidateCmd{})
	}
}

// do acts on path in the pane the keys are in; false when it does not apply there.
func (u *ui) do(ctx context.Context, gtx C, path string, count int) bool {
	t := &u.timeline
	switch path {
	case "jump_to":
		u.focus = &u.filter
	case "jump_back":
		if u.at == 0 {
			return false
		}
		u.at--
		u.openLocked(ctx, u.history[u.at])
	case "jump_forward":
		if u.at >= len(u.history)-1 {
			return false
		}
		u.at++
		u.openLocked(ctx, u.history[u.at])
	case "focus_rail", "focus_rooms", "focus_timeline", "focus_composer":
		u.toPane(map[string]pane{"focus_rail": paneRail, "focus_rooms": paneRooms, "focus_timeline": paneTimeline, "focus_composer": paneComposer}[path])
	case "focus_next", "focus_prev":
		step := 1
		if path == "focus_prev" {
			step = 3
		}
		next := (u.pane + pane(step)) % 4
		if next == paneRail && !u.railShown {
			next = (next + pane(step)) % 4
		}
		u.toPane(next)
	case "insert.send", "insert.attach", "insert.cancel":
		return u.composing(ctx, path)
	case "rail.mark_read":
		if u.pane != paneRail || len(u.rail.rows) == 0 {
			return false
		}
		var ids []domain.RoomID
		for id := range u.rail.holds[u.rail.sel] {
			if u.unread[id].HasUnread(u.local) {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			u.ask("Mark every unread room in "+u.rail.rows[u.rail.sel].Label+" read?", func() { go u.markRooms(ctx, ids) })
		}
	case "rooms.mark_read", "rooms.mark_all_read":
		if u.pane != paneRooms || u.roomAt == "" {
			return false
		}
		go u.markRooms(ctx, []domain.RoomID{u.roomAt})
	case "rooms.mark_unread":
		if u.pane != paneRooms || u.roomAt == "" {
			return false
		}
		room, marked := u.roomAt, u.unread[u.roomAt].Marked
		go func() {
			if err := u.backend.MarkRoomUnread(ctx, room, !marked); err != nil {
				log.Printf("mark unread: %v", err)
			}
		}()
	case "nav.up", "nav.down", "nav.page_up", "nav.page_down", "nav.half_page_up", "nav.half_page_down",
		"nav.select_oldest", "nav.select_newest", "nav.scroll_oldest", "nav.scroll_newest":
		return u.move(path, count)
	case "nav.open":
		switch u.pane {
		case paneRail:
			u.toPane(paneRooms)
		case paneRooms:
			if u.roomAt == "" {
				return false
			}
			if u.roomAt != u.current {
				u.openLocked(ctx, u.roomAt)
			}
			u.toPane(paneTimeline)
		default:
			return false
		}
	case "nav.back":
		switch {
		case u.pane == paneTimeline && t.thread != "":
			t.thread = ""
		case u.pane == paneTimeline:
			u.toPane(paneRooms)
		case u.pane == paneRooms && u.railShown:
			u.toPane(paneRail)
		default:
			return false
		}
	default:
		if u.pane != paneTimeline || !strings.HasPrefix(path, "timeline.") {
			return false
		}
		return u.onMessage(ctx, gtx, path)
	}
	return true
}

// toPane moves the keys to p: a composer takes the focus, any other pane takes it from
// the fields, and its cursor starts where the window is.
func (u *ui) toPane(p pane) {
	u.pane = p
	switch p {
	case paneComposer:
		u.focus = &u.composer
		return
	case paneRooms:
		// The cursor starts on the open room, else the list's first when the row
		// shown does not hold it.
		shown := u.filtered()
		if !slices.ContainsFunc(shown, func(i int) bool { return u.rooms[i].ID == u.roomAt }) {
			u.roomAt = u.current
			if !slices.ContainsFunc(shown, func(i int) bool { return u.rooms[i].ID == u.current }) && len(shown) > 0 {
				u.roomAt = u.rooms[shown[0]].ID
			}
		}
	case paneTimeline:
		if t := &u.timeline; t.sel == "" && len(t.shown) > 0 {
			t.sel = t.msgs[t.shown[len(t.shown)-1]].ID
		}
	}
	u.blur = true
}

// move is a [keys.nav] motion in the pane's list: the rail's rows, the room list, or the
// main timeline's messages.
func (u *ui) move(path string, count int) bool {
	switch u.pane {
	case paneRail:
		n := len(u.rail.rows)
		if n == 0 {
			return false
		}
		to, _ := moved(path, count, u.rail.sel, u.rail.list.Position.Count, n)
		if to != u.rail.sel {
			u.rail.sel = to
			u.roomList.Position = layout.Position{}
		}
		reveal(&u.rail.list.List, to, n)
	case paneRooms:
		shown := u.filtered()
		if len(shown) == 0 {
			return false
		}
		at := max(0, slices.IndexFunc(shown, func(i int) bool { return u.rooms[i].ID == u.roomAt }))
		to, _ := moved(path, count, at, u.roomList.Position.Count, len(shown))
		u.roomAt = u.rooms[shown[to]].ID
		reveal(&u.roomList.List, to, len(shown))
	case paneTimeline:
		t := &u.timeline
		if len(t.shown) == 0 {
			return false
		}
		to, _ := moved(path, count, t.selAt(), t.list.Position.Count, len(t.shown))
		t.selectAt(to)
	default:
		return false
	}
	return true
}

// reveal scrolls l so its k-th of n rows shows, sticking to the end at the last.
func reveal(l *layout.List, k, n int) {
	p := l.Position
	switch {
	case k == n-1 && l.ScrollToEnd:
		l.Position = layout.Position{First: k, BeforeEnd: false}
	case k <= p.First:
		l.ScrollTo(k)
	case p.Count > 0 && k >= p.First+p.Count-1:
		l.ScrollTo(max(0, k-p.Count+2))
	}
}

// selAt is the selected message's place in the main timeline, its newest when none or
// gone.
func (t *timeline) selAt() int {
	k := slices.IndexFunc(t.shown, func(i int) bool { return t.msgs[i].ID == t.sel })
	if k < 0 {
		return len(t.shown) - 1
	}
	return k
}

// selectAt selects the k-th message of the main timeline, scrolling to it.
func (t *timeline) selectAt(k int) {
	t.sel = t.msgs[t.shown[k]].ID
	reveal(&t.list.List, k, len(t.shown))
}

// seek selects the nearest message from the selected one in direction dir (1 newer, -1
// older) that match holds for; false for none.
func (t *timeline) seek(dir int, match func(domain.Message) bool) bool {
	for k := t.selAt() + dir; k >= 0 && k < len(t.shown); k += dir {
		if match(t.msgs[t.shown[k]]) {
			t.selectAt(k)
			return true
		}
	}
	return false
}

// onMessage is a [keys.timeline] action on the selected message.
func (u *ui) onMessage(ctx context.Context, gtx C, path string) bool {
	t := &u.timeline
	if len(t.shown) == 0 {
		return false
	}
	m := t.msgs[t.shown[t.selAt()]]
	switch path {
	case "timeline.insert":
		u.toPane(paneComposer)
	case "timeline.reply":
		u.replyTo, u.editing = m.ID, ""
		u.toPane(paneComposer)
	case "timeline.go_reply":
		return m.ReplyTo != "" && t.seek(-1, func(h domain.Message) bool { return h.ID == m.ReplyTo })
	case "timeline.react":
		u.picking = &picker{target: m.ID}
	case "timeline.redact":
		if m.Redacted {
			return false
		}
		room := t.room
		u.ask("Delete this message for everyone?", func() {
			go func() {
				if err := u.backend.Redact(ctx, room, m.ID, ""); err != nil {
					u.say("Not deleted: " + err.Error())
				}
			}()
		})
	case "timeline.edit":
		if !u.selves[m.Sender] || m.Redacted || m.Media != nil {
			u.note, u.noteAt = "Only your own text messages can be edited", time.Now()
			return true
		}
		if u.editing == "" {
			u.editSaved = u.composer.Text()
		}
		u.editing, u.replyTo = m.ID, ""
		u.composer.SetText(m.Body)
		u.toPane(paneComposer)
	case "timeline.next_mention", "timeline.prev_mention":
		return t.seek(direction(path), func(h domain.Message) bool { return h.Mentioned })
	case "timeline.next_attachment", "timeline.prev_attachment":
		return t.seek(direction(path), func(h domain.Message) bool { return h.Media != nil })
	case "timeline.prev_day":
		// The start of this day first, then the start of the day before.
		k := t.selAt()
		for k > 0 && sameDay(t.msgs[t.shown[k-1]].Timestamp, m.Timestamp) {
			k--
		}
		if k == t.selAt() && k > 0 {
			day := t.msgs[t.shown[k-1]].Timestamp
			for k = k - 1; k > 0 && sameDay(t.msgs[t.shown[k-1]].Timestamp, day); k-- {
			}
		}
		t.selectAt(k)
	case "timeline.next_day":
		return t.seek(1, func(h domain.Message) bool { return !sameDay(h.Timestamp, m.Timestamp) })
	case "timeline.copy_text":
		u.copy(gtx, text(m), "Copied the message")
	case "timeline.copy_url", "timeline.open_url", "timeline.open_url_focus":
		return u.takeLink(ctx, gtx, m, path == "timeline.copy_url")
	case "timeline.download":
		if m.Media == nil {
			return false
		}
		s := u.saves[m.ID]
		if s == nil {
			s = new(save)
			u.saves[m.ID] = s
		}
		if !s.busy {
			s.busy, s.said = true, "Saving"
			go u.saveFile(ctx, m, s)
		}
	case "timeline.view_media":
		p := t.images[m.ID]
		if !m.Media.IsImage() || p == nil || !p.ok {
			return false
		}
		u.viewImage(ctx, t.room, m.ID, p)
	case "timeline.play":
		if m.Media == nil || m.Media.Type != domain.MediaVideo && m.Media.Type != domain.MediaAudio {
			return false
		}
		u.play(ctx, m)
	case "timeline.open_thread":
		root := m.ThreadRoot
		if ths := t.threads[m.ID]; root == "" && len(ths) > 0 {
			root = ths[0].Root
		}
		if root == "" {
			return false
		}
		u.openThread(ctx, root)
	case "timeline.start_thread":
		u.openThread(ctx, m.ID)
	default:
		return false
	}
	return true
}

func direction(path string) int {
	if strings.Contains(path, ".prev_") {
		return -1
	}
	return 1
}

// copy puts s on the clipboard and says so.
func (u *ui) copy(gtx C, s, said string) {
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
	u.note, u.noteAt = said, time.Now()
}

// ask puts a question to the next press.
func (u *ui) ask(question string, yes func()) {
	u.confirm = &confirmation{question: question, yes: yes}
}

// say shows a note from a loader, which does not hold mu.
func (u *ui) say(note string) {
	u.mu.Lock()
	u.note, u.noteAt = note, time.Now()
	u.mu.Unlock()
	u.w.Invalidate()
}

func (u *ui) markRooms(ctx context.Context, ids []domain.RoomID) {
	if _, err := u.backend.MarkRoomsRead(ctx, ids, true); err != nil {
		u.say("Not marked read: " + err.Error())
	}
}

// composing is a [keys.insert] action in whichever composer has the focus.
func (u *ui) composing(ctx context.Context, path string) bool {
	var ed *widget.Editor
	root := domain.EventID("")
	switch u.typingIn {
	case &u.composer:
		ed = &u.composer
	case &u.threadComposer:
		ed, root = &u.threadComposer, u.timeline.thread
	default:
		return false
	}
	switch path {
	case "insert.send":
		u.submit(ctx, ed, root)
	case "insert.attach":
		if ed != &u.composer {
			return false
		}
		go u.attach(ctx, u.current)
	case "insert.cancel":
		if u.replyTo != "" || u.editing != "" {
			u.dropReplyOrEdit()
			return true
		}
		u.toPane(paneTimeline)
	}
	return true
}

// layoutStatus is the line over the composer: a question the next press answers, else
// what the composer's message replies to or replaces, with a button that drops it, and
// a key sequence typed so far.
func (u *ui) layoutStatus(gtx C) D {
	if u.cancel.Clicked(gtx) {
		u.dropReplyOrEdit()
	}
	said, cancel := u.keymap.pending(), false
	switch {
	case u.confirm != nil:
		said = u.confirm.question + "  y / n"
	case u.editing != "":
		said, cancel = "Editing your message", true
	case u.replyTo != "":
		t := &u.timeline
		said, cancel = "Replying", true
		if i := slices.IndexFunc(t.msgs, func(m domain.Message) bool { return m.ID == u.replyTo }); i >= 0 {
			first, _, _ := strings.Cut(text(t.msgs[i]), "\n")
			if r := []rune(first); len(r) > 80 {
				first = string(r[:80]) + "…"
			}
			said = "Replying to " + sender(t.msgs[i]) + ": " + first
		}
	}
	if said == "" {
		return D{}
	}
	return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(14), Top: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				l := material.Body2(u.th, said)
				l.MaxLines = 1
				if u.confirm != nil {
					l.Font.Weight = font.Bold
				} else {
					l.Color = u.pal.muted
				}
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				if !cancel {
					return D{}
				}
				return u.button(gtx, &u.cancel, "✕", true, false)
			}),
		)
	})
}

// dropReplyOrEdit ends a reply or an edit in the composer; an edit puts back the words
// it set aside.
func (u *ui) dropReplyOrEdit() {
	if u.editing != "" {
		u.composer.SetText(u.editSaved)
	}
	u.replyTo, u.editing, u.editSaved = "", "", ""
}
