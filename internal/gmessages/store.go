package gmessages

import (
	"context"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// change is what one batch from the phone changed.
type change struct {
	// convs are the conversations added or changed; gone the ones the phone took away
	// (deleted, moved to spam, blocked).
	convs, gone []string
	// people reports new or renamed people.
	people bool
	// history is messages a listing carried: cached quietly, never announced, so a
	// sync notifies of nothing old. live is messages as they happen.
	history, live []domain.Message
}

// seedRooms starts c's rooms from the account's cached ones.
func (a *Adapter) seedRooms(ctx context.Context, c *conn) {
	if a.cache == nil {
		return
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		a.log.Warn("read the cached conversations failed", "account", c.account.Name, "err", err)
		return
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	for i := range rooms {
		if owner(c.self).Owns(rooms[i].ID) {
			c.rooms[rooms[i].ID] = rooms[i]
		}
	}
}

// store writes what one batch changed into the cache and the streams: people, rooms
// and members, unread state, then messages. Caller holds c.applying.
func (a *Adapter) store(ctx context.Context, c *conn, ch change) {
	if a.cache == nil {
		return
	}
	in := c.inbox
	if ch.people {
		a.keepPeople(ctx, c)
	}
	c.knowMe()
	sms := map[domain.RoomID]bool{}
	for _, conversation := range ch.convs {
		cv := in.convs[conversation]
		if cv == nil {
			continue
		}
		id := roomID(in.self, conversation)
		c.rooms[id] = in.room(cv)
		sms[id] = cv.sms()
		if err := a.cache.SaveMembers(ctx, id, in.roomMembers(cv)); err != nil {
			a.log.Warn("cache a conversation's members failed", "account", c.account.Name, "err", err)
		}
	}
	var gone []domain.RoomID
	for _, conversation := range ch.gone {
		id := roomID(in.self, conversation)
		if _, had := c.rooms[id]; had {
			delete(c.rooms, id)
			gone = append(gone, id)
		}
	}
	if len(ch.convs) > 0 || len(gone) > 0 {
		a.saveRooms(ctx, c, gone, sms)
	}
	counted := map[domain.RoomID]bool{}
	for _, conversation := range ch.convs {
		if cv := in.convs[conversation]; cv != nil {
			a.saveUnread(ctx, c, cv)
			counted[roomID(in.self, conversation)] = true
		}
	}
	a.saveMessages(ctx, c, ch.history, counted)
	live := a.saveMessages(ctx, c, ch.live, counted)
	for i := range live {
		if a.onCached != nil {
			a.onCached(live[i])
		}
		emit(a, a.messages, live[i])
	}
	for room := range counted {
		a.recount(ctx, room)
	}
}

// saveRooms writes c's rooms, or forgets the gone ones when none is left to write, and
// which of the changed ones send as SMS.
func (a *Adapter) saveRooms(ctx context.Context, c *conn, gone []domain.RoomID, sms map[domain.RoomID]bool) {
	rooms := make([]domain.Room, 0, len(c.rooms))
	for id := range c.rooms {
		rooms = append(rooms, c.rooms[id])
	}
	var err error
	if len(rooms) > 0 {
		err = a.cache.SaveRooms(ctx, owner(c.self), rooms)
	} else if len(gone) > 0 {
		err = a.cache.ForgetRooms(ctx, gone)
	}
	if err != nil {
		a.log.Warn("cache the conversations failed", "account", c.account.Name, "err", err)
	}
	if err := a.cache.SetSMS(ctx, sms); err != nil {
		a.log.Warn("keep which conversations send as SMS failed", "account", c.account.Name, "err", err)
	}
	a.roomsChanged()
}

// saveUnread keeps a conversation's unread state: the phone's flag (it has no count) and
// its read time, which only moves the cached position forward. A conversation read in
// kith stays read whatever the phone says.
func (a *Adapter) saveUnread(ctx context.Context, c *conn, cv *conv) {
	n := 0
	if cv.unread {
		n = 1
	}
	u := domain.Unread{RoomID: roomID(c.self, cv.id), Notifications: n}
	if err := a.cache.SaveUnread(ctx, u, cv.readTo.UnixMilli()); err != nil {
		a.log.Warn("save a conversation's unread state failed", "account", c.account.Name, "err", err)
	}
}

// saveMessages caches messages room by room and returns those saved.
func (a *Adapter) saveMessages(ctx context.Context, c *conn, msgs []domain.Message, counted map[domain.RoomID]bool) []domain.Message {
	byRoom := map[domain.RoomID][]domain.Message{}
	for i := range msgs {
		byRoom[msgs[i].RoomID] = append(byRoom[msgs[i].RoomID], msgs[i])
	}
	var saved []domain.Message
	for room, batch := range byRoom {
		if err := a.save(ctx, room, batch); err != nil {
			a.log.Warn("cache messages failed", "account", c.account.Name, "room", room, "count", len(batch), "err", err)
			continue
		}
		saved = append(saved, batch...)
		counted[room] = true
	}
	return saved
}

// save is the cache's save, keeping replaced versions under [display.deleted] keep.
func (a *Adapter) save(ctx context.Context, room domain.RoomID, msgs []domain.Message) error {
	if a.keepsDeleted() {
		return a.cache.SaveMessagesWithRevisions(ctx, room, msgs) //nolint:wrapcheck // the caller logs it
	}
	return a.cache.SaveMessages(ctx, room, msgs) //nolint:wrapcheck // the caller logs it
}

// unsent marks a message deleted, its text kept only under
// [display.deleted] keep. A recall before the message arrived is kept as a tombstone.
func (a *Adapter) unsent(ctx context.Context, c *conn, m domain.Message, at time.Time) {
	if a.cache == nil {
		return
	}
	if err := a.cache.MarkRedacted(ctx, m.RoomID, m.ID, m.Sender, "", at, a.keepsDeleted()); err != nil {
		a.log.Warn("mark a message deleted failed", "account", c.account.Name, "err", err)
		return
	}
	emit(a, a.messages, domain.Message{ID: m.ID, RoomID: m.RoomID, Redacted: true, RedactedBy: m.Sender, RedactedAt: at})
	a.recount(ctx, m.RoomID)
}

// keepPeople gives the directory the people this account knows.
func (a *Adapter) keepPeople(ctx context.Context, c *conn) {
	names := make([]domain.PersonName, 0, len(c.inbox.people))
	for id, name := range c.inbox.people {
		names = append(names, domain.PersonName{ID: id, Name: name, Rank: domain.RankChosen})
	}
	if err := a.cache.SetPeople(ctx, "gmessages:"+c.self, names, nil); err != nil {
		a.log.Warn("keep who people are failed", "account", c.account.Name, "err", err)
	}
}

// readTo moves a room's read position forward to at; the cache never moves it back.
func (a *Adapter) readTo(ctx context.Context, room domain.RoomID, at time.Time) error {
	if a.cache == nil {
		return nil
	}
	if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, at.UnixMilli()); err != nil {
		return fmt.Errorf("gmessages: save the read position of %s: %w", room, err)
	}
	return nil
}

// recount streams a room's locally counted unread state.
func (a *Adapter) recount(ctx context.Context, room domain.RoomID) {
	if a.cache == nil {
		return
	}
	messages, mentions, counted, err := a.cache.CountUnread(ctx, a.Me(), room)
	if err != nil {
		a.log.Warn("count unread failed", "room", room, "err", err)
		return
	}
	emit(a, a.unread, domain.Unread{RoomID: room, Messages: messages, Mentions: mentions, Counted: counted})
}

// emit sends v without blocking; a client not reading misses it and catches up from the
// cache, as with the other adapters.
func emit[T any](a *Adapter, ch chan T, v T) {
	a.streamMu.RLock()
	defer a.streamMu.RUnlock()
	if a.closed {
		return
	}
	select {
	case ch <- v:
	default:
	}
}
