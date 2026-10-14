package meta

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Older messages are asked for a page at a time from the oldest one cached. Meta
// answers on the socket, not to the request: the page arrives as any table does, is
// cached by take, and wakes the waiting request through its thread's range. Encrypted
// chats have no history on Meta's side; their pages are what the cache holds.

// historyWait bounds waiting for Meta's answer to a page request.
const historyWait = 20 * time.Second

// Timeline is a page of a room's history, oldest first, before from (an event ID; ""
// for the newest); Next continues it, "" at the beginning.
func (a *Adapter) Timeline(ctx context.Context, room domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	if a.cache == nil {
		return domain.TimelinePage{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if from == "" {
		msgs, err := a.cache.Messages(ctx, room, limit)
		if err != nil {
			return domain.TimelinePage{}, fmt.Errorf("meta: read %s: %w", room, err)
		}
		return pageOf(msgs, true), nil
	}
	older, err := a.olderThan(ctx, room, domain.EventID(from), limit)
	if err != nil || len(older) >= limit {
		return pageOf(older, true), err
	}
	more, asked, err := a.askOlder(ctx, room, domain.EventID(from))
	switch {
	case err != nil && len(older) == 0:
		// Not the beginning: Meta did not answer. An error, so scrolling asks again.
		return domain.TimelinePage{}, err
	case asked:
		if older, err = a.olderThan(ctx, room, domain.EventID(from), limit); err != nil {
			return domain.TimelinePage{}, err
		}
		return pageOf(older, more), nil
	}
	// Nothing to ask (an encrypted chat), or no answer: what the cache holds.
	return pageOf(older, len(older) > 0), nil
}

// errNoAnswer is a page request Meta did not answer in time.
var errNoAnswer = errors.New("meta: Meta did not send older messages in time")

// pageOf is msgs as a page whose Next is its oldest message, when more may come.
func pageOf(msgs []domain.Message, more bool) domain.TimelinePage {
	page := domain.TimelinePage{Messages: msgs}
	if more && len(msgs) > 0 {
		page.Next = string(msgs[0].ID)
	}
	return page
}

// olderThan is up to limit cached messages before from, oldest first.
func (a *Adapter) olderThan(ctx context.Context, room domain.RoomID, from domain.EventID, limit int) ([]domain.Message, error) {
	around, err := a.cache.MessagesAround(ctx, room, from, limit, 0)
	if err != nil {
		return nil, fmt.Errorf("meta: read %s before %s: %w", room, from, err)
	}
	if n := len(around); n > 0 && around[n-1].ID == from {
		around = around[:n-1]
	}
	return around, nil
}

// askOlder asks Meta for the page before from and waits for it to be cached. more
// reports whether Meta has more before it; asked whether the page came. Nothing to ask
// (an encrypted chat, no connection) is neither asked nor an error.
func (a *Adapter) askOlder(ctx context.Context, room domain.RoomID, from domain.EventID) (more, asked bool, err error) {
	self, thread, ok := threadOf(room)
	c := a.connOf(self)
	if !ok || c == nil {
		return false, false, nil
	}
	anchor, ok, err := a.cache.MessageByID(ctx, room, from)
	if err != nil || !ok {
		return false, false, err //nolint:wrapcheck // the cache's own
	}
	_, mid, _ := strings.Cut(domain.ParseID(string(anchor.ID)).Native, "/")
	c.applying.Lock()
	t := c.inbox.threads[thread]
	if t == nil || t.kind.IsWhatsApp() {
		c.applying.Unlock()
		return false, false, nil // no history on Meta's side
	}
	arrived := make(chan *table.LSInsertNewMessageRange, 1)
	c.ranges[thread] = arrived
	c.applying.Unlock()
	defer func() {
		c.applying.Lock()
		if c.ranges[thread] == arrived {
			delete(c.ranges, thread)
		}
		c.applying.Unlock()
	}()
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return false, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, historyWait)
	defer cancel()
	err = a.guard("asking for older messages", func() error {
		_, err = client.ExecuteTasks(ctx, &socket.FetchMessagesTask{
			ThreadKey: thread, ReferenceTimestampMs: anchor.Timestamp.UnixMilli(), ReferenceMessageId: mid,
			SyncGroup: 1, Cursor: client.GetCursor(1),
		})
		return err //nolint:wrapcheck // logged below
	})
	if err != nil {
		return false, false, fmt.Errorf("meta: ask for older messages: %w", err)
	}
	select {
	case r := <-arrived:
		return r.HasMoreBefore, true, nil
	case <-ctx.Done():
		return false, false, errNoAnswer
	}
}

// heardRanges wakes the requests waiting for these threads' pages. Caller holds
// c.applying, after the table was stored.
func heardRanges(c *conn, tbl *table.LSTable) {
	for _, r := range tbl.LSInsertNewMessageRange {
		if ch := c.ranges[c.inbox.key(r.ThreadKey)]; ch != nil {
			select {
			case ch <- r:
			default:
			}
		}
	}
}
