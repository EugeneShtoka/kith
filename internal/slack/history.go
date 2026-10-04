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
	fetched := time.Now()
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
	msgs, reactions := a.cachePage(ctx, w, channel, resp.Messages, fetched)
	page := domain.TimelinePage{Messages: msgs, Reactions: reactions}
	if resp.HasMore {
		page.Next = resp.ResponseMetaData.NextCursor
	}
	return page, nil
}

// cachePage is a page of a conversation's history, as Slack answers it (newest
// first), oldest first as kith keeps it, its senders named, and the reactions on it;
// it is cached as history: nothing is streamed or notified. The page's reactions are
// Slack's on the messages it carries, so a cached one the page lacks went while kith
// was not listening, and goes; but a message whose reactions changed live since the
// page was fetched keeps the live ones (reactedSince). A thread hanging off it with
// replies the cache lacks is queued to be read (threads.go).
func (a *Adapter) cachePage(ctx context.Context, w *workspace, channel string, page []slackgo.Message, fetched time.Time) ([]domain.Message, []domain.Reaction) {
	raw := make([]slackgo.Msg, len(page))
	for i := range page {
		raw[i] = page[i].Msg
	}
	a.learnPeople(ctx, w, people(raw))
	n := w.names()
	var msgs []domain.Message
	var reactions []domain.Reaction
	settled := map[domain.EventID]bool{} // messages whose reactions this page decides
	for i := range slices.Backward(raw) {
		msg, ok := incoming(channel, &raw[i], n)
		if !ok {
			continue
		}
		msgs = append(msgs, msg)
		if !a.reactedSince(msg.ID, fetched) {
			settled[msg.ID] = true
			reactions = append(reactions, messageReactions(w.creds.Team, channel, &raw[i])...)
		}
	}
	room := roomID(w.creds.Team, channel)
	if len(msgs) < len(raw) {
		var dropped []string
		for i := range raw {
			if _, ok := incoming(channel, &raw[i], n); !ok {
				dropped = append(dropped, "subtype="+raw[i].SubType)
			}
		}
		a.log.Debug("history: messages not shown", "room", room, "fetched", len(raw), "kept", len(msgs), "dropped", dropped)
	}
	if a.cache != nil && len(msgs) > 0 {
		if _, ok := a.record(ctx, w, room, msgs); ok && a.onChanged != nil {
			a.onChanged(room)
		}
		a.settleReactions(ctx, room, settled, reactions)
		for i := range raw {
			if a.repliesBehind(ctx, w.creds.Team, channel, &raw[i]) {
				a.wantThread(w, channel, raw[i].Timestamp, raw[i].LatestReply)
			}
		}
	}
	return msgs, reactions
}

// settleReactions makes the cached reactions on the settled messages the page's: those
// it lacks are dropped, its own are written.
func (a *Adapter) settleReactions(ctx context.Context, room domain.RoomID, settled map[domain.EventID]bool, reactions []domain.Reaction) {
	if len(settled) == 0 {
		return
	}
	keep := make(map[domain.EventID]bool, len(reactions))
	for _, r := range reactions {
		keep[r.ID] = true
	}
	cachedRs, err := a.cache.Reactions(ctx, room)
	if err != nil {
		a.log.Warn("read cached reactions failed", "room", room, "err", err)
		return
	}
	for _, r := range cachedRs {
		if settled[r.Target] && !keep[r.ID] {
			if _, _, err := a.cache.DeleteReaction(ctx, r.ID); err != nil {
				a.log.Warn("drop a reaction taken back failed", "room", room, "err", err)
			}
		}
	}
	if len(reactions) > 0 {
		if err := a.cache.SaveReactions(ctx, reactions); err != nil {
			a.log.Warn("cache reactions failed", "room", room, "err", err)
		}
	}
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
