package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	_ "golang.org/x/image/webp"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/filedialog"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

const (
	pageSize  = 50
	railWidth = 170 // dp
)

// ui is the whole window's state; mu guards it against the loaders and the sync stream.
type ui struct {
	w       *app.Window
	backend *daemon.Remote
	th      *material.Theme

	mu    sync.Mutex
	marks []string
	err   string

	rooms     []domain.Room
	roomBtns  []widget.Clickable
	roomList  widget.List
	filter    widget.Editor
	rail      rail
	unread    map[domain.RoomID]domain.Unread
	local     bool // badges count cached messages, not the server's notifications
	focused   bool
	focus     any // the widget to focus on the next frame
	current   domain.RoomID
	timeline  timeline
	composer  widget.Editor
	sendError string

	pal          palette
	faces        map[face]*picture
	railShown    bool
	history      []domain.RoomID // rooms opened, for back and forward
	at           int             // where in history the open room is
	nav          struct{ rail, back, forward, goTo widget.Clickable }
	search       widget.Editor
	attachBtn    widget.Clickable
	sendBtn      widget.Clickable
	saves        map[domain.EventID]*save
	topHeight    int // the toolbar's, where the panes start
	headerHeight int // the room header's, where the player's dock starts

	last           map[domain.RoomID]time.Time // each room's newest message, the list's order
	typing         map[domain.RoomID][]string
	note           string // a short word on what was just done ("Copied"), shown a moment
	noteAt         time.Time
	viewing        *paint.ImageOp // the image shown over the window; nil for none
	threadComposer widget.Editor
	closeThread    widget.Clickable

	view      uintptr // the Gio window's X11 ID; 0 on Wayland
	player    *player
	closeBtn  widget.Clickable
	leftWidth int // the room list and its rule, where the room pane starts

	keymap   *keymap
	pane     pane
	typingIn any           // the field with the focus, nil for none
	roomAt   domain.RoomID // the room list's cursor
	blur     bool          // take the focus from the fields on the next frame
	confirm  *confirmation
	replyTo  domain.EventID // what the composer's message answers
	editing  domain.EventID // the message of yours the composer replaces
	cancel   widget.Clickable
	selves   map[string]bool // every ID that is this person
	picking  *picker         // the reaction picker, nil when closed

	offer     *completion                         // the composer's completion popup
	dismissed string                              // the word whose popup Esc closed
	mentions  map[*widget.Editor][]domain.Mention // people chosen into each composer's draft
	drafts    drafts                              // each room's draft as kithd stores it
	editSaved string                              // the composer's words an edit set aside
}

// timeline is the open room's messages, oldest first, and how far back they reach.
type timeline struct {
	room       domain.RoomID
	msgs       []domain.Message
	state      map[domain.EventID]*msgState
	images     map[domain.EventID]*picture
	shown      []int                                // the main timeline's messages, as indexes of msgs, as last drawn
	reactions  map[domain.EventID][]domain.Reaction // by the message they annotate
	thread     domain.EventID                       // the thread open beside it; "" for none
	threadList widget.List
	threadBtns map[domain.EventID]*widget.Clickable
	next       string // the page before msgs; "" when the room's start is loaded
	loading    bool
	marked     domain.EventID                     // the newest message a receipt was sent for
	threads    map[domain.EventID][]domain.Thread // by the message each hangs beneath
	pictures   map[string]string                  // each sender's picture version
	slots      map[string]int                     // sender color slots, kithd's then this window's
	drawn      bool
	list       widget.List
	sel        domain.EventID // the main timeline's cursor

	members      []domain.Member // who a mention may name, ranked by kithd
	membersAsked bool
}

// picture is an image message's pixels, once fetched.
type picture struct {
	op  paint.ImageOp
	ok  bool
	err error
}

func newUI(w *app.Window, backend *daemon.Remote, local bool, pal palette, keys config.Keys) *ui {
	u := &ui{
		w: w, backend: backend, th: material.NewTheme(), local: local, pal: pal, railShown: true,
		keymap: newKeymap(keys, actions), pane: paneComposer, selves: map[string]bool{},
		mentions: map[*widget.Editor][]domain.Mention{}, drafts: newDrafts(),
		unread: map[domain.RoomID]domain.Unread{}, faces: map[face]*picture{}, saves: map[domain.EventID]*save{},
		last: map[domain.RoomID]time.Time{}, typing: map[domain.RoomID][]string{},
	}
	// go-text's sans-serif fallback has no emoji family, so name it, or emoji are tofu.
	u.th.Face = "sans-serif, emoji"
	u.th.TextSize = 15
	u.th.Palette = material.Palette{Fg: pal.text, Bg: pal.background, ContrastBg: pal.accent, ContrastFg: pal.focusText}
	u.search.SingleLine, u.search.ReadOnly = true, true
	u.roomList.Axis = layout.Vertical
	u.filter.SingleLine, u.filter.Submit = true, true
	return u
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

// faint is the text color at alpha a: hover and placeholder fills.
func (u *ui) faint(a uint8) color.NRGBA {
	c := u.th.Palette.Fg
	c.A = a
	return c
}

// fill paints the whole of gtx's constraints c, the pane's ground.
func fill(gtx C, c color.NRGBA) {
	paint.FillShape(gtx.Ops, c, clip.Rect{Max: gtx.Constraints.Max}.Op())
}

func (u *ui) mark(what string) {
	u.marks = append(u.marks, fmt.Sprintf("%6.0fms  %s", float64(time.Since(started).Microseconds())/1000, what))
}

func (u *ui) fail(err error) {
	u.mu.Lock()
	u.err = err.Error()
	u.mu.Unlock()
	u.w.Invalidate()
}

// loadRooms lists the rooms and opens room (the most recent when ""), playing play in
// it when set.
func (u *ui) loadRooms(ctx context.Context, room domain.RoomID, play domain.EventID) {
	rooms, err := u.backend.Rooms(ctx)
	if err != nil {
		u.fail(fmt.Errorf("rooms: %w", err))
		return
	}
	last, err := u.backend.LastMessages(ctx)
	if err != nil {
		u.fail(fmt.Errorf("last messages: %w", err))
		return
	}
	byRecent(rooms, last)
	selves, err := u.backend.Selves(ctx)
	if err != nil {
		log.Printf("selves: %v", err)
	}
	u.mu.Lock()
	maps.Copy(u.last, last)
	for _, id := range selves {
		u.selves[id] = true
	}
	u.mu.Unlock()
	unread, err := u.backend.CachedUnread(ctx)
	if err != nil {
		u.fail(fmt.Errorf("unread: %w", err))
		return
	}
	u.mu.Lock()
	for _, n := range unread {
		u.unread[n.RoomID] = n
	}
	u.rooms = rooms
	u.roomBtns = make([]widget.Clickable, len(rooms))
	u.mark(fmt.Sprintf("%d rooms loaded", len(rooms)))
	u.mu.Unlock()
	u.w.Invalidate()
	u.loadRail(ctx)
	u.loadDrafts(ctx)
	if room == "" && len(rooms) > 0 {
		room = rooms[0].ID
	}
	if room == "" {
		return
	}
	u.open(ctx, room)
	if play != "" {
		cached, err := u.backend.CachedTimeline(ctx, room)
		if err != nil {
			u.fail(fmt.Errorf("cached timeline: %w", err))
			return
		}
		i := slices.IndexFunc(cached, func(m domain.Message) bool { return m.ID == play && m.Media != nil })
		if i < 0 {
			u.fail(fmt.Errorf("no cached attachment %s in %s", play, room))
			return
		}
		m := cached[i]
		u.mu.Lock()
		u.play(ctx, m)
		u.mu.Unlock()
		u.w.Invalidate()
	}
}

// open shows a room: the cache at once, then the newest page from kithd, which also
// says where older history starts.
func (u *ui) open(ctx context.Context, room domain.RoomID) {
	u.mu.Lock()
	if u.at >= len(u.history) || u.history[u.at] != room {
		if len(u.history) > 0 {
			u.history = u.history[:u.at+1]
		}
		u.history = append(u.history, room)
		u.at = len(u.history) - 1
	}
	if u.current != room {
		u.switchDraft(u.current, room)
	}
	u.focus = &u.composer
	u.timeline = timeline{
		room: room, loading: true, slots: map[string]int{}, threads: map[domain.EventID][]domain.Thread{},
		pictures:   map[string]string{},
		state:      map[domain.EventID]*msgState{},
		reactions:  map[domain.EventID][]domain.Reaction{},
		threadBtns: map[domain.EventID]*widget.Clickable{},
		images:     map[domain.EventID]*picture{},
		list:       widget.List{List: layout.List{Axis: layout.Vertical, ScrollToEnd: true}},
	}
	u.mu.Unlock()
	go func() {
		slots, err := u.backend.SenderSlots(ctx, room)
		if err != nil {
			log.Printf("sender slots: %v", err)
		}
		pictures, err := u.backend.SenderPictures(ctx, room)
		if err != nil {
			log.Printf("sender pictures: %v", err)
		}
		u.mu.Lock()
		if u.timeline.room == room {
			for who, slot := range slots {
				u.timeline.slots[who] = slot
			}
			maps.Copy(u.timeline.pictures, pictures)
		}
		u.mu.Unlock()
		cached, err := u.backend.CachedTimeline(ctx, room)
		if err != nil {
			u.fail(fmt.Errorf("cached timeline: %w", err))
		}
		u.merge(ctx, room, cached)
		page, err := u.backend.Timeline(ctx, room, "", pageSize)
		if err != nil {
			u.fail(fmt.Errorf("timeline: %w", err))
		}
		u.merge(ctx, room, page.Messages)
		u.loadThreads(ctx, room)
		u.loadReactions(ctx, room)
		u.mu.Lock()
		if u.timeline.room == room {
			u.timeline.react(page.Reactions, false)
		}
		u.mu.Unlock()
		u.mu.Lock()
		if u.timeline.room == room {
			u.timeline.next, u.timeline.loading = page.Next, false
		}
		u.mu.Unlock()
		u.markRead(ctx)
		u.w.Invalidate()
	}()
}

// older fetches the page before the oldest message shown, keeping the view where it is.
func (u *ui) older(ctx context.Context, room domain.RoomID, from string) {
	page, err := u.backend.Timeline(ctx, room, from, pageSize)
	if err != nil {
		u.fail(fmt.Errorf("older: %w", err))
		return
	}
	u.mu.Lock()
	defer u.w.Invalidate()
	defer u.mu.Unlock()
	if u.timeline.room != room {
		return
	}
	added := u.timeline.upsert(page.Messages)
	u.timeline.react(page.Reactions, false)
	u.timeline.list.Position.First += added
	u.timeline.next, u.timeline.loading = page.Next, false
}

// loadThreads reads the room's threads, each to hang beneath its anchor.
func (u *ui) loadThreads(ctx context.Context, room domain.RoomID) {
	threads, err := u.backend.ListThreads(ctx, room)
	if err != nil {
		return // a network without threads (Telegram) says so here
	}
	by := map[domain.EventID][]domain.Thread{}
	for _, t := range threads {
		at := t.Anchor
		if at == "" {
			at = t.Root
		}
		by[at] = append(by[at], t)
	}
	u.mu.Lock()
	if u.timeline.room == room {
		u.timeline.threads = by
	}
	u.mu.Unlock()
	u.w.Invalidate()
}

func (u *ui) merge(ctx context.Context, room domain.RoomID, msgs []domain.Message) {
	u.mu.Lock()
	if u.timeline.room == room {
		u.timeline.upsert(msgs)
	}
	u.mu.Unlock()
	u.markRead(ctx)
	u.w.Invalidate()
}

// upsert folds msgs in by ID, keeping time order; it returns how many were new, in the
// main timeline and older than every message held (what a prepend shifts the view by).
func (t *timeline) upsert(msgs []domain.Message) int {
	var oldest time.Time
	if len(t.msgs) > 0 {
		oldest = t.msgs[0].Timestamp
	}
	before := 0
	for _, m := range msgs {
		m.Body = shortcodes(m.Body)
		if i := slices.IndexFunc(t.msgs, func(h domain.Message) bool { return h.ID == m.ID }); i >= 0 {
			t.msgs[i] = m
			continue
		}
		if !oldest.IsZero() && m.Timestamp.Before(oldest) && isMain(m) {
			before++
		}
		t.msgs = append(t.msgs, m)
	}
	slices.SortStableFunc(t.msgs, func(a, b domain.Message) int {
		if c := a.Timestamp.Compare(b.Timestamp); c != 0 {
			return c
		}
		return int(a.Seq - b.Seq)
	})
	return before
}

// follow folds kithd's streams in: messages into the open room, unread into the
// badges, a reattach into a fresh read of the unread cache. Every other stream is read
// and dropped, since an unread one blocks once its buffer fills.
func (u *ui) follow(ctx context.Context) {
	b := u.backend
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-b.Messages():
			if !ok {
				return
			}
			u.mu.Lock()
			u.bump(m.RoomID, m.Timestamp)
			u.mu.Unlock()
			u.merge(ctx, m.RoomID, []domain.Message{m})
			if m.ThreadRoot != "" && m.ThreadRoot != m.ID {
				go u.loadThreads(ctx, m.RoomID)
			}
		case n := <-b.Unread():
			u.mu.Lock()
			u.unread[n.RoomID] = n
			u.mu.Unlock()
			u.w.Invalidate()
		case back := <-b.Attached():
			if back {
				go u.reloadUnread(ctx)
				go u.loadRail(ctx)
			}
		case c := <-b.Changed():
			if c == domain.ChangeRail {
				go u.loadRail(ctx)
			}
		case a := <-b.Activity():
			u.mu.Lock()
			u.typing[a.RoomID] = a.Typing
			u.mu.Unlock()
			u.w.Invalidate()
		case r := <-b.Reactions():
			u.mu.Lock()
			if u.timeline.room == r.Reaction.RoomID {
				u.timeline.react([]domain.Reaction{r.Reaction}, r.Removed)
			}
			u.mu.Unlock()
			u.w.Invalidate()
		case <-b.Invites():
		case <-b.Verifications():
		case <-b.Follows():
		case <-b.ConfigChanges():
		}
	}
}

func (u *ui) reloadUnread(ctx context.Context) {
	unread, err := u.backend.CachedUnread(ctx)
	if err != nil {
		log.Printf("unread: %v", err)
		return
	}
	u.mu.Lock()
	clear(u.unread)
	for _, n := range unread {
		u.unread[n.RoomID] = n
	}
	u.mu.Unlock()
	u.w.Invalidate()
}

// markRead sends a receipt for the open room's newest main-timeline message, once per
// message, while the window has focus. Always private: the read policy's rules (who
// may see a receipt) are the TUI's to resolve, and the spike doesn't port them.
func (u *ui) markRead(ctx context.Context) {
	u.mu.Lock()
	t := &u.timeline
	var newest domain.EventID
	for _, m := range slices.Backward(t.msgs) {
		if m.ThreadRoot == "" {
			newest = m.ID
			break
		}
	}
	if !u.focused || t.loading || newest == "" {
		u.mu.Unlock()
		return
	}
	room := t.room
	if newest == t.marked {
		u.mu.Unlock()
		return
	}
	t.marked = newest
	u.mu.Unlock()
	go func() {
		log.Printf("read receipt: %s at %s", room, newest)
		if err := u.backend.MarkRead(ctx, room, newest, true); err != nil {
			log.Printf("read receipt: %v", err)
			u.mu.Lock()
			if u.timeline.room == room && u.timeline.marked == newest {
				u.timeline.marked = "" // the next trigger tries again
			}
			u.mu.Unlock()
		}
	}()
}

func (u *ui) drawnTimeline() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.timeline.drawn && !u.timeline.loading
}

func (u *ui) layout(ctx context.Context, gtx C) D {
	u.mu.Lock()
	defer u.mu.Unlock()
	paint.Fill(gtx.Ops, u.pal.background)
	if u.blur {
		gtx.Execute(key.FocusCmd{})
		u.blur = false
	}
	if u.focus != nil {
		gtx.Execute(key.FocusCmd{Tag: u.focus})
		u.focus = nil
	}
	if u.err != "" {
		return layout.UniformInset(unit.Dp(16)).Layout(gtx, material.Body1(u.th, u.err).Layout)
	}
	u.leftWidth = gtx.Dp(300) + gtx.Dp(1)
	if u.railShown {
		u.leftWidth += gtx.Dp(railWidth) + gtx.Dp(1)
	}
	rule := func(gtx C) D {
		size := image.Pt(gtx.Dp(1), gtx.Constraints.Max.Y)
		paint.FillShape(gtx.Ops, u.pal.border, clip.Rect{Max: size}.Op())
		return D{Size: size}
	}
	panes := []layout.FlexChild{}
	if u.railShown {
		panes = append(panes,
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(railWidth)
				gtx.Constraints.Max.X = gtx.Constraints.Min.X
				fill(gtx, u.pal.bar)
				return u.layoutRail(gtx)
			}),
			layout.Rigid(rule))
	}
	panes = append(panes,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(300)
			gtx.Constraints.Max.X = gtx.Constraints.Min.X
			fill(gtx, u.pal.surface)
			return u.layoutRooms(ctx, gtx)
		}),
		layout.Rigid(rule),
		layout.Flexed(1, func(gtx C) D { return u.layoutRoom(ctx, gtx) }))
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			dims := u.layoutToolbar(ctx, gtx)
			u.topHeight = dims.Size.Y
			return dims
		}),
		layout.Flexed(1, func(gtx C) D { return layout.Flex{}.Layout(gtx, panes...) }),
	)
	if u.viewing != nil {
		u.layoutViewer(gtx)
	}
	if u.picking != nil {
		u.layoutPicker(ctx, gtx)
	}
	u.keys(ctx, gtx)
	u.saveDrafts(ctx)
	return dims
}

// layoutToolbar is the desktop's top bar: the rail's toggle, back and forward through
// the rooms opened, search, and the pages. Search, People and Settings are the
// desktop's alone for now, drawn muted.
func (u *ui) layoutToolbar(ctx context.Context, gtx C) D {
	if u.nav.rail.Clicked(gtx) {
		u.railShown = !u.railShown
	}
	if u.nav.back.Clicked(gtx) && u.at > 0 {
		u.at--
		u.openLocked(ctx, u.history[u.at])
	}
	if u.nav.forward.Clicked(gtx) && u.at < len(u.history)-1 {
		u.at++
		u.openLocked(ctx, u.history[u.at])
	}
	if u.nav.goTo.Clicked(gtx) {
		u.focus = &u.filter
	}
	m := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Top: unit.Dp(6), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		gap := layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return u.button(gtx, &u.nav.rail, "▥", true, u.railShown) }), gap,
			layout.Rigid(func(gtx C) D { return u.button(gtx, &u.nav.back, "←", u.at > 0, false) }), gap,
			layout.Rigid(func(gtx C) D { return u.button(gtx, &u.nav.forward, "→", u.at < len(u.history)-1, false) }), gap,
			layout.Flexed(1, func(gtx C) D {
				return u.field(gtx, func(gtx C) D {
					return u.editor(&u.search, "Search: words, from:, since:, until:").Layout(gtx)
				})
			}), gap,
			layout.Rigid(func(gtx C) D { return u.button(gtx, new(widget.Clickable), "Search", false, false) }), gap,
			layout.Rigid(func(gtx C) D { return u.button(gtx, &u.nav.goTo, "Go to", true, false) }), gap,
			layout.Rigid(func(gtx C) D { return u.button(gtx, new(widget.Clickable), "People", false, false) }), gap,
			layout.Rigid(func(gtx C) D { return u.button(gtx, new(widget.Clickable), "Settings", false, false) }),
		)
	})
	call := m.Stop()
	paint.FillShape(gtx.Ops, u.pal.bar, clip.Rect{Max: dims.Size}.Op())
	call.Add(gtx.Ops)
	line := image.Rect(0, dims.Size.Y-gtx.Dp(1), dims.Size.X, dims.Size.Y)
	paint.FillShape(gtx.Ops, u.pal.border, clip.Rect(line).Op())
	u.headerHeight = dims.Size.Y
	return dims
}

// button is a raised button with label; a disabled one is muted and does nothing, an
// on one is in the selection color.
func (u *ui) button(gtx C, btn *widget.Clickable, label string, enabled, on bool) D {
	bg, fg := u.pal.raised, u.pal.text
	switch {
	case !enabled:
		fg = u.pal.muted
	case on:
		bg = u.pal.selection
	}
	if !enabled {
		gtx = gtx.Disabled()
	}
	return btn.Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		dims := layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10), Top: unit.Dp(5), Bottom: unit.Dp(5)}.Layout(gtx, func(gtx C) D {
			l := material.Body2(u.th, label)
			l.Color = fg
			return l.Layout(gtx)
		})
		call := m.Stop()
		paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(6)).Op(gtx.Ops))
		call.Add(gtx.Ops)
		return dims
	})
}

// editor is e drawn with hint in the muted color.
func (u *ui) editor(e *widget.Editor, hint string) material.EditorStyle {
	st := material.Editor(u.th, e, hint)
	st.HintColor = u.pal.muted
	return st
}

// field is an input's box: raised, bordered, rounded.
func (u *ui) field(gtx C, w layout.Widget) D {
	m := op.Record(gtx.Ops)
	dims := widget.Border{Color: u.pal.border, Width: unit.Dp(1), CornerRadius: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Top: unit.Dp(5), Bottom: unit.Dp(5)}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w(gtx)
		})
	})
	call := m.Stop()
	paint.FillShape(gtx.Ops, u.pal.raised, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(6)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func (u *ui) layoutRooms(ctx context.Context, gtx C) D {
	if u.rooms == nil {
		return layout.UniformInset(unit.Dp(12)).Layout(gtx, material.Body2(u.th, "Loading rooms").Layout)
	}
	shown := u.filtered()
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.filter, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			u.filter.SetText("")
			u.focus = &u.composer
		}
	}
	for {
		ev, ok := u.filter.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok && len(shown) > 0 {
			u.filter.SetText("")
			u.openLocked(ctx, u.rooms[shown[0]].ID)
			shown = u.filtered()
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
				return u.field(gtx, u.editor(&u.filter, "Go to room (Ctrl+K)").Layout)
			})
		}),
		layout.Flexed(1, func(gtx C) D {
			return material.List(u.th, &u.roomList).Layout(gtx, len(shown), func(gtx C, j int) D {
				return u.layoutRoomRow(ctx, gtx, shown[j])
			})
		}),
	)
}

// filtered is the indexes of the rooms the list shows: the rail row's, or while the
// filter has text every room whose name holds it, whichever row it is in.
func (u *ui) filtered() []int {
	q := strings.ToLower(strings.TrimSpace(u.filter.Text()))
	shown := make([]int, 0, len(u.rooms))
	for i, room := range u.rooms {
		if q == "" && u.inRow(room.ID) || q != "" && strings.Contains(strings.ToLower(room.DisplayName()), q) {
			shown = append(shown, i)
		}
	}
	return shown
}

// openLocked is open called with mu held.
func (u *ui) openLocked(ctx context.Context, room domain.RoomID) {
	u.mu.Unlock()
	u.open(ctx, room)
	u.mu.Lock()
}

func (u *ui) layoutRoomRow(ctx context.Context, gtx C, i int) D {
	room := u.rooms[i]
	if u.roomBtns[i].Clicked(gtx) {
		u.roomAt = room.ID
		if room.ID != u.current {
			u.openLocked(ctx, room.ID)
		}
	}
	n := u.unread[room.ID]
	count, highlights := n.Count(u.local)
	return u.roomBtns[i].Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		dims := layout.Inset{Left: unit.Dp(10), Right: unit.Dp(12), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return u.layoutFace(ctx, gtx, face{room: room.ID}, room.Picture, string(room.ID), room.DisplayName(), 24)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Flexed(1, func(gtx C) D {
					l := material.Body1(u.th, room.DisplayName())
					l.MaxLines = 1
					if n.HasUnread(u.local) {
						l.Font.Weight = font.Bold
					} else {
						l.Color = u.pal.muted
					}
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					switch {
					case highlights > 0:
						return u.badge(gtx, fmt.Sprint(count), true)
					case count > 0:
						l := material.Caption(u.th, fmt.Sprint(count))
						l.Color = u.pal.muted
						return l.Layout(gtx)
					case n.Marked:
						return u.badge(gtx, "•", false)
					}
					return D{}
				}),
			)
		})
		call := m.Stop()
		r := image.Rectangle{Min: image.Pt(gtx.Dp(4), 0), Max: image.Pt(dims.Size.X-gtx.Dp(4), dims.Size.Y)}
		if room.ID == u.current {
			paint.FillShape(gtx.Ops, u.pal.selection, clip.UniformRRect(r, gtx.Dp(6)).Op(gtx.Ops))
		}
		if u.pane == paneRooms && room.ID == u.roomAt {
			u.outline(gtx, r)
		}
		call.Add(gtx.Ops)
		return dims
	})
}

// badge is a count in a pill: red when it holds a mention, else the accent.
func (u *ui) badge(gtx C, text string, mention bool) D {
	bg, fg := u.pal.unread, u.pal.unreadText
	if mention {
		bg, fg = u.pal.mention, u.pal.mentionText
	}
	l := material.Caption(u.th, text)
	l.Color, l.Font.Weight = fg, font.Bold
	m := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(7), Right: unit.Dp(7), Top: unit.Dp(1), Bottom: unit.Dp(1)}.Layout(gtx, l.Layout)
	call := m.Stop()
	r := dims.Size.Y / 2
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: dims.Size}, r).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func (u *ui) layoutRoom(ctx context.Context, gtx C) D {
	if u.current == "" {
		return D{Size: gtx.Constraints.Max}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return u.layoutHeader(ctx, gtx) }),
		layout.Rigid(u.layoutPlayer),
		layout.Flexed(1, func(gtx C) D {
			main := func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D { return u.layoutTimeline(ctx, gtx) }),
					layout.Rigid(u.layoutTyping),
					layout.Rigid(u.layoutStatus),
					layout.Rigid(func(gtx C) D {
						return u.layoutEditor(ctx, gtx, &u.composer, &u.attachBtn, "A message", "")
					}),
				)
			}
			if u.timeline.thread == "" {
				return main(gtx)
			}
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, main),
				layout.Rigid(func(gtx C) D {
					size := image.Pt(gtx.Dp(1), gtx.Constraints.Max.Y)
					paint.FillShape(gtx.Ops, u.pal.border, clip.Rect{Max: size}.Op())
					return D{Size: size}
				}),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X = min(gtx.Dp(380), gtx.Constraints.Max.X)
					gtx.Constraints.Max.X = gtx.Constraints.Min.X
					return u.layoutThread(ctx, gtx)
				}),
			)
		}),
	)
}

// layoutHeader is the open room's face and name over its timeline.
func (u *ui) layoutHeader(ctx context.Context, gtx C) D {
	i := slices.IndexFunc(u.rooms, func(r domain.Room) bool { return r.ID == u.current })
	if i < 0 {
		return D{}
	}
	room := u.rooms[i]
	m := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(14), Right: unit.Dp(14), Top: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return u.layoutFace(ctx, gtx, face{room: room.ID}, room.Picture, string(room.ID), room.DisplayName(), 26)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
			layout.Flexed(1, func(gtx C) D {
				l := material.Subtitle1(u.th, room.DisplayName())
				l.Font.Weight, l.MaxLines = font.Bold, 1
				return l.Layout(gtx)
			}),
		)
	})
	call := m.Stop()
	paint.FillShape(gtx.Ops, u.pal.bar, clip.Rect{Max: dims.Size}.Op())
	call.Add(gtx.Ops)
	line := image.Rect(0, dims.Size.Y-gtx.Dp(1), dims.Size.X, dims.Size.Y)
	paint.FillShape(gtx.Ops, u.pal.border, clip.Rect(line).Op())
	return dims
}

func (u *ui) layoutTimeline(ctx context.Context, gtx C) D {
	t := &u.timeline
	t.shown = t.shown[:0]
	for i, m := range t.msgs {
		if isMain(m) {
			t.shown = append(t.shown, i)
		}
	}
	dims := material.List(u.th, &t.list).Layout(gtx, len(t.shown), func(gtx C, k int) D {
		return u.layoutMessage(ctx, gtx, t, t.shown, k, false)
	})
	if len(t.msgs) > 0 {
		t.drawn = true
	}
	if t.list.Position.First == 0 && t.next != "" && !t.loading && len(t.msgs) > 0 {
		t.loading = true
		go u.older(ctx, t.room, t.next)
	}
	return dims
}

// layoutMessage draws the k-th of list (indexes of t.msgs, the main timeline's or a
// thread's): a sender's run starts with their face, name and time under a faint line.
// In the main timeline a thread hangs beneath its anchor as a summary that opens it.
func (u *ui) layoutMessage(ctx context.Context, gtx C, t *timeline, list []int, k int, inThread bool) D {
	m := t.msgs[list[k]]
	prev := -1
	if k > 0 {
		prev = list[k-1]
	}
	st := t.stateOf(m.ID)
	newDay := prev < 0 || !sameDay(t.msgs[prev].Timestamp, m.Timestamp)
	head := newDay || t.msgs[prev].Sender != m.Sender ||
		m.Timestamp.Sub(t.msgs[prev].Timestamp) > 10*time.Minute
	const faceSize = 32
	var rows []layout.FlexChild
	if newDay {
		rows = append(rows, layout.Rigid(func(gtx C) D { return u.layoutDay(gtx, m.Timestamp) }))
	} else if head {
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))
				paint.FillShape(gtx.Ops, u.pal.border, clip.Rect{Max: size}.Op())
				return D{Size: size}
			})
		}))
	}
	var body []layout.FlexChild
	if head {
		body = append(body, layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					l := material.Body1(u.th, sender(m))
					l.Font.Weight = font.Bold
					l.Color = t.color(m.Sender)
					return l.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx C) D {
					l := material.Caption(u.th, m.Timestamp.Local().Format("3:04 PM"))
					l.Color = u.pal.muted
					return l.Layout(gtx)
				}),
			)
		}))
	}
	if m.ReplyTo != "" && !m.Redacted {
		body = append(body, layout.Rigid(func(gtx C) D { return u.layoutReplyTo(gtx, t, st, m) }))
	}
	switch {
	case m.Redacted:
	case m.Media.IsImage():
		if st.image.Clicked(gtx) {
			if p := t.images[m.ID]; p != nil && p.ok {
				u.viewing = &p.op
			}
		}
		body = append(body, layout.Rigid(func(gtx C) D {
			return st.image.Layout(gtx, func(gtx C) D { return u.layoutImage(ctx, gtx, t, m) })
		}))
	case m.Media != nil && (m.Media.Type == domain.MediaVideo || m.Media.Type == domain.MediaAudio):
		if st.media.Clicked(gtx) {
			u.play(ctx, m)
		}
		body = append(body, layout.Rigid(func(gtx C) D {
			return u.button(gtx, &st.media, fmt.Sprintf("▶ %s", m.Media.Name), true, false)
		}))
	case m.Media != nil:
		body = append(body, layout.Rigid(func(gtx C) D { return u.layoutSave(ctx, gtx, m) }))
	}
	if txt := text(m); txt != "" && (m.Media == nil || txt != m.Media.Name) {
		body = append(body, layout.Rigid(func(gtx C) D { return u.layoutText(gtx, st, m, txt) }))
	}
	if rs := t.reactions[m.ID]; len(rs) > 0 && !m.Redacted {
		body = append(body, layout.Rigid(func(gtx C) D { return u.layoutReactions(gtx, rs) }))
	}
	for _, th := range t.threads[m.ID] {
		if inThread {
			break
		}
		btn := t.threadBtns[th.Root]
		if btn == nil {
			btn = new(widget.Clickable)
			t.threadBtns[th.Root] = btn
		}
		if btn.Clicked(gtx) {
			u.openThread(ctx, th.Root)
		}
		body = append(body, layout.Rigid(func(gtx C) D {
			replies := "1 reply"
			if th.Count != 1 {
				replies = fmt.Sprintf("%d replies", th.Count)
			}
			l := material.Caption(u.th, replies+" · "+th.LatestSenderName)
			l.Color, l.Font.Weight = u.pal.link, font.Bold
			return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx C) D { return btn.Layout(gtx, l.Layout) })
		}))
	}
	rows = append(rows, layout.Rigid(func(gtx C) D {
		top := unit.Dp(1)
		if head {
			top = unit.Dp(8)
		}
		return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(16), Top: top}.Layout(gtx, func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					if !head {
						return D{Size: image.Pt(gtx.Dp(faceSize), 0)}
					}
					version := t.pictures[m.Sender]
					return u.layoutFace(ctx, gtx, face{room: t.room, person: m.Sender}, version, m.Sender, sender(m), faceSize)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
				layout.Flexed(1, func(gtx C) D {
					return u.copyable(gtx, &st.copy, text(m), func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx, body...)
					}, func() {
						if !inThread {
							t.sel, u.pane = m.ID, paneTimeline
						}
					})
				}),
			)
		})
	}))
	if inThread || u.pane != paneTimeline || m.ID != t.sel {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	}
	rec := op.Record(gtx.Ops)
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	call := rec.Stop()
	paint.FillShape(gtx.Ops, u.faint(0x14), clip.Rect{Max: dims.Size}.Op())
	bar := image.Rect(0, 0, gtx.Dp(3), dims.Size.Y)
	paint.FillShape(gtx.Ops, u.pal.accent, clip.Rect(bar).Op())
	call.Add(gtx.Ops)
	return dims
}

// outline is the keys' cursor around r.
func (u *ui) outline(gtx C, r image.Rectangle) {
	rr := clip.UniformRRect(r, gtx.Dp(6))
	paint.FillShape(gtx.Ops, u.pal.accent, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(gtx.Dp(1.5))}.Op())
}

// stateOf is id's widgets, made on first use.
func (t *timeline) stateOf(id domain.EventID) *msgState {
	st := t.state[id]
	if st == nil {
		st = new(msgState)
		t.state[id] = st
	}
	return st
}

// layoutImage draws an image message at most 360×300dp, holding its box while it loads.
func (u *ui) layoutImage(ctx context.Context, gtx C, t *timeline, m domain.Message) D {
	maxW, maxH := gtx.Dp(360), gtx.Dp(300)
	p := t.images[m.ID]
	if p == nil {
		p = new(picture)
		t.images[m.ID] = p
		go u.loadImage(ctx, t.room, m.ID, p)
	}
	if !p.ok {
		w, h := maxW, maxH/2
		if m.Media.Width > 0 && m.Media.Height > 0 {
			w, h = fit(m.Media.Width, m.Media.Height, maxW, maxH)
		}
		size := image.Pt(w, h)
		paint.FillShape(gtx.Ops, u.faint(0x10), clip.Rect{Max: size}.Op())
		return D{Size: size}
	}
	b := p.op.Size()
	w, h := fit(b.X, b.Y, maxW, maxH)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return widget.Image{Src: p.op, Fit: widget.Contain}.Layout(gtx)
}

func (u *ui) loadImage(ctx context.Context, room domain.RoomID, id domain.EventID, p *picture) {
	data, err := u.backend.LoadImage(ctx, room, id)
	var img image.Image
	if err == nil {
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	u.mu.Lock()
	if err != nil {
		p.err = err
		log.Printf("image %s: %v", id, err)
	} else {
		p.op, p.ok = paint.NewImageOp(img), true
	}
	u.mu.Unlock()
	u.w.Invalidate()
}

// fit scales w×h down to fit maxW×maxH, never up.
func fit(w, h, maxW, maxH int) (int, int) {
	scale := min(1, float64(maxW)/float64(w), float64(maxH)/float64(h))
	return max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
}

// layoutPlayer is the dock over the timeline: the attachment's name, a close button,
// and for a video the 16:9 box its X window is kept over.
func (u *ui) layoutPlayer(gtx C) D {
	p := u.player
	if p == nil {
		return D{}
	}
	if u.closeBtn.Clicked(gtx) {
		u.stopLocked()
		return D{}
	}
	inset := gtx.Dp(8)
	state := "Playing"
	switch {
	case p.err != nil:
		state = "Can't play: " + p.err.Error()
	case p.file == "":
		state = "Fetching"
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	var header int
	return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				dims := layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, material.Body2(u.th, fmt.Sprintf("%s %s: %s", state, p.msg.Media.Type, p.msg.Media.Name)).Layout),
					layout.Rigid(material.Button(u.th, &u.closeBtn, "Close").Layout),
				)
				header = dims.Size.Y
				return dims
			}),
			layout.Rigid(func(gtx C) D {
				if !p.video {
					u.place(image.Rectangle{})
					return D{}
				}
				w := min(gtx.Constraints.Max.X, gtx.Dp(640))
				size := image.Pt(w, w*9/16)
				paint.FillShape(gtx.Ops, color.NRGBA{A: 0xff}, clip.Rect{Max: size}.Op())
				// The dock is the room pane's first row, so its box is at a known place.
				at := image.Pt(u.leftWidth+inset, u.topHeight+u.headerHeight+inset+header)
				u.place(image.Rectangle{Min: at, Max: at.Add(size)})
				return D{Size: size}
			}),
		)
	})
}

// layoutEditor is a composer: Enter is a newline and [keys.insert] send sends (see
// composing). The main composer (attach set) also has Attach, which sends a file picked
// in the desktop's file chooser, and Send; root is the thread a reply goes into, "" for
// the main timeline.
func (u *ui) layoutEditor(ctx context.Context, gtx C, ed *widget.Editor, attach *widget.Clickable, hint string, root domain.EventID) D {
	if attach != nil && u.sendBtn.Clicked(gtx) {
		u.submit(ctx, ed, root)
	}
	offer := u.complete(ctx, gtx, ed)
	if attach != nil && attach.Clicked(gtx) {
		go u.attach(ctx, u.current)
	}
	box := func(gtx C) D {
		return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(6), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
			return u.field(gtx, func(gtx C) D {
				rows := []layout.FlexChild{layout.Rigid(func(gtx C) D {
					h := hint
					if u.sendError != "" {
						h = u.sendError
					}
					return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(6)}.Layout(gtx, u.editor(ed, h).Layout)
				})}
				if attach != nil {
					rows = append(rows, layout.Rigid(func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return u.button(gtx, attach, "Attach", true, false) }),
							layout.Flexed(1, layout.Spacer{}.Layout),
							layout.Rigid(func(gtx C) D { return u.accentButton(gtx, &u.sendBtn, "Send") }),
						)
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
			})
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return u.layoutCompletion(gtx, offer) }),
		layout.Rigid(box))
}

// accentButton is the window's one call to action, in the accent.
func (u *ui) accentButton(gtx C, btn *widget.Clickable, label string) D {
	return btn.Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		dims := layout.Inset{Left: unit.Dp(14), Right: unit.Dp(14), Top: unit.Dp(5), Bottom: unit.Dp(5)}.Layout(gtx, func(gtx C) D {
			l := material.Body2(u.th, label)
			l.Color, l.Font.Weight = u.pal.focusText, font.Bold
			return l.Layout(gtx)
		})
		call := m.Stop()
		paint.FillShape(gtx.Ops, u.pal.accent, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(6)).Op(gtx.Ops))
		call.Add(gtx.Ops)
		return dims
	})
}

// attach asks for a file and sends it under its own name.
func (u *ui) attach(ctx context.Context, room domain.RoomID) {
	path, err := filedialog.File(ctx, "Attach a file")
	if errors.Is(err, filedialog.ErrCanceled) {
		return
	}
	if err == nil {
		err = u.backend.SendFile(ctx, room, path, filepath.Base(path), "")
	}
	u.mu.Lock()
	u.sendError = ""
	if err != nil {
		u.sendError = "Not sent: " + err.Error()
	}
	u.mu.Unlock()
	u.w.Invalidate()
}

// submit sends what ed holds into root ("" for the main timeline): from the main
// composer as a reply or an edit when one is pending.
func (u *ui) submit(ctx context.Context, ed *widget.Editor, root domain.EventID) {
	body := strings.TrimSpace(ed.Text())
	if body == "" {
		return
	}
	ed.SetText("")
	draft := domain.Draft{Body: body, ThreadRoot: root, Mentions: u.mentions[ed]}
	delete(u.mentions, ed)
	if ed == &u.composer {
		draft.ReplyTo, draft.Edits = u.replyTo, u.editing
		if u.editing != "" {
			u.composer.SetText(u.editSaved) // the words the edit set aside
		}
		u.replyTo, u.editing, u.editSaved = "", "", ""
	}
	go u.send(ctx, u.current, draft)
}

func (u *ui) send(ctx context.Context, room domain.RoomID, draft domain.Draft) {
	err := u.backend.Send(ctx, room, draft)
	u.mu.Lock()
	u.sendError = ""
	if err != nil {
		u.sendError = "Not sent: " + err.Error()
	}
	u.mu.Unlock()
	u.w.Invalidate()
}

func sender(m domain.Message) string {
	if m.SenderName != "" {
		return m.SenderName
	}
	return m.Sender
}

func text(m domain.Message) string {
	switch {
	case m.Redacted:
		return "Message deleted"
	case m.Undecrypted:
		return "(waiting for the key)"
	case m.Emote:
		return "* " + m.Body
	}
	return m.Body
}
