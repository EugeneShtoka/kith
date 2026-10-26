package daemon_test

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// kithd's own sinks show the first profile: a room outside it never pops up there,
// whatever the rules say (it is still published to the profiles holding it), and one
// kithd cannot place yet is judged by what it knows (its ID and network).
func TestKithdShowsTheFirstProfile(t *testing.T) {
	t.Parallel()

	stranger := domain.RoomID(mx + "!new:example.org") // in no list kithd has read
	tests := map[string]struct {
		profiles []config.Profile
		// in are the rooms that notify, of dmRoom, chatRm and stranger.
		in []domain.RoomID
	}{
		"* is every room": {
			[]config.Profile{{Name: "all", Include: []string{"*"}}}, []domain.RoomID{dmRoom, chatRm, stranger},
		},
		"a kind of room": {[]config.Profile{{Name: "p", Include: []string{"dm"}}}, []domain.RoomID{dmRoom}},
		"a space":        {[]config.Profile{{Name: "p", Include: []string{spaceEntry}}}, []domain.RoomID{chatRm}},
		"a room by name": {[]config.Profile{{Name: "p", Include: []string{chatNamEntry}}}, []domain.RoomID{chatRm}},
		"a room by ID":   {[]config.Profile{{Name: "p", Include: []string{string(stranger)}}}, []domain.RoomID{stranger}},
		"a network":      {[]config.Profile{{Name: "p", Include: []string{"protocol:whatsapp"}}}, nil},
		"a tag":          {[]config.Profile{{Name: "p", Include: []string{"tag:Family"}}}, []domain.RoomID{dmRoom}},
		"a tag's row":    {[]config.Profile{{Name: "p", Include: []string{"place:Family"}}}, []domain.RoomID{dmRoom}},
		"exclude wins": {
			[]config.Profile{{Name: "p", Include: []string{"protocol:matrix"}, Exclude: []string{spaceEntry}}},
			[]domain.RoomID{dmRoom, stranger},
		},
		"all but one kind": {[]config.Profile{{Name: "p", Include: []string{"*"}, Exclude: []string{"dm"}}}, []domain.RoomID{chatRm, stranger}},
		"only the first counts": {
			[]config.Profile{{Name: "p", Include: []string{"dm"}}, {Name: "q", Include: []string{spaceEntry}}},
			[]domain.RoomID{dmRoom},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := notifsOn("all")
			cfg.Tags = []config.Tag{{Name: "Family", Rule: []string{"dm"}}}
			cfg.Profiles = tc.profiles
			n, rec, _ := notifier(t, cfg)
			for _, room := range []domain.RoomID{dmRoom, chatRm, stranger} {
				want := false
				for _, in := range tc.in {
					want = want || in == room
				}
				before := len(rec.all())
				n.Deliver(context.Background(), msg(room, alice, "hi"))
				if got := len(rec.all()) > before; got != want {
					t.Errorf("%s shown = %v, want %v", room, got, want)
				}
			}
		})
	}
}

// A reload that changes the first profile changes what notifies at once.
func TestAReloadedProfileNotifiesAtOnce(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("all")
	cfg.Profiles = []config.Profile{{Name: "p", Include: []string{"dm"}}}
	n, _, _ := notifier(t, cfg)
	ctx := context.Background()
	if _, notified := n.Deliver(ctx, msg(chatRm, alice, "hi")); notified {
		t.Fatal("a room outside the profile notified")
	}
	cfg.Profiles = nil
	if err := n.Reload(asStarted(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, notified := n.Deliver(ctx, msg(chatRm, alice, "hi")); !notified {
		t.Error("with the profile gone, the room still did not notify")
	}
}

// A filter moving a room into a tag the profile names brings it into the profile for
// the message that moved it.
func TestAFilterMovesARoomIntoTheProfile(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	cfg := notifsOn("all")
	cfg.Tags = []config.Tag{{Name: "Leads"}}
	cfg.Filters = []config.Filter{{Name: "prospects", Tag: "Leads", Words: []string{"quote*"}, Moves: []string{"always"}}}
	cfg.Profiles = []config.Profile{{Name: "sales", Include: []string{"tag:Leads"}}}
	n, _ := spamNotifier(t, cfg, src)
	ctx := context.Background()

	if _, notified := n.Deliver(ctx, msg(chatRm, alice, "hello")); notified {
		t.Fatal("a room outside the profile notified")
	}
	if _, notified := n.Deliver(ctx, msg(chatRm, alice, "quote please")); !notified {
		t.Error("the message that moved the room into the profile's tag did not notify")
	}
}
