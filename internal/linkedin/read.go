package linkedin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reading is local first: what you read in kith is read here before LinkedIn hears of
// it, and stays read if LinkedIn never does. LinkedIn marks a whole conversation read,
// not a message; one mark that cannot go (offline, refused) waits and goes when the
// account connects again. LinkedIn's own read times only ever move the cached position
// forward.

// receiptSpan bounds sending one read mark.
const receiptSpan = 30 * time.Second

// owed is the conversations LinkedIn has not heard are read, by account and thread.
type owed struct {
	mu    sync.Mutex
	marks map[string]map[string]bool // self → thread
}

func (o *owed) add(self, thread string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.marks == nil {
		o.marks = map[string]map[string]bool{}
	}
	if o.marks[self] == nil {
		o.marks[self] = map[string]bool{}
	}
	o.marks[self][thread] = true
}

// take removes and returns an account's owed marks.
func (o *owed) take(self string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	threads := make([]string, 0, len(o.marks[self]))
	for thread := range o.marks[self] {
		threads = append(threads, thread)
	}
	delete(o.marks, self)
	return threads
}

// settle drops a mark LinkedIn took.
func (o *owed) settle(self, thread string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.marks[self], thread)
}

var errNotAConversation = errors.New("linkedin: not a LinkedIn room")

// MarkRead reads a room through event, here first, then on LinkedIn. LinkedIn not
// taking it is logged and retried; the read here stands.
func (a *Adapter) MarkRead(ctx context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	a.readTo(ctx, room, a.timeOf(ctx, room, event))
	a.recount(ctx, room)
	a.reads.add(self, thread)
	if c := a.connOf(self); c != nil {
		a.sendRead(context.WithoutCancel(ctx), c, thread)
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
	latest, err := a.cache.LatestEvents(ctx, rooms)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("linkedin: newest messages: %w", err)
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

// errNoMarkUnread: marking a conversation unread is not supported yet.
var errNoMarkUnread = errors.New("linkedin: marking a conversation unread is not supported yet")

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

// sendRead tells LinkedIn a conversation is read and settles it when LinkedIn takes it.
func (a *Adapter) sendRead(ctx context.Context, c *conn, thread string) {
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
	a.reads.settle(c.profile, thread)
}

// sendOwed sends an account's owed read marks, on (re)connecting.
func (a *Adapter) sendOwed(c *conn) {
	for _, thread := range a.reads.take(c.profile) {
		a.reads.add(c.profile, thread) // owed until LinkedIn takes it
		a.sendRead(context.Background(), c, thread)
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
