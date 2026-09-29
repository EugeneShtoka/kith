package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The thread a message belongs to has to survive a restart like any other fact
// about it: without it the cache cannot tell a thread reply from a plain one, and
// the room reads as a braid again on the next cold start.
func TestAThreadRootSurvivesTheCache(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!work:x")
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$root", RoomID: room, Sender: "@alice:x", Body: "ship it?", Timestamp: now},
		{ID: "$reply", RoomID: room, Sender: "@bob:x", Body: "yes", Timestamp: now.Add(time.Minute), ThreadRoot: "$root"},
	}); err != nil {
		t.Fatal(err)
	}

	rows := messagesIn(t, cache, room)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want both", rows)
	}
	if rows[0].ThreadRoot != "" {
		t.Errorf("the root's ThreadRoot = %q, want empty — a root does not point at itself", rows[0].ThreadRoot)
	}
	if rows[1].ThreadRoot != "$root" {
		t.Errorf("the reply's ThreadRoot = %q, want $root", rows[1].ThreadRoot)
	}
}

// An edit carries its target's ID and its own m.replace relation, so its thread
// root is empty.
func TestEditingAMessageInAThreadKeepsItInTheThread(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!work:x")
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$reply", RoomID: room, Sender: "@bob:x", Body: "yse", Timestamp: now, ThreadRoot: "$root"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$reply", RoomID: room, Sender: "@bob:x", Body: "yes", Timestamp: now.Add(time.Minute), RevisionID: "$edit", Edited: true},
	}); err != nil {
		t.Fatal(err)
	}

	rows := messagesIn(t, cache, room)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the one edited reply", rows)
	}
	if rows[0].ThreadRoot != "$root" {
		t.Errorf("ThreadRoot = %q, want $root — the edit must not evict it", rows[0].ThreadRoot)
	}
	if rows[0].Body != "yes" {
		t.Errorf("Body = %q, want the edited text", rows[0].Body)
	}
}

// A thread root learned later (the reply arriving before the relation is known, or
// a re-walk teaching old rows their thread) writes over an empty one.
func TestALaterSaveTeachesARowItsThread(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!work:x")
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$reply", RoomID: room, Sender: "@bob:x", Body: "yes", Timestamp: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$reply", RoomID: room, Sender: "@bob:x", Body: "yes", Timestamp: now, ThreadRoot: "$root"},
	}); err != nil {
		t.Fatal(err)
	}

	if got := messagesIn(t, cache, room)[0].ThreadRoot; got != "$root" {
		t.Errorf("ThreadRoot = %q, want $root", got)
	}
}

// Sending into a thread needs the event its falling-back reply pointer should name
// — the newest message already in it, which only the process holding the cache can
// know.
func TestLatestInThread(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!work:x")
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "$root", RoomID: room, Body: "ship it?", Timestamp: now},
		{ID: "$r1", RoomID: room, Body: "yes", Timestamp: now.Add(time.Minute), ThreadRoot: "$root"},
		{ID: "$r2", RoomID: room, Body: "done", Timestamp: now.Add(2 * time.Minute), ThreadRoot: "$root"},
		{ID: "$elsewhere", RoomID: room, Body: "later", Timestamp: now.Add(3 * time.Minute)},
		{ID: "$other", RoomID: room, Body: "another thread", Timestamp: now.Add(4 * time.Minute), ThreadRoot: "$root2"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := cache.LatestInThread(ctx, room, "$root")
	if err != nil {
		t.Fatalf("LatestInThread: %v", err)
	}
	if got != "$r2" {
		t.Errorf("latest = %q, want $r2 — not the room's newest, and not another thread's", got)
	}
	// A thread nobody has answered yet has no latest, which the caller reads as
	// "the root is the whole chain" rather than as an error.
	empty, err := cache.LatestInThread(ctx, room, "$nothing")
	if err != nil || empty != "" {
		t.Errorf("LatestInThread(unanswered) = %q, %v; want empty and no error", empty, err)
	}
}
