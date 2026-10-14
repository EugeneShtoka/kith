package gmessages

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reading is local first: what you read in kith is read here before the phone hears of
// it, and stays read if the phone never does. The phone takes a conversation read up to
// a message; a mark that cannot go (the phone offline) waits and goes when the account
// connects again. The phone's own read state only ever moves the cached position
// forward.

// receiptSpan bounds sending one read mark.
const receiptSpan = 30 * time.Second

// mark is a conversation read up to message, sent at.
type mark struct {
	message string
	at      time.Time
}

// owed is the read marks the phone has not taken yet, by account and conversation.
type owed struct {
	mu    sync.Mutex
	marks map[string]map[string]mark // self → conversation
}

// add owes m, unless a later mark is owed already.
func (o *owed) add(self, conversation string, m mark) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.marks == nil {
		o.marks = map[string]map[string]mark{}
	}
	if o.marks[self] == nil {
		o.marks[self] = map[string]mark{}
	}
	if old, ok := o.marks[self][conversation]; !ok || !old.at.After(m.at) {
		o.marks[self][conversation] = m
	}
}

// take removes and returns an account's owed marks.
func (o *owed) take(self string) map[string]mark {
	o.mu.Lock()
	defer o.mu.Unlock()
	marks := o.marks[self]
	delete(o.marks, self)
	return marks
}

// settle drops a mark the phone took, unless a later one was owed meanwhile.
func (o *owed) settle(self, conversation string, m mark) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if old, ok := o.marks[self][conversation]; ok && !old.at.After(m.at) {
		delete(o.marks[self], conversation)
	}
}

var errNotAConversation = errors.New("gmessages: not a Google Messages room")

// MarkRead reads a room through event, here first, then on the phone. The phone not
// taking it is logged and retried; the read here stands.
func (a *Adapter) MarkRead(ctx context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	self, conversation, ok := conversationOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	at := a.timeOf(ctx, room, event)
	a.readTo(ctx, room, at)
	a.recount(ctx, room)
	message, ok := messageOf(event)
	if !ok {
		return nil // nothing the phone could be told is read
	}
	m := mark{message: message, at: at}
	a.reads.add(self, conversation, m)
	if c := a.connOf(self); c != nil {
		a.sendRead(context.WithoutCancel(ctx), c, conversation, m)
	}
	return nil
}

// MarkRoomsRead reads each room through its newest cached message.
func (a *Adapter) MarkRoomsRead(ctx context.Context, rooms []domain.RoomID, _ bool) (domain.ReadResult, error) {
	var result domain.ReadResult
	if a.cache == nil {
		result.Skipped = len(rooms)
		return result, nil
	}
	latestEvents, err := a.cache.LatestEvents(ctx, rooms)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("gmessages: newest messages: %w", err)
	}
	for _, room := range rooms {
		event, ok := latestEvents[room]
		if !ok {
			result.Skipped++
			continue
		}
		if err := a.MarkRead(ctx, room, event, false); err != nil {
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

// errNoMarkUnread: marking a conversation unread is not supported yet.
var errNoMarkUnread = errors.New("gmessages: marking a conversation unread is not supported yet")

// MarkRoomUnread is not supported yet.
func (a *Adapter) MarkRoomUnread(context.Context, domain.RoomID, bool) error { return errNoMarkUnread }

// timeOf is when a cached message was sent; now when it is not cached, so the room is
// read through everything it shows.
func (a *Adapter) timeOf(ctx context.Context, room domain.RoomID, event domain.EventID) time.Time {
	if a.cache != nil && event != "" {
		if msg, ok, err := a.cache.MessageByID(ctx, room, event); err == nil && ok {
			return msg.Timestamp
		}
	}
	return time.Now()
}

// sendRead tells the phone a conversation is read and settles the mark when it takes it.
func (a *Adapter) sendRead(ctx context.Context, c *conn, conversation string, m mark) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, receiptSpan)
	defer cancel()
	err := a.guard("sending a read mark", func() error {
		return client.MarkRead(ctx, conversation, m.message)
	})
	if err != nil {
		a.log.Warn("the phone did not take a read mark; it is kept and sent again on reconnect",
			"account", c.account.Name, "err", err)
		return
	}
	a.reads.settle(c.self, conversation, m)
}

// sendOwed sends an account's owed read marks, on (re)connecting.
func (a *Adapter) sendOwed(ctx context.Context, c *conn) {
	for conversation, m := range a.reads.take(c.self) {
		a.reads.add(c.self, conversation, m) // owed until the phone takes it
		a.sendRead(ctx, c, conversation, m)
	}
}

// connOf is the connection of the account self, or nil.
func (a *Adapter) connOf(self string) *conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.conns {
		if c.self == self {
			return c
		}
	}
	return nil
}
