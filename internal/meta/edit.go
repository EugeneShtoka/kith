package meta

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An edit goes the way its message came, like a reaction. A plain thread's edits come
// as Meta's edit rows, which name the message but not its thread and carry no time,
// only Meta's count of the message's edits: one is found by its ID in the cache and
// dated as the edit it follows, so the count alone orders them, and an echo of an
// older edit of yours never undoes a newer one. Yours is applied at once.

var (
	errNotYours    = errors.New("meta: only your own messages can be edited or unsent")
	errEditRefused = errors.New("meta: Meta did not take the edit")
)

// editRevision is the version of a plain message its count-th edit makes; padded, so
// two dated alike still order by count.
func editRevision(msg domain.EventID, count int64) domain.EventID {
	return domain.EventID(fmt.Sprintf("%s#edit%06d", msg, count))
}

// takeEdits applies a table's edits of plain messages. Under c.applying.
func (a *Adapter) takeEdits(ctx context.Context, c *conn, tbl *table.LSTable) {
	if a.cache == nil || len(tbl.LSEditMessage) == 0 {
		return
	}
	for _, e := range tbl.LSEditMessage {
		if e.MessageID == "" {
			continue
		}
		found, err := a.cache.MessagesEndingIn(ctx, owner(c.metaID), []string{e.MessageID})
		if err != nil {
			a.log.Warn("find an edited message failed", "account", c.account.Name, "err", err)
			continue
		}
		for i := range found {
			msg, ok, err := a.cache.MessageByID(ctx, found[i].RoomID, found[i].ID)
			if err != nil || !ok {
				continue // a message not cached: its listing will carry the edited text
			}
			msg.Body, msg.Edited = e.Text, true
			msg.RevisionID = editRevision(msg.ID, e.EditCount)
			a.edited(ctx, c, msg)
		}
	}
}

// sendEdit replaces the text of your message target in room with body.
func (a *Adapter) sendEdit(ctx context.Context, c *conn, room domain.RoomID, thread int64, target domain.EventID, body string) error {
	if a.cache == nil {
		return errNotCached
	}
	msg, found, err := a.cache.MessageByID(ctx, room, target)
	if err != nil {
		return fmt.Errorf("meta: read %s: %w", target, err)
	}
	if !found {
		return fmt.Errorf("%w: %s", errNotCached, target)
	}
	mid, author, err := replyParts(&msg)
	if err != nil {
		return err
	}
	if author != c.metaID {
		return errNotYours
	}
	msg.Body, msg.Edited = body, true
	if plainMessage(mid) {
		count, err := a.editPlain(ctx, c, mid, body)
		if err != nil {
			return err
		}
		msg.RevisionID = editRevision(msg.ID, count)
	} else {
		c.applying.Lock()
		t := c.inbox.threads[thread]
		group := t != nil && !t.direct()
		c.applying.Unlock()
		id, err := a.editEncrypted(ctx, c, thread, group, mid, body)
		if err != nil {
			return err
		}
		msg.RevisionID, msg.EditedAt = eventID(c.metaID, thread, id), time.Now()
	}
	a.edited(ctx, c, msg)
	return nil
}

// editPlain sends an edit of a plain thread's message and returns its edit count, from
// Meta's answer. An answer with other text is Meta not taking it (or not yet: a later
// row may correct it, and is taken in as any edit).
func (a *Adapter) editPlain(ctx context.Context, c *conn, mid, body string) (int64, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return 0, errNotConnected
	}
	var resp *table.LSTable
	err := a.guard("sending an edit", func() error {
		if err := client.WaitUntilCanSendMessages(ctx, sendWait); err != nil {
			return fmt.Errorf("%w: %w", errNotConnected, err)
		}
		var err error
		resp, err = client.ExecuteTasks(ctx, &socket.EditMessageTask{MessageID: mid, Text: body})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return 0, fmt.Errorf("meta: edit: %w", err)
	}
	if resp != nil {
		for _, e := range resp.LSEditMessage {
			if e.MessageID == mid && e.Text == body {
				return e.EditCount, nil
			}
		}
	}
	return 0, errEditRefused
}

// editEncrypted sends an edit of your encrypted message and returns the edit's ID.
func (a *Adapter) editEncrypted(ctx context.Context, c *conn, thread int64, group bool, mid, body string) (string, error) {
	c.mu.Lock()
	wa := c.e2ee
	c.mu.Unlock()
	if wa == nil {
		return "", errNoEncrypted
	}
	chat := types.JID{User: itoa(thread), Server: types.MessengerServer}
	if group {
		chat.Server = types.GroupServer
	}
	remote, fromMe := chat.String(), true
	msgKey := &waCommon.MessageKey{RemoteJID: &remote, ID: &mid, FromMe: &fromMe}
	if group {
		participant := types.JID{User: itoa(c.metaID), Server: types.MessengerServer}.String()
		msgKey.Participant = &participant
	}
	at := time.Now().UnixMilli()
	content := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
			Content: &waConsumerApplication.ConsumerApplication_Content_EditMessage{
				EditMessage: &waConsumerApplication.ConsumerApplication_EditMessage{
					Key: msgKey, Message: &waCommon.MessageText{Text: &body}, TimestampMS: &at,
				},
			},
		}},
	}}
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	err := a.guard("sending an encrypted edit", func() error {
		_, err := wa.SendFBMessage(ctx, chat, content, nil, whatsmeow.SendRequestExtra{ID: id})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return "", fmt.Errorf("meta: edit in an encrypted chat: %w", err)
	}
	return id, nil
}
