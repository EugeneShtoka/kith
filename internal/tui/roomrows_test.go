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
		RoomID: "!a:x", Counted: true, Messages: 3,
		Threads: []domain.ThreadUnread{
			{Root: "$one", Unread: 2, Latest: "$l1", Title: "ship the release notes"},
			{Root: "$two", Unread: 1, Latest: "$l2", Title: "Q3 planning"},
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
	if m.rows.cursor != "$one" {
		t.Fatalf("threadCursor = %q, want the first thread row", m.rows.cursor)
	}
	m, _ = m.stepRow(1)
	if m.openRoom != "!b:x" || m.rows.cursor != "" {
		t.Errorf("stepped to open=%q thread=%q, want Bravo's own row", m.openRoom, m.rows.cursor)
	}
}

// Marking a thread read from its row sends only that thread's receipt.
func TestMarkingAThreadReadFromItsRow(t *testing.T) {
	t.Parallel()

	b := &readBackend{}
	m := sized(t, withRooms(t, starterNew(b, config.Display{})))
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{
		RoomID: "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: "$one", Unread: 2, Latest: "$l1", Title: "ship it"}},
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

	if len(b.threads) != 1 || b.threads[0] != [2]domain.EventID{"$one", "$l1"} {
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
		RoomID: "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: "$root", Unread: 2, Latest: "$r2", Title: "ship it"}},
	}})
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = update(t, m, timelineMsg{roomID: "!a:x", page: threaded()})
	m, _ = m.stepRow(1)
	if m.rows.cursor != "$root" {
		t.Fatalf("threadCursor = %q, want the thread row", m.rows.cursor)
	}

	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!a:x", Counted: true}})
	row, ok := m.selectedRow()
	if !ok || !row.isThread() || row.thread.Root != "$root" {
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
		RoomID: "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: "$root", Unread: 2, Latest: "$r2", Title: "ship it"}},
	}})
	m.focus = paneRooms
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m, _ = m.stepRow(1)
	m, _ = m.openSelectedRoom()

	if m.thread.open() {
		t.Fatal("the thread opened before its room's history arrived")
	}
	if m.rows.opening != "$root" {
		t.Fatalf("openingThread = %q, want the row's thread held until the history lands", m.rows.opening)
	}
	m = update(t, m, timelineMsg{roomID: "!a:x", page: threaded()})
	if m.thread.root != "$root" {
		t.Errorf("thread root = %q, want the row's thread once the history landed", m.thread.root)
	}
	if m.rows.opening != "" {
		t.Error("the held request was not cleared")
	}
}

// Sending marks the conversation read (focus is the editor while composing, so the
// incoming-message mark does not fire).
func TestSendingMarksTheConversationRead(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@them:x", Body: "one"},
		{ID: "$2", RoomID: "!a:x", Sender: "@them:x", Body: "two"},
	})
	if m.receipts.marked == "$2" || m.receipts.sending == "$2" {
		t.Fatal("already marked read before sending — the test proves nothing")
	}

	sent, cmd := asModel(m.Update(sentMsg{}))
	if sent.receipts.sending != "$2" {
		t.Errorf("receipt sent for %q after sending, want the newest message %q", sent.receipts.sending, "$2")
	}
	if cmd == nil {
		t.Error("sending issued no read receipt")
	}
}

// ...but only the thread you replied in, not the main timeline.
func TestSendingInAThreadMarksOnlyThatThread(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = m.setMessages([]domain.Message{
		{ID: "$main", RoomID: "!a:x", Sender: "@them:x", Body: "on the main timeline"},
		{ID: "$root", RoomID: "!a:x", Sender: "@them:x", Body: "thread root"},
		{ID: "$reply", RoomID: "!a:x", Sender: "@them:x", ThreadRoot: "$root", Body: "in the thread"},
	})
	m.thread = threadState{root: "$root"}

	sent, _ := asModel(m.Update(sentMsg{}))
	if sent.receipts.sending != "$reply" {
		t.Errorf("receipt sent for %q, want the thread's newest %q", sent.receipts.sending, "$reply")
	}
}

// A failed send marks nothing.
func TestAFailedSendMarksNothing(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = m.setMessages([]domain.Message{{ID: "$1", RoomID: "!a:x", Sender: "@them:x", Body: "one"}})

	failed, _ := asModel(m.Update(sentMsg{err: context.Canceled}))
	if failed.receipts.marked == "$1" || failed.receipts.sending == "$1" {
		t.Error("a failed send marked the conversation read")
	}
}
