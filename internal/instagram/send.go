package instagram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// sendAttribution is where Instagram's web app says a message was written.
const sendAttribution = "igd_web_chat_tab:in_thread"

// errNoLongID: unsending and read marks name a thread by its long ID, which the inbox
// learns as it is listed.
var errNoLongID = errors.New("instagram: that thread is not listed yet; try again in a moment")

// sender is the connection of the account a room is, when it is connected.
func (a *Adapter) sender(room domain.RoomID) (*conn, *instameow.Client, string, error) {
	self, chat, ok := threadOf(room)
	if !ok {
		return nil, nil, "", fmt.Errorf("instagram: %s is not an Instagram thread", room)
	}
	c, client := a.connected(self)
	if client == nil {
		return nil, nil, "", fmt.Errorf("%w: this Instagram account is not connected", api.ErrNetworkOff)
	}
	return c, client, chat, nil
}

// otid is a send's offline threading ID, Instagram's own retry key: the one an earlier
// try with txn chose, else a new one.
func (a *Adapter) otid(txn string) string {
	if txn == "" {
		return strconv.FormatInt(methods.GenerateEpochID(), 10)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, ok := a.txns[txn]; ok {
		return id
	}
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	a.txns[txn] = id
	return id
}

func (a *Adapter) forgetTxn(txn string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.txns, txn)
}

// Send sends draft to a thread, then caches it under the ID Instagram gave it (its
// echo, if it comes, is the same message). An edit replaces one of the account's.
func (a *Adapter) Send(ctx context.Context, room domain.RoomID, draft domain.Draft) error {
	c, client, chat, err := a.sender(room)
	if err != nil {
		return err
	}
	if draft.Edits != "" {
		return a.edit(ctx, c, client, chat, draft)
	}
	req := &slidetypes.SendTextRequest{
		IGThreadIGID: &chat, OfflineThreadingID: a.otid(draft.TxnID),
		Text: slidetypes.SensitiveString{Value: draft.Body}, SendAttribution: new(sendAttribution),
	}
	var replyTo domain.EventID
	if draft.ReplyTo != "" {
		if target, ok := messageOf(draft.ReplyTo); ok {
			req.ReplyToMessageID, replyTo = &target, draft.ReplyTo
		}
	}
	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("instagram: send to %s: %w", room, err)
	}
	a.forgetTxn(draft.TxnID)
	sent := resp.GetMessage()
	if sent.ID == "" {
		return nil // its echo brings it
	}
	return a.keepSent(ctx, c, chat, sent, domain.Message{Body: draft.Body, ReplyTo: replyTo})
}

// keepSent caches msg, sent as Instagram named it, as the account's, reading its thread.
func (a *Adapter) keepSent(ctx context.Context, c *conn, chat string, sent slidetypes.SentMessage, msg domain.Message) error {
	msg.ID, msg.RoomID, msg.Sender = eventID(c.self, chat, sent.ID), roomID(c.self, chat), person(c.self)
	msg.Timestamp = sent.TimestampMS.Time
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	if a.cache != nil {
		if err := a.save(ctx, msg.RoomID, msg); err != nil {
			return fmt.Errorf("instagram: cache what was sent: %w", err)
		}
		a.readTo(ctx, c, chat, msg.Timestamp)
	}
	emit(a, a.messages, msg)
	return nil
}

// edit replaces the text of one of the account's messages (Instagram allows five
// edits, within 15 minutes), then caches it.
func (a *Adapter) edit(ctx context.Context, c *conn, client *instameow.Client, chat string, draft domain.Draft) error {
	target, ok := messageOf(draft.Edits)
	if !ok {
		return fmt.Errorf("instagram: %s is not an Instagram message", draft.Edits)
	}
	_, err := client.EditMessage(ctx, &slidetypes.EditMessageRequest{
		ThreadID: chat, TargetMessageID: target, OfflineThreadingID: a.otid(draft.TxnID),
		Body: slidetypes.SensitiveString{Value: draft.Body},
	})
	if err != nil {
		return fmt.Errorf("instagram: edit: %w", err)
	}
	a.forgetTxn(draft.TxnID)
	e := &slidetypes.EditMessageEvent{MessageID: target, TextBody: draft.Body}
	e.SlideEditHistoryEntry.TimestampMS.Time = time.Now()
	a.takeEdit(ctx, c, chat, e)
	return nil
}

// SendReaction puts the account's reaction key on a message (one per message: it
// replaces the one it had), then caches it.
func (a *Adapter) SendReaction(ctx context.Context, room domain.RoomID, target domain.EventID, key string) error {
	c, client, chat, err := a.sender(room)
	if err != nil {
		return err
	}
	message, ok := messageOf(target)
	if !ok {
		return fmt.Errorf("instagram: %s is not an Instagram message", target)
	}
	_, err = client.SendReaction(ctx, &slidetypes.CreateReactionRequest{Input: slidetypes.ReactionInput{
		Emoji: key, MessageID: message, ReactionStatus: slidetypes.ReactionStatusCreated, ThreadID: chat,
	}})
	if err != nil {
		return fmt.Errorf("instagram: react: %w", err)
	}
	self, _ := strconv.ParseInt(c.self, 10, 64)
	a.takeReaction(ctx, c, chat, message, &slidetypes.Reaction{Reaction: key, SenderFBID: self})
	return nil
}

// Redact takes the account's reaction off a message, or unsends one of its messages,
// then caches that.
func (a *Adapter) Redact(ctx context.Context, room domain.RoomID, event domain.EventID, _ string) error {
	c, client, chat, err := a.sender(room)
	if err != nil {
		return err
	}
	if target, sender, ok := strings.Cut(string(event), ":reaction:"); ok {
		if sender != c.self {
			return errors.New("instagram: only your own reaction can be taken off")
		}
		return a.unreact(ctx, c, client, room, chat, domain.EventID(target), event)
	}
	message, ok := messageOf(event)
	if !ok {
		return fmt.Errorf("instagram: %s is not an Instagram message", event)
	}
	th := c.inbox.thread(chat)
	if th == nil || th.long == "" {
		return errNoLongID
	}
	_, err = client.UnsendMessage(ctx, &slidetypes.UnsendMessageRequest{MessageID: message, SendData: slidetypes.SendData{ThreadID: th.long}})
	if err != nil {
		return fmt.Errorf("instagram: unsend: %w", err)
	}
	a.takeUnsend(ctx, c, chat, message)
	return nil
}

// unreact takes the account's reaction (its ID reaction) off target, naming the emoji.
func (a *Adapter) unreact(ctx context.Context, c *conn, client *instameow.Client, room domain.RoomID, chat string, target, reaction domain.EventID) error {
	if a.cache == nil {
		return nil
	}
	rs, err := a.cache.Reactions(ctx, room)
	if err != nil {
		return fmt.Errorf("instagram: read reactions: %w", err)
	}
	key := ""
	for _, r := range rs {
		if r.ID == reaction {
			key = r.Key
		}
	}
	message, ok := messageOf(target)
	if key == "" || !ok {
		return nil // not on it: nothing to take off
	}
	_, err = client.SendReaction(ctx, &slidetypes.CreateReactionRequest{Input: slidetypes.ReactionInput{
		Emoji: key, MessageID: message, ReactionStatus: slidetypes.ReactionStatusDeleted, ThreadID: chat,
	}})
	if err != nil {
		return fmt.Errorf("instagram: take a reaction off: %w", err)
	}
	self, _ := strconv.ParseInt(c.self, 10, 64)
	a.takeUnreaction(ctx, c, chat, message, &slidetypes.Reaction{SenderFBID: self})
	return nil
}

// SendTyping says the account is typing in a thread, or stopped (Instagram lets it lapse
// on its own; timeout is not sent).
func (a *Adapter) SendTyping(ctx context.Context, room domain.RoomID, typing bool, _ time.Duration) error {
	_, client, chat, err := a.sender(room)
	if err != nil {
		return err
	}
	if err := client.SetTyping(ctx, chat, typing); err != nil {
		return fmt.Errorf("instagram: typing: %w", err)
	}
	return nil
}

// tellRead tells Instagram the account read a thread up to message (sent at at), as its
// web app does: the read, then its validation. The read is kith's already: a failure is
// logged.
func (a *Adapter) tellRead(ctx context.Context, room domain.RoomID, event domain.EventID, at time.Time) {
	c, client, chat, err := a.sender(room)
	if err != nil {
		return // read in kith; Instagram hears of the next read once connected
	}
	message, ok := messageOf(event)
	th := c.inbox.thread(chat)
	if !ok || th == nil || th.long == "" {
		return
	}
	meta := slidetypes.MarkReadMetadata{IGThreadIGID: th.long}
	if _, err := client.MarkRead(ctx, &slidetypes.MarkReadRequest{Metadata: meta, Data: slidetypes.MarkReadData{MessageID: message, ItemID: new("")}}); err != nil {
		a.log.Warn("telling Instagram a thread was read failed", "account", c.account.Name, "err", err)
		return
	}
	validate := slidetypes.MarkReadData{MessageID: message}
	validate.MessageTimestampMS.Time = at
	if _, err := client.MarkReadValidation(ctx, &slidetypes.MarkReadRequest{Metadata: meta, Data: validate}); err != nil {
		a.log.Warn("Instagram did not validate a read", "account", c.account.Name, "err", err)
	}
}
