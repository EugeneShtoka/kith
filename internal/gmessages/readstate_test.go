package gmessages

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A read that cannot be kept here reads nothing and owes nothing: a failed lookup of the message read through is an error, not "read
// through now"; a
// position that cannot be saved is an error, and MarkRoomsRead counts the room failed,
// not marked. Either way the network is not told.
func TestAReadThatCannotBeKeptReadsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	cache, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	a := New(cache, newSecrets(), nil, nil)
	room := roomID("phone", "c1")
	first, second := eventID("phone", "c1", "m1"), eventID("phone", "c1", "m2")
	start := time.Unix(1_700_000_000, 0)
	if err = cache.SaveMessages(ctx, room, []domain.Message{
		{ID: first, RoomID: room, Sender: "gmessages:phone/dana", Body: "seen", Timestamp: start.Add(time.Minute)},
		{ID: second, RoomID: room, Sender: "gmessages:phone/dana", Body: "not seen yet", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err = cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	away := func(table string) func() {
		if _, e := raw.ExecContext(ctx, "ALTER TABLE "+table+" RENAME TO "+table+"_away"); e != nil {
			t.Fatal(e)
		}
		return func() {
			if _, e := raw.ExecContext(ctx, "ALTER TABLE "+table+"_away RENAME TO "+table); e != nil {
				t.Fatal(e)
			}
		}
	}
	unread := func() int {
		t.Helper()
		n, _, _, e := cache.CountUnread(ctx, a.Me(), room)
		if e != nil {
			t.Fatal(e)
		}
		return n
	}
	back := away("messages")
	err = a.MarkRead(ctx, room, first, false)
	back()
	if err == nil {
		t.Error("a failed lookup was taken as a read")
	}
	if n := unread(); n != 2 {
		t.Errorf("after a failed lookup: %d unread, want 2 (nothing read)", n)
	}
	if a.reads.Has(room) {
		t.Error("the network is owed a read through a message never looked up")
	}

	back = away("room_unread")
	res, err := a.MarkRoomsRead(ctx, []domain.RoomID{room}, false)
	back()
	if err == nil && res.Marked != 0 {
		t.Errorf("a position not saved counts as marked: %+v", res)
	}
	if n := unread(); n != 2 {
		t.Errorf("after a save that failed: %d unread, want 2", n)
	}
	if a.reads.Has(room) {
		t.Error("the network is owed a read not kept here")
	}
}
