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
	"gioui.org/layout"
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
	drawn   bool
	list    widget.List
}

// picture is an image message's pixels, once fetched.
type picture struct {
	op  paint.ImageOp
	ok  bool
	err error
}

func newUI(w *app.Window, backend *daemon.Remote) *ui {
	u := &ui{w: w, backend: backend, th: material.NewTheme()}
	// go-text's sans-serif fallback has no emoji family, so name it, or emoji are tofu.
	u.th.Face = "sans-serif, emoji"
	u.roomList.Axis = layout.Vertical
	u.composer.Submit = true
	return u
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
	u.mu.Lock()
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
	u.timeline = timeline{
		room: room, loading: true,
		sel:    map[domain.EventID]*widget.Selectable{},
		media:  map[domain.EventID]*widget.Clickable{},
		images: map[domain.EventID]*picture{},
		list:   widget.List{List: layout.List{Axis: layout.Vertical, ScrollToEnd: true}},
	}
	u.mu.Unlock()
	go func() {
		cached, err := u.backend.CachedTimeline(ctx, room)
		if err != nil {
			u.fail(fmt.Errorf("cached timeline: %w", err))
		}
		u.merge(room, cached)
		page, err := u.backend.Timeline(ctx, room, "", pageSize)
		if err != nil {
			u.fail(fmt.Errorf("timeline: %w", err))
		}
		u.merge(room, page.Messages)
		u.mu.Lock()
		if u.timeline.room == room {
			u.timeline.next, u.timeline.loading = page.Next, false
		}
		u.mu.Unlock()
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

func (u *ui) merge(room domain.RoomID, msgs []domain.Message) {
	u.mu.Lock()
	if u.timeline.room == room {
		u.timeline.upsert(msgs)
	}
	u.mu.Unlock()
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

// follow folds the sync stream's messages into the open room.
func (u *ui) follow(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-u.backend.Messages():
			if !ok {
				return
			}
			u.merge(m.RoomID, []domain.Message{m})
		}
	}
}

func (u *ui) drawnTimeline() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.timeline.drawn && !u.timeline.loading
}

func (u *ui) layout(ctx context.Context, gtx C) D {
	u.mu.Lock()
	defer u.mu.Unlock()
	paint.Fill(gtx.Ops, color.NRGBA{R: 0xfa, G: 0xfa, B: 0xfa, A: 0xff})
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
			paint.FillShape(gtx.Ops, color.NRGBA{A: 0x30}, clip.Rect{Max: size}.Op())
			return D{Size: size}
		}),
		layout.Flexed(1, func(gtx C) D { return u.layoutRoom(ctx, gtx) }),
	)
}

func (u *ui) layoutRooms(ctx context.Context, gtx C) D {
	if u.rooms == nil {
		return layout.UniformInset(unit.Dp(12)).Layout(gtx, material.Body2(u.th, "Loading rooms").Layout)
	}
	return material.List(u.th, &u.roomList).Layout(gtx, len(u.rooms), func(gtx C, i int) D {
		room := u.rooms[i]
		if u.roomBtns[i].Clicked(gtx) && room.ID != u.current {
			u.mu.Unlock()
			u.open(ctx, room.ID)
			u.mu.Lock()
		}
		return u.roomBtns[i].Layout(gtx, func(gtx C) D {
			if room.ID == u.current {
				size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(48))
				paint.FillShape(gtx.Ops, color.NRGBA{R: 0x3f, G: 0x51, B: 0xb5, A: 0x30}, clip.Rect{Max: size}.Op())
			}
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(6), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						l := material.Body1(u.th, room.DisplayName())
						l.MaxLines = 1
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						l := material.Caption(u.th, string(room.Network))
						l.Color = color.NRGBA{A: 0x90}
						return l.Layout(gtx)
					}),
				)
			})
		})
	})
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
	head := i == 0 || t.msgs[i-1].Sender != m.Sender ||
		m.Timestamp.Sub(t.msgs[i-1].Timestamp) > 10*time.Minute
	top := unit.Dp(1)
	if head {
		top = unit.Dp(10)
	}
	return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Top: top}.Layout(gtx, func(gtx C) D {
		var rows []layout.FlexChild
		if head {
			rows = append(rows, layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						l := material.Body1(u.th, sender(m))
						l.Font.Weight = font.Bold
						return l.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx C) D {
						l := material.Caption(u.th, m.Timestamp.Local().Format("Jan 2 15:04"))
						l.Color = color.NRGBA{A: 0x90}
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
		paint.FillShape(gtx.Ops, color.NRGBA{A: 0x10}, clip.Rect{Max: size}.Op())
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

func (u *ui) layoutComposer(ctx context.Context, gtx C) D {
	for {
		ev, ok := u.composer.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			body := strings.TrimSpace(u.composer.Text())
			if body != "" {
				u.composer.SetText("")
				go u.send(ctx, u.current, body)
			}
		}
	}
	return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
		return widget.Border{Color: color.NRGBA{A: 0x40}, Width: unit.Dp(1), CornerRadius: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
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
