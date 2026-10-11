package telegram

import (
	"context"
	"slices"
	"time"

	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// After connecting, each chat's history is backfilled in the background, newest chats
// first, to the start or the room's cache depth ([storage] messages_per_room). It
// waits out FLOOD_WAIT and stores progress so a restart resumes.

const (
	// backfillPage is Telegram's maximum page size.
	backfillPage = 100
	// backfillPause spaces pages so backfill doesn't crowd out interactive requests.
	backfillPause = time.Second
)

// backfill backfills an account's listed chats, newest first, until ctx ends.
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

// backfillRoom resumes one chat's backfill.
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
		depth := a.cache.MessagesKept(room) // asked each page: a reload may change it
		next, done = page.Next, page.Next == "" || (depth >= 0 && fetched >= depth)
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
