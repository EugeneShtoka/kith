package instagram

import (
	"context"
	"slices"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// typingLapse is how long someone shows as typing when Instagram gives no TTL.
const typingLapse = 6 * time.Second

// takeDelta writes one change Instagram pushed into the cache and the streams.
func (a *Adapter) takeDelta(ctx context.Context, c *conn, client *instameow.Client, d *slidetypes.Delta) {
	chat := d.ThreadIGID
	switch e := d.Data.(type) {
	case *slidetypes.NewMessageEvent:
		if chat == "" && e.Message != nil {
			chat = e.Message.ThreadFBID
		}
		a.takeMessage(ctx, c, client, chat, e.Message)
	case *slidetypes.AdminMessageEvent:
		a.takeMessage(ctx, c, client, chat, e.Message)
	case *slidetypes.EditMessageEvent:
		a.takeEdit(ctx, c, chat, e)
	case *slidetypes.DeleteMessageEvent:
		a.takeUnsend(ctx, c, chat, e.MessageID)
	case *slidetypes.CreateReactionEvent:
		a.takeReaction(ctx, c, chat, e.MessageID, &e.Reaction)
	case *slidetypes.DeleteReactionEvent:
		a.takeUnreaction(ctx, c, chat, e.MessageID, &e.Reaction)
	case *slidetypes.MarkReadEvent:
		a.readTo(ctx, c, chat, e.ReadTimestampMS.Time)
	case *slidetypes.MarkUnreadEvent:
		a.markUnread(ctx, c, chat, e.MarkedAsUnread)
	case *slidetypes.UpdateThreadNameEvent, *slidetypes.ParticipantJoinEvent, *slidetypes.ParticipantLeaveEvent,
		*slidetypes.AdminChangeEvent:
		a.refreshThread(ctx, c, client, chat)
	case *slidetypes.DeleteThreadEvent:
		a.forgetThread(ctx, c, chat)
	default:
		// Others' read receipts, folders, pins, mutes, presence: not kept.
	}
}

// takeMessage caches a new message and announces it; the account's own (written on
// another device) reads its thread up to there.
func (a *Adapter) takeMessage(ctx context.Context, c *conn, client *instameow.Client, chat string, m *slidetypes.Message) {
	if a.cache == nil || chat == "" || m == nil {
		return
	}
	if err := a.ensureThread(ctx, c, client, chat); err != nil {
		a.log.Warn("a message in a thread kith cannot fetch", "account", c.account.Name, "err", err)
	}
	if m.Sender != nil && fbid(m.SenderFBID) != c.self {
		c.inbox.mu.Lock()
		met := c.inbox.meet(&m.Sender.UserDict)
		c.inbox.mu.Unlock()
		if met {
			if th := c.inbox.thread(chat); th != nil {
				a.keepThread(ctx, c, th, true)
			}
		}
	}
	msg, source, ok := message(c.self, chat, m)
	if !ok {
		return
	}
	if err := a.save(ctx, msg.RoomID, msg); err != nil {
		a.log.Warn("cache a message failed", "account", c.account.Name, "err", err)
		return
	}
	a.keepSource(ctx, msg, source)
	if fbid(m.SenderFBID) == c.self {
		a.readTo(ctx, c, chat, msg.Timestamp)
	}
	if a.onCached != nil {
		a.onCached(msg)
	}
	emit(a, a.messages, msg)
	a.recount(ctx, msg.RoomID)
}

// takeEdit replaces a cached message's text, keeping the old under [display.deleted]
// keep. An edit of a message kith never had is let go.
func (a *Adapter) takeEdit(ctx context.Context, c *conn, chat string, e *slidetypes.EditMessageEvent) {
	if a.cache == nil {
		return
	}
	room, id := roomID(c.self, chat), eventID(c.self, chat, e.MessageID)
	msg, ok, err := a.cache.MessageByID(ctx, room, id)
	if err != nil || !ok {
		return
	}
	at := e.SlideEditHistoryEntry.TimestampMS.Time
	if at.IsZero() {
		at = time.Now()
	}
	msg.Body, msg.Edited, msg.EditedAt = e.TextBody, true, at
	if err := a.save(ctx, room, msg); err != nil {
		a.log.Warn("cache an edit failed", "account", c.account.Name, "err", err)
		return
	}
	if a.onCached != nil {
		a.onCached(msg)
	}
	emit(a, a.messages, msg)
}

// takeUnsend marks a message unsent, its text kept only under [display.deleted] keep. An
// unsend before the message came is kept as a tombstone.
func (a *Adapter) takeUnsend(ctx context.Context, c *conn, chat, message string) {
	if a.cache == nil {
		return
	}
	room, id := roomID(c.self, chat), eventID(c.self, chat, message)
	by := ""
	if msg, ok, err := a.cache.MessageByID(ctx, room, id); err == nil && ok {
		by = msg.Sender
	}
	at := time.Now()
	if err := a.cache.MarkRedacted(ctx, room, id, by, "", at, a.keepsDeleted()); err != nil {
		a.log.Warn("mark a message unsent failed", "account", c.account.Name, "err", err)
		return
	}
	if a.onChanged != nil {
		a.onChanged(room)
	}
	emit(a, a.messages, domain.Message{ID: id, RoomID: room, Redacted: true, RedactedBy: by, RedactedAt: at})
	a.recount(ctx, room)
}

// takeReaction puts a sender's reaction on a message, replacing the one they had.
func (a *Adapter) takeReaction(ctx context.Context, c *conn, chat, message string, r *slidetypes.Reaction) {
	if a.cache == nil || r.Reaction == "" {
		return
	}
	reaction := c.reaction(eventID(c.self, chat, message), r)
	if old, had, err := a.cache.DeleteReaction(ctx, reaction.ID); err == nil && had {
		emit(a, a.reactions, domain.ReactionUpdate{Reaction: old, Removed: true})
	}
	if err := a.cache.SaveReactions(ctx, []domain.Reaction{reaction}); err != nil {
		a.log.Warn("cache a reaction failed", "account", c.account.Name, "err", err)
		return
	}
	emit(a, a.reactions, domain.ReactionUpdate{Reaction: reaction})
}

// takeUnreaction takes a sender's reaction off a message. A removal that names only
// Instagram's log ID finds the reaction it kept under it.
func (a *Adapter) takeUnreaction(ctx context.Context, c *conn, chat, message string, r *slidetypes.Reaction) {
	if a.cache == nil {
		return
	}
	id := domain.EventID(string(eventID(c.self, chat, message)) + ":reaction:" + fbid(r.SenderFBID))
	if r.SenderFBID == 0 {
		c.inbox.mu.Lock()
		kept, ok := c.inbox.reactions[r.LogMessageID]
		c.inbox.mu.Unlock()
		if !ok {
			return
		}
		id = kept.ID
	}
	if old, had, err := a.cache.DeleteReaction(ctx, id); err == nil && had {
		emit(a, a.reactions, domain.ReactionUpdate{Reaction: old, Removed: true})
	}
}

// readTo moves a thread's read position forward to at; the cache never moves it back.
func (a *Adapter) readTo(ctx context.Context, c *conn, chat string, at time.Time) {
	if a.cache == nil || at.IsZero() {
		return
	}
	room := roomID(c.self, chat)
	if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, at.UnixMilli()); err != nil {
		a.log.Warn("save a read position failed", "account", c.account.Name, "err", err)
		return
	}
	a.recount(ctx, room)
}

// markUnread keeps Instagram's marked-unread flag on a thread.
func (a *Adapter) markUnread(ctx context.Context, c *conn, chat string, unread bool) {
	if a.cache == nil {
		return
	}
	n := 0
	if unread {
		n = 1
	}
	room := roomID(c.self, chat)
	if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room, Notifications: n}, 0); err != nil {
		a.log.Warn("mark a thread unread failed", "account", c.account.Name, "err", err)
		return
	}
	a.recount(ctx, room)
}

// refreshThread fetches a thread whose name or people changed, and keeps it again.
func (a *Adapter) refreshThread(ctx context.Context, c *conn, client *instameow.Client, chat string) {
	c.inbox.mu.Lock()
	delete(c.inbox.threads, chat)
	c.inbox.mu.Unlock()
	if err := a.ensureThread(ctx, c, client, chat); err != nil {
		a.log.Warn("refresh a thread failed", "account", c.account.Name, "err", err)
	}
}

// forgetThread drops a thread deleted for the account.
func (a *Adapter) forgetThread(ctx context.Context, c *conn, chat string) {
	if a.cache == nil {
		return
	}
	if err := a.cache.ForgetRooms(ctx, []domain.RoomID{roomID(c.self, chat)}); err != nil {
		a.log.Warn("forget a deleted thread failed", "account", c.account.Name, "err", err)
		return
	}
	a.roomsChanged()
}

// takeTyping shows who types in a thread until they stop or the TTL passes. Typing
// names the long thread ID and an IG user ID.
func (a *Adapter) takeTyping(c *conn, t *slidetypes.TypingNotification) {
	c.inbox.mu.Lock()
	chat, known := c.inbox.byLong[t.ThreadID]
	who, met := c.inbox.byIGID[fbid(t.SenderID)]
	c.inbox.mu.Unlock()
	if !known || !met || who == person(c.self) {
		return
	}
	room := roomID(c.self, chat)
	lapse := typingLapse
	if t.TTL > 0 {
		lapse = time.Duration(t.TTL) * time.Millisecond
	}
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	typers := a.typing[room]
	if typers == nil {
		typers = map[string]*time.Timer{}
		a.typing[room] = typers
	}
	if timer := typers[who]; timer != nil {
		timer.Stop()
		delete(typers, who)
	}
	if t.ActivityStatus != 0 {
		typers[who] = time.AfterFunc(lapse, func() {
			a.typingMu.Lock()
			defer a.typingMu.Unlock()
			if a.typing[room][who] != nil {
				delete(a.typing[room], who)
				a.sayTyping(room)
			}
		})
	}
	a.sayTyping(room)
}

// sayTyping emits who types in room now. Caller holds typingMu.
func (a *Adapter) sayTyping(room domain.RoomID) {
	who := make([]string, 0, len(a.typing[room]))
	for id := range a.typing[room] {
		who = append(who, id)
	}
	slices.Sort(who)
	emit(a, a.activity, domain.Activity{RoomID: room, Typing: who})
}

// save is the cache's save, keeping replaced versions under [display.deleted] keep.
func (a *Adapter) save(ctx context.Context, room domain.RoomID, msgs ...domain.Message) error {
	if a.keepsDeleted() {
		return a.cache.SaveMessagesWithRevisions(ctx, room, msgs) //nolint:wrapcheck // the caller says what failed
	}
	return a.cache.SaveMessages(ctx, room, msgs) //nolint:wrapcheck // the caller says what failed
}
