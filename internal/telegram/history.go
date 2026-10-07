package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat's history is Telegram's to page through, newest first: a page is cached as it
// is read, so what was scrolled through once opens from the cache.

// errNoPeer is a chat whose access hash no listing or update has revealed yet.
var errNoPeer = errors.New("telegram: that chat is not known to its account yet")

// chat is a room's account connection and the peer that names it to Telegram, and the
// forum topic the room is (0 for a chat's own room).
type chat struct {
	conn  conn
	peer  tg.InputPeerClass
	id    int64 // marked
	topic int
}

// room is the room the chat is.
func (c chat) room() domain.RoomID { return chatRoom(c.conn.user, c.id, c.topic) }

// chatOf is roomID's account connection and peer, ErrNetworkOff when the account is not
// connected.
func (a *Adapter) chatOf(ctx context.Context, roomID domain.RoomID) (chat, error) {
	parsed := domain.ParseID(string(roomID))
	native, topicPart, inTopic := strings.Cut(parsed.Native, topicSep)
	self, err1 := strconv.ParseInt(parsed.Account, 10, 64)
	id, err2 := strconv.ParseInt(native, 10, 64)
	topic, err3 := 0, error(nil)
	if inTopic {
		topic, err3 = strconv.Atoi(topicPart)
	}
	if parsed.Network != domain.ProtocolTelegram || err1 != nil || err2 != nil || err3 != nil ||
		strings.Contains(parsed.Native, "/") || (inTopic && topic <= 0) {
		return chat{}, fmt.Errorf("telegram: %s is no Telegram chat", roomID)
	}
	i := slices.IndexFunc(a.connected(), func(c conn) bool { return c.user == self })
	if i < 0 {
		return chat{}, fmt.Errorf("telegram: %s's account: %w", roomID, api.ErrNetworkOff)
	}
	c := a.connected()[i]
	p, err := a.inputPeer(ctx, c, id)
	if err != nil {
		return chat{}, err
	}
	return chat{conn: c, peer: p, id: id, topic: topic}, nil
}

// inputPeer is how calls name the chat with marked ID id, on connection c: by the
// access hash its listing revealed, else the one the store kept from an update.
func (a *Adapter) inputPeer(ctx context.Context, c conn, id int64) (tg.InputPeerClass, error) {
	switch {
	case id == c.user:
		return &tg.InputPeerSelf{}, nil
	case id > 0:
		hash, ok := c.hashes.users[id]
		if !ok && a.store != nil {
			hash, ok, _ = a.store.GetUserAccessHash(ctx, c.user, id)
		}
		if !ok {
			return nil, errNoPeer
		}
		return &tg.InputPeerUser{UserID: id, AccessHash: hash}, nil
	case id > -channelMark:
		return &tg.InputPeerChat{ChatID: -id}, nil
	}
	channel := -id - channelMark
	hash, ok := c.hashes.channels[channel]
	if !ok && a.store != nil {
		hash, ok, _ = a.store.GetChannelAccessHash(ctx, c.user, channel)
	}
	if !ok {
		return nil, errNoPeer
	}
	return &tg.InputPeerChannel{ChannelID: channel, AccessHash: hash}, nil
}

// Timeline is a page of a chat's history, oldest first, from where from left off ("" for
// the newest); Next continues it, "" at the beginning.
func (a *Adapter) Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return domain.TimelinePage{}, err
	}
	read := time.Now()
	res, err := a.historyPage(ctx, ch, from, limit)
	if err != nil {
		return domain.TimelinePage{}, err
	}
	raw, ent, ok := messagesOf(res)
	if !ok {
		return domain.TimelinePage{}, nil
	}
	msgs := a.cachePage(ctx, ch, raw, ent)
	a.pageReactions(ctx, ch.conn.user, raw, read)
	page := domain.TimelinePage{Messages: msgs}
	if len(raw) == limit && len(raw) > 0 {
		page.Next = strconv.Itoa(raw[len(raw)-1].GetID()) // the oldest: Telegram answers newest first
	}
	return page, nil
}

// historyPage is a page of a room's history as Telegram answers it, newest first: a
// chat's, or a forum topic's (topicReplies).
func (a *Adapter) historyPage(ctx context.Context, ch chat, from string, limit int) (tg.MessagesMessagesClass, error) {
	if ch.topic != 0 {
		return a.topicReplies(ctx, ch, from, limit)
	}
	req := &tg.MessagesGetHistoryRequest{Peer: ch.peer, Limit: limit}
	if from != "" {
		var err error
		if req.OffsetID, err = strconv.Atoi(from); err != nil {
			return nil, fmt.Errorf("telegram: %q is no place in %s's history", from, ch.room())
		}
	}
	res, err := ch.conn.client.API().MessagesGetHistory(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("telegram: history of %s: %w", ch.room(), err)
	}
	return res, nil
}

// messagesOf is a messages answer's messages, newest first, and what they name.
func messagesOf(res tg.MessagesMessagesClass) ([]tg.MessageClass, peer.Entities, bool) {
	switch r := res.(type) {
	case *tg.MessagesMessages:
		return r.Messages, peer.EntitiesFromResult(r), true
	case *tg.MessagesMessagesSlice:
		return r.Messages, peer.EntitiesFromResult(r), true
	case *tg.MessagesChannelMessages:
		return r.Messages, peer.EntitiesFromResult(r), true
	}
	return nil, peer.Entities{}, false
}

// cachePage is a page of a chat's history as kith keeps it, oldest first, cached as
// history: nothing is streamed or notified.
func (a *Adapter) cachePage(ctx context.Context, ch chat, raw []tg.MessageClass, ent peer.Entities) []domain.Message {
	var msgs []domain.Message
	for _, m := range slices.Backward(raw) {
		if msg, ok := incoming(ch.conn.user, m, ent); ok && msg.RoomID == ch.room() {
			msgs = append(msgs, msg)
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	if _, err := a.record(ctx, ch.conn.user, msgs[0].RoomID, msgs); err != nil {
		a.log.Warn("cache a page of history failed", "room", msgs[0].RoomID, "err", err)
	}
	return msgs
}

// FetchEvent is one message, asked of Telegram (a reply's original, a link to one).
func (a *Adapter) FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return domain.Message{}, err
	}
	rest, ok := strings.CutPrefix(string(eventID), string(roomID)+"/")
	id, err := strconv.Atoi(rest)
	if !ok || err != nil {
		return domain.Message{}, fmt.Errorf("telegram: %s is no message of %s", eventID, roomID)
	}
	m, ent, err := a.fetchRaw(ctx, ch, id)
	if err != nil {
		return domain.Message{}, fmt.Errorf("telegram: fetch %s: %w", eventID, err)
	}
	if msg, ok := incoming(ch.conn.user, m, ent); ok && msg.ID == eventID {
		if _, err := a.record(ctx, ch.conn.user, roomID, []domain.Message{msg}); err != nil {
			a.log.Warn("cache a fetched message failed", "room", roomID, "err", err)
		}
		return msg, nil
	}
	return domain.Message{}, fmt.Errorf("telegram: %s is gone", eventID)
}

// errGone is a message Telegram no longer has.
var errGone = errors.New("telegram: the message is gone")

// fetchRaw is message id of a chat as Telegram gives it now, and what it names.
func (a *Adapter) fetchRaw(ctx context.Context, ch chat, id int) (tg.MessageClass, peer.Entities, error) {
	var res tg.MessagesMessagesClass
	var err error
	if channel, ok := ch.peer.(*tg.InputPeerChannel); ok {
		res, err = ch.conn.client.API().ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash},
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: id}},
		})
	} else {
		res, err = ch.conn.client.API().MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: id}})
	}
	if err != nil {
		return nil, peer.Entities{}, err //nolint:wrapcheck // the caller names the message
	}
	raw, ent, _ := messagesOf(res)
	for _, m := range raw {
		if msg, ok := m.(*tg.Message); ok && msg.ID == id && samePeer(msg.PeerID, ch.id) {
			return m, ent, nil
		}
	}
	return nil, peer.Entities{}, errGone
}
