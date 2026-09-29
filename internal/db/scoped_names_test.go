package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The people ranked in a set of rooms are named as they are in those rooms. A name
// someone uses only elsewhere stays out: kith-mcp matches the agent's query against
// these names, and a nickname from a withheld room would answer it.
func TestSendersAreNamedFromTheirOwnRooms(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const shown, hidden = domain.RoomID("!shown:x"), domain.RoomID("!hidden:x")
	mustSave(t, cache, shown, domain.Message{ID: "$1", Sender: "@dana:x", Body: "hi"})
	mustSave(t, cache, shown, domain.Message{ID: "$2", Sender: "@eve:x", Body: "hello"})
	for _, m := range []struct {
		room domain.RoomID
		who  domain.Member
	}{
		{hidden, domain.Member{UserID: "@dana:x", DisplayName: "Secret Nick"}},
		{shown, domain.Member{UserID: "@eve:x", DisplayName: "Eve"}},
		{hidden, domain.Member{UserID: "@eve:x", DisplayName: "Eve Elsewhere"}},
	} {
		if err := cache.SaveMember(ctx, m.room, m.who); err != nil {
			t.Fatal(err)
		}
	}

	got, err := cache.SearchSenders(ctx, domain.TheseRooms([]domain.RoomID{shown}), 10)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, m := range got {
		names[m.UserID] = m.DisplayName
	}
	if names["@dana:x"] != "" {
		t.Errorf("@dana:x is named %q, a name from a room outside the set", names["@dana:x"])
	}
	if names["@eve:x"] != "Eve" {
		t.Errorf("@eve:x is named %q, want Eve, her name in the room asked about", names["@eve:x"])
	}

	// With no rooms given, every room counts.
	all, err := cache.SearchSenders(ctx, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.UserID == "@dana:x" && m.DisplayName != "Secret Nick" {
			t.Errorf("unscoped, @dana:x is named %q, want her only name", m.DisplayName)
		}
	}
}
