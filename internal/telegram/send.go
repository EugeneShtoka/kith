package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Send posts a text message with entities, mentions and reply. The random ID comes
// from the draft's transaction, so Telegram dedups a resend. The message is cached
// and streamed here; the answer goes through the updates manager to keep pts in step.
func (a *Adapter) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	if draft.Edits != "" {
		return a.edit(ctx, ch, roomID, draft)
	}
	text, entities, format := outgoing(draft, a.userHash(ctx, ch.conn))
	req := &tg.MessagesSendMessageRequest{Peer: ch.peer, Message: text, RandomID: randomID(draft.TxnID)}
	if len(entities) > 0 {
		req.SetEntities(entities)
	}
	if to, ok := replyTo(ch, draft); ok {
		req.SetReplyTo(to)
	}
	res, err := ch.conn.client.API().MessagesSendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("telegram: send to %s: %w", roomID, err)
	}
	id, date := sentAs(res, req.RandomID)
	if ch.conn.live != nil {
		_ = ch.conn.live.manager.Handle(ctx, res) // its position moves past what was sent
	}
	if id == 0 {
		return nil // sent; it arrives through the updates
	}
	sent := domain.Message{
		ID: inRoom(roomID, id), RoomID: roomID, Sender: personID(ch.conn.user),
		Body: text, Format: format, Mentions: draft.LiveMentions(), Timestamp: date,
	}
	if _, ok := messageNumber(roomID, draft.ReplyTo); ok {
		sent.ReplyTo = draft.ReplyTo
	}
	return a.arrived(ctx, ch.conn.account, ch.conn.user, sent)
}

// userHash is a user's access hash on c, from a listing or an update.
func (a *Adapter) userHash(ctx context.Context, c conn) func(user int64) (int64, bool) {
	return func(user int64) (int64, bool) {
		p, err := a.inputPeer(ctx, c, user)
		if u, ok := p.(*tg.InputPeerUser); ok && err == nil {
			return u.AccessHash, true
		}
		return 0, false
	}
}

// updatedMessages are the new or edited messages in an answer's updates.
func updatedMessages(res tg.UpdatesClass) []tg.MessageClass {
	var out []tg.MessageClass
	for _, u := range updatesIn(res) {
		switch u := u.(type) {
		case *tg.UpdateNewMessage:
			out = append(out, u.Message)
		case *tg.UpdateNewChannelMessage:
			out = append(out, u.Message)
		case *tg.UpdateEditMessage:
			out = append(out, u.Message)
		case *tg.UpdateEditChannelMessage:
			out = append(out, u.Message)
		}
	}
	return out
}

// peerEntities are the entities in an answer's updates.
func peerEntities(res tg.UpdatesClass) peer.Entities {
	switch r := res.(type) {
	case *tg.Updates:
		return peer.EntitiesFromResult(r)
	case *tg.UpdatesCombined:
		return peer.EntitiesFromResult(r)
	}
	return peer.Entities{}
}

// replyTo is a draft's reply header: its reply target, within its topic (the topic's
// message when not replying); false for neither.
func replyTo(ch chat, draft domain.Draft) (*tg.InputReplyToMessage, bool) {
	reply, replies := messageNumber(ch.room(), draft.ReplyTo)
	switch {
	case ch.topic != 0 && replies:
		return &tg.InputReplyToMessage{ReplyToMsgID: reply, TopMsgID: ch.topic}, true
	case ch.topic != 0:
		return &tg.InputReplyToMessage{ReplyToMsgID: ch.topic, TopMsgID: ch.topic}, true
	case replies:
		return &tg.InputReplyToMessage{ReplyToMsgID: reply}, true
	}
	return nil, false
}

// randomID hashes the transaction so a retry dedups; random without one. Never zero.
func randomID(txn string) int64 {
	if txn != "" {
		h := fnv.New64a()
		_, _ = h.Write([]byte(txn))
		if id := int64(h.Sum64()); id != 0 { //nolint:gosec // a bit pattern, not a quantity
			return id
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]) | 1) //nolint:gosec // as above
}

// messageNumber is event's message ID in roomID; false if it isn't in that room.
func messageNumber(roomID domain.RoomID, event domain.EventID) (int, bool) {
	rest, ok := strings.CutPrefix(string(event), string(roomID)+"/")
	if !ok {
		return 0, false
	}
	id, err := strconv.Atoi(rest)
	return id, err == nil && id > 0
}

// sentAs is the ID and date Telegram gave the sent message, from a short answer (DMs)
// or the carrying update; 0 if absent.
func sentAs(res tg.UpdatesClass, random int64) (int, time.Time) {
	switch r := res.(type) {
	case *tg.UpdateShortSentMessage:
		return r.ID, time.Unix(int64(r.Date), 0)
	case *tg.Updates:
		return fromUpdates(r.Updates, random, r.Date)
	case *tg.UpdatesCombined:
		return fromUpdates(r.Updates, random, r.Date)
	}
	return 0, time.Time{}
}

// fromUpdates finds random's message ID and date in updates.
func fromUpdates(updates []tg.UpdateClass, random int64, date int) (int, time.Time) {
	id := 0
	for _, u := range updates {
		if m, ok := u.(*tg.UpdateMessageID); ok && m.RandomID == random {
			id = m.ID
		}
	}
	for _, u := range updates {
		var m tg.MessageClass
		switch u := u.(type) {
		case *tg.UpdateNewMessage:
			m = u.Message
		case *tg.UpdateNewChannelMessage:
			m = u.Message
		}
		if msg, ok := m.(*tg.Message); ok && msg.ID == id {
			date = msg.Date
		}
	}
	return id, time.Unix(int64(date), 0)
}
