package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room's verdicts round-trip whole, per tag and case-insensitively keyed; a release is
// stored, not dropped; a write replaces the room's set; other rooms are untouched.
func TestTagVerdictsMirrorTheRoomsWholeSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x"}, {ID: "!b:x"}}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Millisecond)
	if err := cache.SetRoomVerdicts(ctx, "!b:x", []domain.TagVerdict{{Room: "!b:x", Tag: "Spam", Rule: domain.MoveAlways, At: at}}); err != nil {
		t.Fatal(err)
	}
	set := []domain.TagVerdict{
		{Room: "!a:x", Tag: "Spam", Rule: domain.MoveFirstMessage, Filter: "crypto", At: at},
		{Room: "!a:x", Tag: "Archive", Released: true, Rule: domain.MoveAlways, Filter: "kept?", At: at},
		{Room: "!a:x", Tag: "Nothing", At: at}, // neither in nor released: not stored
	}
	if err := cache.SetRoomVerdicts(ctx, "!a:x", set); err != nil {
		t.Fatal(err)
	}
	got, err := cache.RoomVerdicts(ctx, "!a:x")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Tag != "archive" || !got[0].Released || got[0].Rule != domain.MoveNone || got[0].Filter != "" ||
		got[1].Tag != "spam" || got[1].Rule != domain.MoveFirstMessage || got[1].Filter != "crypto" || !got[1].At.Equal(at) {
		t.Fatalf("RoomVerdicts = %+v, want the release and the verdict, keyed lower-case", got)
	}

	if err := cache.SetRoomVerdicts(ctx, "!a:x", []domain.TagVerdict{{Room: "!a:x", Tag: "SPAM", Rule: domain.MoveMostly, At: at}}); err != nil {
		t.Fatal(err)
	}
	if got, _ = cache.RoomVerdicts(ctx, "!a:x"); len(got) != 1 || got[0].Rule != domain.MoveMostly {
		t.Fatalf("after replacing: %+v, want the one verdict", got)
	}
	if all, _ := cache.TagVerdicts(ctx); len(all) != 2 {
		t.Errorf("TagVerdicts = %+v, want both rooms'", all)
	}
	if err := cache.SetRoomVerdicts(ctx, "!a:x", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = cache.RoomVerdicts(ctx, "!a:x"); len(got) != 0 {
		t.Errorf("after clearing: %+v", got)
	}
}
