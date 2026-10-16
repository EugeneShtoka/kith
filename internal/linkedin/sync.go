package linkedin

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Conversations come two ways. A sync lists them: the first one pages through the
// whole inbox, newest first; later ones ask only what changed since the sync token
// LinkedIn gave last. The realtime stream then pushes each conversation and message as
// it changes. Both are taken in one at a time (conn.applying), and the cache merges
// them whatever their order: a copy older than what is cached never undoes an edit or
// a deletion, and a deleted conversation is not brought back by an older listing.

// listPages bounds the first sync: LinkedIn sends 20 conversations a page.
const listPages = 50

// syncSpan bounds one sync.
const syncSpan = 5 * time.Minute

// sync lists c's conversations after the realtime connection session connected,
// unless that session was synced already.
func (a *Adapter) sync(ctx context.Context, c *conn, session string) {
	c.syncing.Lock()
	defer c.syncing.Unlock()
	c.mu.Lock()
	client, done, first := c.client, c.synced == session && session != "", c.session.SyncToken == ""
	c.mu.Unlock()
	if client == nil || done || c.closed() {
		return
	}
	ctx, cancel := context.WithTimeout(a.withLog(ctx), syncSpan)
	defer cancel()
	if first || !a.hasRooms(ctx, c) {
		if err := a.listAll(ctx, c, client); err != nil {
			// No token until the whole inbox is listed: one kept now would make the next
			// sync ask only what changed, and the conversations not listed yet would never be.
			a.log.Warn("list the conversations failed; listed again on the next sync", "account", c.account.Name, "err", err)
			c.mu.Lock()
			c.session.SyncToken = ""
			c.mu.Unlock()
			a.keepCookies(c)
			return
		}
	}
	if err := a.listChanges(ctx, c, client); err != nil {
		a.log.Warn("list the changed conversations failed", "account", c.account.Name, "err", err)
		return
	}
	c.mu.Lock()
	c.synced = session
	c.mu.Unlock()
	a.keepCookies(c) // the new sync token
}

// hasRooms reports whether the cache holds any of c's conversations.
func (a *Adapter) hasRooms(_ context.Context, c *conn) bool {
	c.applying.Lock()
	defer c.applying.Unlock()
	return len(c.rooms) > 0
}

// listAll pages through the inbox, newest first, moving back by the oldest
// conversation's activity each page; an error is a listing that stopped short.
func (a *Adapter) listAll(ctx context.Context, c *conn, client *linkedingo.Client) error {
	before := time.Now()
	for page := range listPages {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("linkedin: list conversations, page %d: %w", page, err)
		}
		var got *linkedingo.CollectionResponse[linkedingo.ConversationCursorMetadata, linkedingo.Conversation]
		err := a.guard("listing conversations", func() (err error) {
			got, err = client.GetConversationsUpdatedBefore(ctx, before)
			return err //nolint:wrapcheck // logged below
		})
		if err != nil {
			return fmt.Errorf("linkedin: list conversations, page %d: %w", page, err)
		}
		if got == nil || len(got.Elements) == 0 {
			return nil
		}
		a.takeConversations(ctx, c, got.Elements, nil)
		oldest := before
		for i := range got.Elements {
			if at := got.Elements[i].LastActivityAt.Time; !at.IsZero() && at.Before(oldest) {
				oldest = at
			}
		}
		if !oldest.Before(before) {
			return nil // no older conversation: the inbox ends here
		}
		before = oldest
	}
	return nil
}

// listChanges asks LinkedIn what changed since the sync token (or, without one, its
// newest conversations, which brings the first token).
func (a *Adapter) listChanges(ctx context.Context, c *conn, client *linkedingo.Client) error {
	var got *linkedingo.CollectionResponse[linkedingo.ConversationSyncMetadata, linkedingo.Conversation]
	err := a.guard("listing changed conversations", func() (err error) {
		got, err = client.GetConversationsBySyncToken(ctx)
		return err //nolint:wrapcheck // the caller logs it
	})
	if err != nil || got == nil {
		return err
	}
	var deleted []string
	for _, d := range got.Metadata.DeletedURNs {
		if thread, ok := threadIn(d.Conversation.EntityURN); ok {
			deleted = append(deleted, thread)
		}
	}
	a.takeConversations(ctx, c, got.Elements, deleted)
	return nil
}

// takeConversations applies conversations and deletions to c's inbox and stores what
// changed. Their messages are history: cached, not announced.
func (a *Adapter) takeConversations(ctx context.Context, c *conn, convs []linkedingo.Conversation, deleted []string) {
	if c.closed() {
		return
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	in := c.inbox
	var ch change
	for _, thread := range deleted {
		in.drop(thread, time.Now())
		ch.gone = append(ch.gone, thread)
	}
	for i := range convs {
		thread, shown, met := in.take(&convs[i])
		ch.people = ch.people || met
		if thread == "" {
			continue
		}
		if !shown {
			ch.gone = append(ch.gone, thread)
			continue
		}
		ch.convs = append(ch.convs, thread)
		msgs := convs[i].Messages.Elements
		for j := range msgs {
			if msg, ok := in.message(thread, &msgs[j]); ok {
				ch.history = append(ch.history, msg)
			}
		}
	}
	a.store(ctx, c, ch)
}

// event is one realtime event for c, by its topic.
func (a *Adapter) event(ctx context.Context, c *conn, e *linkedingo.DecoratedEvent) {
	data := e.Payload.Data
	switch e.Topic.NthPrefixPart(2) {
	case linkedingo.RealtimeEventTopicConversations, linkedingo.RealtimeEventTopicTabBadgeUpdate:
		if data.DecoratedConversation == nil {
			a.syncChanges(ctx, c) // a nudge without the conversation: ask what changed
			return
		}
		a.takeConversations(ctx, c, []linkedingo.Conversation{data.DecoratedConversation.Result}, nil)
	case linkedingo.RealtimeEventTopicConversationDelete:
		if d := data.DecoratedConversationDelete; d != nil {
			if thread, ok := threadIn(d.Result.EntityURN); ok {
				a.takeConversations(ctx, c, nil, []string{thread})
			}
		}
	case linkedingo.RealtimeEventTopicMessages:
		if m := data.DecoratedMessage; m != nil {
			a.liveMessage(ctx, c, &m.Result, e.LeftServerAt.Time)
		}
	case linkedingo.RealtimeEventTopicMessageSeenReceipts:
		if r := data.DecoratedSeenReceipt; r != nil {
			a.seen(ctx, c, &r.Result)
		}
	case linkedingo.RealtimeEventTopicMessageReactionSummaries:
		if r := data.DecoratedReactionSummary; r != nil {
			a.reacted(ctx, c, &r.Result)
		}
	}
}

// syncChanges lists what changed, outside a connection's own sync.
func (a *Adapter) syncChanges(ctx context.Context, c *conn) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return
	}
	go func() {
		c.syncing.Lock()
		defer c.syncing.Unlock()
		bounded, cancel := context.WithTimeout(a.withLog(ctx), syncSpan)
		defer cancel()
		if err := a.listChanges(bounded, c, client); err != nil {
			a.log.Warn("list the changed conversations failed", "account", c.account.Name, "err", err)
			return
		}
		a.keepCookies(c)
	}()
}

// liveMessage is a message as it happens: new, edited (at), or recalled. A message in
// a conversation not known yet asks for it.
func (a *Adapter) liveMessage(ctx context.Context, c *conn, m *linkedingo.Message, at time.Time) {
	thread, ok := threadIn(m.Conversation.EntityURN)
	if !ok {
		if thread, ok = threadIn(m.ConversationURN); !ok {
			return
		}
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	if c.closed() {
		return
	}
	in := c.inbox
	msg, shown := in.message(thread, m)
	if !shown {
		return
	}
	if in.convs[thread] == nil {
		a.syncChanges(ctx, c)
	} else if cv := in.convs[thread]; m.DeliveredAt.After(cv.lastActive) {
		cv.lastActive = m.DeliveredAt.Time
	}
	switch {
	case msg.Redacted:
		a.unsent(ctx, c, msg, at)
	case msg.Edited:
		msg.EditedAt = at
		a.edited(ctx, c, msg)
	default:
		a.store(ctx, c, change{live: []domain.Message{msg}})
	}
}

// seen is a read receipt; yours (read on another device) moves the read position.
func (a *Adapter) seen(ctx context.Context, c *conn, r *linkedingo.SeenReceipt) {
	if profileOf(r.SeenByParticipant.EntityURN) != c.profile {
		return // others' receipts are not kept yet
	}
	thread, ok := threadIn(r.Message.Conversation.EntityURN)
	if !ok {
		if thread, ok = threadIn(r.Message.ConversationURN); !ok {
			return
		}
	}
	room := roomID(c.profile, thread)
	if err := a.readTo(ctx, room, r.SeenAt.Time); err != nil {
		a.log.Warn("save a read position failed", "account", c.account.Name, "err", err)
		return
	}
	a.recount(ctx, room)
}

// reacted is someone reacting to a message, or taking a reaction back.
func (a *Adapter) reacted(ctx context.Context, c *conn, r *linkedingo.RealtimeReactionSummary) {
	thread, ok := threadIn(r.Message.Conversation.EntityURN)
	if !ok {
		if thread, ok = threadIn(r.Message.ConversationURN); !ok {
			return
		}
	}
	actor, emoji := profileOf(r.Actor.EntityURN), r.ReactionSummary.Emoji
	if r.Message.EntityURN.IsEmpty() || actor == "" || emoji == "" {
		return
	}
	message := r.Message.EntityURN.ID()
	a.applyReaction(ctx, domain.Reaction{
		ID: reactionID(c.profile, thread, message, actor, emoji), RoomID: roomID(c.profile, thread),
		Target: eventID(c.profile, thread, message), Sender: personID(actor), Key: emoji,
	}, r.ReactionAdded)
}
