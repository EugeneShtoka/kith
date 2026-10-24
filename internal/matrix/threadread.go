package matrix

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Thread read receipts (MSC3771). Our own replies never count, and an unthreaded
// receipt moves the room's thread floor. The server keeps one forward-only receipt per
// kind (unthreaded, main, each thread; public and private apart), so the room's
// position is the newest by its event's time; a receipt for an uncached event is held
// until cached or placed by fetchPosition (state events carry no usable time).

// Kinds of held receipt, beside a thread's root: the room's own positions.
const (
	// heldUnthreaded moves the room's position and its thread floor.
	heldUnthreaded domain.EventID = ""
	// heldMain moves the room's position only.
	heldMain = domain.EventID(event.ReadReceiptThreadMain)
)

// readCandidate is one of our receipts that may be the room's read position.
type readCandidate struct {
	Event domain.EventID
	// Unthreaded is a receipt with no thread_id, which reads the threads too.
	Unthreaded bool
	// At is when the receipt was sent: the order used only without a cache.
	At time.Time
}

// threadReceipt is our receipt in one thread.
type threadReceipt struct{ Root, Event domain.EventID }

// selfReceipts is what one room's ephemeral events said about our read position.
type selfReceipts struct {
	// Room is every unthreaded and main-timeline receipt, public and private.
	Room []readCandidate
	// Threads is every threaded one.
	Threads []threadReceipt
}

// readPos is a room's read position: the event and when it happened (0: unknown).
type readPos struct {
	Event domain.EventID
	TS    int64
}

// ahead reports whether next moves a position at cur forward: a later event, or
// another at the same time. An unknown time moves only an unknown position.
func (cur readPos) ahead(next readPos) bool {
	if next.Event == "" || next.TS < cur.TS {
		return false
	}
	return next.TS > cur.TS || next.Event != cur.Event
}

// readReceipts collects all our receipts from one room's ephemeral events; only
// their events' times say which is newest.
func (x ids) readReceipts(events []*event.Event, self id.UserID) selfReceipts {
	out := selfReceipts{}
	for _, evt := range events {
		if evt == nil || evt.Type != event.EphemeralEventReceipt {
			continue
		}
		parse(evt)
		for eventID, receipts := range *evt.Content.AsReceipt() {
			// Private receipts echo back as m.read.private; with send_receipts off
			// they are the only ones.
			for _, kind := range []event.ReceiptType{event.ReceiptTypeRead, event.ReceiptTypeReadPrivate} {
				rr, ok := receipts[kind][self]
				if !ok {
					continue
				}
				switch rr.ThreadID {
				case "", event.ReadReceiptThreadMain:
					out.Room = append(out.Room, readCandidate{
						Event: x.event(eventID), Unthreaded: rr.ThreadID == "", At: rr.Timestamp,
					})
				default:
					out.Threads = append(out.Threads, threadReceipt{
						Root: x.event(rr.ThreadID), Event: x.event(eventID),
					})
				}
			}
		}
	}
	return out
}

// applyReceipts caches thread positions and returns the newest placeable room
// position and whether a thread position moved. Unknown events are held; without a
// cache the most recently sent receipt stands in.
func (b *InProc) applyReceipts(ctx context.Context, roomID domain.RoomID, r selfReceipts) (readPos, bool) {
	if b.cache == nil {
		return newestSent(r.Room), false
	}
	var best readPos
	moved := false
	for _, c := range r.Room {
		kind := heldMain
		if c.Unthreaded {
			kind = heldUnthreaded
		}
		ts, ok := b.positionOf(ctx, roomID, c.Event)
		if !ok {
			b.held.hold(roomID, kind, c.Event)
			continue
		}
		pos, threads := b.placeReceipt(ctx, roomID, kind, c.Event, ts)
		moved = moved || threads
		if best.ahead(pos) {
			best = pos
		}
	}
	for _, t := range r.Threads {
		if b.applyThreadReceipt(ctx, roomID, t.Root, t.Event) {
			moved = true
		}
	}
	return best, moved
}

// newestSent is the most recently sent room receipt, with its event's time unknown.
func newestSent(cands []readCandidate) readPos {
	var best readCandidate
	for _, c := range cands {
		if best.Event == "" || !c.At.Before(best.At) {
			best = c
		}
	}
	return readPos{Event: best.Event}
}

// placeReceipt records a receipt whose event time is known. A room receipt seeds a
// missing thread floor (else all history counts unread); an unthreaded one also moves
// it. threads reports a thread position written.
func (b *InProc) placeReceipt(ctx context.Context, roomID domain.RoomID, kind, eventID domain.EventID, ts int64) (pos readPos, threads bool) {
	switch kind {
	case heldUnthreaded, heldMain:
		if kind == heldUnthreaded {
			err := b.cache.SaveThreadRead(ctx, roomID, "", eventID, ts)
			b.warnIf(ctx, err, "cache thread read floor", "room", roomID, "event", eventID)
			threads = err == nil
		}
		b.warnIf(ctx, b.cache.SeedThreadFloor(ctx, roomID, eventID, ts), "seed thread read floor", "room", roomID, "event", eventID)
		return readPos{Event: eventID, TS: ts}, threads
	default:
		err := b.cache.SaveThreadRead(ctx, roomID, kind, eventID, ts)
		b.warnIf(ctx, err, "cache thread read", "room", roomID, "thread", kind, "event", eventID)
		return readPos{}, err == nil
	}
}

// applyThreadReceipt records one thread's position, or holds it until its event is
// cached: sync listeners run before the response's events, so a catch-up batch can
// carry a receipt for a reply not yet cached.
func (b *InProc) applyThreadReceipt(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID) bool {
	ts, ok := b.positionOf(ctx, roomID, eventID)
	if !ok {
		b.held.hold(roomID, root, eventID)
		return false
	}
	return b.cache.SaveThreadRead(ctx, roomID, root, eventID, ts) == nil
}

// settleReceipts retries a room's held receipts after a message was cached.
func (b *InProc) settleReceipts(ctx context.Context, roomID domain.RoomID) {
	waiting := b.held.in(roomID)

	var best readPos
	for _, h := range waiting {
		ts, ok := b.positionOf(ctx, roomID, h.Event)
		if !ok {
			continue
		}
		pos, _ := b.placeReceipt(ctx, roomID, h.Kind, h.Event, ts)
		if best.ahead(pos) {
			best = pos
		}
		b.held.drop(roomID, h)
	}
	if best.Event != "" {
		b.advanceRead(ctx, roomID, best)
	}
}

// positionOf is an event's cached timestamp; false when it is not cached.
func (b *InProc) positionOf(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (int64, bool) {
	ts, ok, err := b.cache.MessageTS(ctx, roomID, eventID)
	if err != nil {
		b.warnIf(ctx, err, "read cached event time", "room", roomID, "event", eventID)
		return 0, false
	}
	return ts, ok
}

// MarkThreadRead sends a threaded receipt and writes it through to the cache so the
// badge clears before the sync echo.
func (b *InProc) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, private bool) error {
	if err := b.client.SendReceipt(ctx, b.ids.nativeRoom(roomID), b.ids.nativeEvent(eventID), receiptType(private),
		&mautrix.ReqSendReceipt{ThreadID: matrixString(b.ids.nativeEvent(root))}); err != nil {
		return fmt.Errorf("matrix: mark thread read: %w", err)
	}
	if b.cache == nil {
		return nil
	}
	if ts, ok := b.positionOf(ctx, roomID, eventID); ok {
		b.warnIf(ctx, b.cache.SaveThreadRead(ctx, roomID, root, eventID, ts), "cache thread read", "room", roomID, "thread", root, "event", eventID)
		b.recount(ctx, roomID)
	}
	return nil
}

// ListThreads is a room's cached threads, newest activity first, with counts.
func (b *InProc) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	return fromCache(b, "list threads", func(c *db.Cache) ([]domain.Thread, error) {
		return c.Threads(ctx, b.me(), roomID)
	})
}

// addThreadCounts adds unread threads to a room's main-timeline counts. Rooms the
// cache cannot count keep the server's number.
func (b *InProc) addThreadCounts(ctx context.Context, u domain.Unread) domain.Unread {
	if b.cache == nil || !u.Counted {
		return u
	}
	threads, err := b.cache.CountThreadUnread(ctx, b.me(), u.RoomID)
	if err != nil {
		b.warnIf(ctx, err, "count thread unread locally", "room", u.RoomID)
		return u
	}
	return u.WithThreads(threads)
}

// sameThreadUnread reports whether two thread breakdowns match, order included.
func sameThreadUnread(a, b []domain.ThreadUnread) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Root != b[i].Root || a[i].Unread != b[i].Unread || a[i].Mentions != b[i].Mentions {
			return false
		}
	}
	return true
}

// ThreadParticipant reports whether we sent a thread's root or any reply in it.
func (b *InProc) ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool {
	if b.cache == nil || root == "" {
		return false
	}
	spoke, err := b.cache.SpokeInThread(ctx, roomID, root, b.me())
	if err != nil {
		b.warnIf(ctx, err, "read thread participation", "room", roomID, "thread", root)
		return false
	}
	return spoke
}

// threadPageSize is the default page of a thread's scrollback (same as a room's).
const threadPageSize = 50

// ThreadPage fetches a page of a thread's replies via /relations. The root is not a
// relation; callers fetch it with FetchEvent.
func (b *InProc) ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int) (domain.TimelinePage, error) {
	if limit <= 0 {
		limit = threadPageSize
	}
	resp, err := b.client.GetRelations(ctx, b.ids.nativeRoom(roomID), b.ids.nativeEvent(root), &mautrix.ReqGetRelations{
		RelationType: event.RelThread,
		Dir:          mautrix.DirectionBackward,
		From:         from,
		Limit:        limit,
	})
	if err != nil {
		return domain.TimelinePage{}, fmt.Errorf("matrix: thread page: %w", err)
	}
	// An empty chunk is the start of the thread.
	next := resp.NextBatch
	if len(resp.Chunk) == 0 {
		next = ""
	}
	return b.pageFrom(ctx, roomID, resp.Chunk, next), nil
}
