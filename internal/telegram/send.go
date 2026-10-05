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

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Send posts a text message: Markdown as Telegram's entities, the people it names as
// mentions, a reply as one. Telegram drops a second send with the same random ID, which
// is derived from the draft's transaction, so sending a draft twice delivers it once.
// The message is cached and handed to the clients here; Telegram's answer goes to the
// account's updates, which keep their position in step with it.
func (a *Adapter) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	if draft.Edits != "" {
		return fmt.Errorf("%w: editing a Telegram message comes in a later release", api.ErrNotOnNetwork)
	}
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	text, entities, format := outgoing(draft, func(user int64) (int64, bool) {
		p, perr := a.inputPeer(ctx, ch.conn, user)
		if u, ok := p.(*tg.InputPeerUser); ok && perr == nil {
			return u.AccessHash, true
		}
		return 0, false
	})
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
