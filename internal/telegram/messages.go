package telegram

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// A message is one of a chat's, as one account sees it: telegram:<self>/<peer>/<id>,
// its ID unique within the chat (a channel's), or within the account (the rest).

// messageID is a message of a chat as one account sees it.
func messageID(self, peerID int64, id int) domain.EventID {
	return domain.EventID(string(roomID(self, peerID)) + "/" + strconv.Itoa(id))
}

// markedPeer is a peer's marked ID: a user's, a basic group's negated, a channel's
// -100….
func markedPeer(p tg.PeerClass) (int64, bool) {
	switch p := p.(type) {
	case *tg.PeerUser:
		return p.UserID, true
	case *tg.PeerChat:
		return -p.ChatID, true
	case *tg.PeerChannel:
		return -(channelMark + p.ChannelID), true
	}
	return 0, false
}

// incoming is a Telegram message as kith shows it, its sender named from ent; ok false
// for one kith does not show (a service message: someone joined, a title changed).
func incoming(self int64, msg tg.MessageClass, ent peer.Entities) (domain.Message, bool) {
	if svc, ok := msg.(*tg.MessageService); ok {
		return topicCreated(self, svc, ent)
	}
	m, ok := msg.(*tg.Message)
	if !ok {
		return domain.Message{}, false
	}
	chat, ok := markedPeer(m.PeerID)
	if !ok {
		return domain.Message{}, false
	}
	body, format, mentions := formatted(m.Message, m.Entities)
	out := domain.Message{
		ID: messageID(self, chat, m.ID), RoomID: roomID(self, chat),
		Body: body, Format: format, Mentions: mentions,
		Timestamp: time.Unix(int64(m.Date), 0), Mentioned: m.Mentioned,
	}
	if m.Media != nil {
		media, label := attachment(m.Media)
		switch {
		case media != nil:
			out.Media = media
			if body == "" && media.Type == domain.MediaFile {
				out.Body = media.Name // a file sent bare reads as its name
			}
		case label != "":
			out.Body, out.Format = labeled(label, body, format)
		}
	}
	if m.EditDate != 0 && !m.EditHide {
		// Each edit is a version: the cache shows the newest, whichever order they come.
		out.Edited, out.EditedAt = true, time.Unix(int64(m.EditDate), 0)
		out.RevisionID = domain.EventID(string(out.ID) + "@" + strconv.Itoa(m.EditDate))
	}
	out.Sender, out.SenderName = sender(self, chat, m, ent)
	if reply, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && reply.ReplyToMsgID != 0 {
		topic := 0
		if reply.ForumTopic { // in a forum's topic: the topic's thread (topics.go)
			topic = reply.ReplyToMsgID
			if top, ok := reply.GetReplyToTopID(); ok && top != 0 {
				topic = top
			}
			out.ThreadRoot = messageID(self, chat, topic)
		}
		if other, ok := reply.GetReplyToPeerID(); reply.ReplyToMsgID != topic && (!ok || samePeer(other, chat)) {
			out.ReplyTo = messageID(self, chat, reply.ReplyToMsgID)
		}
	}
	return out, true
}

// labeled is an attachment's label before its caption, the caption's formatting kept.
func labeled(label, caption string, format richtext.Formatted) (string, richtext.Formatted) {
	switch {
	case caption == "":
		return label, richtext.Formatted{}
	case format.IsZero():
		return label + " " + caption, format
	}
	f := richtext.FromMarkup(richtext.Sanitize(html.EscapeString(label+" ") + format.Markup()))
	return f.Text(), f
}

// samePeer reports whether p is the chat with marked ID chat.
func samePeer(p tg.PeerClass, chat int64) bool {
	id, ok := markedPeer(p)
	return ok && id == chat
}

// sender is who sent m in chat, and their name: the account itself for its own; the
// user, or the channel posting as itself, it is from; in a private chat, the person.
func sender(self, chat int64, m *tg.Message, ent peer.Entities) (string, string) {
	switch {
	case m.Out:
		if u, ok := ent.User(self); ok {
			return personID(self), personName(u)
		}
		return personID(self), ""
	case m.FromID != nil:
		switch from := m.FromID.(type) {
		case *tg.PeerUser:
			if u, ok := ent.User(from.UserID); ok {
				return personID(from.UserID), personName(u)
			}
			return personID(from.UserID), ""
		case *tg.PeerChannel:
			if c, ok := ent.Channel(from.ChannelID); ok {
				return personID(-(channelMark + from.ChannelID)), c.Title
			}
			return personID(-(channelMark + from.ChannelID)), ""
		}
	case chat > 0: // a private chat: the person is the sender
		if u, ok := ent.User(chat); ok {
			return personID(chat), personName(u)
		}
		return personID(chat), ""
	}
	if c, ok := ent.Channel(-chat - channelMark); ok { // a channel's post
		return personID(chat), c.Title
	}
	return personID(chat), ""
}

// record caches messages of one room, the room made one of the account's first, where
// no listing's sweep can come between the two (see keptRooms). joined reports whether
// the room was not one of the account's before.
func (a *Adapter) record(ctx context.Context, self int64, room domain.RoomID, msgs []domain.Message) (joined bool, err error) {
	if a.cache == nil {
		return false, nil
	}
	a.listing.Lock()
	defer a.listing.Unlock()
	a.heard[room] = time.Now()
	n, err := a.cache.JoinRooms(ctx, domain.AccountRooms(domain.ProtocolTelegram, strconv.FormatInt(self, 10)), []domain.RoomID{room})
	if err != nil {
		return false, fmt.Errorf("telegram: join %s: %w", room, err)
	}
	save := a.cache.SaveMessages
	if a.keepsDeleted() {
		save = a.cache.SaveMessagesWithRevisions // an edit keeps what it replaced
	}
	if err := save(ctx, room, msgs); err != nil {
		return false, fmt.Errorf("telegram: cache %d messages of %s: %w", len(msgs), room, err)
	}
	return n > 0, nil
}

// arrived caches a message heard live and hands it to the clients, and counts it
// unread (Telegram does). A chat the cache did not know (one just begun) is made one
// of the account's rooms, and the listing asked again for its name. A message that
// could not be cached holds the account's updates position (Store.hold), so it is
// asked for again.
func (a *Adapter) arrived(ctx context.Context, account Account, self int64, msg domain.Message) error {
	if err := a.heardLive(ctx, account, self, msg); err != nil {
		return err
	}
	if a.onCached != nil {
		a.onCached(msg)
	}
	a.counted(ctx, msg, msg.Sender == personID(self))
	emit(a, a.messages, msg)
	return nil
}

// heardLive caches a message or a version of one heard live (see arrived).
func (a *Adapter) heardLive(ctx context.Context, account Account, self int64, msg domain.Message) error {
	joined, err := a.record(ctx, self, msg.RoomID, []domain.Message{msg})
	if err != nil {
		if a.store != nil {
			a.store.hold(self)
		}
		a.log.Warn("cache a message failed; it is asked for again on reconnecting", "account", account.Name, "err", err)
		return err
	}
	if joined {
		go a.relist(context.WithoutCancel(ctx), account)
	}
	return nil
}

// relist lists an account's dialogs again, for a chat a message came to first.
func (a *Adapter) relist(ctx context.Context, account Account) {
	for _, c := range a.connected() {
		if c.account.Name != account.Name {
			continue
		}
		if _, err := a.list(ctx, c.account, c.gen, c.user, c.client); err != nil {
			a.log.Warn("list the chats again failed", "account", account.Name, "err", err)
		}
	}
}

// emit hands v to a stream without blocking: a client that is not reading misses it,
// as with the other adapters (it reads the cache when it catches up).
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
