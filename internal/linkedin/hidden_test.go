package linkedin

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Hiding a person recounts the rooms they wrote in, and streams the new count.
func TestHidingAPersonRecountsTheirRooms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache, err := db.Open(ctx, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := New(cache, newSecrets(), nil, nil)
	const room domain.RoomID = "linkedin:1001/2002"
	start := time.Unix(1_700_000_000, 0)
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "linkedin:1001/2002/1", RoomID: room, Sender: "linkedin:3003", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "linkedin:1001/2002/2", RoomID: room, Sender: "linkedin:4004", Body: "buy", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	cache.OnHiddenChange(a.RecountUnread)
	if err := cache.UseHidden(ctx, []string{"linkedin:4004"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-a.Unread():
		if u.RoomID != room || !u.Counted || u.Messages != 1 {
			t.Errorf("streamed %+v, want the room counted at 1 without the hidden person", u)
		}
	default:
		t.Fatal("hiding a person streamed no recount")
	}
}

// A filter's move of a conversation and its mark on a person are kept here, and move
// with their tag's new name.
func TestVerdictsAreKeptAndRetagged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache, err := db.Open(ctx, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := New(cache, newSecrets(), nil, nil)
	const room domain.RoomID = "linkedin:1001/2002"
	if err = a.MarkTag(ctx, domain.TagVerdict{Room: room, Tag: "Spam", Rule: domain.MoveAlways, Filter: "recruiters"}); err != nil {
		t.Fatal(err)
	}
	if err = a.MarkPerson(ctx, domain.PersonVerdict{Person: "linkedin:4004", Tag: "Spam", Rule: domain.MoveAlways, Filter: "recruiters"}); err != nil {
		t.Fatal(err)
	}
	if err = a.RetagVerdicts(ctx, "Spam", "Junk"); err != nil {
		t.Fatal(err)
	}
	rooms, err := cache.TagVerdicts(ctx)
	if err != nil || len(rooms) != 1 || rooms[0].Room != room || rooms[0].Tag != "junk" {
		t.Errorf("kept %+v (%v), want the move under Junk", rooms, err)
	}
	people, err := cache.PersonVerdicts(ctx)
	if err != nil || len(people) != 1 || people[0].Person != "linkedin:4004" || people[0].Tag != "junk" {
		t.Errorf("kept %+v (%v), want the mark under Junk", people, err)
	}
}
