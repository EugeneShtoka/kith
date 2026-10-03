package tui

import (
	"fmt"
	"log/slog"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// threadRows is the room list's thread state (the thread pane is threadState).
type threadRows struct {
	// cursor is the thread row the room-list cursor is on (by root, since rows
	// reindex); empty means the room's own row.
	cursor domain.EventID
	// opening is a thread a row asked to open, held until the room's messages arrive.
	opening domain.EventID
	// known is what ListThreads last answered per room (in_room_list = "all" only).
	known map[domain.RoomID][]domain.Thread
}

func newThreadRows() threadRows {
	return threadRows{known: map[domain.RoomID][]domain.Thread{}}
}

// recording stores what ListThreads answered for a room.
func (t threadRows) recording(room domain.RoomID, threads []domain.Thread) threadRows {
	t.known = withEntry(t.known, room, threads)
	return t
}

// taking returns the pending open request and clears it, so it is answered once.
func (t threadRows) taking() (domain.EventID, threadRows) {
	root := t.opening
	t.opening = ""
	return root, t
}

// roomRow is one room-list row: a room, one of its threads (listed beneath it so
// activity on an old root is findable), or an overflow line counting threads not
// listed.
type roomRow struct {
	room   domain.Room
	thread domain.ThreadUnread
	// more is how many threads an overflow row stands for; the cursor skips it.
	more int
}

func (r roomRow) isThread() bool { return r.thread.Root != "" }

func (r roomRow) selectable() bool { return r.more == 0 }

// roomRows is the room list as drawn: every room of the current group followed by
// its thread rows. The cursor, window and counter all read this one list.
func (m Model) roomRows() []roomRow {
	if m.hasFrameRows {
		return m.frameRows
	}
	rooms := m.filteredRooms()
	rows := make([]roomRow, 0, len(rooms))
	mode := m.prefs.display.Threads.Mode()
	view := m.unreadView()
	for i := range rooms {
		rows = append(rows, roomRow{room: rooms[i]})
		if mode == config.ThreadsNever {
			continue
		}
		// Rooms a silent tag holds list no threads; muted ones do (muting silences
		// notifications, not messages).
		if rooms[i].IsInvite() || view.silenced(rooms[i]) {
			continue
		}
		rows = m.appendThreadRows(rows, rooms[i], mode)
	}
	return rows
}

func (m Model) threadCap(room domain.Room) int {
	rules := make([]domain.ThreadRule, 0, len(m.prefs.display.Threads.Rules))
	for _, rule := range m.prefs.display.Threads.Rules {
		rules = append(rules, domain.ThreadRule{Match: rule.Match, Max: rule.MaxInRoomList})
	}
	return domain.ThreadCap(m.prefs.display.Threads.MaxInRoomList, config.DefaultThreadsInRoomList,
		rules, m.scopeOfRoom(room))
}

// scopeOfRoom is the room as a place rule sees it: ID and first space.
func (m Model) scopeOfRoom(room domain.Room) domain.Scope {
	scope := domain.Scope{RoomID: string(room.ID)}
	if homes := m.homesOf(room.ID); len(homes) > 0 {
		scope.Space = homes[0]
	}
	return scope
}

func (m Model) appendThreadRows(rows []roomRow, room domain.Room, mode string) []roomRow {
	threads := m.threadsUnder(room, mode)
	if len(threads) == 0 {
		return rows
	}
	shown := len(threads)
	if capped := m.threadCap(room); capped > 0 && shown > capped {
		shown = capped
	}
	for i := range threads[:shown] {
		rows = append(rows, roomRow{room: room, thread: threads[i]})
	}
	if rest := len(threads) - shown; rest > 0 {
		rows = append(rows, roomRow{room: room, more: rest})
	}
	return rows
}

// threadsUnder is the threads to list beneath one room, newest activity first:
// unread ones from the room's unread state, plus (mode "all") ListThreads' answer.
func (m Model) threadsUnder(room domain.Room, mode string) []domain.ThreadUnread {
	out := append([]domain.ThreadUnread(nil), m.unread[room.ID].Threads...)
	listed := make(map[domain.EventID]bool, len(out))
	for _, t := range out {
		listed[t.Root] = true
	}
	// The open thread and the cursor's thread keep their rows once read, so a row
	// does not vanish from under the cursor.
	if room.ID == m.openRoom {
		for _, root := range []domain.EventID{m.thread.root, m.rows.cursor} {
			if root == "" || listed[root] {
				continue
			}
			if pinned, ok := m.pinnedThreadRow(root); ok {
				listed[root] = true
				out = append(out, pinned)
			}
		}
	}
	if mode != config.ThreadsAll {
		return out
	}
	known := m.rows.known[room.ID]
	for i := range known {
		if listed[known[i].Root] {
			continue
		}
		out = append(out, domain.ThreadUnread{
			Root:     known[i].Root,
			LatestAt: known[i].LatestAt,
			Latest:   known[i].Latest,
			Title:    known[i].Title,
		})
	}
	return out
}

// pinnedThreadRow rebuilds the row for a thread gone read while looked at, from the
// open room's messages.
func (m Model) pinnedThreadRow(root domain.EventID) (domain.ThreadUnread, bool) {
	_, threads := domain.CollapseThreads(m.timeline.messages)
	for i := range threads {
		if threads[i].Root != root {
			continue
		}
		row := domain.ThreadUnread{Root: root, LatestAt: threads[i].LatestAt, Latest: threads[i].Latest}
		if idx := indexOfMessage(m.timeline.messages, root); idx >= 0 {
			row.Title = m.timeline.messages[idx].Summary()
		}
		return row, true
	}
	return domain.ThreadUnread{}, false
}

// threadRowLabel names a thread's row: alias, then the daemon's title, then a
// fallback (threadName needs the open room's messages; this row may not have them).
func (m Model) threadRowLabel(t domain.ThreadUnread) string {
	title := m.prefs.threadAliases[t.Root]
	if title == "" {
		title = flatten(t.Title)
	}
	if title == "" {
		title = "an older thread"
	}
	return title
}

func (m Model) moreThreadsLabel(n int) string {
	if n == 1 {
		return "+1 more thread"
	}
	return fmt.Sprintf("+%d more threads", n)
}

// rowCursor is the selected row's position, derived from the selected room and
// thread IDs so it follows them across reindexing.
func (m Model) rowCursor(rows []roomRow) int {
	for i := range rows {
		if rows[i].room.ID != m.openRoom || !rows[i].selectable() {
			continue
		}
		if m.rows.cursor == rows[i].thread.Root {
			return i
		}
	}
	return 0
}

// selectRow selects a row. A thread row of the already-open room does not reload
// the timeline.
func (m Model) selectRow(r roomRow) (Model, tea.Cmd) {
	if !r.isThread() {
		m.rows.cursor = ""
		return m.selectRoom(r.room)
	}
	m.rows.cursor = r.thread.Root
	if r.room.ID == m.openRoom {
		return m, nil
	}
	return m.selectRoom(r.room)
}

func (m Model) stepRow(dir int) (Model, tea.Cmd) { return m.stepRows(1, dir) }

// gotoRow selects the nth selectable row, one-based (vim's `12G`); past the end
// lands on the last.
func (m Model) gotoRow(n int) (Model, tea.Cmd) {
	rows := m.roomRows()
	seen := 0
	for i := range rows {
		if !rows[i].selectable() {
			continue
		}
		if seen++; seen == n {
			return m.selectRow(rows[i])
		}
	}
	return m.gotoLastRow()
}

func (m Model) gotoLastRow() (Model, tea.Cmd) {
	rows := m.roomRows()
	for i := range slices.Backward(rows) {
		if rows[i].selectable() {
			return m.selectRow(rows[i])
		}
	}
	return m, nil
}

// stepRows repeats a one-row step count times (`5j`), skipping unselectable rows
// and stopping at the last selectable one.
func (m Model) stepRows(count, dir int) (Model, tea.Cmd) {
	rows := m.roomRows()
	at := m.rowCursor(rows)
	target := at
	for range max(count, 1) {
		next := -1
		for i := target + dir; i >= 0 && i < len(rows); i += dir {
			if rows[i].selectable() {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		target = next
	}
	if target == at {
		return m, nil
	}
	return m.selectRow(rows[target])
}

func (m Model) selectedRow() (roomRow, bool) {
	rows := m.roomRows()
	if len(rows) == 0 {
		return roomRow{}, false
	}
	return rows[m.rowCursor(rows)], true
}

// openSelectedThread opens the cursor's thread. The room's messages are usually not
// loaded yet, so the root is remembered and entered when they land (resolveThread).
func (m Model) openSelectedThread(root domain.EventID) (Model, tea.Cmd) {
	m.focus = paneTimeline
	m.compose.insertMode = false
	// The room behind backfills like any opened room, but is not marked read.
	if !m.timeline.hist.atStart {
		m.timeline.hist.backfilling = true
	}
	if hasThreadReply(m.timeline.messages, root) {
		return m.enterThread(root, "")
	}
	m.rows.opening = root
	return m.doing("opening the thread…"), nil
}

func hasThreadReply(msgs []domain.Message, root domain.EventID) bool {
	for i := range msgs {
		if msgs[i].ThreadRoot == root {
			return true
		}
	}
	return false
}

// markThreadRowRead marks the cursor's thread read without opening it.
func (m Model) markThreadRowRead(row roomRow) (Model, tea.Cmd) {
	if row.thread.Latest == "" {
		return m, nil
	}
	label := m.threadRowLabel(row.thread)
	m = m.say("marking " + isolate(label) + " read…")
	return m, m.markThreadReadCmd(row.room.ID, row.thread.Root, row.thread.Latest, !m.readPolicy().Send)
}

// hasThreadCursorIn reports whether a room's unread state carries this thread.
func hasThreadCursorIn(u domain.Unread, root domain.EventID) bool {
	for i := range u.Threads {
		if u.Threads[i].Root == root {
			return true
		}
	}
	return false
}

// listThreadsCmd fetches, once per room in view, the thread lists mode "all" needs.
// A room's list is refreshed when it is opened (selectRoom).
func (m Model) listThreadsCmd() tea.Cmd {
	if m.prefs.display.Threads.Mode() != config.ThreadsAll {
		return nil
	}
	var cmds []tea.Cmd
	rooms := m.filteredRooms()
	for i := range rooms {
		if _, asked := m.rows.known[rooms[i].ID]; asked || rooms[i].IsInvite() {
			continue
		}
		cmds = append(cmds, m.roomThreadsCmd(rooms[i].ID))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// handleRoomThreads records a room's thread list and asks for names for unnamed ones.
func (m Model) handleRoomThreads(msg roomThreadsMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "list room threads", msg.err)
	if msg.err != nil {
		return m, nil
	}
	m.rows = m.rows.recording(msg.roomID, msg.threads)
	return m.nameThreads(msg.roomID, msg.threads)
}

// roomCursor is the highlighted row's position, derived on every read.
func (m Model) roomCursor() int { return m.rowCursor(m.roomRows()) }
