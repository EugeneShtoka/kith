package matrix

import (
	"context"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reading is local first: a room read in kith is read here before the receipt goes, and
// stays read if the homeserver does not take it (offline, refused). The receipt is then
// owed, with what it was for, and sent again after a sync succeeds, until the
// homeserver takes it.

// owedRead is a receipt the homeserver has not taken: the event read through, whether
// it is private, main-timeline only (an open room) or unthreaded (reading a room
// without opening it, which clears its threads), and whether the room was marked
// unread when read, so the mark is cleared there too.
type owedRead struct {
	event    domain.EventID
	private  bool
	mainOnly bool
	unmark   bool
}

// owedReads is the receipts owed, by room: the latest wins.
type owedReads struct {
	mu    sync.Mutex
	marks map[domain.RoomID]owedRead
	// sending is set while a resend runs, so syncs do not pile them up.
	sending atomic.Bool
}

// add owes r for room; a later read replaces an earlier one, keeping a mark to clear.
func (o *owedReads) add(room domain.RoomID, r owedRead) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.marks == nil {
		o.marks = map[domain.RoomID]owedRead{}
	}
	r.unmark = r.unmark || o.marks[room].unmark
	o.marks[room] = r
}

// settle drops room's receipt for event once the homeserver took it, unless another
// was owed meanwhile.
func (o *owedReads) settle(room domain.RoomID, event domain.EventID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.marks[room].event == event {
		delete(o.marks, room)
	}
}

// owed is a copy of the receipts owed.
func (o *owedReads) owed() map[domain.RoomID]owedRead {
	o.mu.Lock()
	defer o.mu.Unlock()
	return maps.Clone(o.marks)
}

// tellRead sends room's receipt for r, owing it until the homeserver takes it; then
// clears the room's mark there if r asks, and moves the position to the server's
// newest event. It reports whether the homeserver took it, with its refusal.
func (b *InProc) tellRead(ctx context.Context, room domain.RoomID, r owedRead) error {
	b.reads.add(room, r)
	target, hint, err := b.sendRead(ctx, room, r.event, r.private, r.mainOnly)
	if err != nil {
		b.log().Warn("the homeserver did not take a read receipt; it is kept and sent again after a sync",
			"op", "mark read", "room", room, "event", r.event, "err", err)
		return err
	}
	b.reads.settle(room, r.event)
	if r.unmark {
		b.warnIf(ctx, b.MarkRoomUnread(ctx, room, false), "clear marked-unread", "room", room)
	}
	held := heldMain
	if !r.mainOnly {
		held = heldUnthreaded
	}
	b.readThrough(ctx, room, held, target, hint)
	return nil
}

// sendOwed sends the owed receipts, one resend at a time.
func (b *InProc) sendOwed(ctx context.Context) {
	if !b.reads.sending.CompareAndSwap(false, true) {
		return
	}
	defer b.reads.sending.Store(false)
	for room, r := range b.reads.owed() {
		if ctx.Err() != nil {
			return
		}
		_ = b.tellRead(ctx, room, r) // a refusal stays owed, and is logged
	}
}

// readHere reads room through event here, at the newest cached time, and clears its
// marked-unread flag here; it reports whether the room was marked, for the receipt to
// clear the homeserver's.
func (b *InProc) readHere(ctx context.Context, room domain.RoomID, held, event domain.EventID) bool {
	marked := false
	if _, changed := b.unread.update(room, func(cur *domain.Unread, ts *int64) bool {
		if !cur.Marked {
			return false
		}
		marked, cur.Marked = true, false
		b.saveUnread(ctx, *cur, *ts)
		return true
	}); changed {
		b.recountAndEmit(ctx, room, true)
	}
	b.readThrough(ctx, room, held, event, 0)
	return marked
}
