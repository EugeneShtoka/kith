package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	_ "golang.org/x/image/webp"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

const pageSize = 50

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
	unread    map[domain.RoomID]domain.Unread
	local     bool // badges count cached messages, not the server's notifications
	focused   bool
	focus     any // the widget to focus on the next frame
	current   domain.RoomID
	timeline  timeline
	composer  widget.Editor
	sendError string

	view      uintptr // the Gio window's X11 ID; 0 on Wayland
	player    *player
	closeBtn  widget.Clickable
	leftWidth int // the room list and its rule, where the room pane starts
}

// timeline is the open room's messages, oldest first, and how far back they reach.
type timeline struct {
	room    domain.RoomID
	msgs    []domain.Message
	sel     map[domain.EventID]*widget.Selectable
	media   map[domain.EventID]*widget.Clickable
	images  map[domain.EventID]*picture
	next    string // the page before msgs; "" when the room's start is loaded
	loading bool
	marked  domain.EventID // the newest message a receipt was sent for
	slots   map[string]int // sender color slots, kithd's then this window's
	drawn   bool
	list    widget.List
}

// picture is an image message's pixels, once fetched.
type picture struct {
	op  paint.ImageOp
	ok  bool
	err error
}

func newUI(w *app.Window, backend *daemon.Remote, local bool) *ui {
	u := &ui{w: w, backend: backend, th: material.NewTheme(), local: local, unread: map[domain.RoomID]domain.Unread{}}
	// go-text's sans-serif fallback has no emoji family, so name it, or emoji are tofu.
	u.th.Face = "sans-serif, emoji"
	// Dark, as the desktop is: the shared sender colors are made for a dark ground.
	u.th.Palette = material.Palette{
		Fg: rgb(0xe4e4e7), Bg: rgb(0x1f2023), ContrastBg: rgb(0x4c7bd9), ContrastFg: rgb(0xffffff),
	}
	u.roomList.Axis = layout.Vertical
	u.filter.SingleLine, u.filter.Submit = true, true
	return u
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

// faint is the foreground at alpha a: rules, captions, placeholders.
func (u *ui) faint(a uint8) color.NRGBA {
	c := u.th.Palette.Fg
	c.A = a
	return c
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
	u.current = room
	u.focus = &u.composer
	u.timeline = timeline{
		room: room, loading: true, slots: map[string]int{},
		sel:    map[domain.EventID]*widget.Selectable{},
		media:  map[domain.EventID]*widget.Clickable{},
		images: map[domain.EventID]*picture{},
		list:   widget.List{List: layout.List{Axis: layout.Vertical, ScrollToEnd: true}},
	}
	u.mu.Unlock()
	go func() {
		slots, err := u.backend.SenderSlots(ctx, room)
		if err != nil {
			log.Printf("sender slots: %v", err)
		}
		u.mu.Lock()
		if u.timeline.room == room {
			for who, slot := range slots {
				u.timeline.slots[who] = slot
			}
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
	u.timeline.list.Position.First += added
	u.timeline.next, u.timeline.loading = page.Next, false
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

// upsert folds msgs in by ID, keeping time order; it returns how many were new and
// older than every message held (what a prepend shifts the view by).
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
		if !oldest.IsZero() && m.Timestamp.Before(oldest) {
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
			u.merge(ctx, m.RoomID, []domain.Message{m})
		case n := <-b.Unread():
			u.mu.Lock()
			u.unread[n.RoomID] = n
			u.mu.Unlock()
			u.w.Invalidate()
		case back := <-b.Attached():
			if back {
				go u.reloadUnread(ctx)
			}
		case <-b.Activity():
		case <-b.Reactions():
		case <-b.Invites():
		case <-b.Verifications():
		case <-b.Follows():
		case <-b.ConfigChanges():
		case <-b.Changed():
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
	paint.Fill(gtx.Ops, u.th.Palette.Bg)
	for {
		ev, ok := gtx.Event(key.Filter{Name: "K", Required: key.ModShortcut})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			u.focus = &u.filter
		}
	}
	if u.focus != nil {
		gtx.Execute(key.FocusCmd{Tag: u.focus})
		u.focus = nil
	}
	if u.err != "" {
		return layout.UniformInset(unit.Dp(16)).Layout(gtx, material.Body1(u.th, u.err).Layout)
	}
	u.leftWidth = gtx.Dp(300) + gtx.Dp(1)
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(300)
			gtx.Constraints.Max.X = gtx.Constraints.Min.X
			return u.layoutRooms(ctx, gtx)
		}),
		layout.Rigid(func(gtx C) D {
			size := image.Pt(gtx.Dp(1), gtx.Constraints.Max.Y)
			paint.FillShape(gtx.Ops, u.faint(0x30), clip.Rect{Max: size}.Op())
			return D{Size: size}
		}),
		layout.Flexed(1, func(gtx C) D { return u.layoutRoom(ctx, gtx) }),
	)
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
				return widget.Border{Color: u.faint(0x40), Width: unit.Dp(1), CornerRadius: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
					return layout.UniformInset(unit.Dp(6)).Layout(gtx, func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return material.Editor(u.th, &u.filter, "Go to room (Ctrl+K)").Layout(gtx)
					})
				})
			})
		}),
		layout.Flexed(1, func(gtx C) D {
			return material.List(u.th, &u.roomList).Layout(gtx, len(shown), func(gtx C, j int) D {
				return u.layoutRoomRow(ctx, gtx, shown[j])
			})
		}),
	)
}

// filtered is the indexes of the rooms whose names hold the filter's text.
func (u *ui) filtered() []int {
	q := strings.ToLower(strings.TrimSpace(u.filter.Text()))
	shown := make([]int, 0, len(u.rooms))
	for i, room := range u.rooms {
		if q == "" || strings.Contains(strings.ToLower(room.DisplayName()), q) {
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
	if u.roomBtns[i].Clicked(gtx) && room.ID != u.current {
		u.openLocked(ctx, room.ID)
	}
	n := u.unread[room.ID]
	count, highlights := n.Count(u.local)
	return u.roomBtns[i].Layout(gtx, func(gtx C) D {
		if room.ID == u.current {
			size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(36))
			paint.FillShape(gtx.Ops, u.faint(0x20), clip.Rect{Max: size}.Op())
		}
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(7), Bottom: unit.Dp(7)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					l := material.Body1(u.th, room.DisplayName())
					l.MaxLines = 1
					if n.HasUnread(u.local) {
						l.Font.Weight = font.Bold
					} else {
						l.Color = u.faint(0xb0)
					}
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					switch {
					case count > 0:
						return u.badge(gtx, fmt.Sprint(count), highlights > 0)
					case n.Marked:
						return u.badge(gtx, "•", false)
					}
					return D{}
				}),
			)
		})
	})
}

// badge is a count in a pill: red when it holds a mention, else the accent.
func (u *ui) badge(gtx C, text string, mention bool) D {
	bg := u.th.Palette.ContrastBg
	if mention {
		bg = rgb(0xe5484d)
	}
	l := material.Caption(u.th, text)
	l.Color, l.Font.Weight = u.th.Palette.ContrastFg, font.Bold
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
		layout.Rigid(u.layoutPlayer),
		layout.Flexed(1, func(gtx C) D { return u.layoutTimeline(ctx, gtx) }),
		layout.Rigid(func(gtx C) D { return u.layoutComposer(ctx, gtx) }),
	)
}

func (u *ui) layoutTimeline(ctx context.Context, gtx C) D {
	t := &u.timeline
	dims := material.List(u.th, &t.list).Layout(gtx, len(t.msgs), func(gtx C, i int) D {
		return u.layoutMessage(ctx, gtx, t, i)
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

func (u *ui) layoutMessage(ctx context.Context, gtx C, t *timeline, i int) D {
	m := t.msgs[i]
	newDay := i == 0 || !sameDay(t.msgs[i-1].Timestamp, m.Timestamp)
	head := newDay || t.msgs[i-1].Sender != m.Sender ||
		m.Timestamp.Sub(t.msgs[i-1].Timestamp) > 10*time.Minute
	top := unit.Dp(1)
	if head {
		top = unit.Dp(10)
	}
	return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Top: top}.Layout(gtx, func(gtx C) D {
		var rows []layout.FlexChild
		if newDay {
			rows = append(rows, layout.Rigid(func(gtx C) D { return u.layoutDay(gtx, m.Timestamp) }))
		}
		if head {
			rows = append(rows, layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						l := material.Body1(u.th, sender(m))
						l.Font.Weight = font.Bold
						l.Color = t.color(m.Sender)
						return l.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx C) D {
						l := material.Caption(u.th, m.Timestamp.Local().Format("15:04"))
						l.Color = u.faint(0x90)
						return l.Layout(gtx)
					}),
				)
			}))
		}
		if m.Media.IsImage() {
			rows = append(rows, layout.Rigid(func(gtx C) D { return u.layoutImage(ctx, gtx, t, m) }))
		} else if m.Media != nil {
			btn := t.media[m.ID]
			if btn == nil {
				btn = new(widget.Clickable)
				t.media[m.ID] = btn
			}
			if btn.Clicked(gtx) {
				u.play(ctx, m)
			}
			rows = append(rows, layout.Rigid(func(gtx C) D {
				l := material.Body2(u.th, fmt.Sprintf("▶ [%s] %s", m.Media.Type, m.Media.Name))
				l.Color = u.th.Palette.ContrastBg
				return btn.Layout(gtx, l.Layout)
			}))
		}
		if body := text(m); body != "" {
			rows = append(rows, layout.Rigid(func(gtx C) D {
				sel := t.sel[m.ID]
				if sel == nil {
					sel = new(widget.Selectable)
					t.sel[m.ID] = sel
				}
				l := material.Body1(u.th, body)
				l.State = sel
				return l.Layout(gtx)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
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
				at := image.Pt(u.leftWidth+inset, inset+header)
				u.place(image.Rectangle{Min: at, Max: at.Add(size)})
				return D{Size: size}
			}),
		)
	})
}

// layoutComposer takes a message: Enter is a newline and Ctrl or Alt+Enter sends, as
// [keys.insert] has it by default. The editor's own Enter filter allows only Shift, so
// these reach this one.
func (u *ui) layoutComposer(ctx context.Context, gtx C) D {
	var sends []event.Filter
	for _, name := range []key.Name{key.NameReturn, key.NameEnter} {
		for _, mod := range []key.Modifiers{key.ModCtrl, key.ModAlt} {
			sends = append(sends, key.Filter{Focus: &u.composer, Name: name, Required: mod})
		}
	}
	for {
		ev, ok := gtx.Event(sends...)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			body := strings.TrimSpace(u.composer.Text())
			if body != "" {
				u.composer.SetText("")
				go u.send(ctx, u.current, body)
			}
		}
	}
	return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
		return widget.Border{Color: u.faint(0x40), Width: unit.Dp(1), CornerRadius: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
				hint := "Message"
				if u.sendError != "" {
					hint = u.sendError
				}
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return material.Editor(u.th, &u.composer, hint).Layout(gtx)
			})
		})
	})
}

func (u *ui) send(ctx context.Context, room domain.RoomID, body string) {
	err := u.backend.Send(ctx, room, domain.Draft{Body: body})
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
		return "(deleted)"
	case m.Undecrypted:
		return "(waiting for the key)"
	case m.Emote:
		return "* " + m.Body
	}
	return m.Body
}
