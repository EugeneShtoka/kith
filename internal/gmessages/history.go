package gmessages

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Older messages come from the cache while it has them, then from the phone: a page
// before the oldest cached message, cached as history (not announced). Scrolling back
// asks for one page. After each sync a backfill reads every listed conversation back
// the same way, newest conversations first, one page at a time with a pause between, so
// live messages, sends and scrolling never wait behind it. It stops while kith is not
// the active Messages for web session or the phone is not answering (the next sync
// starts it again), and at a room's cache depth ([storage] messages_per_room). A
// conversation the phone has nothing older for is marked in the cache, so a restart
// does not ask the phone again for each one.

const (
	// backfillPage is the messages one backfill request asks for.
	backfillPage = 50
	// backfillPause spaces a backfill's requests: each wakes the phone.
	backfillPause = 2 * time.Second
	// pageSpan bounds one page's request.
	pageSpan = time.Minute
)

// Timeline is a page of a room's history, oldest first, before from (an event ID; ""
// for the newest); Next continues it, "" at the beginning.
func (a *Adapter) Timeline(ctx context.Context, room domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	if a.cache == nil {
		return domain.TimelinePage{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	started, err := a.cache.HistoryStarts(ctx)
	if err != nil {
		return domain.TimelinePage{}, fmt.Errorf("gmessages: read %s: %w", room, err)
	}
	if from == "" {
		msgs, rerr := a.cache.Messages(ctx, room, limit)
		if rerr != nil {
			return domain.TimelinePage{}, fmt.Errorf("gmessages: read %s: %w", room, rerr)
		}
		return pageOf(msgs, len(msgs) == limit || !started[room]), nil
	}
	older, err := a.olderThan(ctx, room, domain.EventID(from), limit)
	if err != nil || len(older) >= limit || started[room] {
		return pageOf(older, len(older) >= limit), err
	}
	more, err := a.pageBack(ctx, room, from, older, limit)
	if err != nil {
		if len(older) == 0 {
			return domain.TimelinePage{}, err // not the beginning: scrolling asks again
		}
		return pageOf(older, true), nil
	}
	if older, err = a.olderThan(ctx, room, domain.EventID(from), limit); err != nil {
		return domain.TimelinePage{}, err
	}
	return pageOf(older, more || len(older) >= limit), nil
}

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
		return nil, fmt.Errorf("gmessages: read %s before %s: %w", room, from, err)
	}
	if n := len(around); n > 0 && around[n-1].ID == from {
		around = around[:n-1]
	}
	return around, nil
}

// pageBack asks the phone for the page before the oldest message cached before from
// (from itself when none is); more reports whether it had any.
func (a *Adapter) pageBack(ctx context.Context, room domain.RoomID, from string, older []domain.Message, limit int) (bool, error) {
	self, _, ok := conversationOf(room)
	if !ok {
		return false, fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	c, client, err := a.connected(self)
	if err != nil {
		return false, err
	}
	anchor := domain.EventID(from)
	if len(older) > 0 {
		anchor = older[0].ID
	}
	msg, ok, err := a.cache.MessageByID(ctx, room, anchor)
	if err != nil || !ok {
		return false, fmt.Errorf("gmessages: the message to page back from is not cached (%s): %w", anchor, err)
	}
	cursor, ok := cursorAt(msg)
	if !ok {
		return false, fmt.Errorf("gmessages: %s is no message to page back from", anchor)
	}
	next, err := a.fetchOlder(ctx, c, client, room, cursor, limit)
	return next != nil, err
}

// cursorAt is the phone's cursor for the messages before msg.
func cursorAt(msg domain.Message) (*gmproto.Cursor, bool) {
	id, ok := messageOf(msg.ID)
	if !ok {
		return nil, false
	}
	return &gmproto.Cursor{LastItemID: id, LastItemTimestamp: msg.Timestamp.UnixMilli()}, true
}

// fetchOlder asks the phone for up to count of a room's messages before cursor and
// caches them as history. next is the cursor before the oldest of them; nil when the
// phone had none, and the room is then marked as cached from its beginning.
func (a *Adapter) fetchOlder(
	ctx context.Context, c *conn, client *libgm.Client, room domain.RoomID, cursor *gmproto.Cursor, count int,
) (*gmproto.Cursor, error) {
	_, conversation, ok := conversationOf(room)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	ctx, cancel := context.WithTimeout(ctx, pageSpan)
	defer cancel()
	asked := time.Now()
	var got *gmproto.ListMessagesResponse
	err := a.guard("fetching older messages", func() (err error) {
		if got, err = client.FetchMessages(ctx, conversation, int64(count), cursor); err != nil {
			return fmt.Errorf("gmessages: fetch the messages of %s before %s: %w", room, cursor.GetLastItemID(), err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return a.takeOlder(ctx, c, room, cursor, got.GetMessages(), count, asked)
}

// takeOlder caches as history those of msgs (a page of up to count the phone sent at
// asked) before cursor, and returns the cursor before the oldest of them. The phone
// counts in microseconds and the cursor in milliseconds: a message in the cursor's
// millisecond is not older, as the bridge reads it. A full page with none older is
// that millisecond's messages filling it: the next page starts below it, with a cursor
// that names no message, whose own millisecond is older. Any other page with none
// older is the room's beginning.
func (a *Adapter) takeOlder(
	ctx context.Context, c *conn, room domain.RoomID, cursor *gmproto.Cursor, msgs []*gmproto.Message, count int, asked time.Time,
) (*gmproto.Cursor, error) {
	var older []*gmproto.Message
	var next *gmproto.Cursor
	for _, m := range msgs {
		ms := m.GetTimestamp() / 1000
		below := ms < cursor.GetLastItemTimestamp() || (cursor.GetLastItemID() == "" && ms == cursor.GetLastItemTimestamp())
		if !below {
			continue
		}
		older = append(older, m)
		if next == nil || ms < next.GetLastItemTimestamp() {
			next = &gmproto.Cursor{LastItemID: m.GetMessageID(), LastItemTimestamp: ms}
		}
	}
	switch {
	case len(older) > 0:
		a.takeMessagesAt(ctx, c, older, false, asked)
		return next, nil
	case len(msgs) >= count:
		return &gmproto.Cursor{LastItemTimestamp: cursor.GetLastItemTimestamp() - 1}, nil
	}
	if err := a.cache.SetHistoryStart(ctx, map[domain.RoomID]bool{room: true}); err != nil {
		return nil, fmt.Errorf("gmessages: mark %s as cached from its beginning: %w", room, err)
	}
	return nil, nil
}

// backfill reads c's listed conversations back from the phone, newest first, until each
// is cached from its beginning or to its depth. One runs at a time; it stops when the
// connection is let go, kith's session is not the active one, or the phone stops
// answering.
func (a *Adapter) backfill(ctx context.Context, c *conn) {
	if a.cache == nil || !c.backfilling.TryLock() {
		return
	}
	defer c.backfilling.Unlock()
	rooms, err := a.backfillRooms(ctx, c)
	if err != nil {
		a.log.Warn("read which conversations to read back failed", "account", c.account.Name, "err", err)
		return
	}
	for _, room := range rooms {
		if !a.backfillRoom(ctx, c, room) {
			return
		}
	}
}

// backfillRooms is c's listed conversations not yet cached from their beginning, the
// newest first. Only listed ones: a message in a conversation the inbox does not know
// asks for a sync.
func (a *Adapter) backfillRooms(ctx context.Context, c *conn) ([]domain.RoomID, error) {
	started, err := a.cache.HistoryStarts(ctx)
	if err != nil {
		return nil, fmt.Errorf("gmessages: read which conversations are read back: %w", err)
	}
	last, err := a.cache.LastMessages(ctx)
	if err != nil {
		return nil, fmt.Errorf("gmessages: read the conversations' newest messages: %w", err)
	}
	c.applying.Lock()
	rooms := make([]domain.RoomID, 0, len(c.inbox.convs))
	for id := range c.inbox.convs {
		if room := roomID(c.self, id); !started[room] {
			rooms = append(rooms, room)
		}
	}
	c.applying.Unlock()
	slices.SortFunc(rooms, func(x, y domain.RoomID) int {
		if n := last[y].Compare(last[x]); n != 0 {
			return n
		}
		return cmp.Compare(x, y)
	})
	return rooms, nil
}

// backfillRoom reads one conversation back, a page at a time, from its oldest cached
// message; false when the backfill should stop.
func (a *Adapter) backfillRoom(ctx context.Context, c *conn, room domain.RoomID) bool {
	oldest, ok, err := a.cache.OldestMessage(ctx, room)
	if err != nil {
		a.log.Warn("read a conversation's oldest message failed", "account", c.account.Name, "room", room, "err", err)
		return true
	}
	if !ok {
		return true // nothing cached to page back from: a sync fetches its newest first
	}
	cursor, ok := cursorAt(oldest)
	if !ok {
		return true
	}
	for {
		if a.deepEnough(ctx, room) {
			return true
		}
		client := c.free()
		if client == nil || ctx.Err() != nil {
			return false
		}
		next, err := a.fetchOlder(ctx, c, client, room, cursor, backfillPage)
		if err != nil {
			a.log.Info("reading a conversation back stopped; the next sync goes on", "account", c.account.Name, "room", room, "err", err)
			return false
		}
		if next == nil {
			return true
		}
		cursor = next
		if !c.pause(ctx, backfillPause) {
			return false
		}
	}
}

// deepEnough reports whether a room holds as many messages as the cache keeps of it.
func (a *Adapter) deepEnough(ctx context.Context, room domain.RoomID) bool {
	kept := a.cache.MessagesKept(room) // asked each page: a reload may change it
	if kept < 0 {
		return false
	}
	n, err := a.cache.CountMessages(ctx, room)
	return err != nil || n >= kept
}

// free is c's client while a backfill may use it: connected, the active session, and
// the phone answering; nil otherwise.
func (c *conn) free() *libgm.Client {
	if c.closed() || c.inactive.Load() || c.silent.Load() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

// pause waits d, false when the connection is let go or ctx ends first.
func (c *conn) pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-c.done:
		return false
	case <-ctx.Done():
		return false
	}
}
