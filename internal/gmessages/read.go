package gmessages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/readstate"
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

// later reports whether mark a was made after b.
func later(a, b mark) bool { return a.at.After(b.at) }

var errNotAConversation = errors.New("gmessages: not a Google Messages room")

// MarkRead reads a room through event, here first, then on the phone. The phone not
// taking it is logged and retried; the read here stands. A read that cannot be kept
// here is an error, and the phone is not told.
func (a *Adapter) MarkRead(ctx context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	self, conversation, ok := conversationOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	at, err := a.timeOf(ctx, room, event)
	if err != nil {
		return err
	}
	if err := a.readTo(ctx, room, at); err != nil {
		return err
	}
	a.recount(ctx, room)
	message, ok := messageOf(event)
	if !ok {
		return nil // nothing the phone could be told is read
	}
	m := mark{message: message, at: at}
	a.reads.Add(room, m)
	if c := a.connOf(self); c != nil {
		a.sendRead(context.WithoutCancel(ctx), c, conversation, m)
	}
	return nil
}

// MarkRoomsRead reads each room through its newest cached message.
func (a *Adapter) MarkRoomsRead(ctx context.Context, rooms []domain.RoomID, _ bool) (domain.ReadResult, error) {
	if a.cache == nil {
		return domain.ReadResult{Skipped: len(rooms)}, nil
	}
	result, err := readstate.MarkRoomsRead(ctx, a.cache, rooms, func(room domain.RoomID, event domain.EventID) error {
		return a.MarkRead(ctx, room, event, false)
	})
	if err != nil {
		return result, fmt.Errorf("gmessages: %w", err)
	}
	return result, nil
}

// errNoMarkUnread: marking a conversation unread is not supported yet.
var errNoMarkUnread = errors.New("gmessages: marking a conversation unread is not supported yet")

// MarkRoomUnread is not supported yet.
func (a *Adapter) MarkRoomUnread(context.Context, domain.RoomID, bool) error { return errNoMarkUnread }

// timeOf is when a cached message was sent; now when it is not cached, so the room is
// read through everything it shows. A lookup that fails is an error, never now.
func (a *Adapter) timeOf(ctx context.Context, room domain.RoomID, event domain.EventID) (time.Time, error) {
	if a.cache == nil {
		return time.Now(), nil
	}
	at, err := readstate.TimeOf(ctx, a.cache, room, event, time.Now())
	if err != nil {
		return time.Time{}, fmt.Errorf("gmessages: %w", err)
	}
	return at, nil
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
	a.reads.Settle(roomID(c.self, conversation), m)
}

// sendOwed sends an account's owed read marks, on (re)connecting.
func (a *Adapter) sendOwed(ctx context.Context, c *conn) {
	for room, m := range a.reads.Take(c.self) {
		a.reads.Add(room, m) // owed until the phone takes it
		if _, conversation, ok := conversationOf(room); ok {
			a.sendRead(ctx, c, conversation, m)
		}
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
