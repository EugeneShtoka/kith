package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An edit that was once cached as a message of its own leaves a row keyed on the
// edit's event ID — the "* <new text>" duplicate.
func TestSavingAnEditDropsTheRowItWasOnceCachedAs(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!notes:x")
	sent := time.Date(2026, 8, 20, 12, 54, 0, 0, time.UTC)

	// The state a pre-fix cache is in: the original, and the edit stored beside it.
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$original", RoomID: room, Sender: "@me:x", Body: "Панамка", Timestamp: sent},
		{ID: "$theEdit", RoomID: room, Sender: "@me:x", Body: "* Панамка, сыр", Timestamp: sent.Add(10 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(messagesIn(t, cache, room)); got != 2 {
		t.Fatalf("%d rows, want the two a pre-fix cache holds", got)
	}

	// The edit, folded the way it is read now.
	if err := cache.SaveMessages(ctx, room, []domain.Message{{
		ID: "$original", RoomID: room, Sender: "@me:x", Body: "Панамка, сыр",
		RevisionID: "$theEdit", Edited: true, Timestamp: sent.Add(10 * time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}

	rows := messagesIn(t, cache, room)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the duplicate gone", rows)
	}
	if rows[0].ID != "$original" || !rows[0].Edited {
		t.Errorf("row = %+v, want the edited original", rows[0])
	}
	if rows[0].Body != "Панамка, сыр" {
		t.Errorf("body = %q, want the replacement text", rows[0].Body)
	}
	// The original's position is kept: an edit's later timestamp must not move it.
	if !rows[0].Timestamp.Equal(sent) {
		t.Errorf("timestamp = %v, want the original's %v", rows[0].Timestamp, sent)
	}
}

// A message that revises nothing deletes nothing — the cleanup must not reach beyond
// the row the edit was actually stored as.
func TestRevisionCleanupTouchesNothingElse(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!notes:x")
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$a", RoomID: room, Sender: "@me:x", Body: "* a genuine markdown bullet", Timestamp: now},
		{ID: "$b", RoomID: room, Sender: "@me:x", Body: "another", Timestamp: now.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$b", RoomID: room, Sender: "@me:x", Body: "another, edited", RevisionID: "$b", Edited: true, Timestamp: now.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(messagesIn(t, cache, room)); got != 2 {
		t.Errorf("%d rows, want both kept — a bullet is not an edit fallback", got)
	}
}

// messagesIn is the room's cached rows, newest last.
func messagesIn(t *testing.T, cache *Cache, room domain.RoomID) []domain.Message {
	t.Helper()
	got, err := cache.Messages(context.Background(), room, 50)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	return got
}
