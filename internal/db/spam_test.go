package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The mirror, and the one property that matters about it: a verdict comes back with the
// rule and the filter that made it, because "why is this room in Spam" has to be
// answerable offline.
func TestSpamRoomsRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}

	at := time.Now().Truncate(time.Millisecond)
	verdict := domain.SpamVerdict{Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto", At: at}
	if err := cache.SetSpam(ctx, verdict); err != nil {
		t.Fatalf("SetSpam: %v", err)
	}
	rooms, err := cache.SpamRooms(ctx)
	if err != nil {
		t.Fatalf("SpamRooms: %v", err)
	}
	if len(rooms) != 1 {
		t.Fatalf("SpamRooms = %+v, want one", rooms)
	}
	if rooms[0].Rule != domain.SpamFirstMessage || rooms[0].Filter != "crypto" {
		t.Errorf("verdict = %+v, want the rule and the filter kept", rooms[0])
	}
	if !rooms[0].At.Equal(at) {
		t.Errorf("at = %v, want %v", rooms[0].At, at)
	}

	// A second verdict on the same room replaces the first: account data is a value,
	// and this mirrors it.
	if err := cache.SetSpam(ctx, domain.SpamVerdict{Room: "!a:x", Rule: domain.SpamMostly, At: at}); err != nil {
		t.Fatalf("SetSpam(again): %v", err)
	}
	rooms, _ = cache.SpamRooms(ctx)
	if len(rooms) != 1 || rooms[0].Rule != domain.SpamMostly {
		t.Fatalf("SpamRooms = %+v, want the replacement", rooms)
	}

	if err := cache.ClearSpam(ctx, "!a:x"); err != nil {
		t.Fatalf("ClearSpam: %v", err)
	}
	if rooms, _ = cache.SpamRooms(ctx); len(rooms) != 0 {
		t.Fatalf("SpamRooms = %+v after clearing, want none", rooms)
	}
}

// A release is a value of the same key, not the absence of one: the room's account data
// says "released", and the mirror has to keep saying it.
func TestSpamReleaseIsMirroredAndReplacesTheVerdict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := time.Now().Truncate(time.Millisecond)

	// Caught first, then released — the order it happens in.
	if err := cache.SetSpam(ctx, domain.SpamVerdict{Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto", At: at}); err != nil {
		t.Fatalf("SetSpam: %v", err)
	}
	// And a release for a room the cache has not listed yet, which is the normal order on
	// a rebuilt cache: account data can arrive before the room list.
	for _, room := range []domain.RoomID{"!a:x", "!cold:x"} {
		if err := cache.SetSpam(ctx, domain.SpamVerdict{Room: room, Released: true, At: at}); err != nil {
			t.Fatalf("SetSpam(release %s): %v", room, err)
		}
	}
	rooms, err := cache.SpamRooms(ctx)
	if err != nil {
		t.Fatalf("SpamRooms: %v", err)
	}
	if len(rooms) != 2 {
		t.Fatalf("SpamRooms = %+v, want the two releases and no verdict", rooms)
	}
	for _, v := range rooms {
		if !v.Released || v.Spam() {
			t.Errorf("%s = %+v, want a release that is not spam", v.Room, v)
		}
		if !v.At.Equal(at) {
			t.Errorf("%s released at %v, want %v", v.Room, v.At, at)
		}
	}

	// A verdict written after a release replaces it, because account data is one value
	// per room; which of the two may be written is the writer's decision, not the mirror's.
	if err := cache.SetSpam(ctx, domain.SpamVerdict{Room: "!a:x", Rule: domain.SpamMostly, At: at}); err != nil {
		t.Fatalf("SetSpam(again): %v", err)
	}
	rooms, _ = cache.SpamRooms(ctx)
	for _, v := range rooms {
		if v.Room == "!a:x" && (v.Released || v.Rule != domain.SpamMostly) {
			t.Errorf("!a:x = %+v, want the verdict that replaced the release", v)
		}
	}

	// Clearing removes either.
	for _, room := range []domain.RoomID{"!a:x", "!cold:x"} {
		if err := cache.ClearSpam(ctx, room); err != nil {
			t.Fatalf("ClearSpam(%s): %v", room, err)
		}
	}
	if rooms, _ = cache.SpamRooms(ctx); len(rooms) != 0 {
		t.Fatalf("SpamRooms = %+v after clearing, want none", rooms)
	}
}
