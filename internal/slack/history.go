package slack

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A conversation's history is Slack's to page through, newest first: a page is cached
// as it is read, so what was scrolled through once opens from the cache.

// longestWait is the longest Slack's rate limit is waited out before a call gives up.
const longestWait = 30 * time.Second

// Timeline is a page of a conversation's history, oldest first, from where from left
// off ("" for the newest); Next continues it, "" at the beginning.
func (a *Adapter) Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return domain.TimelinePage{}, err
	}
	var resp *slackgo.GetConversationHistoryResponse
	err = waitingOut(ctx, func() (err error) {
		resp, err = w.client.GetConversationHistoryContext(ctx, &slackgo.GetConversationHistoryParameters{
			ChannelID: channel, Cursor: from, Limit: limit,
		})
		if err != nil {
			return fmt.Errorf("slack: history of %s: %w", roomID, err)
		}
		return nil
	})
	if err != nil {
		return domain.TimelinePage{}, err
	}
	page := domain.TimelinePage{Messages: a.cachePage(ctx, w, channel, resp.Messages)}
	if resp.HasMore {
		page.Next = resp.ResponseMetaData.NextCursor
	}
	return page, nil
}

// cachePage is a page of a conversation's history, as Slack answers it (newest
// first), oldest first as kith keeps it, its senders named; it is cached as history:
// nothing is streamed or notified.
func (a *Adapter) cachePage(ctx context.Context, w *workspace, channel string, page []slackgo.Message) []domain.Message {
	raw := make([]slackgo.Msg, len(page))
	for i := range page {
		raw[i] = page[i].Msg
	}
	a.learnPeople(ctx, w, people(raw))
	n := w.names()
	var msgs []domain.Message
	for i := range slices.Backward(raw) {
		if msg, ok := incoming(channel, &raw[i], n); ok {
			msgs = append(msgs, msg)
		}
	}
	room := roomID(w.creds.Team, channel)
	if a.cache != nil && len(msgs) > 0 {
		if _, ok := a.record(ctx, w, room, msgs); ok && a.onChanged != nil {
			a.onChanged(room)
		}
	}
	return msgs
}

// conversation is the connected workspace a room is in, and its channel ID there.
func (a *Adapter) conversation(roomID domain.RoomID) (*workspace, string, error) {
	id := domain.ParseID(string(roomID))
	if id.Network != domain.ProtocolSlack || id.Account == "" {
		return nil, "", fmt.Errorf("slack: %s is not a Slack conversation", roomID)
	}
	for _, w := range a.connected() {
		if w.creds.Team == id.Account {
			return w, id.Native, nil
		}
	}
	return nil, "", fmt.Errorf("%w: the Slack workspace %s is not signed in or not connected", errNetworkOff, id.Account)
}

// waitingOut makes a call, and once more after the wait Slack asks for when it is
// rate limited — unless that wait is longer than longestWait or ctx ends first.
func waitingOut(ctx context.Context, call func() error) error {
	err := call()
	limited, ok := errors.AsType[*slackgo.RateLimitedError](err)
	if !ok || limited.RetryAfter > longestWait {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the caller's own
	case <-time.After(limited.RetryAfter):
	}
	return call()
}
