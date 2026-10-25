package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// People ranked in a set of rooms carry only the names they have there, so a nickname
// from a withheld room cannot match a kith-mcp query.
func TestSendersAreNamedFromTheirOwnRooms(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const shown, hidden = domain.RoomID(mx + "!shown:x"), domain.RoomID(mx + "!hidden:x")
	mustSave(t, cache, shown, domain.Message{ID: mx + "$1", Sender: "matrix:@dana:x", Body: "hi"})
	mustSave(t, cache, shown, domain.Message{ID: mx + "$2", Sender: "matrix:@eve:x", Body: "hello"})
	for _, m := range []struct {
		room domain.RoomID
		who  domain.Member
	}{
		{hidden, domain.Member{UserID: "matrix:@dana:x", DisplayName: "Secret Nick"}},
		{shown, domain.Member{UserID: "matrix:@eve:x", DisplayName: "Eve"}},
		{hidden, domain.Member{UserID: "matrix:@eve:x", DisplayName: "Eve Elsewhere"}},
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
	if names["matrix:@dana:x"] != "" {
		t.Errorf("@dana:x is named %q, a name from a room outside the set", names["matrix:@dana:x"])
	}
	if names["matrix:@eve:x"] != "Eve" {
		t.Errorf("@eve:x is named %q, want Eve, her name in the room asked about", names["matrix:@eve:x"])
	}

	// With no rooms given, every room counts.
	all, err := cache.SearchSenders(ctx, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.UserID == "matrix:@dana:x" && m.DisplayName != "Secret Nick" {
			t.Errorf("unscoped, @dana:x is named %q, want her only name", m.DisplayName)
		}
	}
}
