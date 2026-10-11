package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Message IDs are telegram:<self>/<peer>/<id>; Telegram's id is unique per channel,
// or per account for other chats.

// messageID is a message's event ID for account self.
func messageID(self, peerID int64, id int) domain.EventID {
	return inRoom(roomID(self, peerID), id)
}

// markedPeer is a peer's Bot API ID: user as is, basic group negated, channel -100….
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

// incoming converts a Telegram message; false for service messages (joins, renames).
func incoming(self int64, msg tg.MessageClass, ent peer.Entities) (domain.Message, bool) {
	m, ok := msg.(*tg.Message)
	if !ok {
		return domain.Message{}, false
	}
	chat, ok := markedPeer(m.PeerID)
	if !ok {
		return domain.Message{}, false
	}
	topic := topicOf(m)
	room := chatRoom(self, chat, topic)
	body, format, mentions := formatted(m.Message, m.Entities)
	out := domain.Message{
		ID: inRoom(room, m.ID), RoomID: room,
		Body: body, Format: format, Mentions: mentions,
		Timestamp: time.Unix(int64(m.Date), 0), Mentioned: m.Mentioned,
	}
	if poll, ok := m.Media.(*tg.MessageMediaPoll); ok {
		out.Poll = pollOf(&poll.Poll, &poll.Results)
		out.Body, out.Format = out.Poll.Summary(), richtext.Formatted{}
	} else if m.Media != nil {
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
		// Each edit is a version; the cache shows the newest regardless of order.
		out.Edited, out.EditedAt = true, time.Unix(int64(m.EditDate), 0)
		out.RevisionID = domain.EventID(string(out.ID) + "@" + strconv.Itoa(m.EditDate))
	}
	out.Sender, out.SenderName = sender(self, chat, m, ent)
	// A reply to the topic's first message only marks the topic (topics.go).
	if reply, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && reply.ReplyToMsgID != 0 && reply.ReplyToMsgID != topic {
		if other, ok := reply.GetReplyToPeerID(); !ok || samePeer(other, chat) {
			out.ReplyTo = inRoom(room, reply.ReplyToMsgID)
		}
	}
	return out, true
}

// topicOf is a message's forum topic ID from its reply header; 0 for none or General
// (topic 1, the forum's own room).
func topicOf(m *tg.Message) int {
	reply, ok := m.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || !reply.ForumTopic {
		return 0
	}
	topic := reply.ReplyToMsgID
	if top, ok := reply.GetReplyToTopID(); ok && top != 0 {
		topic = top
	}
	return topicNumber(topic)
}

// generalTopic is a forum's General topic, the forum's own room.
const generalTopic = 1

// labeled prefixes the caption with an attachment label, keeping its formatting.
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

// sender is m's sender ID and name: the account, the user, or a channel posting as
// itself; in a private chat, the peer.
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

// record adds the room to the account and caches messages under listing, so no sweep
// can interleave (keptRooms). joined reports whether the room was new.
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

// arrived caches, streams and counts a live message. An unknown chat triggers a
// relist for its name; a cache failure holds the updates position (Store.hold) so the
// message is refetched.
func (a *Adapter) arrived(ctx context.Context, account Account, self int64, msg domain.Message) error {
	if err := a.heardLive(ctx, account, self, msg); errors.Is(err, errNotOurs) {
		return nil
	} else if err != nil {
		return err
	}
	if a.onCached != nil {
		a.onCached(msg)
	}
	a.counted(ctx, msg, msg.Sender == personID(self))
	emit(a, a.messages, msg)
	return nil
}

// heardLive caches a live message or edit (see arrived).
func (a *Adapter) heardLive(ctx context.Context, account Account, self int64, msg domain.Message) error {
	if a.notOurs(self, msg.RoomID) {
		return errNotOurs // a channel left, still followed (following.go)
	}
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

// relist relists an account's dialogs to learn a new chat's name.
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

// emit sends v without blocking; a lagging client misses it and catches up from the
// cache.
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
