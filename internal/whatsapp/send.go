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

// sentRemembered bounds how many sends are remembered for their retries.
const sentRemembered = 512

// Send posts a text message: mentions become WhatsApp's own (@ and the number, and
// the person listed so they are notified), a reply quotes what it answers. A retry
// with the same TxnID goes out under the same message ID, which WhatsApp keeps once.
// WhatsApp does not echo a device its own messages, so the one sent is cached and
// streamed here.
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

// clientFor is the connected client of the account with these digits.
func (a *Adapter) clientFor(digits string) (Account, *whatsmeow.Client, bool) {
	for _, c := range a.connected() {
		if c.account.Digits == digits {
			return c.account, c.client, true
		}
	}
	return Account{}, nil, false
}

// composed is a draft as WhatsApp takes it: mentions written its way, and Markdown
// in its markers unless the draft is to go as typed.
func composed(draft domain.Draft) (string, []types.JID) {
	text, mentioned := mentionText(draft)
	if !draft.Plain {
		text = fromMarkdown(text)
	}
	return text, mentioned
}

// mentionText is the draft's text with each mention of a WhatsApp person written as
// WhatsApp writes it (@ and their number), and the people mentioned.
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

// outgoing is the WhatsApp message for text: plain when it carries nothing more,
// extended when it mentions someone or answers a message.
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

// quotation is what a reply carries of the message it answers.
type quotation struct {
	id, participant *string
	message         *waE2E.Message
}

// quote is the cached message replyTo names, as a reply quotes it; nil when there is
// none to answer (or it is not this account's).
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

// messageIDFor is the WhatsApp ID a send goes out under: the one an earlier try of
// the same TxnID used, else a new one, remembered.
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

// cacheSent keeps our own message, and counts its mentions for the dropdown's ranking.
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
