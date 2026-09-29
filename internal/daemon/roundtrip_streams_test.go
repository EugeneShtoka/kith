package daemon_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// take receives one value from a stream, failing rather than hanging.
func take[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()

	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatalf("%s closed, want a value", what)
		}
		return v
	case <-time.After(settle):
		t.Fatalf("timed out waiting on %s", what)
		panic("unreachable")
	}
}

// All four remaining streams deliver, with their payloads intact.
func TestEveryStreamRoundTrips(t *testing.T) {
	t.Parallel()

	fake := apitest.Nop{
		Msgs:    make(chan domain.Message, 1),
		Unreads: make(chan domain.Unread, 1),
		Reacts:  make(chan domain.ReactionUpdate, 1),
		Invs:    make(chan []domain.Room, 1),
		Verifs:  make(chan domain.Verification, 1),
	}
	r, h := attach(t, &fakeBackend{Nop: fake})
	go func() { _ = r.Start(context.Background()) }()
	h.waitAttached(1)

	wantUnread := domain.Unread{RoomID: "!a:example.org", Notifications: 4, Highlights: 2, ReadEvent: "$seen:example.org"}
	fake.Unreads <- wantUnread
	if got := take(t, r.Unread(), "Unread()"); !reflect.DeepEqual(got, wantUnread) {
		t.Errorf("Unread() = %+v, want %+v", got, wantUnread)
	}

	wantReact := domain.ReactionUpdate{
		Reaction: domain.Reaction{ID: "$r:example.org", RoomID: "!a:example.org", Target: "$m:example.org", Sender: "@ada:example.org", Key: "👍"},
		Removed:  true,
	}
	fake.Reacts <- wantReact
	if got := take(t, r.Reactions(), "Reactions()"); got != wantReact {
		t.Errorf("Reactions() = %+v, want %+v", got, wantReact)
	}

	wantInvites := []domain.Room{{ID: "!invite:example.org", Name: "Book club", Membership: domain.MembershipInvite, InvitedBy: "@ada:example.org"}}
	fake.Invs <- wantInvites
	if got := take(t, r.Invites(), "Invites()"); !reflect.DeepEqual(got, wantInvites) {
		t.Errorf("Invites() = %+v, want %+v", got, wantInvites)
	}

	wantVerif := domain.Verification{
		Kind:     domain.VerificationSAS,
		TxnID:    "txn-1",
		From:     "@ada:example.org",
		Device:   "DEV",
		Emojis:   []domain.SASEmoji{{Glyph: "🐶", Name: "Dog"}, {Glyph: "🎸", Name: "Guitar"}},
		Decimals: []int{1234, 5678, 9012},
	}
	fake.Verifs <- wantVerif
	if got := take(t, r.Verifications(), "Verifications()"); !reflect.DeepEqual(got, wantVerif) {
		t.Errorf("Verifications() = %+v, want %+v", got, wantVerif)
	}
}

// An empty invite set is delivered, not dropped.
func TestEmptyInviteSetIsDelivered(t *testing.T) {
	t.Parallel()

	fake := apitest.Nop{Msgs: make(chan domain.Message), Invs: make(chan []domain.Room, 1)}
	r, h := attach(t, &fakeBackend{Nop: fake})
	go func() { _ = r.Start(context.Background()) }()
	h.waitAttached(1)

	fake.Invs <- []domain.Room{}
	if got := take(t, r.Invites(), "Invites()"); len(got) != 0 {
		t.Errorf("Invites() = %+v, want an empty set", got)
	}
}

// A verification step whose kind this build does not recognize is dropped rather than
// defaulted.
func TestUnknownVerificationKindIsDropped(t *testing.T) {
	t.Parallel()

	fake := apitest.Nop{Msgs: make(chan domain.Message), Verifs: make(chan domain.Verification, 2)}
	r, h := attach(t, &fakeBackend{Nop: fake})
	go func() { _ = r.Start(context.Background()) }()
	h.waitAttached(1)

	// A kind past the ones this build knows converts to UNSPECIFIED on the way out.
	fake.Verifs <- domain.Verification{Kind: domain.VerificationRestored + 7, TxnID: "bogus"}
	fake.Verifs <- domain.Verification{Kind: domain.VerificationDone, TxnID: "real"}

	got := take(t, r.Verifications(), "Verifications()")
	if got.TxnID != "real" || got.Kind != domain.VerificationDone {
		t.Errorf("Verifications() = %+v, want the unknown kind dropped and txn-real delivered", got)
	}
}

// Two attached clients both see every message.
func TestTwoClientsBothSeeEveryMessage(t *testing.T) {
	t.Parallel()

	msgs := make(chan domain.Message, 1)
	h := serve(t, &fakeBackend{Nop: apitest.Nop{Msgs: msgs}})
	first, second := h.client(), h.client()
	go func() { _ = first.Start(context.Background()) }()
	go func() { _ = second.Start(context.Background()) }()
	h.waitAttached(2)

	want := domain.Message{ID: "$one:example.org", RoomID: "!a:example.org", Body: "seen by both"}
	msgs <- want

	if got := take(t, first.Messages(), "first client"); got.ID != want.ID {
		t.Errorf("first client got %+v, want %+v", got, want)
	}
	if got := take(t, second.Messages(), "second client"); got.ID != want.ID {
		t.Errorf("second client got %+v, want %+v", got, want)
	}
}
