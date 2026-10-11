package whatsapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// sentRemembered bounds how many TxnIDs are remembered for retries.
const sentRemembered = 512

// Send posts a text message with WhatsApp-style mentions and reply quote. A retry
// with the same TxnID reuses the message ID, so WhatsApp dedupes it. WhatsApp does
// not echo own messages, so the sent one is cached and streamed here.
func (a *Adapter) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	if err := a.mayPost(roomID); err != nil {
		return err
	}
	if draft.Edits != "" {
		return a.edit(ctx, roomID, draft)
	}
	id := domain.ParseID(string(roomID))
	chat, err := types.ParseJID(id.Native)
	if err != nil {
		return fmt.Errorf("whatsapp: %s is not a chat: %w", roomID, err)
	}
	account, client, ok := a.clientFor(id.Account)
	if !ok {
		return fmt.Errorf("%w: WhatsApp account %s is not linked or not connected", api.ErrNetworkOff, id.Account)
	}

	text, mentioned := composed(draft)
	message, replyTo := a.outgoing(ctx, account.Digits, roomID, text, mentioned, draft.ReplyTo)
	msgID := a.messageIDFor(client, draft.TxnID)
	resp, err := client.SendMessage(ctx, chat, message, whatsmeow.SendRequestExtra{ID: msgID})
	if err != nil {
		return fmt.Errorf("whatsapp: send to %s: %w", roomID, err)
	}

	own := selfOf(client)
	sent := domain.Message{
		ID:        domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account.Digits, resp.ID)),
		RoomID:    roomID,
		Sender:    domain.NativePerson(domain.ProtocolWhatsApp, own.pn.String()),
		Timestamp: resp.Timestamp,
		ReplyTo:   replyTo,
	}
	sent.Body, sent.Format = formatted(text)
	for _, jid := range mentioned {
		sent.Mentions = append(sent.Mentions, domain.Mention{
			UserID: domain.NativePerson(domain.ProtocolWhatsApp, jid.String()), Name: "@" + jid.User,
		})
	}
	a.cacheSent(ctx, account, sent)
	emit(a, a.messages, sent)
	return nil
}

func (a *Adapter) clientFor(digits string) (Account, *whatsmeow.Client, bool) {
	for _, c := range a.connected() {
		if c.account.Digits == digits {
			return c.account, c.client, true
		}
	}
	return Account{}, nil, false
}

// composed converts a draft to WhatsApp text: its mention form, and Markdown as
// WhatsApp markers unless sent as typed.
func composed(draft domain.Draft) (string, []types.JID) {
	text, mentioned := mentionText(draft)
	if !draft.Plain {
		text = fromMarkdown(text)
	}
	return text, mentioned
}

// mentionText rewrites mentions of WhatsApp people as @number and returns them.
func mentionText(draft domain.Draft) (string, []types.JID) {
	text := draft.Body
	var mentioned []types.JID
	for _, m := range draft.LiveMentions() {
		id := domain.ParseID(m.UserID)
		if id.Network != domain.ProtocolWhatsApp || id.Account != "" {
			continue
		}
		jid, err := types.ParseJID(id.Native)
		if err != nil {
			continue
		}
		jid = jid.ToNonAD()
		text = strings.Replace(text, m.Name, "@"+jid.User, 1)
		mentioned = append(mentioned, jid)
	}
	return text, mentioned
}

// outgoing builds the message: plain, or extended when it mentions or replies.
func (a *Adapter) outgoing(ctx context.Context, account string, roomID domain.RoomID, text string, mentioned []types.JID, replyTo domain.EventID) (*waE2E.Message, domain.EventID) {
	var info *waE2E.ContextInfo
	for _, jid := range mentioned {
		if info == nil {
			info = &waE2E.ContextInfo{}
		}
		info.MentionedJID = append(info.MentionedJID, jid.String())
	}
	if quoted := a.quote(ctx, account, roomID, replyTo); quoted != nil {
		if info == nil {
			info = &waE2E.ContextInfo{}
		}
		info.StanzaID, info.Participant, info.QuotedMessage = quoted.id, quoted.participant, quoted.message
	} else {
		replyTo = ""
	}
	if info == nil {
		return &waE2E.Message{Conversation: new(text)}, ""
	}
	return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: new(text), ContextInfo: info}}, replyTo
}

// quotation is what a reply carries of the quoted message.
type quotation struct {
	id, participant *string
	message         *waE2E.Message
}

// quote is the quotation for replyTo; nil when it is not cached or not this account's.
func (a *Adapter) quote(ctx context.Context, account string, roomID domain.RoomID, replyTo domain.EventID) *quotation {
	if replyTo == "" || a.cache == nil {
		return nil
	}
	target := domain.ParseID(string(replyTo))
	if target.Network != domain.ProtocolWhatsApp || target.Account != account {
		return nil
	}
	original, found, err := a.cache.MessageByID(ctx, roomID, replyTo)
	if err != nil || !found {
		return nil
	}
	sender, err := types.ParseJID(domain.ParseID(original.Sender).Native)
	if err != nil {
		return nil
	}
	return &quotation{
		id:          new(target.Native),
		participant: new(sender.String()),
		message:     &waE2E.Message{Conversation: new(original.Body)},
	}
}

// messageIDFor reuses the ID of an earlier try of txn, else generates and remembers one.
func (a *Adapter) messageIDFor(client *whatsmeow.Client, txn string) types.MessageID {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, retried := a.sent[txn]; retried && txn != "" {
		return id
	}
	id := client.GenerateMessageID()
	if txn != "" {
		if len(a.sent) >= sentRemembered {
			clear(a.sent) // a retry comes within seconds; older ones are long settled
		}
		a.sent[txn] = id
	}
	return id
}

// cacheSent caches an own message and counts its mentions for mention ranking.
func (a *Adapter) cacheSent(ctx context.Context, account Account, sent domain.Message) {
	if a.cache == nil {
		return
	}
	a.record(ctx, account, sent, func() {})
	for _, m := range sent.Mentions {
		if err := a.cache.RecordMention(ctx, sent.RoomID, m.UserID, time.Now().UnixMilli()); err != nil {
			a.log.Warn("record a mention failed", "room", sent.RoomID, "err", err)
		}
	}
}
