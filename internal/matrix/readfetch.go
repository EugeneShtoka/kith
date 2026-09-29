package matrix

import (
	"context"
	"slices"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Read positions on events the cache does not hold. A receipt can point at an event
// older than the cached window, or at one that is not a message at all (a bridge's
// state event), and then its room cannot be counted locally (the badge falls back to
// the server's notification count) and gets no thread floor. Such an event is
// resolved once, in the background, from the server's stream order (resolveReceipt);
// a failed ask leaves things as they were.
//
// The event's own time is not its position: bridges stamp messages with the remote
// network's clock, so a state event can carry an earlier origin_server_ts than a
// message the server ordered before it. The position is the newest message at or
// before the receipt in stream order, by that message's time.

// readFetchWorkers caps concurrent event fetches: a cold start can ask for one per
// room.
const readFetchWorkers = 4

// fetchKey is one event asked for.
type fetchKey struct {
	room  domain.RoomID
	event domain.EventID
}

// fetchHeld fetches the held receipts of earlier syncs. Called as a sync begins: by
// then the previous response's events have all been cached, so whatever is still
// held is not arriving on its own. Held receipts are stamped from here on with the
// new generation.
func (b *InProc) fetchHeld(ctx context.Context) {
	for _, j := range b.held.nextSync() {
		b.fetchPosition(ctx, j.room, j.held)
	}
}

// fetchPosition learns where a receipt reads its room up to, in the background, and
// places it there. Each event is asked for once per run; a failure is final.
func (b *InProc) fetchPosition(ctx context.Context, roomID domain.RoomID, h heldReceipt) {
	if b.cache == nil || b.client == nil || h.Event == "" {
		return
	}
	slots, first := b.fetches.first(fetchKey{room: roomID, event: h.Event})
	if !first {
		return
	}
	b.fetches.wg.Go(func() {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-slots }()
		at, ok := b.resolveReceipt(ctx, roomID, h.Kind, h.Event)
		if !ok {
			return
		}
		pos, threads := b.placeReceipt(ctx, roomID, h.Kind, at.Event, at.TS)
		b.held.drop(roomID, h)
		if pos.Event != "" {
			b.advanceRead(ctx, roomID, pos)
			return
		}
		if threads {
			b.recount(ctx, roomID)
		}
	})
}

// timelineTypes are the event types the cache holds as messages, as the server
// sees them (encrypted ones before decryption).
var timelineTypes = []event.Type{event.EventMessage, event.EventSticker, event.EventEncrypted}

// contextLimit is the /context window: the server splits it around the event, and
// one message each side is all resolveReceipt reads.
const contextLimit = 4

// isTimeline reports whether an event is one the cache would hold as a message.
func isTimeline(evt *event.Event) bool {
	if evt == nil {
		return false
	}
	return slices.Contains(timelineTypes, evt.Type)
}

// firstTimeline is the first message in a /context run (nearest the event first).
func firstTimeline(events []*event.Event) *event.Event {
	for _, evt := range events {
		if isTimeline(evt) {
			return evt
		}
	}
	return nil
}

// resolveReceipt is where a receipt on an uncached event reads a room up to, asked
// of the server's /context. A thread receipt keeps its event and time (threads are
// counted per reply). The room's newest cached message is read before the request:
// anything cached by then is on the server, so "nothing after the receipt" covers it.
func (b *InProc) resolveReceipt(ctx context.Context, roomID domain.RoomID, kind, eventID domain.EventID) (readPos, bool) {
	if ts, ok := b.positionOf(ctx, roomID, eventID); ok {
		return readPos{Event: eventID, TS: ts}, true
	}
	newest := b.newestCached(ctx, roomID)
	resp, err := b.client.Context(ctx, id.RoomID(roomID), id.EventID(eventID),
		&mautrix.FilterPart{Types: timelineTypes, LazyLoadMembers: true}, contextLimit)
	if err != nil || resp == nil || resp.Event == nil || resp.Event.Timestamp <= 0 {
		// Tried once per receipt (receiptFetches); the room then counts from the server.
		b.warnIf(ctx, err, "fetch a receipt's event", "room", roomID, "event", eventID)
		return readPos{}, false
	}
	if kind != heldUnthreaded && kind != heldMain {
		return readPos{Event: eventID, TS: resp.Event.Timestamp}, true
	}
	return streamPosition(resp.Event, resp.EventsBefore, resp.EventsAfter, newest)
}

// streamPosition places a room receipt by stream order: at the newest message at or
// before it (the receipt's own event, else the nearest before), by that message's
// time. With no message after it, the room is read through: the position is also
// at least the receipt's time and the newest cached message's (newest). A receipt
// before every message sits just before the first. ok is false when the server's
// answer cannot say (it ignored the filter and showed no message on either side
// that settles it).
func streamPosition(receipt *event.Event, before, after []*event.Event, newest readPos) (readPos, bool) {
	var anchor readPos
	if isTimeline(receipt) {
		anchor = readPos{Event: domain.EventID(receipt.ID), TS: receipt.Timestamp}
	} else if m := firstTimeline(before); m != nil {
		anchor = readPos{Event: domain.EventID(m.ID), TS: m.Timestamp}
	}
	if len(after) > 0 {
		if anchor.Event != "" {
			return anchor, true
		}
		next := firstTimeline(after)
		if next == nil {
			return readPos{}, false
		}
		return readPos{Event: domain.EventID(receipt.ID), TS: min(receipt.Timestamp, next.Timestamp-1)}, true
	}
	pos := anchor
	if newest.Event != "" && newest.TS >= pos.TS {
		pos = newest
	}
	if pos.Event == "" {
		pos.Event = domain.EventID(receipt.ID)
	}
	pos.TS = max(pos.TS, receipt.Timestamp)
	return pos, true
}

// newestCached is a room's newest cached message by time; zero when none.
func (b *InProc) newestCached(ctx context.Context, roomID domain.RoomID) readPos {
	latest, err := b.cache.LatestEvents(ctx, []domain.RoomID{roomID})
	if err != nil {
		b.warnIf(ctx, err, "read newest cached event", "room", roomID)
		return readPos{}
	}
	eventID, ok := latest[roomID]
	if !ok {
		return readPos{}
	}
	ts, ok := b.positionOf(ctx, roomID, eventID)
	if !ok {
		return readPos{}
	}
	return readPos{Event: eventID, TS: ts}
}

// advanceRead moves a room's read position forward (never back), persists it, and
// recounts, publishing the room even when only the marker moved.
func (b *InProc) advanceRead(ctx context.Context, roomID domain.RoomID, pos readPos) {
	_, moved := b.unread.update(roomID, func(cur *domain.Unread, ts *int64) bool {
		if !(readPos{Event: cur.ReadEvent, TS: *ts}).ahead(pos) {
			return false
		}
		cur.RoomID, cur.ReadEvent = roomID, pos.Event
		*ts = pos.TS
		b.saveUnread(ctx, *cur, *ts)
		return true
	})
	if moved {
		b.recountAndEmit(ctx, roomID, true)
	}
}

// saveUnread persists a room's unread state and read time. Called from an unreadBook
// change, under its lock. Best-effort.
func (b *InProc) saveUnread(ctx context.Context, u domain.Unread, readTS int64) {
	if b.cache == nil {
		return
	}
	b.warnIf(ctx, b.cache.SaveUnread(ctx, u, readTS), "cache unread state", "room", u.RoomID)
}

// fetchUnplaced resolves each cached read event that has no position, so its room
// can be counted locally. Called at startup with the cached state. A placed
// position is resolved again while its event is not a cached message and the room
// still counts unread: an earlier build placed such events by their own time, which
// a bridge's clock can put after messages the server ordered before them. Rooms
// counting nothing are left alone: a position only moves forward, so asking could
// not lower their count.
func (b *InProc) fetchUnplaced(ctx context.Context, cached []domain.Unread, placed map[domain.RoomID]int64) {
	for _, u := range cached {
		if u.ReadEvent == "" {
			continue
		}
		if _, ok := placed[u.RoomID]; ok && !b.worthResolving(ctx, u) {
			continue
		}
		b.fetchPosition(ctx, u.RoomID, heldReceipt{Kind: heldMain, Event: u.ReadEvent})
	}
}

// worthResolving reports whether a placed position may be ahead of where it sits:
// its event is not a cached message and the room counts something unread.
func (b *InProc) worthResolving(ctx context.Context, u domain.Unread) bool {
	if !u.Counted || u.Messages == 0 {
		return false
	}
	_, cachedMsg := b.positionOf(ctx, u.RoomID, u.ReadEvent)
	return !cachedMsg
}
