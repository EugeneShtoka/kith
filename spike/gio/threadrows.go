package main

import (
	"context"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// listRow is one row of the room list, as the TUI's roomRow: a room (an index of
// u.rooms), one of its threads, or the count of its threads not listed. A count's row
// carries the first thread it stands for, where the cursor goes once it is entered.
type listRow struct {
	room   int
	thread domain.ThreadUnread
	more   int
}

// rowKey is a row within its room: a thread's or a count's by the thread's root (one
// room's), the room's own by none.
type rowKey struct {
	root domain.EventID
	more bool
}

func (r listRow) key() rowKey { return rowKey{r.thread.Root, r.more > 0} }

// listRows is the room list as drawn (roomrows.go roomRows, threadrows.ts listRows): each
// of shown's rooms, and under it its threads (threadsUnder), at most threadCap of them,
// the rest counted into one row. None are listed while [display.threads] in_room_list is
// "never", nor under an invite, a room a silent tag holds, or one folded away; the room
// whose count was entered lists them all.
func (u *ui) listRows(shown []int) []listRow {
	threads := u.display.Threads
	rows := make([]listRow, 0, len(shown))
	for _, i := range shown {
		room := u.rooms[i]
		rows = append(rows, listRow{room: i})
		if threads.Mode() == config.ThreadsNever || room.IsInvite() || u.rail.silenced[room.ID] || threads.Folds(string(room.ID)) {
			continue
		}
		under := u.threadsUnder(room.ID)
		listed := len(under)
		if c := u.threadCap(room.ID); c > 0 && listed > c && room.ID != u.expanded {
			listed = c
		}
		for _, th := range under[:listed] {
			rows = append(rows, listRow{room: i, thread: th})
		}
		if rest := len(under) - listed; rest > 0 {
			rows = append(rows, listRow{room: i, thread: under[listed], more: rest})
		}
	}
	return rows
}

// threadsUnder is room's threads with unread replies, newest first, then in the open
// room the thread open beside the timeline, which keeps its row once read.
// shortcut: in_room_list = "all" lists these too, as kithd's ListThreads is not read
// per room here; read it when this window is to list every thread.
func (u *ui) threadsUnder(room domain.RoomID) []domain.ThreadUnread {
	out := slices.Clone(u.unread[room].Threads)
	t := &u.timeline
	if t.room != room || t.thread == "" || slices.ContainsFunc(out, func(th domain.ThreadUnread) bool { return th.Root == t.thread }) {
		return out
	}
	open := domain.ThreadUnread{Root: t.thread}
	for _, m := range slices.Backward(t.msgs) {
		switch {
		case m.ID == t.thread:
			open.Title = m.Summary()
		case m.ThreadRoot == t.thread && open.Latest == "":
			open.Latest, open.LatestAt = m.ID, m.Timestamp
		}
	}
	if open.Latest == "" {
		return out // no reply held yet: no row, as the TUI's pinnedThreadRow
	}
	return append(out, open)
}

// threadCap is how many of room's threads are listed under it: the narrowest
// [[display.threads.rule]] naming it or its first home, else max_in_room_list; 0 is no cap.
func (u *ui) threadCap(room domain.RoomID) int {
	threads := u.display.Threads
	rules := make([]domain.ThreadRule, 0, len(threads.Rules))
	for _, r := range threads.Rules {
		rules = append(rules, domain.ThreadRule{Match: r.Match, Max: r.MaxInRoomList})
	}
	scope := domain.Scope{RoomID: string(room)}
	if r, ok := u.rail.served[room]; ok {
		scope.Space, scope.SpaceLabel = r.Home, r.HomeLabel
	}
	return domain.ThreadCap(threads.MaxInRoomList, config.DefaultThreadsInRoomList, rules, scope)
}

// cursorRow is the index in rows of the room list's cursor: its thread's or count's row,
// else its room's own; -1 when no row is its room's.
func (u *ui) cursorRow(rows []listRow) int {
	at := -1
	for i, r := range rows {
		if u.rooms[r.room].ID != u.roomAt {
			continue
		}
		if r.key() == u.threadAt {
			return i
		}
		if r.key() == (rowKey{}) {
			at = i
		}
	}
	return at
}

// enterRow does what Enter or a click does on row: a room's opens it, a thread's opens
// the thread beside its room, a count's lists every thread of its room, the cursor on
// the first it stood for.
func (u *ui) enterRow(ctx context.Context, row listRow) {
	room := u.rooms[row.room].ID
	u.roomAt, u.threadAt = room, row.key()
	if room != u.current {
		u.openLocked(ctx, room)
	}
	switch {
	case row.more > 0:
		u.expanded, u.threadAt.more = room, false
	case row.thread.Root != "":
		u.toPane(paneTimeline)
		u.openThread(ctx, row.thread.Root)
	}
}

// threadLabel names a thread's row: its root's text on one line, else a fallback.
func threadLabel(th domain.ThreadUnread) string {
	if title := strings.Join(strings.Fields(th.Title), " "); title != "" {
		return title
	}
	return "an older thread"
}

// foldThreads hides room's thread rows, or shows them again (rooms.fold_threads), as the
// TUI and the desktop do: kept in the device file ([display.threads] folded), written as
// a change to the file as it is then, so another window's folds stay, and read back
// with them.
// shortcut: written on the UI goroutine, a file lock and a small write; move it off if a
// held lock is ever seen to stall a frame.
func (u *ui) foldThreads(room domain.RoomID) {
	was := config.Device{Display: u.display}
	now := was
	folded := maps.Clone(u.display.Threads.Folded)
	if folded[string(room)] {
		delete(folded, string(room))
	} else {
		if folded == nil {
			folded = map[string]bool{}
		}
		folded[string(room)] = true
		u.threadAt = rowKey{} // its rows go: the cursor to its room's
	}
	now.Display.Threads.Folded = folded
	u.display.Threads.Folded = folded
	written, err := config.UpdateDevice(u.devicePath, was, now)
	if err != nil {
		log.Printf("fold threads: %v", err)
		return
	}
	u.display.Threads.Folded = written.Display.Threads.Folded
}
