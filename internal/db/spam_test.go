package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A verdict round-trips with its rule and filter, so "why is this Spam" is answerable
// offline.
func TestSpamRoomsRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
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

	// A second verdict replaces the first: account data is one value per room.
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

// A release is a stored value, not the absence of a verdict.
func TestSpamReleaseIsMirroredAndReplacesTheVerdict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := time.Now().Truncate(time.Millisecond)

	if err := cache.SetSpam(ctx, domain.SpamVerdict{Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto", At: at}); err != nil {
		t.Fatalf("SetSpam: %v", err)
	}
	// Also a room not listed yet: account data can arrive before the room list.
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

	// A later verdict replaces a release; which may be written is the writer's call.
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
