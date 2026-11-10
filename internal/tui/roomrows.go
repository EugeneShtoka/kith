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
	// cursor is the thread row under the room-list cursor (by root, since rows reindex); empty
	// means the room's own row.
	cursor domain.EventID
	// onMore puts the cursor on the open room's overflow row ("+N more threads") instead.
	onMore bool
	// expanded is the open room when its overflow row was entered: all its threads are listed,
	// past the cap, until the room is left (left).
	expanded domain.RoomID
	// opening is a thread a row asked to open, held until the room's messages arrive.
	opening domain.EventID
	// known is what ListThreads last answered per room (in_room_list = "all" only).
	known map[domain.RoomID][]domain.Thread
}

func newThreadRows() threadRows {
	return threadRows{known: map[domain.RoomID][]domain.Thread{}}
}

func (t threadRows) recording(room domain.RoomID, threads []domain.Thread) threadRows {
	t.known = withEntry(t.known, room, threads)
	return t
}

// left is t once the open room is left: the cursor is off its overflow row, and its cap is
// back.
func (t threadRows) left() threadRows {
	t.onMore, t.expanded = false, ""
	return t
}

// taking returns the pending open request and clears it, so it is answered once.
func (t threadRows) taking() (domain.EventID, threadRows) {
	root := t.opening
	t.opening = ""
	return root, t
}

// roomRow is one room-list row: a room, one of its threads (listed so activity on an old root
// is findable), or an overflow line counting unlisted threads.
type roomRow struct {
	room   domain.Room
	thread domain.ThreadUnread
	// more is how many threads an overflow row stands for; entering it lists them.
	more int
}

func (r roomRow) isThread() bool { return r.thread.Root != "" }

// roomRows is the room list as drawn: each room of the current group followed by its thread
// rows, unless they are folded away. Cursor, window and counter all read this list.
func (m Model) roomRows() []roomRow {
	if m.hasFrameRows {
		return m.frameRows
	}
	rooms := m.filteredRooms()
	rows := make([]roomRow, 0, len(rooms))
	mode := m.prefs.device.Threads.Mode()
	view := m.unreadView()
	for i := range rooms {
		rows = append(rows, roomRow{room: rooms[i]})
		if mode == config.ThreadsNever {
			continue
		}
		// Rooms a silent tag holds list no threads; muted ones do (muting silences notifications, not
		// messages).
		if rooms[i].IsInvite() || view.silenced(rooms[i]) || m.prefs.device.Threads.Folds(string(rooms[i].ID)) {
			continue
		}
		rows = m.appendThreadRows(rows, rooms[i], mode)
	}
	return rows
}

func (m Model) threadCap(room domain.Room) int {
	rules := make([]domain.ThreadRule, 0, len(m.prefs.device.Threads.Rules))
	for _, rule := range m.prefs.device.Threads.Rules {
		rules = append(rules, domain.ThreadRule{Match: rule.Match, Max: rule.MaxInRoomList})
	}
	return domain.ThreadCap(m.prefs.device.Threads.MaxInRoomList, config.DefaultThreadsInRoomList,
		rules, m.scopeOfRoom(room))
}

// scopeOfRoom is the room as a place rule sees it: ID and first space.
func (m Model) scopeOfRoom(room domain.Room) domain.Scope {
	scope := domain.Scope{RoomID: string(room.ID)}
	if homes := m.homesOf(room.ID); len(homes) > 0 {
		scope.Space = homes[0]
		scope.SpaceLabel = m.rail.homes.Label(homes[0])
	}
	return scope
}

func (m Model) appendThreadRows(rows []roomRow, room domain.Room, mode string) []roomRow {
	threads := m.threadsUnder(room, mode)
	if len(threads) == 0 {
		return rows
	}
	shown := len(threads)
	if capped := m.threadCap(room); capped > 0 && shown > capped && room.ID != m.rows.expanded {
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

// threadsUnder is the threads listed beneath a room, newest activity first: unread ones, plus
// ListThreads' answer in mode "all".
func (m Model) threadsUnder(room domain.Room, mode string) []domain.ThreadUnread {
	out := append([]domain.ThreadUnread(nil), m.unread[room.ID].Threads...)
	listed := make(map[domain.EventID]bool, len(out))
	for _, t := range out {
		listed[t.Root] = true
	}
	// The open thread and the cursor's thread keep their rows once read, so a row does not vanish
	// from under the cursor; a thread the full list carries keeps its place there (pinning it here
	// would make stepping down never pass it).
	known := m.rows.known[room.ID]
	if mode != config.ThreadsAll {
		known = nil
	}
	if room.ID == m.openRoom {
		for _, root := range []domain.EventID{m.thread.root, m.rows.cursor} {
			if root == "" || listed[root] || slices.ContainsFunc(known, func(t domain.Thread) bool { return t.Root == root }) {
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

// pinnedThreadRow rebuilds the row of a thread gone read while viewed, from the open room's
// messages.
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

// threadRowLabel names a thread row: alias, then the daemon's title, then a fallback
// (threadName needs the open room's messages, which this row may lack).
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

// rowCursor is the selected row's position, derived from the selected room and thread IDs so
// it survives reindexing. A row gone from under it (folded, capped) leaves it on its room's
// own row.
func (m Model) rowCursor(rows []roomRow) int {
	own := 0
	for i := range rows {
		if rows[i].room.ID != m.openRoom {
			continue
		}
		if m.rows.onMore == (rows[i].more > 0) && m.rows.cursor == rows[i].thread.Root {
			return i
		}
		if !rows[i].isThread() && rows[i].more == 0 {
			own = i
		}
	}
	return own
}

// selectRow selects a row. A thread or overflow row of the already-open room does not reload
// the timeline.
func (m Model) selectRow(r roomRow) (Model, tea.Cmd) {
	if !r.isThread() && r.more == 0 {
		m.rows.cursor, m.rows.onMore = "", false
		return m.selectRoom(r.room)
	}
	m.rows.cursor = r.thread.Root
	var cmd tea.Cmd
	if r.room.ID != m.openRoom {
		m, cmd = m.selectRoom(r.room)
	}
	m.rows.onMore = r.more > 0
	return m, cmd
}

func (m Model) stepRow(dir int) (Model, tea.Cmd) { return m.stepRows(1, dir) }

// gotoRow selects the nth row, one-based (vim's `12G`); past the end
// lands on the last.
func (m Model) gotoRow(n int) (Model, tea.Cmd) {
	rows := m.roomRows()
	if n >= 1 && n <= len(rows) {
		return m.selectRow(rows[n-1])
	}
	return m.gotoLastRow()
}

func (m Model) gotoLastRow() (Model, tea.Cmd) {
	rows := m.roomRows()
	if len(rows) == 0 {
		return m, nil
	}
	return m.selectRow(rows[len(rows)-1])
}

// stepRows repeats a one-row step count times (`5j`), stopping at either end.
func (m Model) stepRows(count, dir int) (Model, tea.Cmd) {
	rows := m.roomRows()
	at := m.rowCursor(rows)
	target := min(max(at+dir*max(count, 1), 0), len(rows)-1)
	if target == at || len(rows) == 0 {
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

// openSelectedThread opens the cursor's thread. The room's messages are usually not loaded
// yet, so the root is entered when they land (resolveThread).
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

// expandThreads lists all the open room's threads, past the cap, until the room is left; the
// cursor goes from the overflow row to the first thread it hid.
func (m Model) expandThreads() Model {
	at := m.roomCursor()
	m.rows.expanded, m.rows.onMore = m.openRoom, false
	if rows := m.roomRows(); at < len(rows) && rows[at].room.ID == m.openRoom {
		m.rows.cursor = rows[at].thread.Root
	}
	return m
}

// toggleThreadsFold hides the open room's thread rows, or shows them again, remembered in the
// device file ([display.threads] folded). Folding takes the cursor to the room's own row.
func (m Model) toggleThreadsFold() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	dev := m.conf.device.Clone()
	id := string(room.ID)
	if dev.Display.Threads.Folds(id) {
		delete(dev.Display.Threads.Folded, id)
		if len(dev.Display.Threads.Folded) == 0 {
			dev.Display.Threads.Folded = nil // as the file reads with none
		}
	} else {
		if dev.Display.Threads.Folded == nil {
			dev.Display.Threads.Folded = map[string]bool{}
		}
		dev.Display.Threads.Folded[id] = true
		m.rows.cursor, m.rows.onMore = "", false
	}
	m.conf.device = dev
	m.prefs.device.Threads.Folded = dev.Display.Threads.Folded
	return m, m.saveDeviceCmd(dev)
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

// listThreadsCmd fetches, once per room in view, the thread lists mode "all" needs; a room's
// list is refreshed on open (selectRoom).
func (m Model) listThreadsCmd() tea.Cmd {
	if m.prefs.device.Threads.Mode() != config.ThreadsAll {
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

func (m Model) roomCursor() int { return m.rowCursor(m.roomRows()) }
