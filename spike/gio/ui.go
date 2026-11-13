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
	"math"
	"path/filepath"
	"runtime"
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
	gtext "gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/image/draw"
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
	foldBtns  []widget.Clickable           // each room's thread rows' fold, by room
	rowBtns   map[rowKey]*widget.Clickable // the thread and count rows
	roomList  widget.List
	placeBtns []widget.Clickable // the switcher's rail rows, by row
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
	nav          struct{ rail, back, forward, goTo, search, scope widget.Clickable }
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
	threadAt rowKey        // and the row it is on within its room
	expanded domain.RoomID // the open room once its count of threads was entered: it lists them all
	blur     bool          // take the focus from the fields on the next frame
	confirm  *confirmation
	replyTo  domain.EventID // what the composer's message answers
	editing  domain.EventID // the message of yours the composer replaces
	cancel   widget.Clickable
	selves   map[string]bool // every ID that is this person
	picking  *picker         // the reaction picker, nil when closed
	linking  *linkChoice     // the link chooser, nil when closed

	offer          *completion                         // the composer's completion popup
	completionKeys map[string]string                   // [keys.completion]: each press's action
	dismissed      string                              // the word whose popup Esc closed
	mentions       map[*widget.Editor][]domain.Mention // people chosen into each composer's draft
	drafts         drafts                              // each room's draft as kithd stores it
	editSaved      string                              // the composer's words an edit set aside

	presents    chan domain.RoomID   // the room to tell kithd is open (present)
	display     config.DeviceDisplay // the device's [display]: receipts, the unread line
	jumps       config.Jumps         // [[keys.jump]]: where each jump.<n> goes
	titled      int                  // the unread count the window's title shows
	frame       uint64               // frames laid out so far
	imageSlots  chan struct{}        // images read and decoded at once (loadImage)
	helping     *helpView            // the help overlay, nil when closed
	searching   *searchView          // a search's hits, listed in the room list's place; nil for none
	searchScope searchScope          // where the next search looks
	helpRows    []helpRow            // what it lists
	link        string               // a link kithd handed over before the rooms were read
	devicePath  string               // the device file, where folds are kept
}

// timeline is the open room's messages, oldest first, and how far back they reach.
type timeline struct {
	room         domain.RoomID
	msgs         []domain.Message
	state        map[domain.EventID]*msgState
	images       map[domain.EventID]*picture
	shown        []int                                // the main timeline's messages, as indexes of msgs, as last drawn
	reactions    map[domain.EventID][]domain.Reaction // by the message they annotate
	thread       domain.EventID                       // the thread open beside it; "" for none
	threadList   widget.List
	threadBtns   map[domain.EventID]*widget.Clickable
	next         string // the page before msgs; "" when the room's start is loaded
	loading      bool
	marked       domain.EventID                     // the newest message a receipt was sent for
	threadMarked domain.EventID                     // and the open thread's newest reply
	threads      map[domain.EventID][]domain.Thread // by the message each hangs beneath
	pictures     map[string]string                  // each sender's picture version
	slots        map[string]int                     // sender color slots, kithd's then this window's
	drawn        bool
	list         widget.List
	sel          domain.EventID // the main timeline's cursor
	jump         domain.EventID // a linked message to select once loaded
	unreadFrom   domain.EventID // the read receipt's message as the room opened: "New" goes under it

	members      []domain.Member // who a mention may name, ranked by kithd
	membersAsked bool
}

// picture is an image message's pixels, once fetched.
type picture struct {
	op    paint.ImageOp
	ok    bool
	err   error
	drawn uint64 // the frame it was last drawn in, a placeholder or not
}

func newUI(w *app.Window, backend *daemon.Remote, local bool, pal palette, dev config.Device, devicePath string) *ui {
	keys := dev.Keys
	u := &ui{
		w: w, backend: backend, th: material.NewTheme(), local: local, pal: pal, railShown: true,
		keymap: newKeymap(keys, actions), pane: paneComposer, selves: map[string]bool{},
		mentions: map[*widget.Editor][]domain.Mention{}, drafts: newDrafts(),
		unread: map[domain.RoomID]domain.Unread{}, faces: map[face]*picture{}, saves: map[domain.EventID]*save{},
		last: map[domain.RoomID]time.Time{}, typing: map[domain.RoomID][]string{},
		presents: make(chan domain.RoomID, 1), completionKeys: map[string]string{}, display: dev.Display, jumps: keys.Jump,
		helpRows: helpRows(keys), imageSlots: make(chan struct{}, max(1, runtime.NumCPU()/2)),
		rowBtns: map[rowKey]*widget.Clickable{}, devicePath: devicePath,
	}
	for _, path := range []string{"completion.next", "completion.prev", "completion.accept", "completion.dismiss"} {
		binding, _ := keys.Binding(path)
		for _, press := range config.SplitKeys(binding) {
			u.completionKeys[press] = path
		}
	}
	// go-text's sans-serif fallback has no emoji family, so name it, or emoji are tofu.
	u.th.Face = "sans-serif, emoji"
	u.th.TextSize = 15
	u.th.Palette = material.Palette{Fg: pal.text, Bg: pal.background, ContrastBg: pal.accent, ContrastFg: pal.focusText}
	u.search.SingleLine, u.search.Submit = true, true
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
	u.foldBtns = make([]widget.Clickable, len(rooms))
	u.mark(fmt.Sprintf("%d rooms loaded", len(rooms)))
	link := u.link
	u.mu.Unlock()
	u.w.Invalidate()
	u.loadRail(ctx)
	u.loadDrafts(ctx)
	if link != "" {
		u.followLink(ctx, link)
		return
	}
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
		u.present(room)
		u.expanded = "" // left: its threads are capped again
	}
	u.focus = &u.composer
	u.timeline = timeline{
		room: room, loading: true, slots: map[string]int{}, threads: map[domain.EventID][]domain.Thread{},
		unreadFrom: u.unread[room].ReadEvent,
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

// older fetches the limit messages before the oldest shown, keeping the view where it is.
func (u *ui) older(ctx context.Context, room domain.RoomID, from string, limit int) {
	page, err := u.backend.Timeline(ctx, room, from, limit)
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
	at := make(map[domain.EventID]int, len(t.msgs)+len(msgs))
	for i, h := range t.msgs {
		at[h.ID] = i
	}
	for _, m := range msgs {
		m.Body = shortcodes(m.Body)
		if i, ok := at[m.ID]; ok {
			t.msgs[i] = m
			continue
		}
		if !oldest.IsZero() && m.Timestamp.Before(oldest) && isMain(m) {
			before++
		}
		at[m.ID] = len(t.msgs)
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
				go u.loadDrafts(ctx) // written elsewhere meanwhile
			}
		case c := <-b.Changed():
			switch c {
			case domain.ChangeRail:
				go u.loadRail(ctx)
			case domain.ChangeDrafts:
				// Another window, or kith-mcp, wrote a draft: each changed one is taken
				// where nothing typed here is unsaved (loadDrafts).
				go u.loadDrafts(ctx)
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
		case uri := <-b.Follows():
			go u.followLink(ctx, uri)
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
// message, while the window has focus: announced or private as send_receipts and the
// [[display.read_rule]] naming the room, its home or the sender say (domain.ResolveRead);
// then the open thread's (markThreadRead).
func (u *ui) markRead(ctx context.Context) {
	defer u.markThreadRead(ctx)
	u.mu.Lock()
	t := &u.timeline
	var newest domain.EventID
	place := domain.DownloadPlace{RoomID: string(t.room)}
	for _, m := range slices.Backward(t.msgs) {
		if m.ThreadRoot == "" {
			newest, place.Sender, place.Person = m.ID, m.Sender, sender(m)
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
	private := u.receiptPrivate(place)
	u.mu.Unlock()
	go func() {
		log.Printf("read receipt: %s at %s (private %v)", room, newest, private)
		if err := u.backend.MarkRead(ctx, room, newest, private); err != nil {
			log.Printf("read receipt: %v", err)
			u.mu.Lock()
			if u.timeline.room == room && u.timeline.marked == newest {
				u.timeline.marked = "" // the next trigger tries again
			}
			u.mu.Unlock()
		}
	}()
}

// receiptPrivate is whether a receipt at place goes private, as send_receipts and the
// [[display.read_rule]] naming its room, the room's home or the sender say.
func (u *ui) receiptPrivate(place domain.DownloadPlace) bool {
	if r, ok := u.rail.served[domain.RoomID(place.RoomID)]; ok {
		place.Space, place.SpaceLabel = r.Home, r.HomeLabel
	}
	return !domain.ResolveRead(u.display.SendsReceipts(), u.display.ReadAfter(), readRules(u.display), place).Send
}

// markThreadRead sends the open thread's receipt for its newest reply, once per reply,
// while the window has focus: its own (MSC3771), the room's position left alone, so its
// row in the room list goes once read.
func (u *ui) markThreadRead(ctx context.Context) {
	u.mu.Lock()
	t := &u.timeline
	var newest domain.Message
	for _, m := range slices.Backward(t.msgs) {
		if t.thread != "" && m.ThreadRoot == t.thread {
			newest = m
			break
		}
	}
	if !u.focused || newest.ID == "" || newest.ID == t.threadMarked {
		u.mu.Unlock()
		return
	}
	t.threadMarked = newest.ID
	room, root := t.room, t.thread
	private := u.receiptPrivate(domain.DownloadPlace{RoomID: string(room), Thread: string(root), Sender: newest.Sender, Person: sender(newest)})
	u.mu.Unlock()
	go func() {
		if err := u.backend.MarkThreadRead(ctx, room, root, newest.ID, private); err != nil {
			log.Printf("thread receipt: %v", err)
			u.mu.Lock()
			if u.timeline.room == room && u.timeline.threadMarked == newest.ID {
				u.timeline.threadMarked = "" // the next trigger tries again
			}
			u.mu.Unlock()
		}
	}()
}

// markThreadRow marks a thread read from its row, unopened (rooms.mark_read on it), at
// its newest unread reply.
func (u *ui) markThreadRow(ctx context.Context, room domain.RoomID, th domain.ThreadUnread) {
	if th.Latest == "" {
		return
	}
	private := u.receiptPrivate(domain.DownloadPlace{RoomID: string(room), Thread: string(th.Root)})
	go func() {
		if err := u.backend.MarkThreadRead(ctx, room, th.Root, th.Latest, private); err != nil {
			log.Printf("thread receipt: %v", err)
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
	u.frame++
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
	if u.linking != nil {
		u.layoutLinking(ctx, gtx)
	}
	if u.helping != nil {
		u.layoutHelp(gtx)
	}
	u.keys(ctx, gtx)
	u.saveDrafts(ctx)
	u.title()
	return dims
}

// title puts the unread count in the window's title, where the desktop has its tray
// badge: every room's count summed, as its badge does (rooms.ts total).
// shortcut: no tray icon; a tray needs a new dependency (systray), wanted once this
// window replaces the desktop.
func (u *ui) title() {
	n := 0
	for _, r := range u.rooms {
		c, _ := u.unread[r.ID].Count(u.local)
		n += c
	}
	if n == u.titled {
		return
	}
	u.titled = n
	title := "kith (gio)"
	if n > 0 {
		title = fmt.Sprintf("(%d) %s", n, title)
	}
	u.w.Option(app.Title(title))
}

// layoutToolbar is the desktop's top bar: the rail's toggle, back and forward through
// the rooms opened, search and its scope, and the pages. People and Settings are the
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
	if u.nav.scope.Clicked(gtx) {
		u.searchScope = (u.searchScope + 1) % searchScope(len(scopeNames))
	}
	asked := u.nav.search.Clicked(gtx)
	for {
		ev, ok := u.search.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			asked = true
		}
	}
	if asked {
		if u.searchScope == scopeRoom && u.current == "" {
			u.searchScope = scopeAll
		}
		u.runSearch(ctx)
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
			layout.Rigid(func(gtx C) D { return u.button(gtx, &u.nav.scope, scopeNames[u.searchScope], true, false) }), gap,
			layout.Rigid(func(gtx C) D { return u.button(gtx, &u.nav.search, "Search", true, false) }), gap,
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
	if u.searching != nil {
		return u.layoutSearch(ctx, gtx)
	}
	shown, places := u.filtered(), u.places()
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
		if _, ok := ev.(widget.SubmitEvent); ok {
			switch {
			case len(shown) > 0:
				u.filter.SetText("")
				u.openLocked(ctx, u.rooms[shown[0]].ID)
			case len(places) > 0:
				u.goToPlace(places[0])
			}
			shown, places = u.filtered(), u.places()
		}
	}
	if len(u.placeBtns) != len(u.rail.rows) {
		u.placeBtns = make([]widget.Clickable, len(u.rail.rows))
	}
	rows := u.listRows(shown)
	cursor := u.cursorRow(rows)
	// The open thread's row, when listed, is the one selected; else the open room's.
	threadListed := slices.ContainsFunc(rows, func(r listRow) bool {
		return r.more == 0 && r.thread.Root != "" && r.thread.Root == u.timeline.thread
	})
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
				return u.field(gtx, u.editor(&u.filter, "Go to a room or place (Ctrl+K)").Layout)
			})
		}),
		layout.Flexed(1, func(gtx C) D {
			return material.List(u.th, &u.roomList).Layout(gtx, len(rows)+len(places), func(gtx C, j int) D {
				if j < len(rows) {
					r := rows[j]
					open := u.rooms[r.room].ID == u.current
					if r.key() == (rowKey{}) {
						open = open && !threadListed
					} else {
						open = open && r.more == 0 && r.thread.Root == u.timeline.thread
					}
					return u.layoutListRow(ctx, gtx, r, open, u.pane == paneRooms && j == cursor)
				}
				return u.layoutPlaceRow(gtx, places[j-len(rows)])
			})
		}),
	)
}

// filtered is the indexes of the rooms the list shows: the rail row's, or while the
// filter has text, as the desktop's switcher (switcher.ts): every room whose name,
// members or ID hold each word typed, whichever row it is in, those with unread first.
func (u *ui) filtered() []int {
	typed := u.filter.Text()
	shown := make([]int, 0, len(u.rooms))
	var unread []int
	for i, room := range u.rooms {
		switch {
		case strings.TrimSpace(typed) == "":
			if u.inRow(room.ID) {
				shown = append(shown, i)
			}
		case holdsAll(typed, room.DisplayName(), strings.Join(room.Members, " "), string(room.ID)):
			if c, _ := u.unread[room.ID].Count(u.local); c > 0 {
				unread = append(unread, i)
			} else {
				shown = append(shown, i)
			}
		}
	}
	return append(unread, shown...)
}

// places is the rail rows whose label or key hold each word typed in the filter, which
// the switcher offers after the rooms; none while it is empty.
// shortcut: reached by click or by Enter when no room matches, not by the room list's
// cursor.
func (u *ui) places() []int {
	typed := u.filter.Text()
	if strings.TrimSpace(typed) == "" {
		return nil
	}
	var out []int
	for i, row := range u.rail.rows {
		if holdsAll(typed, row.Label, row.Key) {
			out = append(out, i)
		}
	}
	return out
}

// holdsAll is whether texts hold every word of typed, in any case.
func holdsAll(typed string, texts ...string) bool {
	all := strings.ToLower(strings.Join(texts, " "))
	for _, w := range strings.Fields(strings.ToLower(typed)) {
		if !strings.Contains(all, w) {
			return false
		}
	}
	return true
}

// goToPlace shows rail row i's rooms, the filter emptied.
func (u *ui) goToPlace(i int) {
	u.filter.SetText("")
	u.rail.sel = i
	u.roomList.Position = layout.Position{}
	reveal(&u.rail.list.List, i, len(u.rail.rows))
}

// openLocked is open called with mu held.
func (u *ui) openLocked(ctx context.Context, room domain.RoomID) {
	u.mu.Unlock()
	u.open(ctx, room)
	u.mu.Lock()
}

// layoutListRow is a row of the room list: a room's, a thread's or a count's. open is
// whether it is the room or thread shown, cursor whether the keys are on it.
func (u *ui) layoutListRow(ctx context.Context, gtx C, row listRow, open, cursor bool) D {
	btn := &u.roomBtns[row.room]
	if row.key() != (rowKey{}) {
		if btn = u.rowBtns[row.key()]; btn == nil {
			btn = new(widget.Clickable)
			u.rowBtns[row.key()] = btn
		}
	}
	if btn.Clicked(gtx) {
		u.enterRow(ctx, row)
	}
	m := op.Record(gtx.Ops)
	var dims D
	if row.key() == (rowKey{}) {
		dims = u.layoutRoomRow(ctx, gtx, btn, row.room)
	} else {
		dims = btn.Layout(gtx, func(gtx C) D { return u.layoutThreadRow(gtx, row) })
	}
	call := m.Stop()
	r := image.Rectangle{Min: image.Pt(gtx.Dp(4), 0), Max: image.Pt(dims.Size.X-gtx.Dp(4), dims.Size.Y)}
	if open {
		paint.FillShape(gtx.Ops, u.pal.selection, clip.UniformRRect(r, gtx.Dp(6)).Op(gtx.Ops))
	}
	if cursor {
		u.outline(gtx, r)
	}
	call.Add(gtx.Ops)
	return dims
}

// layoutRoomRow is room i's row: its picture, name and unread, and before them, when it
// has thread rows, the fold that hides or shows them.
func (u *ui) layoutRoomRow(ctx context.Context, gtx C, btn *widget.Clickable, i int) D {
	room := u.rooms[i]
	threads := 0
	if u.display.Threads.Mode() != config.ThreadsNever && !room.IsInvite() && !u.rail.silenced[room.ID] {
		threads = len(u.threadsUnder(room.ID))
	}
	if u.foldBtns[i].Clicked(gtx) {
		u.foldThreads(room.ID)
	}
	n := u.unread[room.ID]
	count, highlights := n.Count(u.local)
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(14)
			if threads == 0 {
				return D{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}
			mark := "▾"
			if u.display.Threads.Folds(string(room.ID)) {
				mark = "▸"
			}
			return u.foldBtns[i].Layout(gtx, func(gtx C) D {
				l := material.Caption(u.th, mark)
				l.Color, l.Alignment = u.pal.muted, gtext.End
				return l.Layout(gtx)
			})
		}),
		layout.Flexed(1, func(gtx C) D {
			return btn.Layout(gtx, func(gtx C) D {
				return layout.Inset{Left: unit.Dp(2), Right: unit.Dp(12), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
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
			})
		}),
	)
}

// layoutThreadRow is a thread's row under its room, its marker pointing up at it: the
// root's text and its unread replies; or a count's, of the threads not listed.
func (u *ui) layoutThreadRow(gtx C, row listRow) D {
	label := threadLabel(row.thread)
	if row.more > 0 {
		label = fmt.Sprintf("+%d more threads", row.more)
		if row.more == 1 {
			label = "+1 more thread"
		}
	}
	return layout.Inset{Left: unit.Dp(30), Right: unit.Dp(12), Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				l := material.Body2(u.th, u.display.Threads.RowMark()+" ")
				l.Color = u.pal.muted
				return l.Layout(gtx)
			}),
			layout.Flexed(1, func(gtx C) D {
				l := material.Body2(u.th, label)
				l.MaxLines = 1
				if row.more > 0 || row.thread.Unread == 0 {
					l.Color = u.pal.muted
				} else {
					l.Font.Weight = font.Bold
				}
				return l.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
			layout.Rigid(func(gtx C) D {
				switch th := row.thread; {
				case row.more > 0:
				case th.Mentions > 0:
					return u.badge(gtx, fmt.Sprint(th.Unread), true)
				case th.Unread > 0:
					l := material.Caption(u.th, fmt.Sprint(th.Unread))
					l.Color = u.pal.muted
					return l.Layout(gtx)
				}
				return D{}
			}),
		)
	})
}

// layoutPlaceRow is rail row i offered by the switcher: a click goes to it.
func (u *ui) layoutPlaceRow(gtx C, i int) D {
	if u.placeBtns[i].Clicked(gtx) {
		u.goToPlace(i)
	}
	return u.placeBtns[i].Layout(gtx, func(gtx C) D {
		return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(12), Top: unit.Dp(6), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					l := material.Body1(u.th, u.rail.rows[i].Label)
					l.MaxLines = 1
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					l := material.Caption(u.th, "place")
					l.Color = u.pal.muted
					return l.Layout(gtx)
				}),
			)
		})
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
	if t.jump != "" {
		u.seek(ctx)
	}
	dims := material.List(u.th, &t.list).Layout(gtx, len(t.shown), func(gtx C, k int) D {
		return u.layoutMessage(ctx, gtx, t, t.shown, k, false)
	})
	if len(t.msgs) > 0 {
		t.drawn = true
	}
	if t.list.Position.First == 0 && t.next != "" && !t.loading && len(t.msgs) > 0 {
		t.loading = true
		go u.older(ctx, t.room, t.next, pageSize)
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
	newSince := !inThread && prev >= 0 && t.msgs[prev].ID == t.unreadFrom && u.display.ShowUnreadLine()
	head := newDay || newSince || t.msgs[prev].Sender != m.Sender ||
		m.Timestamp.Sub(t.msgs[prev].Timestamp) > 10*time.Minute
	const faceSize = 32
	var rows []layout.FlexChild
	if newDay {
		rows = append(rows, layout.Rigid(func(gtx C) D { return u.layoutDay(gtx, m.Timestamp) }))
	}
	if newSince {
		rows = append(rows, layout.Rigid(u.layoutNew))
	} else if head && !newDay {
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
				u.viewImage(ctx, t.room, m.ID, p)
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
		go u.loadImage(ctx, t.room, m.ID, p, maxW, maxH)
	}
	p.drawn = u.frame
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

// loadImage reads and decodes an image message's picture in one of the window's few
// image slots. One whose turn comes after it left the screen (the last frame did not
// draw it: scrolled past) is dropped, read again once drawn again; idle, no frame passes,
// so what is on screen stays.
func (u *ui) loadImage(ctx context.Context, room domain.RoomID, id domain.EventID, p *picture, maxW, maxH int) {
	u.imageSlots <- struct{}{}
	defer func() { <-u.imageSlots }()
	u.mu.Lock()
	gone := p.drawn != u.frame
	if t := &u.timeline; gone && t.room == room && t.images[id] == p {
		delete(t.images, id)
	}
	u.mu.Unlock()
	if gone {
		return
	}
	data, err := u.backend.LoadImage(ctx, room, id)
	var img paint.ImageOp
	if err == nil {
		img, err = decode(data, maxW, maxH)
	}
	u.mu.Lock()
	if err != nil {
		p.err = err
		log.Printf("image %s: %v", id, err)
	} else {
		p.op, p.ok = img, true
	}
	u.mu.Unlock()
	u.w.Invalidate()
}

// decode reads an image scaled down to fit maxW×maxH px, ready to draw. Kept at full
// size, every photo scrolled past held megabytes for as long as its room was open.
func decode(data []byte, maxW, maxH int) (paint.ImageOp, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return paint.ImageOp{}, err
	}
	b := img.Bounds()
	w, h := fit(b.Dx(), b.Dy(), maxW, maxH)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	} else {
		// A camera photo is ten times the size drawn: CatmullRom over all of it is
		// 380 ms. A cheap step to twice the size first leaves it 25 ms, alike to see.
		var src image.Image = img
		if b.Dx() > 2*w && b.Dy() > 2*h {
			mid := image.NewRGBA(image.Rect(0, 0, 2*w, 2*h))
			draw.ApproxBiLinear.Scale(mid, mid.Bounds(), img, b, draw.Src, nil)
			src, b = mid, mid.Bounds()
		}
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	}
	return paint.NewImageOp(dst), nil
}

// viewImage shows p, an image message's picture, over the window at once, and the image at
// full size once read: the timeline keeps only the size it draws.
func (u *ui) viewImage(ctx context.Context, room domain.RoomID, id domain.EventID, p *picture) {
	shown := &p.op
	u.viewing = shown
	go func() {
		data, err := u.backend.LoadImage(ctx, room, id)
		var full paint.ImageOp
		if err == nil {
			full, err = decode(data, math.MaxInt, math.MaxInt)
		}
		if err != nil {
			log.Printf("image %s: %v", id, err)
			return
		}
		u.mu.Lock()
		if u.viewing == shown {
			u.viewing = &full
		}
		u.mu.Unlock()
		u.w.Invalidate()
	}()
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
