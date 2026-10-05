package telegram

import (
	"context"
	"slices"
	"time"

	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Each chat's history is read back in the background once an account connects, so
// what kith holds is not only what was opened: newest chats first, a page at a time,
// to the chat's beginning or as deep as the cache keeps a room. Telegram limits how
// fast an account may ask (FLOOD_WAIT); the reading waits as long as it is told, and
// keeps its place in the store, so a restart carries on where it stopped.

const (
	// backfillPage is how many messages a page asks for: Telegram's most.
	backfillPage = 100
	// backfillDepth is how far back a chat is read: the newest 2,000 messages, what the
	// cache keeps of a room.
	backfillDepth = 2000
	// backfillPause is the rest between pages, so reading back never crowds out what
	// the person is doing.
	backfillPause = time.Second
)

// backfill reads back the history of an account's listed chats, newest first, until
// ctx ends (the connection does).
func (a *Adapter) backfill(ctx context.Context, account Account, self int64, rooms []domain.Room) {
	if a.store == nil || a.cache == nil {
		return
	}
	last, err := a.cache.LastMessages(ctx)
	if err != nil {
		a.log.Warn("read back the chats failed", "account", account.Name, "err", err)
		return
	}
	ordered := slices.Clone(rooms)
	slices.SortStableFunc(ordered, func(x, y domain.Room) int { return last[y.ID].Compare(last[x.ID]) })
	for i := range ordered {
		if err := a.backfillRoom(ctx, self, ordered[i].ID); err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("read back a chat failed; it is tried again next time", "room", ordered[i].ID, "err", err)
		}
	}
}

// backfillRoom reads back one chat from where it was left, page by page.
func (a *Adapter) backfillRoom(ctx context.Context, self int64, room domain.RoomID) error {
	next, fetched, done, err := a.store.backfilled(ctx, self, string(room))
	if err != nil || done {
		return err
	}
	for {
		page, err := a.Timeline(ctx, room, next, backfillPage)
		if wait, ok := tgerr.AsFloodWait(err); ok {
			if !sleep(ctx, wait) {
				return ctx.Err() //nolint:wrapcheck // the connection ended: no failure to name
			}
			continue
		}
		if err != nil {
			return err
		}
		fetched += len(page.Messages)
		next, done = page.Next, page.Next == "" || fetched >= backfillDepth
		if err := a.store.keepBackfill(ctx, self, string(room), next, fetched, done); err != nil {
			return err
		}
		if done || !sleep(ctx, backfillPause) {
			return ctx.Err() //nolint:wrapcheck // nil, or the connection ended
		}
	}
}

// sleep waits d, false when ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
