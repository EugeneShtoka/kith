package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
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

// The four remaining streams deliver with payloads intact.
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

	wantUnread := domain.Unread{RoomID: mx + "!a:example.org", Notifications: 4, Highlights: 2, ReadEvent: mx + "$seen:example.org"}
	fake.Unreads <- wantUnread
	if got := take(t, r.Unread(), "Unread()"); !reflect.DeepEqual(got, wantUnread) {
		t.Errorf("Unread() = %+v, want %+v", got, wantUnread)
	}

	wantReact := domain.ReactionUpdate{
		Reaction: domain.Reaction{ID: mx + "$r:example.org", RoomID: mx + "!a:example.org", Target: mx + "$m:example.org", Sender: "matrix:@ada:example.org", Key: "👍"},
		Removed:  true,
	}
	fake.Reacts <- wantReact
	if got := take(t, r.Reactions(), "Reactions()"); got != wantReact {
		t.Errorf("Reactions() = %+v, want %+v", got, wantReact)
	}

	wantInvites := []domain.Room{{ID: mx + "!invite:example.org", Name: "Book club", Membership: domain.MembershipInvite, InvitedBy: "matrix:@ada:example.org"}}
	fake.Invs <- wantInvites
	if got := take(t, r.Invites(), "Invites()"); !reflect.DeepEqual(got, wantInvites) {
		t.Errorf("Invites() = %+v, want %+v", got, wantInvites)
	}

	wantVerif := domain.Verification{
		Kind:     domain.VerificationSAS,
		TxnID:    "txn-1",
		From:     "matrix:@ada:example.org",
		Device:   "DEV",
		Emojis:   []domain.SASEmoji{{Glyph: "🐶", Name: "Dog"}, {Glyph: "🎸", Name: "Guitar"}},
		Decimals: []int{1234, 5678, 9012},
	}
	fake.Verifs <- wantVerif
	if got := take(t, r.Verifications(), "Verifications()"); !reflect.DeepEqual(got, wantVerif) {
		t.Errorf("Verifications() = %+v, want %+v", got, wantVerif)
	}
}

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

// A verification kind this build does not know is dropped, not defaulted.
func TestUnknownVerificationKindIsDropped(t *testing.T) {
	t.Parallel()

	fake := apitest.Nop{Msgs: make(chan domain.Message), Verifs: make(chan domain.Verification, 2)}
	r, h := attach(t, &fakeBackend{Nop: fake})
	go func() { _ = r.Start(context.Background()) }()
	h.waitAttached(1)

	// An unknown kind converts to UNSPECIFIED on the way out.
	fake.Verifs <- domain.Verification{Kind: domain.VerificationRestored + 7, TxnID: "bogus"}
	fake.Verifs <- domain.Verification{Kind: domain.VerificationDone, TxnID: "real"}

	got := take(t, r.Verifications(), "Verifications()")
	if got.TxnID != "real" || got.Kind != domain.VerificationDone {
		t.Errorf("Verifications() = %+v, want the unknown kind dropped and txn-real delivered", got)
	}
}

func TestTwoClientsBothSeeEveryMessage(t *testing.T) {
	t.Parallel()

	msgs := make(chan domain.Message, 1)
	h := serve(t, &fakeBackend{Nop: apitest.Nop{Msgs: msgs}})
	first, second := h.client(), h.client()
	go func() { _ = first.Start(context.Background()) }()
	go func() { _ = second.Start(context.Background()) }()
	h.waitAttached(2)

	want := domain.Message{ID: mx + "$one:example.org", RoomID: mx + "!a:example.org", Body: "seen by both"}
	msgs <- want

	if got := take(t, first.Messages(), "first client"); got.ID != want.ID {
		t.Errorf("first client got %+v, want %+v", got, want)
	}
	if got := take(t, second.Messages(), "second client"); got.ID != want.ID {
		t.Errorf("second client got %+v, want %+v", got, want)
	}
}

// A network rewriting its rooms reaches every attached client.
func TestRoomsChangedReachesTheClient(t *testing.T) {
	t.Parallel()
	r, h := attach(t, &fakeBackend{Nop: apitest.Nop{}})
	go func() { _ = r.Start(context.Background()) }()
	h.waitAttached(1)
	deadline := time.Now().Add(settle)
	for {
		h.streams.RoomsChanged()
		select {
		case <-r.RoomsChanged():
			return
		case <-time.After(20 * time.Millisecond): // its stream may not be open yet
		}
		if time.Now().After(deadline) {
			t.Fatal("RoomsChanged() never arrived")
		}
	}
}

// A window's config change reaches every other window; one on a stale revision comes
// back as ErrConfigMoved.
func TestAConfigChangeReachesEveryWindow(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("allow_token_file = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := serve(t, &fakeBackend{Nop: apitest.Nop{}}, func(d *daemon.Daemon) { d.Config = daemon.NewConfigFile(path, "") })
	saving, watching := h.client(), h.client()
	go func() { _ = watching.Start(context.Background()) }()
	h.waitAttached(1)
	ctx := t.Context()
	read, err := saving.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changed := read.Config.Clone()
	changed.Display.FPS = 42
	original := read.Revision // older than whatever lands
	deadline := time.Now().Add(settle)
	var rev string
	for { // the watching window's stream may not be open yet: save until it hears one
		rev, err = saving.UpdateConfig(ctx, read.Revision, changed)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case snap := <-watching.ConfigChanges():
			if snap.Config.Display.FPS != 42 || snap.Revision != rev {
				t.Errorf("heard %d at %s, want 42 at %s", snap.Config.Display.FPS, snap.Revision, rev)
			}
			if _, err := saving.UpdateConfig(ctx, original, changed); !errors.Is(err, api.ErrConfigMoved) {
				t.Errorf("a save on the older revision = %v, want ErrConfigMoved", err)
			}
			return
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the change never reached the other window")
		}
		read.Revision = rev
	}
}
