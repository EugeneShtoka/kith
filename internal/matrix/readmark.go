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

// The read receipt goes to the newest event in server stream order, not the newest
// cached message by time: bridges stamp the remote clock, and a receipt behind the
// server's position is ignored without an echo. Locally the room is then read through
// the newest cached time (not the receipted event's), without waiting for the echo.

// MarkRead sends a main-timeline receipt (MSC3771 thread "main"; reading a room is
// not reading its threads) for the open room, whose newest shown event is eventID.
func (b *InProc) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	target, hint, err := b.sendRead(ctx, roomID, eventID, private, true)
	if err != nil {
		return fmt.Errorf("matrix: mark read: %w", err)
	}
	b.clearMarked(ctx, roomID)
	b.readThrough(ctx, roomID, heldMain, target, hint)
	return nil
}

// MarkRoomsRead marks rooms read without opening them, skipping rooms with no cached
// event. The receipt is unthreaded, which per spec clears threads too; a refusal is
// counted and the walk continues.
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
			// Keep the first reason: a bare count hides a systematic refusal.
			result.Failed++
			if result.FirstError == "" {
				result.FirstError = err.Error()
			}
			b.log().Warn("mark room read failed", "op", "mark room read", "room", roomID, "event", eventID, "err", err)
			continue
		}
		b.clearMarked(ctx, roomID)
		// Unthreaded: move the thread floor now, not on the echo, or thread rows linger.
		b.readThrough(ctx, roomID, heldUnthreaded, target, hint)
		result.Marked++
	}
	return result, nil
}

// sendRead receipts the room's newest event in stream order, else fallback (newest
// cached). mainOnly skips a threaded newest event; a refusal there retries at
// fallback. It returns the receipted event and, when known, its server time.
func (b *InProc) sendRead(ctx context.Context, roomID domain.RoomID, fallback domain.EventID, private, mainOnly bool) (domain.EventID, int64, error) {
	// Never nil: a nil body marshals as `null`, which the server rejects (M_BAD_JSON).
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

// streamNewest is the room's newest event of any type in server stream order.
func (b *InProc) streamNewest(ctx context.Context, roomID domain.RoomID) (*event.Event, bool) {
	resp, err := b.client.Messages(ctx, id.RoomID(roomID), "", "", mautrix.DirectionBackward, nil, 1)
	if err != nil || resp == nil || len(resp.Chunk) == 0 || resp.Chunk[0] == nil || resp.Chunk[0].ID == "" {
		return nil, false
	}
	return resp.Chunk[0], true
}

// inThread reports whether an event is a thread reply, from its m.relates_to (which is
// outside the ciphertext).
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
		return false
	}
	return c.RelatesTo.RelType == event.RelThread
}

// readThrough moves a room's position to the just-receipted eventID at the newest time
// known (cache, eventID's own, or hint from the server). Forward only.
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
