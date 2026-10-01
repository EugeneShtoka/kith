package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A person list bigger than one statement can bind still resolves.
func TestNamesResolveBeyondTheParameterCeiling(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	room := domain.RoomID("!crowd:example.org")
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: room, Name: "Crowd"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	// Two people who are really there, so the answer is checkable rather than empty.
	for _, m := range []domain.Member{
		{UserID: "@first:example.org", DisplayName: "First"},
		{UserID: "@last:example.org", DisplayName: "Last"},
	} {
		if err := cache.SaveMember(ctx, room, m); err != nil {
			t.Fatalf("SaveMember: %v", err)
		}
	}

	ask := make([]string, 0, sqliteMaxParams+2)
	ask = append(ask, "@first:example.org")
	for i := range sqliteMaxParams {
		ask = append(ask, fmt.Sprintf("@nobody%05d:example.org", i))
	}
	ask = append(ask, "@last:example.org")
	if len(ask) <= sqliteMaxParams {
		t.Fatalf("asked for %d, which one statement can carry; the test proves nothing", len(ask))
	}

	names, err := cache.namesOf(ctx, ask, domain.EveryRoom())
	if err != nil {
		t.Fatalf("namesOf(%d people): %v", len(ask), err)
	}
	// One from the first chunk and one from the last, so a fix that only ran the first
	// batch fails here.
	if got := names["@first:example.org"]; got != "First" {
		t.Errorf("first person resolved to %q, want First", got)
	}
	if got := names["@last:example.org"]; got != "Last" {
		t.Errorf("last person resolved to %q, want Last — the final chunk was not read", got)
	}
}
