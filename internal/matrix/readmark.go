package matrix

import (
	"context"
	"encoding/json"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Marking a room read: the user says the room is read, now. Two things follow.
//
// The receipt goes to the room's newest event in the server's stream order
// (streamNewest), not the newest cached message by time: bridges stamp messages with
// the remote network's clock, so the time-newest message can sit behind the
// server's position, and a receipt there is ignored without an echo. The cached
// message is the fallback when the server cannot say.
//
// The local position does not wait for the echo: once the receipt lands the room is
// read through the newest time the cache holds (readThrough), forward only. The
// newest time, not the receipted event's: neighboring events a bridge stamped out
// of order must not be left counted.

// MarkRead sends a main-timeline receipt (MSC3771 thread "main"; reading a room is
// not reading its threads) for the open room, whose newest shown event is eventID.
func (b *InProc) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	target, hint, err := b.sendRead(ctx, roomID, eventID, private, true)
	if err != nil {
		return fmt.Errorf("matrix: mark read: %w", err)
	}
	// Only once the receipt landed.
	b.clearMarked(ctx, roomID)
	b.readThrough(ctx, roomID, heldMain, target, hint)
	return nil
}

// MarkRoomsRead marks rooms read without opening them (no cached event: skipped).
// The receipt is unthreaded, which per spec clears the threads too. Sequential; a
// refusal is counted and the walk continues. ctx is the daemon's lifetime, so
// cancellation means shutdown.
func (b *InProc) MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error) {
	if b.cache == nil {
		return domain.ReadResult{}, fmt.Errorf("matrix: no cache to resolve the newest event of %d room(s)", len(roomIDs))
	}
	latest, err := b.cache.LatestEvents(ctx, roomIDs)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("matrix: mark rooms read: %w", err)
	}
	var result domain.ReadResult
	for _, roomID := range roomIDs {
		if ctx.Err() != nil {
			return result, fmt.Errorf("matrix: mark rooms read: %w", ctx.Err())
		}
		eventID, ok := latest[roomID]
		if !ok {
			result.Skipped++
			continue
		}
		target, hint, err := b.sendRead(ctx, roomID, eventID, private, false)
		if err != nil {
			// Counted for the report, and the reason kept: a count alone hid a
			// refused receipt body (M_BAD_JSON) in every room for hours.
			result.Failed++
			if result.FirstError == "" {
				result.FirstError = err.Error()
			}
			b.log().Warn("mark room read failed", "op", "mark room read", "room", roomID, "event", eventID, "err", err)
			continue
		}
		b.clearMarked(ctx, roomID)
		// Unthreaded: the thread floor moves with the room, now rather than on the
		// echo, or thread rows linger.
		b.readThrough(ctx, roomID, heldUnthreaded, target, hint)
		result.Marked++
	}
	return result, nil
}

// sendRead receipts the room's newest event in stream order, else fallback (the
// newest cached one). A main-timeline receipt (mainOnly) skips a threaded newest
// event, which is not on the main timeline, and a receipt the server refuses at the
// stream's newest is retried at fallback. It returns the event receipted and, when
// the server said, its time.
func (b *InProc) sendRead(ctx context.Context, roomID domain.RoomID, fallback domain.EventID, private, mainOnly bool) (domain.EventID, int64, error) {
	// Always a struct: a typed nil pointer here would marshal as the body `null`, which
	// the homeserver rejects (M_BAD_JSON); the empty struct marshals as {}.
	opts := &mautrix.ReqSendReceipt{}
	if mainOnly {
		opts.ThreadID = string(event.ReadReceiptThreadMain)
	}
	send := func(eventID domain.EventID) error {
		return b.client.SendReceipt(ctx, id.RoomID(roomID), id.EventID(eventID), receiptType(private), opts)
	}
	if newest, ok := b.streamNewest(ctx, roomID); ok && (!mainOnly || !inThread(newest)) {
		target := domain.EventID(newest.ID)
		err := send(target)
		if err == nil {
			return target, newest.Timestamp, nil
		}
		if target == fallback {
			return "", 0, err
		}
	}
	if err := send(fallback); err != nil {
		return "", 0, err
	}
	return fallback, 0, nil
}

// streamNewest is the room's newest event in the server's stream order, of any
// type: GET /messages, backwards, one event.
func (b *InProc) streamNewest(ctx context.Context, roomID domain.RoomID) (*event.Event, bool) {
	resp, err := b.client.Messages(ctx, id.RoomID(roomID), "", "", mautrix.DirectionBackward, nil, 1)
	if err != nil || resp == nil || len(resp.Chunk) == 0 || resp.Chunk[0] == nil || resp.Chunk[0].ID == "" {
		return nil, false
	}
	return resp.Chunk[0], true
}

// inThread reports whether an event is a thread reply, read from its m.relates_to
// (outside the ciphertext, so encrypted events answer too).
func inThread(evt *event.Event) bool {
	var c struct {
		RelatesTo struct {
			RelType event.RelationType `json:"rel_type"`
		} `json:"m.relates_to"`
	}
	raw := evt.Content.VeryRaw
	if len(raw) == 0 {
		return false
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return false // ignored: content that is not JSON relates to nothing
	}
	return c.RelatesTo.RelType == event.RelThread
}

// readThrough moves a room's position to eventID, just receipted, at the newest time
// the room knows: the cache's newest message or edit, eventID's own when cached, and
// hint (the server's time for it). Forward only; nothing known, nothing moves.
func (b *InProc) readThrough(ctx context.Context, roomID domain.RoomID, kind, eventID domain.EventID, hint int64) {
	if b.cache == nil || eventID == "" {
		return
	}
	ts := hint
	if own, ok := b.positionOf(ctx, roomID, eventID); ok {
		ts = max(ts, own)
	}
	if newest, err := b.cache.NewestTS(ctx, roomID); err == nil {
		ts = max(ts, newest)
	}
	if ts <= 0 {
		return
	}
	pos, threads := b.placeReceipt(ctx, roomID, kind, eventID, ts)
	b.advanceRead(ctx, roomID, pos)
	if threads {
		b.recount(ctx, roomID)
	}
}
