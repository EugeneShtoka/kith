package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// danaChanges is Dana changing one of her messages: an edit, a deletion or a reaction.
func danaChanges(id string, m *waE2E.Message) *events.Message {
	e := danaWrites(id, "")
	e.Message = m
	e.Info.Timestamp = time.Now().Add(time.Second)
	return e
}

func key(id string) *waCommon.MessageKey { return &waCommon.MessageKey{ID: new(id)} }

// An edit folds onto its message: the new words, marked edited, kept apart from the
// earlier ones only under [display.deleted] keep.
func TestAnEditFoldsOntoItsMessage(t *testing.T) {
	t.Parallel()
	for _, keep := range []bool{false, true} {
		ctx := context.Background()
		account := Account{Name: "bg", Digits: ownDigits}
		a, cache, store := offline(t, account)
		a.KeepDeleted(keep)
		client := linkedClient(t, store, ownDigits)
		a.onMessage(ctx, account, client, danaWrites("3EB0E", "the plna"))
		a.onMessage(ctx, account, client, danaChanges("3EB0E2", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: key("3EB0E"),
			EditedMessage: &waE2E.Message{Conversation: new("the plan")},
		}}))
		msgs, _ := cache.Messages(ctx, danaChat, 10)
		if len(msgs) != 1 || msgs[0].Body != "the plan" || !msgs[0].Edited {
			t.Fatalf("keep=%v: messages = %+v, want the one, edited", keep, msgs)
		}
		versions, _, _ := a.MessageHistory(ctx, danaChat, "whatsapp:"+ownDigits+"/3EB0E")
		if keep && len(versions) < 2 {
			t.Errorf("keep=true: versions = %+v, want the original kept", versions)
		}
	}
}

// A deletion takes the words away — or keeps them under [display.deleted] keep — and
// says who deleted it.
func TestADeletionAsYouAskedItKept(t *testing.T) {
	t.Parallel()
	for _, keep := range []bool{false, true} {
		ctx := context.Background()
		account := Account{Name: "bg", Digits: ownDigits}
		a, cache, store := offline(t, account)
		a.KeepDeleted(keep)
		client := linkedClient(t, store, ownDigits)
		a.onMessage(ctx, account, client, danaWrites("3EB0D", "oops"))
		<-a.Messages()
		a.onMessage(ctx, account, client, danaChanges("3EB0D2", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: key("3EB0D"),
		}}))
		msgs, _ := cache.Messages(ctx, danaChat, 10)
		if len(msgs) != 1 || !msgs[0].Redacted || (msgs[0].Body == "oops") != keep {
			t.Errorf("keep=%v: cached %+v", keep, msgs)
		}
		gone := <-a.Messages()
		if !gone.Redacted || gone.RedactedBy != "whatsapp:"+danaPhone+"@s.whatsapp.net" || (gone.Body == "oops") != keep {
			t.Errorf("keep=%v: streamed %+v", keep, gone)
		}
	}
}

// One reaction per person per message: a new one replaces it, an empty one takes it
// back; each change is streamed as what was removed and what was added.
func TestAReactionReplacesTheLast(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	react := func(id, emoji string) {
		a.onMessage(ctx, account, client, danaChanges(id, &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
			Key: key("3EB0T"), Text: new(emoji),
		}}))
	}
	updates := func() (out []domain.ReactionUpdate) {
		for {
			select {
			case u := <-a.Reactions():
				out = append(out, u)
			default:
				return out
			}
		}
	}
	react("3EB0R1", "👍")
	if got, _ := cache.Reactions(ctx, danaChat); len(got) != 1 || got[0].Key != "👍" || got[0].Target != "whatsapp:"+ownDigits+"/3EB0T" {
		t.Fatalf("reactions = %+v", got)
	}
	if u := updates(); len(u) != 1 || u[0].Removed {
		t.Errorf("streamed %+v", u)
	}
	react("3EB0R2", "❤️")
	if got, _ := cache.Reactions(ctx, danaChat); len(got) != 1 || got[0].Key != "❤️" {
		t.Errorf("after replacing, reactions = %+v, want only the new one", got)
	}
	if u := updates(); len(u) != 2 || !u[0].Removed || u[0].Reaction.Key != "👍" || u[1].Reaction.Key != "❤️" {
		t.Errorf("a replacement streamed %+v", u)
	}
	react("3EB0R3", "")
	if got, _ := cache.Reactions(ctx, danaChat); len(got) != 0 {
		t.Errorf("after taking it back, reactions = %+v", got)
	}
}

// Changing a message needs its account connected; a message kith never cached cannot
// be fetched from WhatsApp, which keeps none to ask for.
func TestChangesNeedAConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, cache, _ := offline(t, Account{Name: "bg", Digits: ownDigits})
	msg := domain.EventID("whatsapp:" + ownDigits + "/3EB0X")
	for name, err := range map[string]error{
		"Redact":       a.Redact(ctx, danaChat, msg, ""),
		"SendReaction": a.SendReaction(ctx, danaChat, msg, "👍"),
		"edit":         a.Send(ctx, danaChat, domain.Draft{Body: "x", Edits: msg}),
	} {
		if !errors.Is(err, api.ErrNetworkOff) {
			t.Errorf("%s with nothing connected = %v", name, err)
		}
	}
	if _, err := a.FetchEvent(ctx, danaChat, msg); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("fetching what was never cached = %v", err)
	}
	if err := cache.SaveMessages(ctx, danaChat, []domain.Message{{ID: msg, RoomID: danaChat, Body: "here"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.FetchEvent(ctx, danaChat, msg); err != nil || got.Body != "here" {
		t.Errorf("fetching a cached message = (%+v, %v)", got, err)
	}
}
