package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Everything the timeline caches is answered from a key, and a cache is only ever as
// correct as that key is complete.

// Closing a room and opening another must not show the first one's timeline.
func TestCacheInvalidatedByRoomChange(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "in room A", Timestamp: at(1)},
	})
	if !strings.Contains(strings.Join(m.layoutRows(), "\n"), "in room A") {
		t.Fatal("room A's message was not drawn")
	}
	// Leaving nils the messages, which is how the client closes a room.
	m.openRoom = "!b:x"
	m = m.setMessages(nil)
	if got := strings.Join(m.layoutRows(), "\n"); strings.Contains(got, "in room A") {
		t.Errorf("room A's message survived into room B:\n%s", got)
	}
	m = m.setMessages([]domain.Message{
		{ID: "$2", RoomID: "!b:x", Sender: "@carol:x", SenderName: "Carol", Body: "in room B", Timestamp: at(2)},
	})
	if got := strings.Join(m.layoutRows(), "\n"); !strings.Contains(got, "in room B") {
		t.Errorf("room B's message was not drawn:\n%s", got)
	}
}

// A terminal resize re-wraps every message, so no row may survive it.
func TestCacheInvalidatedByWidthChange(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	long := strings.Repeat("wrap me around the pane ", 12)
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: long, Timestamp: at(1)},
	})
	wide := len(m.layoutRows())
	m.width /= 2
	narrow := len(m.layoutRows())
	if narrow <= wide {
		t.Errorf("halving the width gave %d rows, not more than the %d it wrapped to before", narrow, wide)
	}
	// And back again, to prove the entry keyed on width rather than merely being
	// dropped once.
	m.width *= 2
	if again := len(m.layoutRows()); again != wide {
		t.Errorf("restoring the width gave %d rows, want the original %d", again, wide)
	}
}

// A live config reload changes the identities and the theme, both of which the derived
// answers were computed against.
func TestCacheInvalidatedByConfigReload(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "hello", Timestamp: at(1)},
	})
	if got := strings.Join(m.layoutRows(), "\n"); !strings.Contains(got, "Bob") {
		t.Fatalf("sender name missing before the reload:\n%s", got)
	}
	cfg := m.conf.base.Clone()
	cfg.Display.Identities = []config.Identity{{Alias: "Robert", IDs: []string{"@bob:x"}}}
	next, _ := m.applyConfig(cfg, "", "")
	m = next
	if got := strings.Join(m.layoutRows(), "\n"); !strings.Contains(got, "Robert") {
		t.Errorf("the reloaded alias was not drawn:\n%s", got)
	}
}

// A thread summary draws its own unread badge, and those counts move without any
// message changing — a receipt landing, or the thread being read in another client.
func TestCacheInvalidatedByUnreadChange(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = m.setMessages([]domain.Message{
		{ID: "$root", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "question", Timestamp: at(1)},
		{ID: "$r1", RoomID: "!a:x", Sender: "@carol:x", SenderName: "Carol", Body: "answer", ThreadRoot: "$root", Timestamp: at(2)},
	})
	m.unread["!a:x"] = domain.Unread{Threads: []domain.ThreadUnread{{Root: "$root", Unread: 2}}}
	if got := strings.Join(m.layoutRows(), "\n"); !strings.Contains(got, "●2") {
		t.Fatalf("thread badge missing:\n%s", got)
	}
	// Read elsewhere: same messages, different counts.
	m.unread["!a:x"] = domain.Unread{Threads: []domain.ThreadUnread{{Root: "$root", Unread: 0}}}
	if got := strings.Join(m.layoutRows(), "\n"); strings.Contains(got, "●2") {
		t.Errorf("the thread badge survived being read:\n%s", got)
	}
}

// Selection is never cached, so moving the cursor must repaint immediately.
func TestSelectionNotCached(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m.focus = paneTimeline
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "first", Timestamp: at(1)},
		{ID: "$2", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "second", Timestamp: at(2)},
	})
	m.timeline.selected = "$1"
	one := strings.Join(m.layoutRows(), "\n")
	m.timeline.selected = "$2"
	two := strings.Join(m.layoutRows(), "\n")
	if one == two {
		t.Error("moving the selection drew an identical frame")
	}
	m.timeline.selected = "$1"
	if back := strings.Join(m.layoutRows(), "\n"); back != one {
		t.Error("moving the selection back did not restore the earlier frame")
	}
}
