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

// Send posts a text message: Markdown as Telegram's entities, the people it names as
// mentions, a reply as one. Telegram drops a second send with the same random ID, which
// is derived from the draft's transaction, so sending a draft twice delivers it once.
// The message is cached and handed to the clients here; Telegram's answer goes to the
// account's updates, which keep their position in step with it.
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
	if reply, ok := messageNumber(roomID, draft.ReplyTo); ok {
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: reply})
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
		ID: messageID(ch.conn.user, ch.id, id), RoomID: roomID, Sender: personID(ch.conn.user),
		Body: text, Format: format, Mentions: draft.LiveMentions(), Timestamp: date,
	}
	if _, ok := messageNumber(roomID, draft.ReplyTo); ok {
		sent.ReplyTo = draft.ReplyTo
	}
	return a.arrived(ctx, ch.conn.account, ch.conn.user, sent)
}

// userHash is how a draft names people to Telegram over connection c: by the access
// hash a listing or an update revealed.
func (a *Adapter) userHash(ctx context.Context, c conn) func(user int64) (int64, bool) {
	return func(user int64) (int64, bool) {
		p, err := a.inputPeer(ctx, c, user)
		if u, ok := p.(*tg.InputPeerUser); ok && err == nil {
			return u.AccessHash, true
		}
		return 0, false
	}
}

// updatedMessages are the messages an answer's updates carry, new or edited.
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

// peerEntities are the users and chats an answer's updates name.
func peerEntities(res tg.UpdatesClass) peer.Entities {
	switch r := res.(type) {
	case *tg.Updates:
		return peer.EntitiesFromResult(r)
	case *tg.UpdatesCombined:
		return peer.EntitiesFromResult(r)
	}
	return peer.Entities{}
}

// randomID is a send's random ID: the transaction's hash, so a retried send is the same
// one; random when there is no transaction. Never zero.
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

// messageNumber is event's message ID within roomID; false when it is no message of it.
func messageNumber(roomID domain.RoomID, event domain.EventID) (int, bool) {
	rest, ok := strings.CutPrefix(string(event), string(roomID)+"/")
	if !ok {
		return 0, false
	}
	id, err := strconv.Atoi(rest)
	return id, err == nil && id > 0
}

// sentAs is the ID and time Telegram gave the message sent with random ID random: a
// private chat answers in short, the rest with the update that carries it. 0 when the
// answer does not say.
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

// fromUpdates is the ID the updates gave random's message, and its date.
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
