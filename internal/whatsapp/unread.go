package whatsapp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/readstate"
)

// Unread is counted locally from a read position kith keeps (WhatsApp exposes no
// counts). The position is placed when a room is first seen (from history's unread
// count, or just before its first live message) and moved by reads on any device.

// placed reports whether a room has a read position, loading positions on first use.
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

// placeRead sets a room's read position to at unless it already has one.
func (a *Adapter) placeRead(ctx context.Context, room domain.RoomID, at time.Time) {
	if a.placed(ctx, room) {
		return
	}
	if err := a.readTo(ctx, room, "", at); err != nil {
		a.log.Warn("save a read position failed", "err", err)
	}
}

// readTo moves a room's read position forward to event; a late receipt for an older
// message is ignored (the cache only moves forward).
func (a *Adapter) readTo(ctx context.Context, room domain.RoomID, event domain.EventID, at time.Time) error {
	if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room, ReadEvent: event}, at.UnixMilli()); err != nil {
		return fmt.Errorf("whatsapp: save the read position of %s: %w", room, err)
	}
	a.mu.Lock()
	if a.positions != nil && at.After(a.positions[room]) {
		a.positions[room] = at
	}
	a.mu.Unlock()
	return nil
}

func (a *Adapter) recount(ctx context.Context, room domain.RoomID) {
	messages, mentions, counted, err := a.cache.CountUnread(ctx, a.Me(), room)
	if err != nil {
		a.log.Warn("count unread failed", "room", room, "err", err)
		return
	}
	emit(a, a.unread, domain.Unread{RoomID: room, Messages: messages, Mentions: mentions, Counted: counted})
}

// RecountUnread counts rooms' unread again, when the cache counts them differently
// (who is hidden changed).
func (a *Adapter) RecountUnread(ctx context.Context, rooms []domain.RoomID) {
	if a.cache == nil {
		return
	}
	for _, room := range rooms {
		a.recount(ctx, room)
	}
}

// CachedUnread returns every WhatsApp room's unread counts.
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
// Others' receipts are ignored.
func (a *Adapter) onReceipt(ctx context.Context, account Account, e *events.Receipt) {
	if e.Type != types.ReceiptTypeReadSelf || len(e.MessageIDs) == 0 || a.cache == nil {
		return
	}
	info := types.MessageInfo{MessageSource: e.MessageSource}
	room := roomID(account.Digits, chatOf(ctx, &info, a.lookupFor(account)))
	last := domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account.Digits, e.MessageIDs[len(e.MessageIDs)-1]))
	// A read position is "up to this message", not "up to the receipt's time"; the
	// receipt's time is what your phone read through, so it stands in for one not found.
	at, err := readstate.TimeOf(ctx, a.cache, room, last, e.Timestamp)
	if err != nil {
		at = e.Timestamp
	}
	if err := a.readTo(ctx, room, last, at); err != nil {
		a.log.Warn("save a read position failed", "account", account.Name, "err", err)
		return
	}
	a.recount(ctx, room)
}

// lookupFor is an account's LID lookup, nil when not connected.
func (a *Adapter) lookupFor(account Account) pnOf {
	if _, client, ok := a.clientFor(account.Digits); ok {
		return a.pnLookup(account, client)
	}
	return nil
}

// MarkRead marks a room read up to eventID here, then on WhatsApp; private tells only
// the account's own devices. Read here first: WhatsApp not taking the receipt
// (offline, refused) leaves it read here, and the receipt is owed and sent again when
// the account connects. The read position only moves forward. A read that cannot be
// kept here is an error, and WhatsApp is not told.
func (a *Adapter) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	roomID = a.filedUnder(ctx, roomID) // a window still on a chat since merged
	room := domain.ParseID(string(roomID))
	chat, err := types.ParseJID(room.Native)
	if err != nil {
		return fmt.Errorf("whatsapp: %s is not a chat: %w", roomID, err)
	}
	if a.cache != nil {
		at, err := readstate.TimeOf(ctx, a.cache, roomID, eventID, time.Now())
		if err != nil {
			return fmt.Errorf("whatsapp: %w", err)
		}
		if err := a.readTo(ctx, roomID, eventID, at); err != nil {
			return err
		}
		a.recount(ctx, roomID)
	}
	// Channel posts are marked viewed by server ID, which kith does not keep: local only.
	if chat.Server == types.NewsletterServer {
		return nil
	}
	m := receipt{event: eventID, private: private, at: time.Now()}
	a.reads.Add(roomID, m)
	a.sendRead(ctx, roomID, m)
	return nil
}

// receipt is a read mark: the message read through, and whether only your devices hear.
type receipt struct {
	event   domain.EventID
	private bool
	at      time.Time
}

// sendRead sends a read receipt and settles it when WhatsApp takes it; one it does not
// take (or an account not connected) stays owed.
func (a *Adapter) sendRead(ctx context.Context, roomID domain.RoomID, m receipt) {
	room := domain.ParseID(string(roomID))
	chat, err := types.ParseJID(room.Native)
	if err != nil {
		return
	}
	_, client, ok := a.clientFor(room.Account)
	if !ok {
		return // not connected: owed until it is
	}
	var sender types.JID
	if chat.Server == types.GroupServer && a.cache != nil {
		// In a group, a receipt names the message's sender.
		if from, err := a.cache.SenderOf(ctx, roomID, m.event); err == nil {
			sender, _ = types.ParseJID(domain.ParseID(from).Native)
		}
	}
	kind := []types.ReceiptType{}
	if m.private {
		kind = append(kind, types.ReceiptTypeReadSelf)
	}
	if err := client.MarkRead(ctx, []types.MessageID{domain.ParseID(string(m.event)).Native}, m.at, chat, sender, kind...); err != nil {
		a.log.Warn("WhatsApp did not take a read receipt; it is kept and sent again on reconnect", "room", roomID, "err", err)
		return
	}
	a.reads.Settle(roomID, m)
}

// sendOwed sends an account's owed read receipts, on (re)connecting.
func (a *Adapter) sendOwed(ctx context.Context, account Account) {
	for room, m := range a.reads.Take(account.Digits) {
		a.reads.Add(room, m) // owed until WhatsApp takes it
		a.sendRead(ctx, room, m)
	}
}

// laterReceipt reports whether receipt a was made after b.
func laterReceipt(a, b receipt) bool { return a.at.After(b.at) }

// MarkRoomsRead marks each room read up to its newest cached message.
func (a *Adapter) MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error) {
	if a.cache == nil {
		return domain.ReadResult{Skipped: len(roomIDs)}, nil
	}
	result, err := readstate.MarkRoomsRead(ctx, a.cache, roomIDs, func(room domain.RoomID, event domain.EventID) error {
		return a.MarkRead(ctx, room, event, private)
	})
	if err != nil {
		return result, fmt.Errorf("whatsapp: %w", err)
	}
	return result, nil
}
