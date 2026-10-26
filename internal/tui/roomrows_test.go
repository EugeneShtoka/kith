package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// mark is the default thread-row marker.
var mark = config.Threads{}.RowMark()

// withThreadRows opens a room list where Alpha has two unread threads.
func withThreadRows(t *testing.T, display config.Display) Model {
	t.Helper()
	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, display)))
	return update(t, m, unreadUpdateMsg{u: domain.Unread{
		RoomID: mx + "!a:x", Counted: true, Messages: 3,
		Threads: []domain.ThreadUnread{
			{Root: mx + "$one", Unread: 2, Latest: mx + "$l1", Title: "ship the release notes"},
			{Root: mx + "$two", Unread: 1, Latest: mx + "$l2", Title: "Q3 planning"},
		},
	}})
}

// An unread thread appears beneath its room with its own count.
func TestUnreadThreadsAreListedUnderTheirRoom(t *testing.T) {
	t.Parallel()

	m := withThreadRows(t, config.Display{})
	m.focus = paneRooms
	frame := stripStyles(m.View().Content)
	for _, want := range []string{"Alpha", mark + " ship the release", mark + " Q3 planning", "●2", "●1"} {
		if !strings.Contains(frame, want) {
			t.Errorf("%q is not in the room list:\n%s", want, frame)
		}
	}
}

// The thread cap names what it left out.
func TestTheThreadCapSaysWhatItLeftOut(t *testing.T) {
	t.Parallel()

	m := withThreadRows(t, config.Display{Threads: config.Threads{MaxInRoomList: 1}})
	m.focus = paneRooms
	frame := stripStyles(m.View().Content)
	if !strings.Contains(frame, mark+" ship the release") {
		t.Errorf("the first thread should still be listed:\n%s", frame)
	}
	if strings.Contains(frame, "Q3 planning") {
		t.Errorf("the cap did not apply:\n%s", frame)
	}
	if !strings.Contains(frame, "+1 more thread") {
		t.Errorf("the overflow was not said:\n%s", frame)
	}
}

func TestThreadsCanBeKeptOutOfTheRoomList(t *testing.T) {
	t.Parallel()

	m := withThreadRows(t, config.Display{Threads: config.Threads{InRoomList: "never"}})
	m.focus = paneRooms
	if frame := stripStyles(m.View().Content); strings.Contains(frame, mark) {
		t.Errorf("threads are listed under never:\n%s", frame)
	}
}

// The cursor walks rooms and threads as one list, skipping the overflow line.
func TestTheCursorWalksRoomsAndThreadsAsOneList(t *testing.T) {
	t.Parallel()

	m := withThreadRows(t, config.Display{Threads: config.Threads{MaxInRoomList: 1}})
	m.focus = paneRooms
	m, _ = m.selectRoom(m.filteredRooms()[0])
	if got := m.roomCursor(); got != 0 {
		t.Fatalf("cursor = %d, want the room's own row", got)
	}

	m, _ = m.stepRow(1)
	if m.rows.cursor != mx+"$one" {
		t.Fatalf("threadCursor = %q, want the first thread row", m.rows.cursor)
	}
	m, _ = m.stepRow(1)
	if m.openRoom != mx+"!b:x" || m.rows.cursor != "" {
		t.Errorf("stepped to open=%q thread=%q, want Bravo's own row", m.openRoom, m.rows.cursor)
	}
}

// Marking a thread read from its row sends only that thread's receipt.
func TestMarkingAThreadReadFromItsRow(t *testing.T) {
	t.Parallel()

	b := &readBackend{}
	m := sized(t, withRooms(t, starterNew(b, config.Display{})))
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{
		RoomID: mx + "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: mx + "$one", Unread: 2, Latest: mx + "$l1", Title: "ship it"}},
	}})
	m.focus = paneRooms
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m, _ = m.stepRow(1)

	row, ok := m.selectedRow()
	if !ok || !row.isThread() {
		t.Fatalf("the cursor is not on a thread row: %+v", row)
	}
	m, cmd := m.markThreadRowRead(row)
	deliver(t, m, cmd)

	if len(b.threads) != 1 || b.threads[0] != [2]domain.EventID{mx + "$one", mx + "$l1"} {
		t.Errorf("thread receipts = %v, want $one read to its newest unread reply", b.threads)
	}
	if len(b.main) != 0 {
		t.Errorf("marking a thread read sent the room's receipt too: %v", b.main)
	}
}

// A thread row under the cursor survives going read.
func TestTheSelectedThreadRowSurvivesBeingRead(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{
		RoomID: mx + "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: mx + "$root", Unread: 2, Latest: mx + "$r2", Title: "ship it"}},
	}})
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: threaded()})
	m, _ = m.stepRow(1)
	if m.rows.cursor != mx+"$root" {
		t.Fatalf("threadCursor = %q, want the thread row", m.rows.cursor)
	}

	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: mx + "!a:x", Counted: true}})
	row, ok := m.selectedRow()
	if !ok || !row.isThread() || row.thread.Root != mx+"$root" {
		t.Fatalf("the row under the cursor went away: %+v", row)
	}
	if row.thread.Unread != 0 {
		t.Errorf("a read thread's row still carries a badge: %+v", row.thread)
	}
}

// Opening a thread row opens the thread once the room's history lands.
func TestOpeningAThreadRowOpensTheThread(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{
		RoomID: mx + "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: mx + "$root", Unread: 2, Latest: mx + "$r2", Title: "ship it"}},
	}})
	m.focus = paneRooms
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m, _ = m.stepRow(1)
	m, _ = m.openSelectedRoom()

	if m.thread.open() {
		t.Fatal("the thread opened before its room's history arrived")
	}
	if m.rows.opening != mx+"$root" {
		t.Fatalf("openingThread = %q, want the row's thread held until the history lands", m.rows.opening)
	}
	// The backfill reads the cache past the network's last page before it settles.
	m, cmd := asModel(m.Update(timelineMsg{roomID: mx + "!a:x", page: threaded()}))
	m = deliver(t, m, cmd)
	if m.thread.root != mx+"$root" {
		t.Errorf("thread root = %q, want the row's thread once the history landed", m.thread.root)
	}
	if m.rows.opening != "" {
		t.Error("the held request was not cleared")
	}
}

// Sending marks the conversation read (focus is on the editor, so the incoming-message mark
// does not fire).
func TestSendingMarksTheConversationRead(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = m.setMessages([]domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@them:x", Body: "one"},
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@them:x", Body: "two"},
	})
	if m.receipts.marked == mx+"$2" || m.receipts.sending == mx+"$2" {
		t.Fatal("already marked read before sending — the test proves nothing")
	}

	sent, cmd := asModel(m.Update(sentMsg{}))
	if sent.receipts.sending != mx+"$2" {
		t.Errorf("receipt sent for %q after sending, want the newest message %q", sent.receipts.sending, mx+"$2")
	}
	if cmd == nil {
		t.Error("sending issued no read receipt")
	}
}

// Sending in a thread marks only that thread read.
func TestSendingInAThreadMarksOnlyThatThread(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = m.setMessages([]domain.Message{
		{ID: mx + "$main", RoomID: mx + "!a:x", Sender: "matrix:@them:x", Body: "on the main timeline"},
		{ID: mx + "$root", RoomID: mx + "!a:x", Sender: "matrix:@them:x", Body: "thread root"},
		{ID: mx + "$reply", RoomID: mx + "!a:x", Sender: "matrix:@them:x", ThreadRoot: mx + "$root", Body: "in the thread"},
	})
	m.thread = threadState{root: mx + "$root"}

	sent, _ := asModel(m.Update(sentMsg{}))
	if sent.receipts.sending != mx+"$reply" {
		t.Errorf("receipt sent for %q, want the thread's newest %q", sent.receipts.sending, mx+"$reply")
	}
}

// A failed send marks nothing.
func TestAFailedSendMarksNothing(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = m.setMessages([]domain.Message{{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@them:x", Body: "one"}})

	failed, _ := asModel(m.Update(sentMsg{err: context.Canceled}))
	if failed.receipts.marked == mx+"$1" || failed.receipts.sending == mx+"$1" {
		t.Error("a failed send marked the conversation read")
	}
}
