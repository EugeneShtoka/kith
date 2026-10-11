package matrix

import (
	"context"
	"slices"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Receipts on uncached events (older than the cache, or a bridge's state event) are
// resolved once in the background from the server's stream order; until then the
// room uses the server's count. The position is the newest message at or before the
// receipt in stream order, by that message's time: bridges stamp events with the
// remote clock, so the event's own time is not its position.

// readFetchWorkers caps concurrent event fetches: a cold start can ask for one per
// room.
const readFetchWorkers = 4

// fetchKey is one event asked for.
type fetchKey struct {
	room  domain.RoomID
	event domain.EventID
}

// fetchHeld fetches receipts still held from earlier syncs. Called as a sync begins,
// when the previous response's events are all cached, so these won't arrive alone.
func (b *InProc) fetchHeld(ctx context.Context) {
	for _, j := range b.held.nextSync() {
		b.fetchPosition(ctx, j.room, j.held)
	}
}

// fetchPosition resolves and places a receipt in the background. Each event is asked
// for once per run; a failure is final.
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

// contextLimit is the /context window, split around the event; resolveReceipt needs
// one message each side.
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

// resolveReceipt asks /context where a receipt on an uncached event reads up to. A
// thread receipt keeps its event and time. The newest cached message is read before
// the request, so "nothing after the receipt" covers it.
func (b *InProc) resolveReceipt(ctx context.Context, roomID domain.RoomID, kind, eventID domain.EventID) (readPos, bool) {
	if ts, ok := b.positionOf(ctx, roomID, eventID); ok {
		return readPos{Event: eventID, TS: ts}, true
	}
	newest := b.newestCached(ctx, roomID)
	resp, err := b.client.Context(ctx, id.RoomID(roomID), id.EventID(eventID),
		&mautrix.FilterPart{Types: timelineTypes, LazyLoadMembers: true}, contextLimit)
	if err != nil || resp == nil || resp.Event == nil || resp.Event.Timestamp <= 0 {
		// Tried once per receipt; the room then counts from the server.
		b.warnIf(ctx, err, "fetch a receipt's event", "room", roomID, "event", eventID)
		return readPos{}, false
	}
	if kind != heldUnthreaded && kind != heldMain {
		return readPos{Event: eventID, TS: resp.Event.Timestamp}, true
	}
	return streamPosition(resp.Event, resp.EventsBefore, resp.EventsAfter, newest)
}

// streamPosition places a room receipt at the newest message at or before it, by that
// message's time. With nothing after it the room is read through (at least the
// receipt's time and newest). A receipt before every message sits just before the
// first. ok is false when the answer settles nothing (the server ignored the filter).
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

// saveUnread persists a room's unread state and read time, best-effort. Called under
// the unreadBook lock.
func (b *InProc) saveUnread(ctx context.Context, u domain.Unread, readTS int64) {
	if b.cache == nil {
		return
	}
	b.warnIf(ctx, b.cache.SaveUnread(ctx, u, readTS), "cache unread state", "room", u.RoomID)
}

// fetchUnplaced resolves, at startup, each cached read event without a position. A
// placed one is re-resolved while its event is uncached and the room counts unread,
// since it may have been placed by a bridge's clock. Rooms counting nothing are
// skipped: a position only moves forward.
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

// worthResolving reports whether a placed position's event is uncached while the room
// counts something unread.
func (b *InProc) worthResolving(ctx context.Context, u domain.Unread) bool {
	if !u.Counted || u.Messages == 0 {
		return false
	}
	_, cachedMsg := b.positionOf(ctx, u.RoomID, u.ReadEvent)
	return !cachedMsg
}
