package meta

import (
	"context"
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

// Unsending goes the way the message came, like an edit, and only your own can be
// unsent. A plain thread's unsends come as Meta's delete rows, or as the message again
// marked unsent; an encrypted chat's as a revoke. Once unsent a message stays so: the
// cache keeps a mark that a later copy of it does not undo.

// unsentAt is when a message marked unsent was, or now when Meta does not say.
func unsentAt(ms int64) time.Time {
	if ms > 0 {
		return time.UnixMilli(ms)
	}
	return time.Now()
}

// takeUnsends applies a table's unsends of plain messages. Under c.applying.
func (a *Adapter) takeUnsends(ctx context.Context, c *conn, tbl *table.LSTable) {
	in := c.inbox
	for _, d := range tbl.LSDeleteMessage {
		if d.ThreadKey == 0 || d.MessageId == "" {
			continue
		}
		k := in.key(d.ThreadKey)
		room, id := roomID(in.self, k), eventID(in.self, k, d.MessageId)
		var by string
		if a.cache != nil {
			by, _ = a.cache.SenderOf(ctx, room, id) // only its sender can unsend it
		}
		a.unsent(ctx, c, room, id, by, time.Now())
	}
	for _, d := range tbl.LSDeleteThenInsertMessage {
		if !d.IsUnsent || d.ThreadKey == 0 || d.MessageId == "" {
			continue
		}
		k := in.key(d.ThreadKey)
		a.unsent(ctx, c, roomID(in.self, k), eventID(in.self, k, d.MessageId), personID(d.SenderId), unsentAt(d.UnsentTimestampMs))
	}
}

// Redact unsends your message for everyone.
func (a *Adapter) Redact(ctx context.Context, room domain.RoomID, target domain.EventID, _ string) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAThread, room)
	}
	c := a.connOf(self)
	if c == nil {
		return errNotConnected
	}
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
	if author != self {
		return errNotYours
	}
	if plainMessage(mid) {
		err = a.unsendPlain(ctx, c, mid)
	} else {
		c.applying.Lock()
		t := c.inbox.threads[thread]
		group := t != nil && !t.direct()
		c.applying.Unlock()
		err = a.unsendEncrypted(ctx, c, thread, group, mid)
	}
	if err != nil {
		return err
	}
	a.unsent(ctx, c, room, target, personID(self), time.Now())
	return nil
}

// unsendPlain unsends a plain thread's message.
func (a *Adapter) unsendPlain(ctx context.Context, c *conn, mid string) error {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return errNotConnected
	}
	err := a.guard("unsending a message", func() error {
		if err := client.WaitUntilCanSendMessages(ctx, sendWait); err != nil {
			return fmt.Errorf("%w: %w", errNotConnected, err)
		}
		_, err := client.ExecuteTasks(ctx, &socket.DeleteMessageTask{MessageId: mid})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("meta: unsend: %w", err)
	}
	return nil
}

// unsendEncrypted unsends your encrypted message.
func (a *Adapter) unsendEncrypted(ctx context.Context, c *conn, thread int64, group bool, mid string) error {
	c.mu.Lock()
	wa := c.e2ee
	c.mu.Unlock()
	if wa == nil {
		return errNoEncrypted
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
	content := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_ApplicationData{
			ApplicationData: &waConsumerApplication.ConsumerApplication_ApplicationData{
				ApplicationContent: &waConsumerApplication.ConsumerApplication_ApplicationData_Revoke{
					Revoke: &waConsumerApplication.ConsumerApplication_RevokeMessage{Key: msgKey},
				},
			},
		},
	}}
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	err := a.guard("unsending an encrypted message", func() error {
		_, err := wa.SendFBMessage(ctx, chat, content, nil, whatsmeow.SendRequestExtra{ID: id})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("meta: unsend in an encrypted chat: %w", err)
	}
	return nil
}
