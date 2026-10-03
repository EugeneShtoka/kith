package whatsapp

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// danaReplies is Dana answering a message kith never cached, quoting it.
func danaReplies(id, text, quotedID string, quoted *waE2E.Message) *events.Message {
	e := danaWrites(id, "")
	e.Message = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new(text),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID: new(quotedID), Participant: new(pn(danaPhone).String()), QuotedMessage: quoted,
		},
	}}
	return e
}

// A reply to a message kith never cached still shows what it answers: the quoted
// sender and words come with the reply, and FetchEvent finds them. Once the message
// itself is cached, it is the answer.
func TestAReplyKeepsWhatItQuotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)

	a.onMessage(ctx, account, client, danaReplies("3EB0R1", "yes, at six", "3EB0OLD", &waE2E.Message{Conversation: new("*dinner* tonight?")}))
	old := domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, ownDigits, "3EB0OLD"))
	got, err := a.FetchEvent(ctx, danaChat, old)
	if err != nil || got.Body != "dinner tonight?" || got.Sender != "whatsapp:"+danaPhone+"@s.whatsapp.net" || got.ID != old {
		t.Fatalf("FetchEvent(quoted) = (%+v, %v), want Dana's quoted words, formatting taken out", got, err)
	}

	a.onMessage(ctx, account, client, danaReplies("3EB0R2", "nice", "3EB0PIC", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: new("image/jpeg")}}))
	pic := domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, ownDigits, "3EB0PIC"))
	if got, err := a.FetchEvent(ctx, danaChat, pic); err != nil || got.Body != "(image)" {
		t.Errorf("FetchEvent(a quoted photo) = (%+v, %v), want it named as an image", got, err)
	}

	original := domain.Message{ID: old, RoomID: danaChat, Sender: "whatsapp:" + danaPhone + "@s.whatsapp.net", Body: "dinner tonight? (the original)"}
	if err := cache.SaveMessages(ctx, danaChat, []domain.Message{original}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.FetchEvent(ctx, danaChat, old); got.Body != original.Body {
		t.Errorf("FetchEvent with the original cached = %q, want the original", got.Body)
	}
	if _, err := a.FetchEvent(ctx, danaChat, "whatsapp:"+ownDigits+"/NEVER"); err == nil {
		t.Error("a message neither cached nor quoted was found")
	}
}
