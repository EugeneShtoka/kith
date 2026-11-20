package main

import (
	"context"
	"slices"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The send queue (/scheduled, the desktop's Queue): what /at and /in left with kithd, this
// room's or every room's, soonest first, each with Cancel, and Try again where the
// network refused it or it keeps failing. It is read again while shown, a little after
// its soonest message is due and every queueRefresh, so one sent meanwhile goes.
// shortcut: daemon.Remote's CancelScheduled drops kithd's "was it still waiting", so a
// cancel of one already sent says Canceled too; return it to say so.

// queueRefresh is how often the queue shown is read again; queueAfterDue, how long past
// a message's time.
const (
	queueRefresh  = 15 * time.Second
	queueAfterDue = time.Second
)

// queueView is the queue as the card shows it: room's ("" every room's), and each read.
type queueView struct {
	card    *historyView
	room    domain.RoomID
	entries []domain.ScheduledMessage
	read    bool
	err     error
	reads   int // the newest read asked; an older one's answer is dropped
	timer   *time.Timer
	every   widget.Clickable
	btns    map[string]*[2]widget.Clickable // each message's Try again and Cancel
}

// showQueue opens the card on room's queue.
func (u *ui) showQueue(ctx context.Context, room domain.RoomID) {
	q := &queueView{room: room, btns: map[string]*[2]widget.Clickable{}}
	q.card = &historyView{
		title:  "Queued in this room",
		rowsOf: func() []historyRow { return u.queueRows(ctx, q, time.Now()) },
		list:   widget.List{List: layout.List{Axis: layout.Vertical}},
	}
	u.revisions = q.card
	go u.readQueue(ctx, q)
}

// readQueue reads the queue from kithd, and again later while the card is up.
func (u *ui) readQueue(ctx context.Context, q *queueView) {
	u.mu.Lock()
	q.reads++
	mine := q.reads
	u.mu.Unlock()
	entries, err := u.backend.ScheduledMessages(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	if mine != q.reads || u.revisions != q.card {
		return // a newer read answers, or the card is gone
	}
	q.read, q.err = true, err
	if err == nil {
		q.entries = entries
	}
	now, next := time.Now(), queueRefresh
	for i := range q.entries {
		if due := q.entries[i].At.Sub(now) + queueAfterDue; due > 0 && due < next {
			next = due
		}
	}
	if q.timer != nil {
		q.timer.Stop()
	}
	q.timer = time.AfterFunc(next, func() { u.readQueue(ctx, q) })
	u.w.Invalidate()
}

// queueRows are the card's lines: the switch between this room and every room, then
// each message queued there, or why none is listed.
func (u *ui) queueRows(ctx context.Context, q *queueView, now time.Time) []historyRow {
	other := "Every room"
	if q.room == "" {
		other = "This room"
	}
	rows := []historyRow{{buttons: []cardButton{{label: other, click: &q.every, do: func() {
		if q.room == "" {
			q.room, q.card.title = u.current, "Queued in this room"
		} else {
			q.room, q.card.title = "", "Queued in every room"
		}
	}}}}}
	shown := slices.DeleteFunc(slices.Clone(q.entries), func(m domain.ScheduledMessage) bool { return q.room != "" && m.RoomID != q.room })
	switch {
	case !q.read:
		return append(rows, historyRow{head: "Reading the queue…"})
	case q.err != nil:
		rows = append(rows, historyRow{head: "Could not read the queue: " + q.err.Error()})
	}
	if len(shown) == 0 && q.err == nil {
		empty := "Nothing scheduled in this room"
		if q.room == "" {
			empty = "Nothing scheduled"
		}
		return append(rows, historyRow{head: empty})
	}
	for i := range shown {
		m := &shown[i]
		btns := q.btns[m.ID]
		if btns == nil {
			btns = new([2]widget.Clickable)
			q.btns[m.ID] = btns
		}
		head := ""
		if q.room == "" {
			head = u.roomNamed(m.RoomID)
		}
		var buttons []cardButton
		why := ""
		switch {
		case m.Refused != "":
			why = "refused: " + m.Refused
		case m.Failing != "":
			why = "failing: " + m.Failing
		}
		if why != "" {
			head = joinDot(head, why)
			buttons = append(buttons, cardButton{label: "Try again", click: &btns[0], do: func() {
				go u.queueDo(ctx, q, "Trying again", func() error { return u.backend.RetryScheduled(ctx, m.ID) })
			}})
		}
		buttons = append(buttons, cardButton{label: "Cancel", click: &btns[1], do: func() {
			go u.queueDo(ctx, q, "Canceled", func() error { return u.backend.CancelScheduled(ctx, m.ID) })
		}})
		rows = append(rows, historyRow{head: head, body: m.Summary(now), buttons: buttons})
	}
	return rows
}

// queueDo asks kithd to cancel or retry, says what came of it, and reads the queue again.
func (u *ui) queueDo(ctx context.Context, q *queueView, done string, call func() error) {
	if err := call(); err != nil {
		done = "Could not: " + err.Error()
	}
	u.say(done)
	u.readQueue(ctx, q)
}

// joinDot joins a and b with a middle dot, either may be "".
func joinDot(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + " · " + b
}

// roomNamed is room's name as the rail shows it, else its ID.
func (u *ui) roomNamed(room domain.RoomID) string {
	if r, ok := u.roomOf(room); ok {
		return u.roomName(r)
	}
	return string(room)
}

// roomOf is room among the profile's; false when it holds none of that ID.
func (u *ui) roomOf(room domain.RoomID) (domain.Room, bool) {
	if i := slices.IndexFunc(u.rooms, func(r domain.Room) bool { return r.ID == room && room != "" }); i >= 0 {
		return u.rooms[i], true
	}
	return domain.Room{}, false
}
