package meta

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/readstate"
)

// Reading is local first: what you read in kith is read here before Meta hears of it,
// and stays read if Meta never does. The read mark goes to Meta after; one that cannot
// go (offline, refused) waits and goes when the account connects again. Meta's own
// marks only ever move the cached position forward.

// receiptSpan bounds sending one read mark.
const receiptSpan = 30 * time.Second

var errNotAThread = errors.New("meta: not a Messenger room")

// MarkRead reads a room through eventID, here first, then on Meta. Meta not taking it
// is logged and retried; the read here stands. A read that cannot be kept here is an
// error, and Meta is not told.
func (a *Adapter) MarkRead(ctx context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAThread, room)
	}
	at, err := a.timeOf(ctx, room, event)
	if err != nil {
		return err
	}
	if err := a.readTo(ctx, room, at); err != nil {
		return err
	}
	a.recount(ctx, room)
	a.tellRead(ctx, self, thread, at.UnixMilli())
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
		return result, fmt.Errorf("meta: %w", err)
	}
	return result, nil
}

// errNoMarkUnread: marking a thread unread is not supported yet.
var errNoMarkUnread = errors.New("meta: marking a thread unread is not supported yet")

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
		return time.Time{}, fmt.Errorf("meta: %w", err)
	}
	return at, nil
}

// tellRead sends Meta a thread's read mark, owing it when it cannot go now.
func (a *Adapter) tellRead(ctx context.Context, self, thread, at int64) {
	a.reads.Add(roomID(self, thread), at)
	c := a.connOf(self)
	if c == nil {
		return // owed until the account connects
	}
	a.sendRead(context.WithoutCancel(ctx), c, thread, at)
}

// sendRead sends one read mark and settles it when Meta takes it: over the encrypted
// connection for an encrypted chat (owed until that connection is up), else as a
// Messenger task.
func (a *Adapter) sendRead(ctx context.Context, c *conn, thread, at int64) {
	c.applying.Lock()
	t := c.inbox.threads[thread]
	encrypted, group := t != nil && t.kind.IsWhatsApp(), t != nil && !t.direct()
	c.applying.Unlock()
	c.mu.Lock()
	client, wa := c.client, c.e2ee
	c.mu.Unlock()
	if client == nil || encrypted && wa == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, receiptSpan)
	defer cancel()
	err := a.guard("sending a read mark", func() error {
		if encrypted {
			return a.readEncrypted(ctx, c, wa, thread, at, group)
		}
		_, err := client.ExecuteTasks(ctx, &socket.ThreadMarkReadTask{ThreadId: thread, LastReadWatermarkTs: at, SyncGroup: 1})
		return err //nolint:wrapcheck // logged below with what it was for
	})
	if err != nil {
		a.log.Warn("Meta did not take a read mark; it is kept and sent again on reconnect",
			"account", c.account.Name, "err", err)
		return
	}
	a.reads.Settle(roomID(c.metaID, thread), at)
}

// sendOwed sends an account's owed read marks, on (re)connecting.
func (a *Adapter) sendOwed(c *conn) {
	for room, at := range a.reads.Take(itoa(c.metaID)) {
		a.reads.Add(room, at) // owed until Meta takes it
		if _, thread, ok := threadOf(room); ok {
			a.sendRead(context.Background(), c, thread, at)
		}
	}
}

// connOf is the connection of the account with Meta ID self, or nil.
func (a *Adapter) connOf(self int64) *conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.conns {
		if c.metaID == self {
			return c
		}
	}
	return nil
}
