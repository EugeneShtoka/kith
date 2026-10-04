package slack

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Unread is counted locally, as the other networks' is: the cache counts what came
// after a room's read position that is not ours. The position is Slack's: last_read
// from client.counts as a workspace catches up, moved when the conversation is read
// here or on another client (channel_marked and its kin). A room Slack gave no
// position for is read up to just before what made it known.

// placed reports whether a room's read position is known, loading them the first time.
func (a *Adapter) placed(ctx context.Context, room domain.RoomID) bool {
	a.mu.Lock()
	loaded := a.positions != nil
	a.mu.Unlock()
	if !loaded {
		positions := map[domain.RoomID]time.Time{}
		if read, err := a.cache.ReadPositions(ctx); err == nil {
			for r, ms := range read {
				if domain.NetworkOf(string(r)) == domain.ProtocolSlack {
					positions[r] = time.UnixMilli(ms)
				}
			}
		} else {
			a.log.Warn("read unread positions failed", "err", err)
		}
		a.mu.Lock()
		if a.positions == nil {
			a.positions = positions
		}
		a.mu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, known := a.positions[room]
	return known
}

// placeRead gives a room a read position at at, unless it has one.
func (a *Adapter) placeRead(ctx context.Context, room domain.RoomID, at time.Time) {
	if a.cache == nil || a.placed(ctx, room) {
		return
	}
	a.readTo(ctx, room, "", at)
}

// readTo moves a room's read position forward to event (at its time); an older one
// leaves it (the cache moves a position only forward).
func (a *Adapter) readTo(ctx context.Context, room domain.RoomID, event domain.EventID, at time.Time) {
	if a.cache == nil {
		return
	}
	if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room, ReadEvent: event}, at.UnixMilli()); err != nil {
		a.log.Warn("save a read position failed", "room", room, "err", err)
		return
	}
	a.mu.Lock()
	if a.positions != nil && at.After(a.positions[room]) {
		a.positions[room] = at
	}
	a.mu.Unlock()
}

// recount streams a room's unread counts.
func (a *Adapter) recount(ctx context.Context, room domain.RoomID) {
	if a.cache == nil {
		return
	}
	messages, mentions, counted, err := a.cache.CountUnread(ctx, a.Me(), room)
	if err != nil {
		a.log.Warn("count unread failed", "room", room, "err", err)
		return
	}
	emit(a, a.unread, domain.Unread{RoomID: room, Messages: messages, Mentions: mentions, Counted: counted})
}

// onMarked moves a conversation's read position to ts, read on this or another client.
func (a *Adapter) onMarked(ctx context.Context, w *workspace, channel, ts string) {
	if channel == "" || ts == "" {
		return
	}
	room := roomID(w.creds.Team, channel)
	a.readTo(ctx, room, messageID(w.creds.Team, channel, ts), tsTime(ts))
	a.recount(ctx, room)
}

// CachedUnread is every Slack room's unread counts.
func (a *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	if a.cache == nil {
		return nil, nil
	}
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack: read unread state: %w", err)
	}
	rows = slices.DeleteFunc(rows, func(u domain.Unread) bool {
		return domain.NetworkOf(string(u.RoomID)) != domain.ProtocolSlack
	})
	counts, err := a.cache.CountUnreadAll(ctx, a.Me())
	if err != nil {
		return nil, fmt.Errorf("slack: count unread: %w", err)
	}
	for i := range rows {
		if c, ok := counts[rows[i].RoomID]; ok {
			rows[i].Messages, rows[i].Mentions, rows[i].Counted = c.Messages, c.Mentions, true
		}
	}
	return rows, nil
}

// MarkRead marks a conversation read up to eventID, on Slack (every client of yours
// follows) and in kith's count. Slack shows nobody else how far you read, so private
// changes nothing.
func (a *Adapter) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, _ bool) error {
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return err
	}
	in, ts, ok := cutLast(domain.ParseID(string(eventID)).Native)
	if !ok || in != channel {
		return fmt.Errorf("slack: %s is not a message of %s", eventID, roomID)
	}
	if err := w.client.MarkConversationContext(ctx, channel, ts); err != nil {
		return fmt.Errorf("slack: mark %s read: %w", roomID, err)
	}
	a.readTo(ctx, roomID, eventID, tsTime(ts))
	a.recount(ctx, roomID)
	return nil
}

// MarkRoomsRead marks each room read up to its newest cached message.
func (a *Adapter) MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error) {
	var result domain.ReadResult
	if a.cache == nil {
		result.Skipped = len(roomIDs)
		return result, nil
	}
	latest, err := a.cache.LatestEvents(ctx, roomIDs)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("slack: newest messages: %w", err)
	}
	for _, room := range roomIDs {
		event, ok := latest[room]
		if !ok {
			result.Skipped++
			continue
		}
		if err := a.MarkRead(ctx, room, event, private); err != nil {
			result.Failed++
			if result.FirstError == "" {
				result.FirstError = err.Error()
			}
			continue
		}
		result.Marked++
	}
	return result, nil
}
