package gmessages

import (
	"context"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Conversations come two ways. A sync, when the connection becomes the active session,
// lists the inbox page by page and fetches the newest messages of each conversation
// with something newer than the cache; the phone then pushes each conversation and
// message as it changes, a message again with every change of its status or text.
// libgm hands both to its event handler, which must not block: they go onto a queue,
// taken in order by one worker. The cache merges listing and live copies whatever their
// order: an older copy never undoes a deletion, and a conversation that left the inbox
// is not brought back by a listing fetched before.

// The sync's bounds: conversations a page, pages, and conversations whose messages are
// fetched (each fetch wakes the phone).
const (
	listCount     = 50
	listPages     = 10
	historyConvs  = 50
	historyCount  = 25
	queueCapacity = 256
)

// syncSpan bounds one sync.
const syncSpan = 5 * time.Minute

// enqueue hands a conversation or message event to c's worker, waiting rather than
// dropping it; a closed connection drops it.
func (c *conn) enqueue(evt any) {
	select {
	case c.queue <- evt:
	case <-c.done:
	}
}

// work takes c's queued events in order until the connection is let go.
func (a *Adapter) work(ctx context.Context, c *conn) {
	for {
		select {
		case evt := <-c.queue:
			if err := a.guard("taking an event", func() error { a.take(ctx, c, evt); return nil }); err != nil {
				a.log.Error("an event was dropped", "account", c.account.Name, "err", err)
			}
		case <-c.done:
			return
		}
	}
}

// take is one queued event.
func (a *Adapter) take(ctx context.Context, c *conn, evt any) {
	switch e := evt.(type) {
	case *gmproto.Conversation:
		a.takeConversations(ctx, c, []*gmproto.Conversation{e})
	case *libgm.WrappedMessage:
		a.takeMessages(ctx, c, []*gmproto.Message{e.Message}, !e.IsOld)
	}
}

// takeConversations applies conversations to c's inbox and stores what changed.
func (a *Adapter) takeConversations(ctx context.Context, c *conn, convs []*gmproto.Conversation) {
	if c.closed() {
		return
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	var ch change
	for _, v := range convs {
		isShown, met := c.inbox.take(v)
		ch.people = ch.people || met
		if id := v.GetConversationID(); id != "" {
			if isShown {
				ch.convs = append(ch.convs, id)
			} else {
				ch.gone = append(ch.gone, id)
			}
		}
	}
	a.store(ctx, c, ch)
}

// takeMessages stores messages: live ones are announced, a listing's are history. A
// deleted one is marked so; one in a conversation not known yet asks for a sync.
func (a *Adapter) takeMessages(ctx context.Context, c *conn, msgs []*gmproto.Message, live bool) {
	a.takeMessagesAt(ctx, c, msgs, live, time.Now())
}

// takeMessagesAt is takeMessages for messages as the phone had them at at (a listing's
// when it was asked for), so their reactions never undo a newer change.
func (a *Adapter) takeMessagesAt(ctx context.Context, c *conn, msgs []*gmproto.Message, live bool, at time.Time) {
	if c.closed() {
		return
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	var ch change
	var kept []*gmproto.Message // the messages cached, with what is kept beside them
	unknown := false
	for _, m := range msgs {
		a.outbox.echoed(m.GetTmpID())
		c.inbox.switched(m)
		msg, ok := c.inbox.message(m)
		if !ok {
			continue
		}
		if c.inbox.convs[m.GetConversationID()] == nil {
			unknown = true
		}
		switch {
		case msg.Redacted:
			if live {
				a.unsent(ctx, c, msg, time.Now())
			}
		case live:
			ch.live = append(ch.live, msg)
			kept = append(kept, m)
		default:
			ch.history = append(ch.history, msg)
			kept = append(kept, m)
		}
	}
	a.store(ctx, c, ch)
	a.keepSources(ctx, c, kept)
	a.takeReactions(ctx, c, kept, live, at)
	if unknown {
		go a.sync(context.WithoutCancel(ctx), c)
	}
}

// sync lists c's inbox and fetches the newest messages of conversations newer than the
// cache, then starts reading the conversations back (history.go). One runs at a time;
// one asked for while another runs is not needed.
func (a *Adapter) sync(ctx context.Context, c *conn) {
	if !c.syncing.TryLock() {
		return
	}
	defer c.syncing.Unlock()
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || c.closed() {
		return
	}
	defer func(ctx context.Context) { go a.backfill(ctx, c) }(ctx)
	ctx, cancel := context.WithTimeout(ctx, syncSpan)
	defer cancel()
	var cursor *gmproto.Cursor
	fetched := 0
	for page := 0; page < listPages && ctx.Err() == nil; page++ {
		var got *gmproto.ListConversationsResponse
		err := a.guard("listing conversations", func() (err error) {
			got, err = client.ListConversations(ctx, &gmproto.ListConversationsRequest{
				Count: listCount, Folder: gmproto.ListConversationsRequest_INBOX, Cursor: cursor,
			})
			return err //nolint:wrapcheck // logged below
		})
		if err != nil {
			a.log.Warn("list the conversations failed", "account", c.account.Name, "page", page, "err", err)
			return
		}
		convs := got.GetConversations()
		a.takeConversations(ctx, c, convs)
		fetched = a.fetchNewer(ctx, c, client, convs, fetched)
		if got.GetCursor() == nil || len(convs) < listCount {
			return
		}
		cursor = got.GetCursor()
	}
}

// fetchNewer fetches the newest messages of each listed conversation with something
// newer than the cache, up to historyConvs in a sync, and returns how many it fetched.
func (a *Adapter) fetchNewer(ctx context.Context, c *conn, client *libgm.Client, convs []*gmproto.Conversation, fetched int) int {
	for _, v := range convs {
		if fetched >= historyConvs || ctx.Err() != nil {
			return fetched
		}
		if !shown(v.GetStatus()) || !a.newerThanCache(ctx, roomID(c.self, v.GetConversationID()), micros(v.GetLastMessageTimestamp())) {
			continue
		}
		fetched++
		asked := time.Now()
		var got *gmproto.ListMessagesResponse
		err := a.guard("fetching messages", func() (err error) {
			got, err = client.FetchMessages(ctx, v.GetConversationID(), historyCount, nil)
			return err //nolint:wrapcheck // logged below
		})
		if err != nil {
			a.log.Warn("fetch a conversation's messages failed", "account", c.account.Name, "err", err)
			continue
		}
		a.takeMessagesAt(ctx, c, got.GetMessages(), false, asked)
	}
	return fetched
}

// newerThanCache reports whether a conversation active at has anything the cache lacks.
func (a *Adapter) newerThanCache(ctx context.Context, room domain.RoomID, at time.Time) bool {
	if a.cache == nil {
		return false
	}
	newest, err := a.cache.Messages(ctx, room, 1)
	if err != nil || len(newest) == 0 {
		return err == nil
	}
	return at.After(newest[len(newest)-1].Timestamp)
}
