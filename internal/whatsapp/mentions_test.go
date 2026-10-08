package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	eliLID   = "100000000000005"
	eliPhone = "1500000005"
	finPhone = "1500000006"
)

var (
	eli = domain.NativePerson(domain.ProtocolWhatsApp, pn(eliPhone).String())
	fin = domain.NativePerson(domain.ProtocolWhatsApp, pn(finPhone).String())
)

// mentionsOf is the people a cached message mentions.
func mentionsOf(t *testing.T, msgs []domain.Message, id domain.EventID) []domain.Mention {
	t.Helper()
	for i := range msgs {
		if msgs[i].ID == id {
			return msgs[i].Mentions
		}
	}
	t.Fatalf("%s is not cached", id)
	return nil
}

// WhatsApp writes a mention as "@" and the person's LID, naming them beside the
// text. kith keeps who it is as their number, as it keeps a sender, with the text
// that names them, so the client can draw the name they are known by.
func TestAMentionIsThePersonByTheirNumber(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	if err := client.Store.LIDs.PutLIDMapping(ctx, lid(eliLID), pn(eliPhone)); err != nil {
		t.Fatal(err)
	}
	e := danaWrites("3EB0M1", "")
	e.Message = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text:        new("@" + eliLID + " can you look?"),
		ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{lid(eliLID).String()}},
	}}
	a.onMessage(ctx, account, client, e)

	msgs, err := cache.Messages(ctx, danaChat, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := mentionsOf(t, msgs, domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, ownDigits, "3EB0M1")))
	if len(got) != 1 || got[0].UserID != eli || got[0].Name != "@"+eliLID {
		t.Errorf("mentions = %+v, want %s named by @%s", got, eli, eliLID)
	}
}

// Messages cached before mentions were kept get them when the account connects: a
// LID the store knows is that person, a member's number is that member, and other
// digits stay text. Doing it again changes nothing.
func TestOldMessagesGetTheirMentions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	if err := client.Store.LIDs.PutLIDMapping(ctx, lid(eliLID), pn(eliPhone)); err != nil {
		t.Fatal(err)
	}
	a.onMessage(ctx, account, client, danaWrites("3EB0R0", "hi")) // the chat
	if err := cache.SaveMembers(ctx, danaChat, []domain.Member{{UserID: fin, DisplayName: "Fin"}}); err != nil {
		t.Fatal(err)
	}
	id := func(s string) domain.EventID {
		return domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, ownDigits, s))
	}
	old := []domain.Message{
		{ID: id("3EB0R1"), Sender: eli, Body: "@" + eliLID + " are you in?", Timestamp: time.Unix(1700000000+1, 0)},
		{ID: id("3EB0R2"), Sender: eli, Body: "ask @" + finPhone + " too", Timestamp: time.Unix(1700000000+2, 0)},
		{ID: id("3EB0R3"), Sender: eli, Body: "the code is @123456789", Timestamp: time.Unix(1700000000+3, 0)},
	}
	if err := cache.SaveMessages(ctx, danaChat, old); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		a.listOldMentions(ctx, account, client)
		msgs, err := cache.Messages(ctx, danaChat, 10)
		if err != nil {
			t.Fatal(err)
		}
		if got := mentionsOf(t, msgs, id("3EB0R1")); len(got) != 1 || got[0].UserID != eli || got[0].Name != "@"+eliLID {
			t.Errorf("a LID mention = %+v, want %s", got, eli)
		}
		if got := mentionsOf(t, msgs, id("3EB0R2")); len(got) != 1 || got[0].UserID != fin {
			t.Errorf("a member's number = %+v, want %s", got, fin)
		}
		if got := mentionsOf(t, msgs, id("3EB0R3")); len(got) != 0 {
			t.Errorf("digits that are no one = %+v, want no mention", got)
		}
	}
}
