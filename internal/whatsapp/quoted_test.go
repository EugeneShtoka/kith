package whatsapp

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// danaReplies is Dana replying to an uncached message, quoting it.
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

// A reply to an uncached message keeps its quote for FetchEvent; once the message is
// cached, the cached one wins.
func TestAReplyKeepsWhatItQuotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
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
	if _, err := a.FetchEvent(ctx, danaChat, "whatsapp:"+ownDigits+"/NEVER"); !errors.Is(err, api.ErrNoSuchMessage) {
		t.Errorf("fetching a message neither cached nor quoted = %v, want ErrNoSuchMessage", err)
	}
}
