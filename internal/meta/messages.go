package meta

import (
	"context"
	"slices"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Messages come in two kinds. Live ones (LSInsertMessage) are cached, announced and
// streamed. The ones a page or a sync carries (LSUpsertMessage) are history: cached
// quietly, never announced, so loading the inbox notifies of nothing old.

// thumbsUp are the sticker IDs of Messenger's like button, which reads as 👍.
var thumbsUp = []int64{369239263222822, 369239343222814, 369239383222810}

// message is a Meta message as kith's, false for what is not shown: an admin notice
// ("X named the group"), an unsent message, or one with nothing to show.
func (in *inbox) message(m *table.WrappedMessage) (domain.Message, bool) {
	if m.IsAdminMessage || m.IsUnsent {
		return domain.Message{}, false
	}
	parts := []string{}
	if text := strings.TrimSpace(m.Text); text != "" {
		parts = append(parts, m.Text)
	}
	parts = append(parts, attachmentsOf(m)...)
	if len(parts) == 0 {
		return domain.Message{}, false
	}
	msg := domain.Message{
		ID: eventID(in.self, m.ThreadKey, m.MessageId), RoomID: roomID(in.self, m.ThreadKey),
		Sender: personID(m.SenderId), SenderName: in.people[m.SenderId],
		Body: strings.Join(parts, "\n"), Timestamp: time.UnixMilli(m.TimestampMs),
		Edited: m.EditCount > 0,
	}
	if m.ReplySourceId != "" {
		msg.ReplyTo = eventID(in.self, m.ThreadKey, m.ReplySourceId)
	}
	return msg, true
}

// attachmentsOf names what a message carries besides its text, until files and
// pictures are shown.
func attachmentsOf(m *table.WrappedMessage) []string {
	var out []string
	for _, b := range m.BlobAttachments {
		out = append(out, attachmentLabel(b.AttachmentType, b.Filename))
	}
	for _, at := range m.Attachments {
		out = append(out, attachmentLabel(at.AttachmentType, at.Filename))
	}
	for _, x := range m.XMAAttachments {
		if x.LSInsertXmaAttachment == nil {
			continue
		}
		switch {
		case x.TitleText != "" && x.ActionUrl != "":
			out = append(out, x.TitleText+" "+x.ActionUrl)
		case x.ActionUrl != "":
			out = append(out, x.ActionUrl)
		case x.TitleText != "":
			out = append(out, "["+x.TitleText+"]")
		}
	}
	for range m.Stickers {
		out = append(out, "[sticker]")
	}
	if m.StickerId != 0 && len(m.Stickers) == 0 {
		if slices.Contains(thumbsUp, m.StickerId) {
			out = append(out, "👍")
		} else {
			out = append(out, "[sticker]")
		}
	}
	return out
}

func attachmentLabel(kind table.AttachmentType, name string) string {
	switch kind {
	case table.AttachmentTypeImage, table.AttachmentTypeEphemeralImage:
		return "[photo]"
	case table.AttachmentTypeAnimatedImage:
		return "[GIF]"
	case table.AttachmentTypeVideo, table.AttachmentTypeEphemeralVideo:
		return "[video]"
	case table.AttachmentTypeAudio, table.AttachmentTypeSoundBite:
		return "[voice message]"
	case table.AttachmentTypeSticker, table.AttachmentTypeSelfieSticker, table.AttachmentTypeThirdPartySticker:
		return "[sticker]"
	case table.AttachmentTypeNone, table.AttachmentTypeFile, table.AttachmentTypeXMA,
		table.AttachmentTypeCatalogItem, table.AttachmentTypePowerUp:
	}
	if name != "" {
		return "[file: " + name + "]"
	}
	return "[attachment]"
}

// store writes what one table changed into the cache and the streams: people, rooms
// and members, read marks, then messages. Live messages are announced; history is not.
func (a *Adapter) store(ctx context.Context, c *conn, ch change) {
	if a.cache == nil {
		return
	}
	in := c.inbox
	if ch.people {
		a.keepPeople(ctx, c)
	}
	roomsChanged := len(ch.gone) > 0
	for _, key := range ch.threads {
		t := in.threads[key]
		if t == nil {
			continue
		}
		c.rooms[roomID(in.self, key)] = in.room(t)
		if err := a.cache.SaveMembers(ctx, roomID(in.self, key), in.roomMembers(t)); err != nil {
			a.log.Warn("cache a thread's members failed", "account", c.account.Name, "err", err)
		}
		roomsChanged = true
	}
	for _, key := range ch.gone {
		delete(c.rooms, roomID(in.self, key))
	}
	if roomsChanged {
		rooms := make([]domain.Room, 0, len(c.rooms))
		for id := range c.rooms {
			rooms = append(rooms, c.rooms[id])
		}
		if len(rooms) > 0 {
			if err := a.cache.SaveRooms(ctx, owner(in.self), rooms); err != nil {
				a.log.Warn("cache the threads failed", "account", c.account.Name, "err", err)
			}
		} else if len(ch.gone) > 0 {
			a.forgetRooms(ctx, c, ch.gone)
		}
		a.roomsChanged()
	}
	counted := map[domain.RoomID]bool{}
	for _, key := range ch.threads {
		if t := in.threads[key]; t != nil {
			a.saveThreadUnread(ctx, t, c)
			counted[roomID(in.self, key)] = true
		}
	}
	for key, at := range ch.read {
		if !counted[roomID(in.self, key)] {
			a.readTo(ctx, roomID(in.self, key), time.UnixMilli(at))
			counted[roomID(in.self, key)] = true
		}
	}
	a.storeMessages(ctx, c, ch.messages, counted)
	for room := range counted {
		a.recount(ctx, room)
	}
}

// forgetRooms drops threads Meta took away when none is left to save over them.
func (a *Adapter) forgetRooms(ctx context.Context, c *conn, gone []int64) {
	ids := make([]domain.RoomID, 0, len(gone))
	for _, key := range gone {
		ids = append(ids, roomID(c.inbox.self, key))
	}
	if err := a.cache.ForgetRooms(ctx, ids); err != nil {
		a.log.Warn("forget gone threads failed", "account", c.account.Name, "err", err)
	}
}

// saveThreadUnread keeps a thread's unread state: Meta's count (or, without one,
// whether anything is newer than your read mark), and its read mark, which only moves
// the cached position forward. A thread read in kith stays read whatever Meta says.
func (a *Adapter) saveThreadUnread(ctx context.Context, t *thread, c *conn) {
	n := int(max(t.unread, 0))
	if t.unread < 0 && t.lastActive > t.readTo {
		n = 1
	}
	u := domain.Unread{RoomID: roomID(c.inbox.self, t.key), Notifications: n}
	if err := a.cache.SaveUnread(ctx, u, t.readTo); err != nil {
		a.log.Warn("save a thread's unread state failed", "account", c.account.Name, "err", err)
	}
}

// storeMessages caches a table's messages room by room, announcing and streaming the
// live ones.
func (a *Adapter) storeMessages(ctx context.Context, c *conn, msgs []*table.WrappedMessage, counted map[domain.RoomID]bool) {
	byRoom := map[domain.RoomID][]domain.Message{}
	var live []domain.Message
	for _, m := range msgs {
		msg, ok := c.inbox.message(m)
		if !ok {
			continue
		}
		byRoom[msg.RoomID] = append(byRoom[msg.RoomID], msg)
		if !m.IsUpsert {
			live = append(live, msg)
		}
	}
	a.cacheMessages(ctx, c, byRoom, live, counted)
}

// cacheMessages caches messages by room, then announces and streams live, the ones
// among them that arrived now.
func (a *Adapter) cacheMessages(ctx context.Context, c *conn, byRoom map[domain.RoomID][]domain.Message, live []domain.Message, counted map[domain.RoomID]bool) {
	save := a.cache.SaveMessages
	if a.keepsDeleted() {
		save = a.cache.SaveMessagesWithRevisions // an edit keeps what it replaced
	}
	saved := map[domain.RoomID]bool{}
	for room, batch := range byRoom {
		if err := save(ctx, room, batch); err != nil {
			a.log.Warn("cache messages failed", "account", c.account.Name, "room", room, "count", len(batch), "err", err)
			continue
		}
		saved[room] = true
		counted[room] = true
	}
	for i := range live {
		if !saved[live[i].RoomID] {
			continue
		}
		if a.onCached != nil {
			a.onCached(live[i])
		}
		emit(a, a.messages, live[i])
	}
}

// keepPeople gives the directory the people this account knows.
func (a *Adapter) keepPeople(ctx context.Context, c *conn) {
	names := make([]domain.PersonName, 0, len(c.inbox.people))
	for id, name := range c.inbox.people {
		names = append(names, domain.PersonName{ID: personID(id), Name: name, Rank: domain.RankChosen})
	}
	if err := a.cache.SetPeople(ctx, "messenger:"+itoa(c.inbox.self), names, nil); err != nil {
		a.log.Warn("keep who people are failed", "account", c.account.Name, "err", err)
	}
}

// readTo moves a room's read position forward to at; the cache never moves it back.
func (a *Adapter) readTo(ctx context.Context, room domain.RoomID, at time.Time) {
	if a.cache == nil {
		return
	}
	if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, at.UnixMilli()); err != nil {
		a.log.Warn("save a read position failed", "room", room, "err", err)
	}
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
