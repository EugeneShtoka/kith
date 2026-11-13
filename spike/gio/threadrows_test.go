package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// rowNames is rows as "room", "room/root" or "room+N", to compare at a glance.
func rowNames(u *ui, rows []listRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		name := string(u.rooms[r.room].ID)
		switch {
		case r.more > 0:
			name += fmt.Sprintf("+%d", r.more)
		case r.thread.Root != "":
			name += "/" + string(r.thread.Root)
		}
		out = append(out, name)
	}
	return out
}

func threadsUnread(roots ...domain.EventID) domain.Unread {
	n := domain.Unread{Counted: true}
	for _, root := range roots {
		n.Threads = append(n.Threads, domain.ThreadUnread{Root: root, Unread: 1, Latest: root + "r"})
	}
	return n
}

// Under each room its threads with unread replies are listed, at most the cap, the rest
// counted into one row; none under a room folded, silenced, an invite, or while threads
// are never listed; the room whose count was entered lists them all; the thread open
// keeps its row once read, but only once a reply of it is held.
func TestThreadRowsUnderTheirRooms(t *testing.T) {
	u := &ui{
		rooms: []domain.Room{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d", Membership: domain.MembershipInvite}},
		unread: map[domain.RoomID]domain.Unread{
			"a": threadsUnread("a1", "a2", "a3"),
			"b": threadsUnread("b1"),
			"c": threadsUnread("c1"),
			"d": threadsUnread("d1"),
		},
		rail: rail{silenced: map[domain.RoomID]bool{"c": true}},
	}
	u.display.Threads.MaxInRoomList = 2
	all := []int{0, 1, 2, 3}
	want := func(what string, names ...string) {
		t.Helper()
		if got := rowNames(u, u.listRows(all)); !slices.Equal(got, names) {
			t.Errorf("%s: rows %v, want %v", what, got, names)
		}
	}
	want("capped", "a", "a/a1", "a/a2", "a+1", "b", "b/b1", "c", "d")

	u.expanded = "a"
	want("a's count entered", "a", "a/a1", "a/a2", "a/a3", "b", "b/b1", "c", "d")
	u.expanded = ""

	u.display.Threads.Rules = []config.ThreadRule{{Match: "a", MaxInRoomList: -1}}
	want("a rule lifting a's cap", "a", "a/a1", "a/a2", "a/a3", "b", "b/b1", "c", "d")
	u.display.Threads.Rules = nil

	u.display.Threads.Folded = map[string]bool{"a": true}
	want("a folded", "a", "b", "b/b1", "c", "d")
	u.display.Threads.Folded = nil

	u.display.Threads.InRoomList = config.ThreadsNever
	want("never", "a", "b", "c", "d")
	u.display.Threads.InRoomList = ""

	// b1 read while open in b: it keeps its row, after any unread, once a reply is held.
	u.unread["b"] = threadsUnread()
	u.current = "b"
	u.timeline = timeline{room: "b", thread: "b1", msgs: []domain.Message{{ID: "b1", Body: "the root"}}}
	want("b1 open, no reply held", "a", "a/a1", "a/a2", "a+1", "b", "c", "d")
	u.timeline.msgs = append(u.timeline.msgs, domain.Message{ID: "b1r", ThreadRoot: "b1", Timestamp: time.Unix(1, 0)})
	want("b1 open, read", "a", "a/a1", "a/a2", "a+1", "b", "b/b1", "c", "d")
	if under := u.threadsUnder("b"); under[0].Title != "the root" || under[0].Latest != "b1r" || under[0].Unread != 0 {
		t.Errorf("b1's kept row = %+v, want its root's text, its reply, nothing unread", under[0])
	}
}

// The cursor is on its row within its room, else on its room's own row: a thread row
// gone, or a row of another room, falls back to the room.
func TestTheRoomListCursorFallsBackToItsRoom(t *testing.T) {
	u := &ui{
		rooms:  []domain.Room{{ID: "a"}, {ID: "b"}},
		unread: map[domain.RoomID]domain.Unread{"a": threadsUnread("a1"), "b": threadsUnread("b1")},
	}
	rows := u.listRows([]int{0, 1}) // a, a/a1, b, b/b1
	for _, tc := range []struct {
		room domain.RoomID
		at   rowKey
		want int
	}{
		{"b", rowKey{root: "b1"}, 3},
		{"b", rowKey{}, 2},
		{"b", rowKey{root: "a1"}, 2},   // another room's thread
		{"b", rowKey{root: "gone"}, 2}, // read and gone
		{"z", rowKey{}, -1},
	} {
		u.roomAt, u.threadAt = tc.room, tc.at
		if got := u.cursorRow(rows); got != tc.want {
			t.Errorf("cursor %s %+v: row %d, want %d", tc.room, tc.at, got, tc.want)
		}
	}
}

// A fold is kept in the device file as a change to it as it is then: a fold another
// window wrote meanwhile stays, and is read back; unfolding the last empties it.
func TestAFoldIsKeptBesideAnotherWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.toml")
	u := &ui{devicePath: path, roomAt: "a", threadAt: rowKey{root: "a1"}}

	var other config.Device
	other.Display.Threads.Folded = map[string]bool{"elsewhere": true}
	if _, err := config.UpdateDevice(path, config.Device{}, other); err != nil {
		t.Fatal(err)
	}
	u.foldThreads("a")
	want := map[string]bool{"a": true, "elsewhere": true}
	if got := u.display.Threads.Folded; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("folded a: window has %v, want %v", got, want)
	}
	if u.threadAt != (rowKey{}) {
		t.Errorf("folded a: cursor stayed on %+v, want a's own row", u.threadAt)
	}
	read, err := config.LoadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := read.Display.Threads.Folded; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("folded a: file has %v, want %v", got, want)
	}
	u.foldThreads("a")
	u.foldThreads("elsewhere")
	if read, _ = config.LoadDevice(path); read.Display.Threads.Folded != nil || u.display.Threads.Folded != nil {
		t.Errorf("all unfolded: file %v, window %v, want none", read.Display.Threads.Folded, u.display.Threads.Folded)
	}
}
