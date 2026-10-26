package daemon_test

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A notification is published to every profile holding its room, by the profile's
// name in lower case, and to none when no profile holds it, whatever the rules say. A
// room kithd cannot place yet is judged by what it knows (its ID and network).
func TestANotificationGoesToTheProfilesHoldingItsRoom(t *testing.T) {
	t.Parallel()

	stranger := domain.RoomID(mx + "!new:example.org") // in no list kithd has read
	tests := map[string]struct {
		profiles []config.Profile
		// to is each room's topics, of dmRoom, chatRm and stranger; a room left out has
		// none.
		to map[domain.RoomID]string
	}{
		"* is every room": {
			[]config.Profile{{Name: "All", Include: []string{"*"}}},
			map[domain.RoomID]string{dmRoom: "all", chatRm: "all", stranger: "all"},
		},
		"a kind of room": {[]config.Profile{{Name: "p", Include: []string{"dm"}}}, map[domain.RoomID]string{dmRoom: "p"}},
		"a space":        {[]config.Profile{{Name: "p", Include: []string{spaceEntry}}}, map[domain.RoomID]string{chatRm: "p"}},
		"a room by name": {[]config.Profile{{Name: "p", Include: []string{chatNamEntry}}}, map[domain.RoomID]string{chatRm: "p"}},
		"a room by ID":   {[]config.Profile{{Name: "p", Include: []string{string(stranger)}}}, map[domain.RoomID]string{stranger: "p"}},
		"a network":      {[]config.Profile{{Name: "p", Include: []string{"protocol:whatsapp"}}}, nil},
		"a tag":          {[]config.Profile{{Name: "p", Include: []string{"tag:Family"}}}, map[domain.RoomID]string{dmRoom: "p"}},
		"a tag's row":    {[]config.Profile{{Name: "p", Include: []string{"place:Family"}}}, map[domain.RoomID]string{dmRoom: "p"}},
		"exclude wins": {
			[]config.Profile{{Name: "p", Include: []string{"protocol:matrix"}, Exclude: []string{spaceEntry}}},
			map[domain.RoomID]string{dmRoom: "p", stranger: "p"},
		},
		"all but one kind": {
			[]config.Profile{{Name: "p", Include: []string{"*"}, Exclude: []string{"dm"}}},
			map[domain.RoomID]string{chatRm: "p", stranger: "p"},
		},
		"each profile holding it": {
			[]config.Profile{{Name: "p", Include: []string{"dm"}}, {Name: "Q", Include: []string{spaceEntry, "dm"}}},
			map[domain.RoomID]string{dmRoom: "p,q", chatRm: "q"},
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
				before := len(rec.published())
				n.Deliver(context.Background(), msg(room, alice, "hi"))
				got := ""
				if after := rec.published(); len(after) > before {
					got = strings.Join(after[before].Topics, ",")
				}
				if got != tc.to[room] {
					t.Errorf("%s published to %q, want %q", room, got, tc.to[room])
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
