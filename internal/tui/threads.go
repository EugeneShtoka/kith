package tui

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// threadMark leads a thread's summary row.
const threadMark = "💬"

// threadState is the thread the timeline pane has been taken over by, if any, and
// how to put the room back.
type threadState struct {
	// root is the open thread; empty means the pane is showing the room.
	root domain.EventID
	// returnTo and returnScroll are where the room was when the thread opened.
	returnTo     domain.EventID
	returnScroll int
	// fetching is set while a root older than the cached window is fetched.
	fetching bool
	// A thread pages itself through /relations/{root}/m.thread, so reaching old
	// replies does not page the whole room back.
	scrollback
}

// open reports whether a thread has taken the pane over.
func (t threadState) open() bool { return t.root != "" }

// threadOf is the thread msg belongs to or would open: its root for a reply, itself
// for an answered root, else the thread whose summary row hangs off it (how an
// older thread whose root aged out is reached). Empty for an ordinary message.
func (m Model) threadOf(msg domain.Message) domain.EventID {
	if root := domain.ThreadRootOf(m.timeline.messages, msg.ID); root != "" {
		return root
	}
	if msg.ID == "" {
		return ""
	}
	// The derived cache's structure: collapsing every loaded message again here cost
	// half of what a keypress allocated, and the legend asks on every draw.
	d := m.derivedFor()
	main, threads := d.main, d.threads
	// A thread with nothing before it anchors above the first drawn message.
	first := domain.EventID("")
	if len(main) > 0 {
		first = main[0].ID
	}
	for i := range threads {
		if threads[i].Anchor == msg.ID || threads[i].Anchor == "" && msg.ID == first {
			return threads[i].Root
		}
	}
	return ""
}

// hasThread reports whether the selected message is in a conversation, which is
// what puts the open-thread key in the legend.
func (m Model) hasThread() bool {
	msg, ok := m.selectedMessage()
	return ok && m.threadOf(msg) != ""
}

// openThread takes the pane over with the thread the selected message belongs to.
func (m Model) openThread() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	root := m.threadOf(msg)
	if root == "" {
		return m.say("no thread here — start one with " + m.keys.keyHint(scopeTimeline, actStartThread) +
			", or list this room's with " + m.keys.keyHint(scopeTimeline, actListThreads)), nil
	}
	return m.enterThread(root, msg.ID)
}

// startReply points the composer at the selected message. A message already in a
// conversation is answered in it (the thread opens first), so the reply carries
// the thread relation on the wire. ThreadRootOf rather than threadOf: a message
// that only anchors an orphaned summary row is not in that thread.
//
// Where the protocol has no reply that is not a thread (Slack), the bridge makes a
// thread anyway, so answering an unthreaded message starts one here too.
func (m Model) startReply() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok || msg.ID == "" {
		return m, nil
	}
	root, note := domain.ThreadRootOf(m.timeline.messages, msg.ID), ""
	if protocol := m.roomProtocol(); root == "" && protocol.RepliesAreThreads() {
		root, note = msg.ID, "answering in a thread — "+protocol.String()+" has no other kind of reply"
	}
	if root == "" || root == m.thread.root {
		m.compose.replyTo = msg.ID
		m.compose.insertMode = true
		return m, nil
	}
	next, cmd := m.enterThread(root, msg.ID)
	next.compose.replyTo = msg.ID
	next.compose.insertMode = true
	if note != "" {
		next = next.say(note)
	}
	return next, cmd
}

// startThread opens an empty thread rooted at the selected message with the
// composer ready; it exists on the server once the first reply is sent. Matrix has
// no nested threads, so on a message already in one it opens that one.
func (m Model) startThread() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok || msg.ID == "" {
		return m, nil
	}
	if root := m.threadOf(msg); root != "" {
		return m.enterThread(root, msg.ID)
	}
	next, cmd := m.enterThread(msg.ID, msg.ID)
	next.compose.insertMode = true
	return next, cmd
}

// enterThread opens root with selected selected, remembering where the room was. A
// root older than the cached window is fetched in the background.
func (m Model) enterThread(root, selected domain.EventID) (Model, tea.Cmd) {
	if !m.thread.open() {
		m.thread.returnTo, m.thread.returnScroll = m.timeline.selected, m.timeline.scroll
	}
	if m.thread.root != root {
		m.thread.token, m.thread.atStart, m.thread.loading = "", false, false
	}
	m.thread.root = root
	m.timeline.selected = selected
	m.timeline.scroll = 0
	m.compose.insertMode = false
	m.compose.replyTo = ""
	if selected == "" {
		msgs := m.shownMessages()
		if len(msgs) > 0 {
			m.timeline.selected = msgs[len(msgs)-1].ID
		}
	}
	m = m.scrollToSelection()
	// Marked here so every way into a thread clears its badge.
	m, read := m.markRead()
	if indexOfMessage(m.timeline.messages, root) >= 0 || m.thread.fetching {
		return m, read
	}
	m.thread.fetching = true
	room, ok := m.currentRoom()
	if !ok {
		return m, read
	}
	return m.doing("fetching the start of this thread…"),
		tea.Batch(read, m.fetchEventCmd(room.ID, root))
}

// loadOlderInThread pulls the page of replies before the oldest one loaded.
func (m Model) loadOlderInThread() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || m.thread.loading || m.thread.atStart {
		return m, nil
	}
	m.thread.loading = true
	m = m.doing("loading older replies…")
	return m, m.threadPageCmd(room.ID, m.thread.root, m.thread.token)
}

// handleThreadPage merges a page of a thread's history into m.messages. A page for
// a thread no longer open is dropped.
func (m Model) handleThreadPage(msg threadPageMsg) (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || msg.roomID != room.ID || msg.root != m.thread.root {
		return m, nil
	}
	m.thread.loading = false
	if msg.err != nil {
		return m.sayErr("could not load older replies", msg.err), nil
	}
	m = m.keepAnchored(func(m Model) Model {
		m = m.setMessages(domain.MergeMessages(m.timeline.messages, msg.page.Messages))
		return m.withReactions(msg.page.Reactions...)
	})
	m.thread.token = msg.page.Next
	m.thread.atStart = msg.page.Next == ""
	return m.doing(threadHistoryStatus(len(domain.ThreadMessages(m.timeline.messages, m.thread.root)), m.thread.atStart)), nil
}

// threadHistoryStatus says how much of a thread is loaded.
func threadHistoryStatus(n int, atStart bool) string {
	if atStart {
		return fmt.Sprintf("the whole thread — %d messages", n)
	}
	return fmt.Sprintf("%d messages in this thread", n)
}

// closeThread puts the room back, with the cursor on the thread's root.
func (m Model) closeThread() (Model, tea.Cmd) {
	root, back, scroll := m.thread.root, m.thread.returnTo, m.thread.returnScroll
	m.thread = threadState{}
	m.compose.insertMode = false
	m.compose.replyTo = ""
	m.timeline.scroll = scroll
	m.timeline.selected = back
	if indexOfMessage(m.timeline.messages, root) >= 0 {
		m.timeline.selected = root
	}
	// The room's receipt was suppressed while the thread was open.
	m, read := m.scrollToSelection().markRead()
	return m, read
}

// revealMessage selects a message wherever it is drawn — inside its thread (opening
// it) or in the main timeline — and scrolls to it. Search hits and reply targets
// jump through it.
func (m Model) revealMessage(id domain.EventID) (Model, tea.Cmd) {
	idx := indexOfMessage(m.timeline.messages, id)
	if idx < 0 {
		return m, nil
	}
	// The conversation it is drawn in, not the relation it carries (a plain reply
	// into a thread is drawn inside it, see domain.Conversations).
	root := domain.ThreadRootOf(m.timeline.messages, id)
	if root == id {
		root = "" // a root is drawn in the room, under its summary row
	}
	var read tea.Cmd
	switch {
	case root != "" && root != m.thread.root:
		return m.enterThread(root, id)
	case root == "" && m.thread.open():
		m, read = m.closeThread()
	}
	m.timeline.selected = id
	return m.scrollToSelection(), read
}

// resolveThread enters the thread a room-list row asked for once one of its
// replies is loaded. settled means the live page landed, so a thread still missing
// never will be.
func (m Model) resolveThread(settled bool) (Model, tea.Cmd) {
	if m.rows.opening == "" {
		return m, nil
	}
	if hasThreadReply(m.timeline.messages, m.rows.opening) {
		root, rows := m.rows.taking()
		m.rows = rows
		next, cmd := m.enterThread(root, "")
		return next.clearStatus(), cmd
	}
	if settled {
		_, m.rows = m.rows.taking()
		m = m.say("that thread is no longer cached")
	}
	return m, nil
}

// handleFetchedEvent merges a fetched thread root into the timeline.
func (m Model) handleFetchedEvent(msg fetchedEventMsg) (Model, tea.Cmd) {
	m.thread.fetching = false
	room, ok := m.currentRoom()
	if !ok || msg.roomID != room.ID {
		return m, nil
	}
	if msg.err != nil {
		return m.sayErr("could not fetch the start of this thread", msg.err), nil
	}
	m = m.setMessages(domain.MergeMessages(m.timeline.messages, []domain.Message{msg.message}))
	return m.clearStatus(), nil
}

// shownMessages is what the timeline pane draws: its messages, with threads drawn as
// a summary row instead of their replies. Every timeline index means this list, not
// m.timeline.messages. Cached per message revision (derived.go).
func (m Model) shownMessages() []domain.Message {
	return m.derivedFor().shown
}

// hasAttachments reports whether anything the pane shows carries a file.
func (m Model) hasAttachments() bool { return m.derivedFor().hasMedia }

// threadTitleWidth caps the root snippet in the breadcrumb; the root is also the
// view's first row, so a few words identify it.
const threadTitleWidth = 40

// threadName is what a thread is called everywhere: its `[display] thread_alias`,
// else a snippet of its root, else the title the daemon gave it (a forum topic's,
// whose root is older than the timeline), else a placeholder.
func (m Model) threadName(root domain.EventID) string {
	return m.threadNamed(root, m.knownTitle(root))
}

// threadNamed is threadName with the daemon's title for the thread at hand.
func (m Model) threadNamed(root domain.EventID, title string) string {
	if alias, ok := m.prefs.threadAliases[root]; ok {
		return alias
	}
	if i := indexOfMessage(m.timeline.messages, root); i >= 0 {
		if snippet := flatten(m.timeline.messages[i].Summary()); snippet != "" {
			return snippet
		}
	}
	if title = flatten(title); title != "" {
		return title
	}
	return "an older thread"
}

// knownTitle is the open room's thread's title as the daemon last gave it: in the
// room's thread list, or its unread threads.
func (m Model) knownTitle(root domain.EventID) string {
	known := m.rows.known[m.openRoom]
	for i := range known {
		if known[i].Root == root && known[i].Title != "" {
			return known[i].Title
		}
	}
	for _, t := range m.unread[m.openRoom].Threads {
		if t.Root == root && t.Title != "" {
			return t.Title
		}
	}
	return ""
}

// threadTitle is the thread view's breadcrumb: the room, then the conversation.
func (m Model) threadTitle() string {
	room := "(no room)"
	if r, ok := m.currentRoom(); ok {
		room = m.roomName(r)
	}
	name := "…"
	if !m.thread.fetching {
		// Cut in logical order, then isolated, so an RTL name reads correctly.
		name = isolate(truncateLogical(m.threadName(m.thread.root), threadTitleWidth))
	}
	return room + " ▸ " + name
}

// repliesLabel is "1 reply" or "N replies".
func repliesLabel(n int) string {
	if n == 1 {
		return "1 reply"
	}
	return strconv.Itoa(n) + " replies"
}

// threadRow renders one thread's summary row, in place of the replies:
//
//	14:52       💬 7 replies · Bob
//
// The time is the newest reply's; the rest sits under the body column like a
// reaction row.
func (m Model) threadRow(t domain.Thread, nameW, width int) string {
	label := repliesLabel(t.Count)
	if !t.RootLoaded {
		label += " in an older thread"
	}
	if name := m.personIn(t.RoomID, t.LatestSender, t.LatestSenderName); name != "" {
		label += " · " + name
	}
	ts := m.rowTime(t.LatestAt)
	body := m.theme.Faint.Render(emojiCell(threadMark) + " " + drawSentence(label))
	// The unread badge matches the room list's, and is not faint.
	if t.Unread > 0 {
		body += " " + m.theme.Badge(t.Mentions > 0).Render(fmt.Sprintf("●%d", t.Unread))
	}
	if m.mirrored() {
		// Our sentence stays left to right, flushed against the time on the right.
		return padStart(body, width-m.bodyColumn(nameW)) + strings.Repeat(" ", nameW+2) + ts
	}
	row := ts + " " + strings.Repeat(" ", nameW+1) + body
	return truncateDrawn(row, width)
}

// renameThreadInView names the open thread, else the one the selected message
// belongs to or would open (the same resolution as openThread).
func (m Model) renameThreadInView() (Model, tea.Cmd) {
	if m.thread.open() {
		return m.renameThread(m.thread.root)
	}
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	root := m.threadOf(msg)
	if root == "" {
		return m, nil
	}
	return m.renameThread(root)
}

// renameSelectedThread names the thread under the cursor in the thread picker.
func (m Model) renameSelectedThread() (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok {
		return m, nil
	}
	m = m.closePicker()
	return m.renameThread(domain.EventID(item.value))
}

// listThreads opens the picker over every thread in the open room, as the daemon
// lists them: the timeline holds only the newest messages, which miss old threads (a
// forum's topics) and their roots.
func (m Model) listThreads() (Model, tea.Cmd) {
	if m.openRoom == "" {
		return m, nil
	}
	return m.doing("looking for threads…"), m.scopeThreadsCmd([]domain.RoomID{m.openRoom})
}

// threadItem is one thread as a picker row, matchable by name and by newest sender.
// A non-nil in leads the row with that room's name, for a list across rooms. The name
// is isolated either way: the picker draws labels as a sentence of ours (LTR).
func (m Model) threadItem(t domain.Thread, roomID domain.RoomID, in *domain.Room) pickerItem {
	name := m.threadNamed(t.Root, cmp.Or(t.Title, m.knownTitle(t.Root)))
	sender := m.personIn(roomID, t.LatestSender, t.LatestSenderName)
	item := pickerItem{
		label:  isolate(name),
		detail: threadDetail(t, sender, m.prefs.clock),
		value:  string(t.Root),
		match:  name + " " + sender,
	}
	if in != nil {
		room := m.roomName(*in)
		item.label, item.match = room+" · "+isolate(name), room+" "+name+" "+sender
	}
	return item
}

// threadDetail is a thread row's qualifier: size, last activity, last sender.
func threadDetail(t domain.Thread, sender string, clock domain.Clock) string {
	detail := repliesLabel(t.Count)
	if !t.LatestAt.IsZero() {
		detail += " · " + clock.Time(t.LatestAt)
	}
	if sender != "" {
		detail += " · " + sender
	}
	return detail
}

// openPickedThread enters the thread chosen from the picker: from the timeline's
// list directly; from the room list via the armed request (openSelectedThread);
// and in another room by opening that room first and taking it on arrival.
func (m Model) openPickedThread(root domain.EventID, room domain.RoomID) (Model, tea.Cmd) {
	if room != "" && room != m.openRoom {
		next, cmd := m.goToRoom(room)
		next.rows.opening = root
		return next, cmd
	}
	m = m.closePicker()
	if m.focus == paneRooms {
		next, cmd := m.openSelectedThread(root)
		return next, tea.Batch(cmd, repaint())
	}
	next, cmd := m.enterThread(root, "")
	return next, tea.Batch(cmd, repaint())
}

// listRoomThreads lists the threads of the room under the room-list cursor. It asks
// the daemon: the client holds only that room's preview, which misses old threads.
func (m Model) listRoomThreads() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	return m.doing("looking for threads…"), m.scopeThreadsCmd([]domain.RoomID{room.ID})
}

// roomsOf lists a room set's rooms: every joined room for all of them.
func (m Model) roomsOf(set domain.RoomSet) []domain.RoomID {
	if !set.All {
		return set.IDs
	}
	out := make([]domain.RoomID, 0, len(m.rooms.all))
	for i := range m.rooms.all {
		out = append(out, m.rooms.all[i].ID)
	}
	return out
}

// scopeThreadsCap bounds the per-room fan-out of a scoped `:threads`.
const scopeThreadsCap = 50

// openThreads lists the threads in scope: the open room from memory, or a wider
// scope asked of the daemon room by room.
func (m Model) openThreads(scoped bool) (Model, tea.Cmd) {
	rooms := []domain.RoomID{m.openRoom}
	if scoped {
		rooms = m.roomsOf(m.searchRooms(m.defaultSearchScope()))
	}
	if len(rooms) == 0 || (len(rooms) == 1 && rooms[0] == m.openRoom) {
		return m.listThreads()
	}
	if len(rooms) > scopeThreadsCap {
		m = m.say(fmt.Sprintf("looking in the first %d rooms of %d", scopeThreadsCap, len(rooms)))
		rooms = rooms[:scopeThreadsCap]
	}
	return m.doing("looking for threads…"), m.scopeThreadsCmd(rooms)
}

// scopeThreadsMsg carries the threads of every room asked about.
type scopeThreadsMsg struct {
	threads []scopeThread
	// only is the room asked about when exactly one was; the labels then omit it.
	only domain.Room
	err  error
}

// scopeThread is one thread and its room.
type scopeThread struct {
	room   domain.Room
	thread domain.Thread
}

// withTimelineThreads adds to the daemon's threads of the open room those its
// timeline holds that the daemon did not list yet (one just begun).
func (m Model) withTimelineThreads(listed []scopeThread, room domain.Room) []scopeThread {
	_, threads := domain.CollapseThreads(m.timeline.messages)
	for i := range threads {
		if !slices.ContainsFunc(listed, func(s scopeThread) bool { return s.thread.Root == threads[i].Root }) {
			listed = append(listed, scopeThread{room: room, thread: threads[i]})
		}
	}
	return listed
}

// scopeThreadsCmd asks for each room's threads in turn (local cached calls, capped,
// so sequential is fine).
func (m Model) scopeThreadsCmd(rooms []domain.RoomID) tea.Cmd {
	// Resolved on the event loop: reading m.rooms inside the closure would race.
	resolved := make([]domain.Room, 0, len(rooms))
	for _, id := range rooms {
		if room, ok := m.roomByID(id); ok {
			resolved = append(resolved, room)
		}
	}
	only := domain.Room{}
	if len(resolved) == 1 {
		only = resolved[0]
	}
	backend, ctx := m.backend, m.ctx
	return func() tea.Msg {
		var out []scopeThread
		for i := range resolved {
			threads, err := backend.ListThreads(ctx, resolved[i].ID)
			if err != nil {
				return scopeThreadsMsg{err: err}
			}
			for j := range threads {
				out = append(out, scopeThread{room: resolved[i], thread: threads[j]})
			}
		}
		return scopeThreadsMsg{threads: out, only: only}
	}
}

// handleScopeThreads opens the picker over what came back, newest activity first
// across all rooms.
func (m Model) handleScopeThreads(msg scopeThreadsMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not list threads", msg.err), nil
	}
	if msg.only.ID != "" && msg.only.ID == m.openRoom {
		msg.threads = m.withTimelineThreads(msg.threads, msg.only)
	}
	if len(msg.threads) == 0 {
		if msg.only.ID != "" {
			return m.say("no threads in " + m.roomName(msg.only) + " yet — start one with " +
				m.keys.keyHint(scopeTimeline, actStartThread)), nil
		}
		return m.say("no threads in scope"), nil
	}
	found := msg.threads
	sort.SliceStable(found, func(i, j int) bool {
		return found[i].thread.LatestAt.After(found[j].thread.LatestAt)
	})
	items := make([]pickerItem, 0, len(found))
	for i := range found {
		// One room asked about: its name would repeat on every row, so it is left off.
		var in *domain.Room
		if msg.only.ID == "" {
			in = &found[i].room
		}
		item := m.threadItem(found[i].thread, found[i].room.ID, in)
		item.room = found[i].room.ID
		items = append(items, item)
	}
	m = m.clearStatus()
	m.picker = newPicker(pickerThread, items)
	return m, repaint()
}
