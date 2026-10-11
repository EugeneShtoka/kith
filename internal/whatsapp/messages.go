package whatsapp

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// self is an account's own phone-number and LID JIDs.
type self struct{ pn, lid types.JID }

func (s self) is(jid types.JID) bool {
	jid = jid.ToNonAD()
	return (!s.pn.IsEmpty() && jid == s.pn) || (!s.lid.IsEmpty() && jid == s.lid)
}

// pnOf maps a LID to its phone number, or the empty JID.
type pnOf func(ctx context.Context, lid types.JID) types.JID

// person is the JID a person is kept under: the phone number when known (alt or the
// store), so they fold across groups, bridges and accounts; else the LID.
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

// chatOf is a message's chat: a direct chat under the peer's person JID, so PN- and
// LID-addressed messages share a room; a group under its own JID.
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

// incoming converts a message; false for protocol messages (reactions, edits,
// deletions and the like).
func incoming(account string, room domain.RoomID, info *types.MessageInfo, msg *waE2E.Message, sender string, me self) (domain.Message, bool) {
	body, context, ok := content(msg)
	if !ok {
		return domain.Message{}, false
	}
	words, format := formatted(body)
	out := domain.Message{
		ID:        domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account, info.ID)),
		RoomID:    room,
		Sender:    sender,
		Body:      words,
		Format:    format,
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

// content is a message's text and context info (reply, mentions).
func content(msg *waE2E.Message) (string, *waE2E.ContextInfo, bool) {
	switch {
	case msg == nil:
		return "", nil, false
	case msg.GetConversation() != "":
		return msg.GetConversation(), nil, true
	case msg.GetExtendedTextMessage() != nil:
		m := msg.GetExtendedTextMessage()
		return m.GetText(), m.GetContextInfo(), true
	// An attachment's text is its caption (a document's name); the media chip shows its kind.
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetCaption(), msg.GetImageMessage().GetContextInfo(), true
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetCaption(), msg.GetVideoMessage().GetContextInfo(), true
	case msg.GetAudioMessage() != nil:
		return "", msg.GetAudioMessage().GetContextInfo(), true
	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		return cmpOr(m.GetCaption(), m.GetFileName()), m.GetContextInfo(), true
	case msg.GetStickerMessage() != nil:
		return "", msg.GetStickerMessage().GetContextInfo(), true
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

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// labeled is a placeholder for unsupported content, followed by its text.
func labeled(kind, words string) string {
	label := "[" + kind + "]"
	if words = strings.TrimSpace(words); words != "" {
		return label + " " + words
	}
	return label
}
