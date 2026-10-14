package whatsapp

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
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
	a.readTo(ctx, room, "", at)
}

// readTo moves a room's read position forward to event; a late receipt for an older
// message is ignored (the cache only moves forward).
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

func (a *Adapter) recount(ctx context.Context, room domain.RoomID) {
	messages, mentions, counted, err := a.cache.CountUnread(ctx, a.Me(), room)
	if err != nil {
		a.log.Warn("count unread failed", "room", room, "err", err)
		return
	}
	emit(a, a.unread, domain.Unread{RoomID: room, Messages: messages, Mentions: mentions, Counted: counted})
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
	a.readTo(ctx, room, last, a.timeOf(ctx, room, last, e.Timestamp))
	a.recount(ctx, room)
}

// timeOf is a cached message's time, or fallback when not cached. A read position is
// "up to this message", not "up to the receipt's time".
func (a *Adapter) timeOf(ctx context.Context, room domain.RoomID, event domain.EventID, fallback time.Time) time.Time {
	if msg, found, err := a.cache.MessageByID(ctx, room, event); err == nil && found && !msg.Timestamp.IsZero() {
		return msg.Timestamp
	}
	return fallback
}

// lookupFor is an account's LID lookup, nil when not connected.
func (a *Adapter) lookupFor(account Account) pnOf {
	if _, client, ok := a.clientFor(account.Digits); ok {
		return a.pnLookup(client)
	}
	return nil
}

// MarkRead marks a room read up to eventID here, then on WhatsApp; private tells only
// the account's own devices. Read here first: WhatsApp not taking the receipt
// (offline, refused) leaves it read here, and the receipt is owed and sent again when
// the account connects. The read position only moves forward.
func (a *Adapter) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	room := domain.ParseID(string(roomID))
	chat, err := types.ParseJID(room.Native)
	if err != nil {
		return fmt.Errorf("whatsapp: %s is not a chat: %w", roomID, err)
	}
	if a.cache != nil {
		a.readTo(ctx, roomID, eventID, a.timeOf(ctx, roomID, eventID, time.Now()))
		a.recount(ctx, roomID)
	}
	// Channel posts are marked viewed by server ID, which kith does not keep: local only.
	if chat.Server == types.NewsletterServer {
		return nil
	}
	m := receipt{event: eventID, private: private, at: time.Now()}
	a.reads.add(roomID, m)
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
	a.reads.settle(roomID, m)
}

// sendOwed sends an account's owed read receipts, on (re)connecting.
func (a *Adapter) sendOwed(ctx context.Context, account Account) {
	for room, m := range a.reads.take(account.Digits) {
		a.reads.add(room, m) // owed until WhatsApp takes it
		a.sendRead(ctx, room, m)
	}
}

// owed is the read receipts WhatsApp has not taken yet, by room: the latest.
type owed struct {
	mu    sync.Mutex
	marks map[domain.RoomID]receipt
}

// add owes m, unless a later receipt is owed already.
func (o *owed) add(room domain.RoomID, m receipt) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.marks == nil {
		o.marks = map[domain.RoomID]receipt{}
	}
	if old, ok := o.marks[room]; !ok || !old.at.After(m.at) {
		o.marks[room] = m
	}
}

// take removes and returns the receipts owed for rooms of account (its phone digits).
func (o *owed) take(account string) map[domain.RoomID]receipt {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := map[domain.RoomID]receipt{}
	for room, m := range o.marks {
		if domain.ParseID(string(room)).Account == account {
			out[room] = m
			delete(o.marks, room)
		}
	}
	return out
}

// settle drops a receipt WhatsApp took, unless a later one was owed meanwhile.
func (o *owed) settle(room domain.RoomID, m receipt) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if old, ok := o.marks[room]; ok && !old.at.After(m.at) {
		delete(o.marks, room)
	}
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
