package whatsapp

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// self is an account's own addresses: its phone number and its LID.
type self struct{ pn, lid types.JID }

// is reports whether jid is this account.
func (s self) is(jid types.JID) bool {
	jid = jid.ToNonAD()
	return (!s.pn.IsEmpty() && jid == s.pn) || (!s.lid.IsEmpty() && jid == s.lid)
}

// pnOf finds the phone number behind a LID, or the empty JID.
type pnOf func(ctx context.Context, lid types.JID) types.JID

// person is the address a person is kept under: the phone number when WhatsApp gives
// one (alt, or the store knows it), so they fold with the same contact seen in a
// group, through a bridge or by another account; the LID otherwise.
func person(ctx context.Context, jid, alt types.JID, lookup pnOf) types.JID {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if !alt.IsEmpty() && alt.Server == types.DefaultUserServer {
		return alt.ToNonAD()
	}
	if lookup != nil {
		if pn := lookup(ctx, jid); !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid
}

// chatOf is the chat a message belongs in. A direct chat is kept under the other
// person's address (person), so the conversation is one room however WhatsApp
// addressed each message; a group is its own JID.
func chatOf(ctx context.Context, info *types.MessageInfo, lookup pnOf) types.JID {
	if info.IsGroup {
		return info.Chat.ToNonAD()
	}
	alt := info.SenderAlt
	if info.IsFromMe {
		alt = info.RecipientAlt
	}
	return person(ctx, info.Chat, alt, lookup)
}

// incoming is a message as kith keeps it. What kith does not show from WhatsApp yet —
// reactions, edits, deletions, the protocol's own messages — is false.
func incoming(account string, room domain.RoomID, info *types.MessageInfo, msg *waE2E.Message, sender string, me self) (domain.Message, bool) {
	body, context, ok := content(msg)
	if !ok {
		return domain.Message{}, false
	}
	out := domain.Message{
		ID:        domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account, info.ID)),
		RoomID:    room,
		Sender:    sender,
		Body:      body,
		Timestamp: info.Timestamp,
	}
	if context == nil {
		return out, true
	}
	if quoted := context.GetStanzaID(); quoted != "" {
		out.ReplyTo = domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account, quoted))
	}
	for _, mentioned := range context.GetMentionedJID() {
		jid, err := types.ParseJID(mentioned)
		if err != nil {
			continue
		}
		out.Mentions = append(out.Mentions, domain.Mention{
			UserID: domain.NativePerson(domain.ProtocolWhatsApp, jid.ToNonAD().String()),
			Name:   "@" + jid.User, // as the body writes it
		})
		if me.is(jid) {
			out.Mentioned = true
		}
	}
	return out, true
}

// content is what a message says, and the context (reply, mentions) it carries.
// Media are shown by kind and caption until they can be loaded.
func content(msg *waE2E.Message) (string, *waE2E.ContextInfo, bool) {
	switch {
	case msg == nil:
		return "", nil, false
	case msg.GetConversation() != "":
		return msg.GetConversation(), nil, true
	case msg.GetExtendedTextMessage() != nil:
		m := msg.GetExtendedTextMessage()
		return m.GetText(), m.GetContextInfo(), true
	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		return labeled("photo", m.GetCaption()), m.GetContextInfo(), true
	case msg.GetVideoMessage() != nil:
		m := msg.GetVideoMessage()
		return labeled("video", m.GetCaption()), m.GetContextInfo(), true
	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		kind := "audio"
		if m.GetPTT() {
			kind = "voice message"
		}
		return labeled(kind, ""), m.GetContextInfo(), true
	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		return labeled("document: "+m.GetFileName(), m.GetCaption()), m.GetContextInfo(), true
	case msg.GetStickerMessage() != nil:
		return labeled("sticker", ""), msg.GetStickerMessage().GetContextInfo(), true
	case msg.GetLocationMessage() != nil:
		return labeled("location", msg.GetLocationMessage().GetName()), msg.GetLocationMessage().GetContextInfo(), true
	case msg.GetContactMessage() != nil:
		return labeled("contact", msg.GetContactMessage().GetDisplayName()), msg.GetContactMessage().GetContextInfo(), true
	case msg.GetPollCreationMessage() != nil:
		return labeled("poll", msg.GetPollCreationMessage().GetName()), nil, true
	case msg.GetPollCreationMessageV3() != nil:
		return labeled("poll", msg.GetPollCreationMessageV3().GetName()), nil, true
	}
	return "", nil, false
}

// labeled is a placeholder for what kith cannot show yet, with its own words after.
func labeled(kind, words string) string {
	label := "[" + kind + "]"
	if words = strings.TrimSpace(words); words != "" {
		return label + " " + words
	}
	return label
}
