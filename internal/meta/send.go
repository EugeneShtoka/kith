package meta

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Sending goes the way the chat's messages come: a plain thread's as a Messenger task
// (its answer names the message), an encrypted chat's over the encrypted connection.
// The sent message is cached at once, so it shows without waiting for Meta's echo,
// which carries the same ID and merges into it.

// sendWait bounds waiting for a connection to be able to send.
const sendWait = 15 * time.Second

var (
	errNotConnected  = fmt.Errorf("%w: the Messenger account is not connected", api.ErrNetworkOff)
	errNoEncrypted   = fmt.Errorf("%w: encrypted chats are not connected yet", api.ErrNetworkOff)
	errEditsNotYet   = errors.New("meta: editing a sent message is not supported yet")
	errSendRefused   = errors.New("meta: Meta refused the message")
	errEmptyMessage  = errors.New("meta: an empty message")
	errUnknownTarget = errors.New("meta: the message replied to is not a Messenger message")
)

// Send sends a draft's text, as a reply when it is one.
func (a *Adapter) Send(ctx context.Context, room domain.RoomID, draft domain.Draft) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAThread, room)
	}
	if draft.Edits != "" {
		return errEditsNotYet
	}
	body := draft.Body
	if draft.Emote {
		body = "/me " + body
	}
	if strings.TrimSpace(body) == "" {
		return errEmptyMessage
	}
	c := a.connOf(self)
	if c == nil {
		return errNotConnected
	}
	c.applying.Lock()
	t := c.inbox.threads[thread]
	encrypted, group, request := t != nil && t.kind.IsWhatsApp(), t != nil && !t.direct(), t != nil && t.folder == "pending"
	c.applying.Unlock()
	var reply *domain.Message
	if draft.ReplyTo != "" && a.cache != nil {
		if m, ok, err := a.cache.MessageByID(ctx, room, draft.ReplyTo); err == nil && ok {
			reply = &m
		}
	}
	var sent domain.Message
	var err error
	if encrypted {
		sent, err = a.sendEncrypted(ctx, c, thread, group, body, reply)
	} else {
		sent, err = a.sendPlain(ctx, c, thread, request, body, reply)
	}
	if err != nil {
		return err
	}
	if a.cache != nil {
		a.cacheMessages(ctx, c, map[domain.RoomID][]domain.Message{room: {sent}}, []domain.Message{sent}, map[domain.RoomID]bool{})
		a.recount(ctx, room)
	}
	return nil
}

// replyParts is a cached reply target's Meta message ID and sender.
func replyParts(reply *domain.Message) (mid string, sender int64, err error) {
	_, mid, ok := strings.Cut(domain.ParseID(string(reply.ID)).Native, "/")
	sender, perr := strconv.ParseInt(domain.ParseID(reply.Sender).Native, 10, 64)
	if !ok || mid == "" || perr != nil {
		return "", 0, fmt.Errorf("%w: %s", errUnknownTarget, reply.ID)
	}
	return mid, sender, nil
}

// sendPlain sends text to a plain thread; request marks a message request, which
// replying accepts.
func (a *Adapter) sendPlain(ctx context.Context, c *conn, thread int64, request bool, body string, reply *domain.Message) (domain.Message, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return domain.Message{}, errNotConnected
	}
	otid := methods.GenerateEpochID()
	task := &socket.SendMessageTask{
		ThreadId: thread, Otid: otid, Source: table.MESSENGER_INBOX_IN_THREAD, InitiatingSource: table.FACEBOOK_INBOX,
		SendType: table.TEXT, SyncGroup: 1, Text: body,
	}
	if request {
		task.Source = table.MESSENGER_INBOX_PENDING_REQUESTS
	}
	msg := domain.Message{RoomID: roomID(c.metaID, thread), Sender: personID(c.metaID), Body: body}
	if reply != nil {
		mid, sender, err := replyParts(reply)
		if err != nil {
			return domain.Message{}, err
		}
		task.ReplyMetaData = &socket.ReplyMetaData{ReplyMessageId: mid, ReplySourceType: 1, ReplySender: sender}
		msg.ReplyTo = reply.ID
	}
	var resp *table.LSTable
	err := a.guard("sending a message", func() error {
		if err := client.WaitUntilCanSendMessages(ctx, sendWait); err != nil {
			return fmt.Errorf("%w: %w", errNotConnected, err)
		}
		var err error
		resp, err = client.ExecuteTasks(ctx, task)
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("meta: send: %w", err)
	}
	mid, err := sentID(resp, otid)
	if err != nil {
		return domain.Message{}, err
	}
	msg.ID = eventID(c.metaID, thread, mid)
	msg.Timestamp = time.Now()
	if ms, err := methods.ParseMessageID(mid); err == nil && ms > 0 {
		msg.Timestamp = time.UnixMilli(ms)
	}
	return msg, nil
}

// sentID is the ID Meta gave the message sent as otid, or why it refused it.
func sentID(resp *table.LSTable, otid int64) (string, error) {
	if resp == nil {
		return "", fmt.Errorf("%w: no answer", errSendRefused)
	}
	want := strconv.FormatInt(otid, 10)
	for _, r := range resp.LSReplaceOptimsiticMessage {
		if r.OfflineThreadingId == want && r.MessageId != "" {
			return r.MessageId, nil
		}
	}
	for _, f := range resp.LSMarkOptimisticMessageFailed {
		return "", fmt.Errorf("%w: %s", errSendRefused, f.Message)
	}
	for _, f := range resp.LSHandleFailedTask {
		return "", fmt.Errorf("%w: %s", errSendRefused, f.Message)
	}
	return "", fmt.Errorf("%w: its answer named no message", errSendRefused)
}

// sendEncrypted sends text to an encrypted chat over the encrypted connection.
func (a *Adapter) sendEncrypted(ctx context.Context, c *conn, thread int64, group bool, body string, reply *domain.Message) (domain.Message, error) {
	c.mu.Lock()
	wa := c.e2ee
	c.mu.Unlock()
	if wa == nil {
		return domain.Message{}, errNoEncrypted
	}
	chat := types.JID{User: itoa(thread), Server: types.MessengerServer}
	if group {
		chat.Server = types.GroupServer
	}
	content, meta := encryptedText(body)
	msg := domain.Message{RoomID: roomID(c.metaID, thread), Sender: personID(c.metaID), Body: body}
	if reply != nil {
		mid, sender, err := replyParts(reply)
		if err != nil {
			return domain.Message{}, err
		}
		participant := types.JID{User: itoa(sender), Server: types.MessengerServer}.String()
		meta.QuotedMessage = &waMsgApplication.MessageApplication_Metadata_QuotedMessage{StanzaID: &mid, Participant: &participant}
		msg.ReplyTo = reply.ID
	}
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	var resp whatsmeow.SendResponse
	err := a.guard("sending an encrypted message", func() error {
		var err error
		resp, err = wa.SendFBMessage(ctx, chat, content, meta, whatsmeow.SendRequestExtra{ID: id})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("meta: send to an encrypted chat: %w", err)
	}
	msg.ID, msg.Timestamp = eventID(c.metaID, thread, id), resp.Timestamp
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	return msg, nil
}

// encryptedText is a text message as the encrypted connection sends it.
func encryptedText(body string) (*waConsumerApplication.ConsumerApplication, *waMsgApplication.MessageApplication_Metadata) {
	return &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
			Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{MessageText: &waCommon.MessageText{Text: &body}},
		}},
	}}, &waMsgApplication.MessageApplication_Metadata{}
}
