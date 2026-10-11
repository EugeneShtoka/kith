package slack

import (
	"context"
	"fmt"
	"slices"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// On connect, client.counts gives each conversation's latest message time in one call;
// a conversation newer than the cache is read from where the cache left off, so the
// room list can sort by last message.

// Catch-up reads at most catchUpPages pages of catchUpPage messages per conversation;
// older history loads on scroll.
const (
	catchUpPage  = 100
	catchUpPages = 5
)

// goCatchUp catches a workspace up in the background unless already running.
func (a *Adapter) goCatchUp(ctx context.Context, w *workspace) {
	go func() {
		if !w.catching.TryLock() {
			return
		}
		defer w.catching.Unlock()
		a.listEveryone(ctx, w)
		if err := a.catchUp(ctx, w); err != nil {
			a.log.Warn("catch up failed", "account", w.account.Name, "err", err)
		}
		w.filesReread.Do(func() { a.refetchFileMessages(ctx, w) })
	}()
}

// catchUp caches each conversation's messages newer than the cache holds.
func (a *Adapter) catchUp(ctx context.Context, w *workspace) error {
	if a.cache == nil {
		return nil
	}
	var counts *slackgo.ClientCountsResponse
	err := waitingOut(ctx, func() (err error) {
		counts, err = w.client.ClientCountsContext(ctx, &slackgo.ClientCountsParams{})
		if err != nil {
			return fmt.Errorf("slack: counts of %s: %w", w.account.Name, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	cached, err := a.cache.LastMessages(ctx)
	if err != nil {
		return fmt.Errorf("slack: read the last cached messages: %w", err)
	}
	listed := w.listedNow()
	a.readPositions(ctx, w, counts)
	late := behind(w.creds.Team, counts, cached, listed)
	a.log.Info("catching up", "account", w.account.Name, "conversations", len(late),
		"counted", len(counts.Channels)+len(counts.IMs)+len(counts.MpIMs))
	for _, c := range late {
		if ctx.Err() != nil {
			return ctx.Err() //nolint:wrapcheck // the caller's own
		}
		if err := a.readSince(ctx, w, c, cached[roomID(w.creds.Team, c)]); err != nil {
			a.log.Warn("catch up a conversation failed", "account", w.account.Name, "channel", c, "err", err)
		}
	}
	// A conversation without a Slack read position (a quiet one) counts as read.
	for _, c := range listed {
		room := roomID(w.creds.Team, c)
		a.placeRead(ctx, room, time.Now())
		a.recount(ctx, room)
	}
	return nil
}

// readPositions moves each counted conversation's read position to Slack's last_read.
func (a *Adapter) readPositions(ctx context.Context, w *workspace, counts *slackgo.ClientCountsResponse) {
	for _, list := range [][]slackgo.ClientCountsChannel{counts.Channels, counts.IMs, counts.MpIMs} {
		for _, c := range list {
			if c.LastRead == "" || tsTime(c.LastRead).IsZero() {
				continue
			}
			a.readTo(ctx, roomID(w.creds.Team, c.ID), messageID(w.creds.Team, c.ID, c.LastRead), tsTime(c.LastRead))
		}
	}
}

// behind is the listed conversations whose last message is newer than the cache's
// (any, if none is cached), newest first. Slack omits quiet conversations from counts;
// those with nothing cached come last, read once.
func behind(team string, counts *slackgo.ClientCountsResponse, cached map[domain.RoomID]time.Time, listed []string) []string {
	type conv struct {
		id     string
		latest time.Time
	}
	var out []conv
	counted := map[string]bool{}
	for _, list := range [][]slackgo.ClientCountsChannel{counts.Channels, counts.IMs, counts.MpIMs} {
		for _, c := range list {
			counted[c.ID] = true
			latest := tsTime(c.Latest)
			// The cache keeps milliseconds; Slack's ts has microseconds.
			if latest.IsZero() || !slices.Contains(listed, c.ID) || !latest.Truncate(time.Millisecond).After(cached[roomID(team, c.ID)]) {
				continue
			}
			out = append(out, conv{c.ID, latest})
		}
	}
	slices.SortFunc(out, func(x, y conv) int { return y.latest.Compare(x.latest) })
	ids := make([]string, 0, len(out))
	for i := range out {
		ids = append(ids, out[i].id)
	}
	for _, c := range listed {
		if _, has := cached[roomID(team, c)]; !has && !counted[c] {
			ids = append(ids, c)
		}
	}
	return ids
}

// readSince caches a conversation's messages after since (recent ones when zero),
// newest page first.
func (a *Adapter) readSince(ctx context.Context, w *workspace, channel string, since time.Time) error {
	room := roomID(w.creds.Team, channel)
	oldest := ""
	if !since.IsZero() {
		oldest = fmt.Sprintf("%d.%06d", since.Unix(), since.Nanosecond()/int(time.Microsecond))
	}
	cursor := ""
	for range catchUpPages {
		var resp *slackgo.GetConversationHistoryResponse
		fetched := time.Now()
		err := waitingOut(ctx, func() (err error) {
			resp, err = w.client.GetConversationHistoryContext(ctx, &slackgo.GetConversationHistoryParameters{
				ChannelID: channel, Oldest: oldest, Cursor: cursor, Limit: catchUpPage,
			})
			if err != nil {
				return fmt.Errorf("slack: history of %s: %w", room, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		a.cachePage(ctx, w, channel, resp.Messages, fetched)
		if cursor = resp.ResponseMetaData.NextCursor; !resp.HasMore || cursor == "" {
			return nil
		}
	}
	return nil
}
