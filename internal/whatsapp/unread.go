package whatsapp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Unread is counted locally, as Matrix's is: the cache counts what came after a
// room's read position that is not ours. WhatsApp keeps no counts kith can ask for,
// so the position is kith's to keep: placed when a room is first seen (from
// history's unread count, or just before its first live message), and moved by
// reading, here or on any other of the account's devices.

// placed reports whether a room's read position is known, loading the set of placed
// rooms the first time.
func (a *Adapter) placed(ctx context.Context, room domain.RoomID) bool {
	a.mu.Lock()
	loaded := a.positions != nil
	a.mu.Unlock()
	if !loaded {
		a.loadPositions(ctx)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, known := a.positions[room]
	return known
}

// loadPositions reads the WhatsApp rooms' read positions.
func (a *Adapter) loadPositions(ctx context.Context) {
	positions := map[domain.RoomID]time.Time{}
	if read, err := a.cache.ReadPositions(ctx); err == nil {
		for room, ms := range read {
			if domain.NetworkOf(string(room)) == domain.ProtocolWhatsApp {
				positions[room] = time.UnixMilli(ms)
			}
		}
	} else {
		a.log.Warn("read unread positions failed", "err", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.positions == nil {
		a.positions = positions
	}
}

// placeRead gives a room a read position at at, unless it has one: a room first seen
// is read up to just before what made it known.
func (a *Adapter) placeRead(ctx context.Context, room domain.RoomID, at time.Time) {
	if a.placed(ctx, room) {
		return
	}
	a.readTo(ctx, room, "", at)
}

// readTo moves a room's read position forward to event (at its time); a read of
// something older — a receipt arriving late — leaves it where it is (the cache only
// moves a position forward).
func (a *Adapter) readTo(ctx context.Context, room domain.RoomID, event domain.EventID, at time.Time) {
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
	messages, mentions, counted, err := a.cache.CountUnread(ctx, a.Me(), room)
	if err != nil {
		a.log.Warn("count unread failed", "room", room, "err", err)
		return
	}
	emit(a, a.unread, domain.Unread{RoomID: room, Messages: messages, Mentions: mentions, Counted: counted})
}

// CachedUnread is every WhatsApp room's unread counts.
func (a *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	if a.cache == nil {
		return nil, nil
	}
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read unread state: %w", err)
	}
	rows = slices.DeleteFunc(rows, func(u domain.Unread) bool {
		return domain.NetworkOf(string(u.RoomID)) != domain.ProtocolWhatsApp
	})
	counts, err := a.cache.CountUnreadAll(ctx, a.Me())
	if err != nil {
		return nil, fmt.Errorf("whatsapp: count unread: %w", err)
	}
	for i := range rows {
		if c, ok := counts[rows[i].RoomID]; ok {
			rows[i].Messages, rows[i].Mentions, rows[i].Counted = c.Messages, c.Mentions, true
		}
	}
	return rows, nil
}

// onReceipt moves a room's read position when the account read it on another device.
// Others' receipts (delivered, read by them) are not kith's to show yet.
func (a *Adapter) onReceipt(ctx context.Context, account Account, e *events.Receipt) {
	if e.Type != types.ReceiptTypeReadSelf || len(e.MessageIDs) == 0 || a.cache == nil {
		return
	}
	info := types.MessageInfo{MessageSource: e.MessageSource}
	room := roomID(account.Digits, chatOf(ctx, &info, a.lookupFor(account)))
	last := domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account.Digits, e.MessageIDs[len(e.MessageIDs)-1]))
	a.readTo(ctx, room, last, a.timeOf(ctx, room, last, e.Timestamp))
	a.recount(ctx, room)
}

// timeOf is a cached message's time: a read position is "up to this message", so a
// message that arrived before the read but after this one stays unread. fallback
// when the message is not cached.
func (a *Adapter) timeOf(ctx context.Context, room domain.RoomID, event domain.EventID, fallback time.Time) time.Time {
	if msg, found, err := a.cache.MessageByID(ctx, room, event); err == nil && found && !msg.Timestamp.IsZero() {
		return msg.Timestamp
	}
	return fallback
}

// lookupFor is an account's LID lookup, or none when it is not connected.
func (a *Adapter) lookupFor(account Account) pnOf {
	if _, client, ok := a.clientFor(account.Digits); ok {
		return a.pnLookup(client)
	}
	return nil
}

// MarkRead marks a room read up to eventID: on WhatsApp (the sender sees it read,
// unless private, which tells only your own devices) and in kith's count.
func (a *Adapter) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	room := domain.ParseID(string(roomID))
	event := domain.ParseID(string(eventID))
	chat, err := types.ParseJID(room.Native)
	if err != nil {
		return fmt.Errorf("whatsapp: %s is not a chat: %w", roomID, err)
	}
	_, client, ok := a.clientFor(room.Account)
	if !ok {
		return fmt.Errorf("whatsapp: account %s is not connected: %w", room.Account, errNetworkOff)
	}
	var sender types.JID
	if chat.Server == types.GroupServer && a.cache != nil {
		// In a group, a receipt names whose message it is.
		if from, err := a.cache.SenderOf(ctx, roomID, eventID); err == nil {
			sender, _ = types.ParseJID(domain.ParseID(from).Native)
		}
	}
	kind := []types.ReceiptType{}
	if private {
		kind = append(kind, types.ReceiptTypeReadSelf)
	}
	now := time.Now()
	// A channel's posts are marked viewed by server ID, which kith does not keep: read
	// there is kith's own.
	if chat.Server != types.NewsletterServer {
		if err := client.MarkRead(ctx, []types.MessageID{event.Native}, now, chat, sender, kind...); err != nil {
			return fmt.Errorf("whatsapp: mark %s read: %w", roomID, err)
		}
	}
	if a.cache != nil {
		a.readTo(ctx, roomID, eventID, a.timeOf(ctx, roomID, eventID, now))
		a.recount(ctx, roomID)
	}
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
		return domain.ReadResult{}, fmt.Errorf("whatsapp: newest messages: %w", err)
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
