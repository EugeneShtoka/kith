package local

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// waMe is this person's WhatsApp account, as the daemon names it.
const waMe = "whatsapp:359880000001@s.whatsapp.net"

// A reaction from any of this person's accounts counts toward their emoji: on
// WhatsApp alone, and beside Matrix.
func TestEmojiScoresCountEveryAccountsReactions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, me := range map[string][]string{"WhatsApp alone": {waMe}, "Matrix and WhatsApp": {"@me:x", waMe}} {
		s := backendAs(t, me...)
		room := domain.RoomID("whatsapp:359880000001/1203@g.us")
		if err := s.cache.SaveReactions(ctx, []domain.Reaction{
			{ID: "r1", RoomID: room, Target: "m1", Sender: waMe, Key: "🎉"},
			{ID: "r2", RoomID: room, Target: "m1", Sender: "whatsapp:359880000002@s.whatsapp.net", Key: "👀"},
		}); err != nil {
			t.Fatal(err)
		}
		scores, err := s.EmojiScores(ctx, domain.EmojiReaction, room, nil, "room")
		if err != nil || scores["🎉"] == 0 || scores["👀"] != 0 {
			t.Errorf("%s: scores = (%v, %v), want this person's 🎉 only", name, scores, err)
		}
	}
}

// What to call this person in a prompt comes from whichever account has a name: the
// WhatsApp one without Matrix, a Matrix localpart when nothing has a name, else "you".
func TestAccountNameFromAnyAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	wa := backendAs(t, waMe)
	if got := wa.accountName(ctx); got != "you" {
		t.Errorf("WhatsApp with no name cached = %q, want you", got)
	}
	if err := wa.cache.SaveMembers(ctx, "whatsapp:359880000001/1203@g.us", []domain.Member{{UserID: waMe, DisplayName: "Ada Lovelace"}}); err != nil {
		t.Fatal(err)
	}
	if got := wa.accountName(ctx); got != "Ada Lovelace" {
		t.Errorf("WhatsApp with a name = %q, want Ada Lovelace", got)
	}
	if got := wa.accountNames(ctx); got != "Ada Lovelace, Ada" {
		t.Errorf("accountNames = %q, want the name and its first word", got)
	}

	both := backendAs(t, "@ada:x", waMe)
	if got := both.accountName(ctx); got != "ada" {
		t.Errorf("Matrix and WhatsApp, no names = %q, want the Matrix localpart", got)
	}
	if err := both.cache.SaveMembers(ctx, "whatsapp:359880000001/1203@g.us", []domain.Member{{UserID: waMe, DisplayName: "Ada Lovelace"}}); err != nil {
		t.Fatal(err)
	}
	if got := both.accountName(ctx); got != "Ada Lovelace" {
		t.Errorf("Matrix unnamed, WhatsApp named = %q, want the WhatsApp name", got)
	}
	if got := backendAs(t).accountName(ctx); got != "you" {
		t.Errorf("nobody = %q, want you", got)
	}
}

// A word this person sends from any account weighs as their own in completion, both
// from the cache and as it arrives: otherwise the tie goes alphabetically, to the
// other person's word.
func TestCompletionWeighsEveryAccountsWordsAsOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := backendAs(t, "@me:x", waMe)
	far := domain.RoomID("whatsapp:359880000001/1203@g.us")
	if err := s.cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x"}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	cached := []domain.Message{
		{ID: "m1", RoomID: far, Sender: "whatsapp:359880000002@s.whatsapp.net", Body: "zzalpha", Timestamp: at},
		{ID: "m2", RoomID: far, Sender: waMe, Body: "zzbeta", Timestamp: at.Add(time.Minute)},
	}
	if err := s.cache.SaveMessages(ctx, far, cached); err != nil {
		t.Fatal(err)
	}
	rank := func() []string {
		got, err := s.CompleteWord(ctx, domain.CompleteRequest{
			Prefix: "zz", RoomIDs: []domain.RoomID{"!a:x"}, Scope: "room", Sources: []string{sourceHistory}, Limit: 5,
		})
		if err != nil {
			t.Fatal(err)
		}
		words := make([]string, len(got))
		for i, c := range got {
			words[i] = c.Word
		}
		return words
	}
	if got := rank(); len(got) < 2 || got[0] != "zzbeta" {
		t.Errorf("from the cache = %v, want this person's zzbeta first", got)
	}
	for i, m := range []domain.Message{
		{ID: "m3", RoomID: far, Sender: "whatsapp:359880000002@s.whatsapp.net", Body: "zzdelta"},
		{ID: "m4", RoomID: far, Sender: waMe, Body: "zzgamma"},
	} {
		m.Timestamp = at.Add(time.Duration(2+i) * time.Minute)
		s.MessageCached(m)
	}
	got := rank()
	if slices.Index(got, "zzgamma") < 0 || slices.Index(got, "zzdelta") >= 0 && slices.Index(got, "zzgamma") > slices.Index(got, "zzdelta") {
		t.Errorf("as they arrive = %v, want this person's zzgamma above zzdelta", got)
	}
}
