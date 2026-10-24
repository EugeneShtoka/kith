package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every timeline cache is answered from a key; a cache is only as correct as its key is
// complete.

// Closing a room and opening another must not show the first one's timeline.
func TestCacheInvalidatedByRoomChange(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = mx + "!a:x"
	m = m.setMessages([]domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "in room A", Timestamp: at(1)},
	})
	if !strings.Contains(strings.Join(m.layoutRows(), "\n"), "in room A") {
		t.Fatal("room A's message was not drawn")
	}
	// Leaving nils the messages, which is how the client closes a room.
	m.openRoom = mx + "!b:x"
	m = m.setMessages(nil)
	if got := strings.Join(m.layoutRows(), "\n"); strings.Contains(got, "in room A") {
		t.Errorf("room A's message survived into room B:\n%s", got)
	}
	m = m.setMessages([]domain.Message{
		{ID: mx + "$2", RoomID: mx + "!b:x", Sender: "matrix:@carol:x", SenderName: "Carol", Body: "in room B", Timestamp: at(2)},
	})
	if got := strings.Join(m.layoutRows(), "\n"); !strings.Contains(got, "in room B") {
		t.Errorf("room B's message was not drawn:\n%s", got)
	}
}

// A terminal resize re-wraps every message, so no row may survive it.
func TestCacheInvalidatedByWidthChange(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = mx + "!a:x"
	long := strings.Repeat("wrap me around the pane ", 12)
	m = m.setMessages([]domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: long, Timestamp: at(1)},
	})
	wide := len(m.layoutRows())
	m.width /= 2
	narrow := len(m.layoutRows())
	if narrow <= wide {
		t.Errorf("halving the width gave %d rows, not more than the %d it wrapped to before", narrow, wide)
	}
	// And back again: the entry is keyed on width, not just dropped once.
	m.width *= 2
	if again := len(m.layoutRows()); again != wide {
		t.Errorf("restoring the width gave %d rows, want the original %d", again, wide)
	}
}

// A config reload changes identities and theme, which derived answers depend on.
func TestCacheInvalidatedByConfigReload(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = mx + "!a:x"
	m = m.setMessages([]domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "hello", Timestamp: at(1)},
	})
	if got := strings.Join(m.layoutRows(), "\n"); !strings.Contains(got, "Bob") {
		t.Fatalf("sender name missing before the reload:\n%s", got)
	}
	cfg := m.conf.base.Clone()
	cfg.Display.Identities = []config.Identity{{Alias: "Robert", IDs: []string{"matrix:@bob:x"}}}
	next, _ := m.applyConfig(cfg, "")
	m = next
	if got := strings.Join(m.layoutRows(), "\n"); !strings.Contains(got, "Robert") {
		t.Errorf("the reloaded alias was not drawn:\n%s", got)
	}
}

// A thread summary's unread badge changes without any message changing (a receipt, or a read
// in another client).
func TestCacheInvalidatedByUnreadChange(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = mx + "!a:x"
	m = m.setMessages([]domain.Message{
		{ID: mx + "$root", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "question", Timestamp: at(1)},
		{ID: mx + "$r1", RoomID: mx + "!a:x", Sender: "matrix:@carol:x", SenderName: "Carol", Body: "answer", ThreadRoot: mx + "$root", Timestamp: at(2)},
	})
	m.unread[mx+"!a:x"] = domain.Unread{Threads: []domain.ThreadUnread{{Root: mx + "$root", Unread: 2}}}
	if got := strings.Join(m.layoutRows(), "\n"); !strings.Contains(got, "●2") {
		t.Fatalf("thread badge missing:\n%s", got)
	}
	// Read elsewhere: same messages, different counts.
	m.unread[mx+"!a:x"] = domain.Unread{Threads: []domain.ThreadUnread{{Root: mx + "$root", Unread: 0}}}
	if got := strings.Join(m.layoutRows(), "\n"); strings.Contains(got, "●2") {
		t.Errorf("the thread badge survived being read:\n%s", got)
	}
}

// Selection is never cached, so moving the cursor must repaint immediately.
func TestSelectionNotCached(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = mx + "!a:x"
	m.focus = paneTimeline
	m = m.setMessages([]domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "first", Timestamp: at(1)},
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "second", Timestamp: at(2)},
	})
	m.timeline.selected = mx + "$1"
	one := strings.Join(m.layoutRows(), "\n")
	m.timeline.selected = mx + "$2"
	two := strings.Join(m.layoutRows(), "\n")
	if one == two {
		t.Error("moving the selection drew an identical frame")
	}
	m.timeline.selected = mx + "$1"
	if back := strings.Join(m.layoutRows(), "\n"); back != one {
		t.Error("moving the selection back did not restore the earlier frame")
	}
}
