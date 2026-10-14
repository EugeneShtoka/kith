package meta

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reading is local first: what you read in kith is read here before Meta hears of it,
// and stays read if Meta never does. The read mark goes to Meta after; one that cannot
// go (offline, refused) waits and goes when the account connects again. Meta's own
// marks only ever move the cached position forward.

// receiptSpan bounds sending one read mark.
const receiptSpan = 30 * time.Second

// owed is the read marks Meta has not taken yet, by account Meta ID and thread.
type owed struct {
	mu    sync.Mutex
	marks map[int64]map[int64]int64 // self → thread → read through, ms
}

func (o *owed) add(self, thread, at int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.marks == nil {
		o.marks = map[int64]map[int64]int64{}
	}
	if o.marks[self] == nil {
		o.marks[self] = map[int64]int64{}
	}
	o.marks[self][thread] = max(o.marks[self][thread], at)
}

// take removes and returns an account's owed marks.
func (o *owed) take(self int64) map[int64]int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	marks := o.marks[self]
	delete(o.marks, self)
	return marks
}

// settle drops a mark Meta took, unless a later one was owed meanwhile.
func (o *owed) settle(self, thread, at int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.marks[self][thread] <= at {
		delete(o.marks[self], thread)
	}
}

var errNotAThread = errors.New("meta: not a Messenger room")

// MarkRead reads a room through eventID, here first, then on Meta. Meta not taking it
// is logged and retried; the read here stands.
func (a *Adapter) MarkRead(ctx context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAThread, room)
	}
	at := a.timeOf(ctx, room, event)
	a.readTo(ctx, room, at)
	a.recount(ctx, room)
	a.tellRead(ctx, self, thread, at.UnixMilli())
	return nil
}

// MarkRoomsRead reads each room through its newest cached message.
func (a *Adapter) MarkRoomsRead(ctx context.Context, rooms []domain.RoomID, _ bool) (domain.ReadResult, error) {
	var result domain.ReadResult
	if a.cache == nil {
		result.Skipped = len(rooms)
		return result, nil
	}
	latest, err := a.cache.LatestEvents(ctx, rooms)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("meta: newest messages: %w", err)
	}
	for _, room := range rooms {
		event, ok := latest[room]
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

// errNoMarkUnread: marking a thread unread is not supported yet.
var errNoMarkUnread = errors.New("meta: marking a thread unread is not supported yet")

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

// tellRead sends Meta a thread's read mark, owing it when it cannot go now.
func (a *Adapter) tellRead(ctx context.Context, self, thread, at int64) {
	a.reads.add(self, thread, at)
	c := a.connOf(self)
	if c == nil {
		return // owed until the account connects
	}
	a.sendRead(context.WithoutCancel(ctx), c, thread, at)
}

// sendRead sends one read mark and settles it when Meta takes it.
func (a *Adapter) sendRead(ctx context.Context, c *conn, thread, at int64) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, receiptSpan)
	defer cancel()
	err := a.guard("sending a read mark", func() error {
		_, err := client.ExecuteTasks(ctx, &socket.ThreadMarkReadTask{ThreadId: thread, LastReadWatermarkTs: at, SyncGroup: 1})
		return err //nolint:wrapcheck // logged below with what it was for
	})
	if err != nil {
		a.log.Warn("Meta did not take a read mark; it is kept and sent again on reconnect",
			"account", c.account.Name, "err", err)
		return
	}
	a.reads.settle(c.metaID, thread, at)
}

// sendOwed sends an account's owed read marks, on (re)connecting.
func (a *Adapter) sendOwed(c *conn) {
	for thread, at := range a.reads.take(c.metaID) {
		a.reads.add(c.metaID, thread, at) // owed until Meta takes it
		a.sendRead(context.Background(), c, thread, at)
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
