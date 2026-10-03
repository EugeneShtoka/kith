package whatsapp

import (
	"context"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// quote is what a reply quotes: WhatsApp sends the quoted message's sender and
// content with every reply, so a reply to a message kith never cached (older than a
// room keeps, or from before linking) still shows what it answers.
type quote struct {
	id     domain.EventID
	sender string
	body   string
}

// quotedBy is what msg, a reply, quotes; nil when it quotes nothing kith can show.
func quotedBy(ctx context.Context, account string, msg *waE2E.Message, lookup pnOf) *quote {
	_, info, ok := content(msg)
	if !ok || info == nil || info.GetStanzaID() == "" || info.GetQuotedMessage() == nil {
		return nil
	}
	text, _, _ := content(info.GetQuotedMessage())
	body, _ := formatted(text)
	if body == "" {
		if media, _ := attachment(info.GetQuotedMessage()); media != nil {
			body = "(" + string(media.Type) + ")"
		}
	}
	q := &quote{id: domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account, info.GetStanzaID())), body: body}
	if from, err := types.ParseJID(info.GetParticipant()); err == nil && !from.IsEmpty() {
		q.sender = domain.NativePerson(domain.ProtocolWhatsApp, person(ctx, from, types.EmptyJID, lookup).String())
	}
	return q
}

// keepQuote caches what a reply in room quotes, for FetchEvent.
func (a *Adapter) keepQuote(ctx context.Context, room domain.RoomID, q *quote) {
	if q == nil || a.cache == nil {
		return
	}
	if err := a.cache.SaveQuoted(ctx, room, q.id, q.sender, q.body); err != nil {
		a.log.Warn("keep what a reply quotes failed", "room", room, "err", err)
	}
}

// quotedMessage is a message WhatsApp keeps no copy of, as a reply quoted it.
func (a *Adapter) quotedMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, bool) {
	if a.cache == nil {
		return domain.Message{}, false
	}
	msg, ok, err := a.cache.Quoted(ctx, roomID, eventID)
	if err != nil || !ok {
		return domain.Message{}, false
	}
	if msg.Sender != "" {
		if name, err := a.cache.MemberName(ctx, msg.Sender); err == nil {
			msg.SenderName = name
		}
	}
	return msg, true
}
