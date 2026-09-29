package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A space is a bridge's when a bridge made it or fills it.
func TestSpaceOwnership(t *testing.T) {
	t.Parallel()

	child := func(sender, room string) *event.Event {
		return &event.Event{
			Sender:   id.UserID(sender),
			StateKey: &room,
			Content:  event.Content{VeryRaw: []byte(`{"via":["x"]}`)},
		}
	}
	// A removed child (empty content) says nothing about who curates the space.
	removed := func(sender, room string) *event.Event {
		return &event.Event{Sender: id.UserID(sender), StateKey: &room, Content: event.Content{VeryRaw: []byte(`{}`)}}
	}

	tests := map[string]struct {
		creator  string
		children []*event.Event
		want     domain.Protocol
	}{
		"a bridge made it": {
			creator: "@whatsappbot:x",
			want:    domain.ProtocolWhatsApp,
		},
		"you made it and a bridge fills it": {
			creator:  "@me:x",
			children: []*event.Event{child("@whatsappbot:x", "!1:x"), child("@whatsapp_44:x", "!2:x")},
			want:     domain.ProtocolWhatsApp,
		},
		"you made it and you fill it": {
			creator:  "@me:x",
			children: []*event.Event{child("@me:x", "!1:x"), child("@me:x", "!2:x")},
			want:     domain.ProtocolMatrix,
		},
		"one room of your own hands the space back to you": {
			creator:  "@me:x",
			children: []*event.Event{child("@whatsappbot:x", "!1:x"), child("@me:x", "!2:x")},
			want:     domain.ProtocolMatrix,
		},
		"two bridges filling one space is nobody's": {
			creator:  "@me:x",
			children: []*event.Event{child("@whatsappbot:x", "!1:x"), child("@slackbot:x", "!2:x")},
			want:     domain.ProtocolMatrix,
		},
		"an empty space is yours": {
			creator: "@me:x",
			want:    domain.ProtocolMatrix,
		},
		"rooms taken out say nothing about who curates it": {
			creator:  "@me:x",
			children: []*event.Event{removed("@whatsappbot:x", "!1:x")},
			want:     domain.ProtocolMatrix,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := spaceOwner(tc.creator, tc.children); got != tc.want {
				t.Errorf("owner = %q, want %q", got, tc.want)
			}
		})
	}
}

// A space's keeper is a member other than you who is in many of your rooms; no names
// are consulted.
func TestSpaceKeeper(t *testing.T) {
	t.Parallel()

	const me = "@eugene:x"
	member := func(who string, m event.Membership) (string, *event.Event) {
		return who, &event.Event{Content: event.Content{Parsed: &event.MemberEventContent{Membership: m}}}
	}
	members := func(pairs ...any) map[string]*event.Event {
		out := map[string]*event.Event{}
		for i := 0; i < len(pairs); i += 2 {
			k, _ := pairs[i].(string)
			v, _ := pairs[i+1].(*event.Event)
			out[k] = v
		}
		return out
	}
	// Whoever is inside five or more of your rooms.
	wide := map[string]bool{"@whatsappbot_bg:x": true, "@somebot:x": true}

	k1, e1 := member("@whatsappbot_bg:x", event.MembershipJoin)
	k2, e2 := member(me, event.MembershipJoin)
	if got := spaceKeeper(members(k1, e1, k2, e2), me, wide); got != "@whatsappbot_bg:x" {
		t.Errorf("a bridge in the space: keeper = %q", got)
	}

	// A space only you are in is yours, however bridged its rooms happen to be.
	if got := spaceKeeper(members(k2, e2), me, wide); got != "" {
		t.Errorf("a space of your own: keeper = %q, want none", got)
	}

	// A person you share it with is not a keeper: their reach is the rooms you share.
	k3, e3 := member("@lior:x", event.MembershipJoin)
	if got := spaceKeeper(members(k2, e2, k3, e3), me, wide); got != "" {
		t.Errorf("a person in the space: keeper = %q, want none", got)
	}

	// An invitation nobody answered says nothing about who keeps the space.
	k4, e4 := member("@somebot:x", event.MembershipInvite)
	if got := spaceKeeper(members(k2, e2, k4, e4), me, wide); got != "" {
		t.Errorf("an unanswered invite: keeper = %q, want none", got)
	}

	// You never keep your own space.
	if got := spaceKeeper(members(k2, e2), me, map[string]bool{me: true}); got != "" {
		t.Errorf("you: keeper = %q, want none", got)
	}
}

// /joined_rooms includes spaces; the refresh drops them by sifting against the cached
// hierarchy. This is the only guard (Cache.Rooms does not filter).
func TestRefreshRoomsDropsCachedSpaces(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/joined_rooms"):
			_, _ = w.Write([]byte(`{"joined_rooms": ["!chat:x", "!work:x"]}`))
		case strings.HasSuffix(r.URL.Path, "/joined_members"):
			_, _ = w.Write([]byte(`{"joined": {"@bob:x": {"display_name": "Bob"}}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	cache := testCache(t)
	if saveErr := cache.SaveSpaces(ctx, []domain.Space{{ID: "!work:x", Name: "Work"}}); saveErr != nil {
		t.Fatalf("SaveSpaces: %v", saveErr)
	}

	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	b := New(cache)
	b.client = client

	rooms, err := b.RefreshRooms(ctx)
	if err != nil {
		t.Fatalf("RefreshRooms: %v", err)
	}
	if len(rooms) != 1 || rooms[0].ID != "!chat:x" {
		t.Fatalf("RefreshRooms() = %+v, want only the chat", rooms)
	}
	cached, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(cached) != 1 || cached[0].ID != "!chat:x" {
		t.Errorf("cached rooms = %+v, want only the chat", cached)
	}
}
