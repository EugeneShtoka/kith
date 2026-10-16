package linkedin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/readstate"
)

// Reading is local first: what you read in kith is read here before LinkedIn hears of
// it, and stays read if LinkedIn never does. LinkedIn marks a whole conversation read,
// not a message; one mark that cannot go (offline, refused) waits and goes when the
// account connects again. LinkedIn's own read times only ever move the cached position
// forward.

// receiptSpan bounds sending one read mark.
const receiptSpan = 30 * time.Second

var errNotAConversation = errors.New("linkedin: not a LinkedIn room")

// MarkRead reads a room through event, here first, then on LinkedIn. LinkedIn not
// taking it is logged and retried; the read here stands. A read that cannot be kept
// here is an error, and LinkedIn is not told.
func (a *Adapter) MarkRead(ctx context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	self, thread, ok := threadOf(room)
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
	a.reads.Add(room, at.UnixMilli())
	if c := a.connOf(self); c != nil {
		a.sendRead(context.WithoutCancel(ctx), c, thread, at.UnixMilli())
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
		return result, fmt.Errorf("linkedin: %w", err)
	}
	return result, nil
}

// MarkRoomUnread marks a conversation unread on LinkedIn, then here; a mark LinkedIn
// does not take is an error and changes nothing. LinkedIn takes a mark away only by
// reading the conversation, so clearing one reads it through its newest message.
func (a *Adapter) MarkRoomUnread(ctx context.Context, room domain.RoomID, unread bool) error {
	if !unread {
		result, err := a.MarkRoomsRead(ctx, []domain.RoomID{room}, false)
		if err != nil {
			return err
		}
		if result.Failed > 0 {
			return fmt.Errorf("linkedin: clear the unread mark: %s", result.FirstError)
		}
		return nil
	}
	c, client, thread, err := a.client(room)
	if err != nil {
		return err
	}
	err = a.ask(ctx, "marking a conversation unread", func(ctx context.Context) error {
		_, markErr := client.MarkConversationUnread(ctx, conversationURN(c.profile, thread))
		return markErr //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("linkedin: mark %s unread: %w", room, err)
	}
	if a.cache != nil {
		if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room, Notifications: 1, Marked: true}, 0); err != nil {
			return fmt.Errorf("linkedin: keep the unread mark of %s: %w", room, err)
		}
	}
	a.recount(ctx, room)
	return nil
}

// timeOf is when a cached message was sent; now when it is not cached, so the room is
// read through everything it shows. A lookup that fails is an error, never now.
func (a *Adapter) timeOf(ctx context.Context, room domain.RoomID, event domain.EventID) (time.Time, error) {
	if a.cache == nil {
		return time.Now(), nil
	}
	at, err := readstate.TimeOf(ctx, a.cache, room, event, time.Now())
	if err != nil {
		return time.Time{}, fmt.Errorf("linkedin: %w", err)
	}
	return at, nil
}

// sendRead tells LinkedIn a conversation is read through at and settles that when
// LinkedIn takes it.
func (a *Adapter) sendRead(ctx context.Context, c *conn, thread string, at int64) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.withLog(ctx), receiptSpan)
	defer cancel()
	err := a.guard("sending a read mark", func() error {
		_, err := client.MarkConversationRead(ctx, conversationURN(c.profile, thread))
		return err //nolint:wrapcheck // logged below with what it was for
	})
	if err != nil {
		a.log.Warn("LinkedIn did not take a read mark; it is kept and sent again on reconnect",
			"account", c.account.Name, "err", err)
		return
	}
	a.reads.Settle(roomID(c.profile, thread), at)
}

// sendOwed sends an account's owed read marks, on (re)connecting.
func (a *Adapter) sendOwed(c *conn) {
	for room, at := range a.reads.Take(c.profile) {
		a.reads.Add(room, at) // owed until LinkedIn takes it
		if _, thread, ok := threadOf(room); ok {
			a.sendRead(context.Background(), c, thread, at)
		}
	}
}

// connOf is the connection of the account with profile self, or nil.
func (a *Adapter) connOf(self string) *conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.conns {
		if c.profile == self {
			return c
		}
	}
	return nil
}
