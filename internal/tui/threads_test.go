package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// threaded is a room timeline with a root, two replies, and an unrelated message
// between them.
func threaded() domain.TimelinePage {
	return domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$root", RoomID: mx + "!a:x", Sender: "matrix:@alice:x", SenderName: "Alice", Body: "ship the release notes?", Timestamp: at(1)},
		{ID: mx + "$r1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "on it", Timestamp: at(2), ThreadRoot: mx + "$root"},
		{ID: mx + "$loose", RoomID: mx + "!a:x", Sender: "matrix:@carol:x", SenderName: "Carol", Body: "unrelated", Timestamp: at(3)},
		{ID: mx + "$r2", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "done", Timestamp: at(4), ThreadRoot: mx + "$root"},
	}}
}

func TestAThreadCollapsesToOneSummaryRow(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: threaded()})

	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, "ship the release notes?") || !strings.Contains(rows, "unrelated") {
		t.Errorf("main timeline lost a message it should draw:\n%s", rows)
	}
	if strings.Contains(rows, "on it") || strings.Contains(rows, "done") {
		t.Errorf("a thread reply was drawn in the main timeline:\n%s", rows)
	}
	if !strings.Contains(rows, "2 replies") || !strings.Contains(rows, "Bob") {
		t.Errorf("no summary row for the thread:\n%s", rows)
	}
}

// The summary is drawn immediately under its root, not at the end of the room or
// where the replies were.
func TestTheSummaryRowSitsUnderItsRoot(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: threaded()})

	rows := m.layoutRows()
	root, summary := -1, -1
	for i, r := range rows {
		switch {
		case strings.Contains(r, "ship the release notes?"):
			root = i
		case strings.Contains(r, "2 replies"):
			summary = i
		}
	}
	if root < 0 || summary != root+1 {
		t.Errorf("root at %d, summary at %d — want the summary on the next row:\n%s", root, summary, strings.Join(rows, "\n"))
	}
}

// A thread reply has no row, so j/k must skip it or the selection disappears.
func TestTheCursorSkipsCollapsedReplies(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: threaded()})

	if got := m.selectedID(); got != mx+"$loose" {
		t.Fatalf("selection = %q, want the newest *drawn* message", got)
	}
	next, _ := m.moveSelection(-1)
	if got := next.selectedID(); got != mx+"$root" {
		t.Errorf("moving up selected %q, want $root — the replies between are not drawn", got)
	}
}

// A thread whose root aged out of the cached window still appears: its summary anchors
// at the newest message before its newest reply, saying what it cannot show.
func TestAnOlderThreadSaysSo(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$a", RoomID: mx + "!a:x", Sender: "matrix:@alice:x", Body: "loaded", Timestamp: at(1)},
		{ID: mx + "$r1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "reply", Timestamp: at(2), ThreadRoot: mx + "$gone"},
	}}})

	rows := m.layoutRows()
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "in an older thread") {
		t.Fatalf("no summary for a thread whose root is not loaded:\n%s", joined)
	}
	if got := len(rows); !strings.Contains(rows[got-1], "in an older thread") {
		t.Errorf("summary is not anchored after the message it followed:\n%s", joined)
	}
}

func TestTheSummaryRowCountsOne(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$root", RoomID: mx + "!a:x", Sender: "matrix:@alice:x", Body: "question", Timestamp: at(1)},
		{ID: mx + "$r1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "answer", Timestamp: at(2), ThreadRoot: mx + "$root"},
	}}})

	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "1 reply ") {
		t.Errorf("summary does not read as one reply:\n%s", rows)
	}
}

// threadBackend records sent drafts (the only way to see what a composed message
// carried) and answers the fetch for a thread with no loaded root.
type threadBackend struct {
	apitest.Nop
	sent   []domain.Draft
	rooms  []domain.RoomID
	events []domain.EventID
	fetch  domain.Message
}

func (b *threadBackend) Send(_ context.Context, roomID domain.RoomID, draft domain.Draft) error {
	b.rooms = append(b.rooms, roomID)
	b.sent = append(b.sent, draft)
	return nil
}

func (b *threadBackend) FetchEvent(_ context.Context, _ domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	b.events = append(b.events, eventID)
	return b.fetch, nil
}

// inThread opens room !a:x with threaded() and puts the cursor on the root.
func inThread(t *testing.T, b api.Backend) Model {
	t.Helper()
	return inRoom(t, b, threaded())
}

// inRoom is that room with the given timeline; roomProtocol reads the network from
// who talks in it, so the fixture decides e.g. "a Slack room".
func inRoom(t *testing.T, b api.Backend, page domain.TimelinePage) Model {
	t.Helper()
	m := sized(t, update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
	}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: page})
	m.focus, m.compose.insertMode = paneTimeline, false
	m.timeline.selected = mx + "$root"
	return m
}

func TestOpeningAThreadTakesThePaneOver(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	next, _ := m.openThread()
	m = next

	if m.thread.root != mx+"$root" {
		t.Fatalf("open thread = %q, want $root", m.thread.root)
	}
	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, "on it") || !strings.Contains(rows, "done") {
		t.Errorf("the thread's replies are not shown:\n%s", rows)
	}
	if !strings.Contains(rows, "ship the release notes?") {
		t.Errorf("the root is not pinned as the first row:\n%s", rows)
	}
	if strings.Contains(rows, "unrelated") {
		t.Errorf("a message from the room leaked into the thread:\n%s", rows)
	}
	if !strings.Contains(m.frameView(), "ship the release notes") {
		t.Error("the breadcrumb does not name the thread")
	}
}

// Back closes the thread before the pane, with the cursor on the root's row.
func TestBackClosesTheThreadFirst(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	next, _ := m.openThread()
	m = next

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.thread.open() {
		t.Fatal("back should close the thread")
	}
	if m.focus != paneTimeline {
		t.Error("back out of a thread should stay in the timeline pane")
	}
	if m.timeline.selected != mx+"$root" {
		t.Errorf("selected = %q, want the thread's root", m.timeline.selected)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.focus != paneRooms {
		t.Error("back again should leave the pane")
	}
}

// An answer typed in a thread lands in the thread, not the room's main timeline.
func TestSendingInAThreadCarriesTheRoot(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	next, _ := m.openThread()
	m = next

	m.compose.insertMode = true
	m.compose.input = "shipping now"
	sent, cmd := m.submit()
	m = sent
	if cmd != nil {
		runCmd(t, cmd)
	}

	if len(b.sent) != 1 {
		t.Fatalf("sent %d drafts, want 1", len(b.sent))
	}
	if b.sent[0].ThreadRoot != mx+"$root" {
		t.Errorf("ThreadRoot = %q, want $root — the answer landed in the main timeline", b.sent[0].ThreadRoot)
	}
	if b.sent[0].ReplyTo != "" {
		t.Errorf("ReplyTo = %q, want empty: typing into a thread is not replying to a message in it", b.sent[0].ReplyTo)
	}
}

func TestSendingInTheRoomCarriesNoThread(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.compose.insertMode = true
	m.compose.input = "unrelated news"
	sent, cmd := m.submit()
	m = sent
	if cmd != nil {
		runCmd(t, cmd)
	}

	if len(b.sent) != 1 || b.sent[0].ThreadRoot != "" {
		t.Errorf("drafts = %+v, want one with no thread", b.sent)
	}
}

// r inside a thread answers within the thread, carrying both pointers, so it is a real
// reply rather than the chain fallback.
func TestReplyingInsideAThreadKeepsBoth(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	next, _ := m.openThread()
	m = next
	m.timeline.selected = mx + "$r1"

	acted, _, handled := m.timelineAction(actReply)
	if !handled {
		t.Fatal("reply was not handled")
	}
	m = acted
	m.compose.input = "which one?"
	sent, cmd := m.submit()
	m = sent
	if cmd != nil {
		runCmd(t, cmd)
	}

	if len(b.sent) != 1 {
		t.Fatalf("sent %d drafts, want 1", len(b.sent))
	}
	if b.sent[0].ThreadRoot != mx+"$root" || b.sent[0].ReplyTo != mx+"$r1" {
		t.Errorf("draft = %+v, want a reply to $r1 inside $root", b.sent[0])
	}
}

// T on an unanswered message opens an empty thread rooted at it with the composer
// ready; nothing exists on the server until the first reply.
func TestStartingAThread(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.timeline.selected = mx + "$loose"
	next, _ := m.startThread()
	m = next

	if m.thread.root != mx+"$loose" {
		t.Fatalf("thread root = %q, want the selected message", m.thread.root)
	}
	if !m.compose.insertMode {
		t.Error("starting a thread should open the composer")
	}
	msgs := m.shownMessages()
	if len(msgs) != 1 || msgs[0].ID != mx+"$loose" {
		t.Errorf("thread shows %d messages, want just the root it was started on", len(msgs))
	}
}

// A thread whose root aged out opens anyway; the missing root is fetched rather than
// left blank.
func TestOpeningAThreadFetchesAMissingRoot(t *testing.T) {
	t.Parallel()

	b := &threadBackend{fetch: domain.Message{ID: mx + "$gone", RoomID: mx + "!a:x", Sender: "matrix:@alice:x", Body: "the original question", Timestamp: at(0)}}
	m := inThread(t, b)
	m = update(t, m, incomingMsg{message: domain.Message{
		ID: mx + "$r9", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", Body: "still going", Timestamp: at(9), ThreadRoot: mx + "$gone",
	}})
	// The cursor goes on the message the summary row hangs under, the only way into a
	// thread whose root was never cached.
	m.timeline.selected = mx + "$loose"

	next, cmd := m.openThread()
	m = next
	if !m.thread.fetching {
		t.Fatal("a thread with no loaded root should be fetching it")
	}
	if cmd == nil {
		t.Fatal("no fetch was issued")
	}
	m = deliver(t, m, cmd)

	if m.thread.fetching {
		t.Error("the fetch should have settled")
	}
	if len(b.events) != 1 || b.events[0] != mx+"$gone" {
		t.Errorf("fetched %v, want the missing root", b.events)
	}
	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "the original question") {
		t.Errorf("the fetched root is not shown:\n%s", rows)
	}
}

// t on a message in no conversation says so.
func TestOpeningAThreadWhereThereIsNone(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	m.timeline.selected = mx + "$loose"
	next, _ := m.openThread()
	m = next

	if m.thread.open() {
		t.Error("a message in no thread should not open one")
	}
	if !strings.Contains(m.status(), "no thread here") {
		t.Errorf("status = %q, should say there is nothing to open", m.status())
	}
}

// A search hit inside a thread is not drawn in the room, so jumping to it opens the thread.
func TestRevealingAMessageOpensItsThread(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	next, _ := m.revealMessage(mx + "$r2")
	m = next

	if m.thread.root != mx+"$root" {
		t.Fatalf("open thread = %q, want the hit's thread", m.thread.root)
	}
	if m.timeline.selected != mx+"$r2" {
		t.Errorf("selected = %q, want the revealed message", m.timeline.selected)
	}
	// It is actually on screen, not just selected.
	if _, spans := m.layoutIndexed(); func() bool { _, ok := spans[mx+"$r2"]; return !ok }() {
		t.Error("the revealed message has no rows in the pane")
	}
}

// Revealing a room message closes a thread in the way rather than selecting a hidden row.
func TestRevealingLeavesAThreadWhenItHasTo(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	opened, _ := m.openThread()
	m = opened

	next, _ := m.revealMessage(mx + "$loose")
	m = next

	if m.thread.open() {
		t.Error("revealing a main-timeline message should close the thread")
	}
	if m.timeline.selected != mx+"$loose" {
		t.Errorf("selected = %q, want $loose", m.timeline.selected)
	}
}

// Clients chain thread replies to the previous message, so quoting each would double
// the height to repeat the row above.
func TestAThreadDropsTheQuoteOfTheRowAbove(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		// $r3 answers the root, three rows up, not the message before it.
		{ID: mx + "$r3", RoomID: mx + "!a:x", Sender: "matrix:@carol:x", SenderName: "Carol", Body: "back to the top", Timestamp: at(5), ThreadRoot: mx + "$root", ReplyTo: mx + "$root"},
	}}})
	// $r2 chains to $r1, the row directly above it.
	msgs := m.timeline.messages
	for i := range msgs {
		if msgs[i].ID == mx+"$r2" {
			msgs[i].ReplyTo = mx + "$r1"
		}
	}
	m = m.setMessages(msgs)

	opened, _ := m.openThread()
	m = opened
	// Styling puts escapes between the marker and the name, so rows are stripped first.
	rows := ansi.Strip(strings.Join(m.layoutRows(), "\n"))

	if strings.Contains(rows, "↪ Bob: on it") {
		t.Errorf("a reply quoted the row directly above it:\n%s", rows)
	}
	if !strings.Contains(rows, "↪ Alice: ship the release notes?") {
		t.Errorf("a reply reaching further back lost its quote:\n%s", rows)
	}
}

// A room sits at the bottom like a feed; a short thread starts at the top.
func TestAShortThreadStartsAtTheTop(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	opened, _ := m.openThread()
	m = opened

	lines := m.messageLines(m.msgAreaRows(), m.contentWidth())
	if strings.TrimSpace(lines[0]) == "" {
		t.Errorf("the thread starts with a blank row:\n%s", strings.Join(lines, "\n"))
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "" {
		t.Error("a short thread should leave its spare rows at the bottom")
	}
}

// The breadcrumb names the thread rather than repeating the root's whole message,
// which is already the first row.
func TestTheBreadcrumbIsALabelNotAParagraph(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	long := strings.Repeat("a very long opening message ", 10)
	msgs := m.timeline.messages
	for i := range msgs {
		if msgs[i].ID == mx+"$root" {
			msgs[i].Body = long
		}
	}
	m = m.setMessages(msgs)
	opened, _ := m.openThread()
	m = opened

	title := m.threadTitle()
	if len(title) > len("Alpha ▸ ")+threadTitleWidth*2 {
		t.Errorf("breadcrumb = %q, want the root snippet capped", title)
	}
	if !strings.Contains(title, "…") {
		t.Errorf("breadcrumb = %q, want it to say it was cut", title)
	}
}

// readBackend records which receipt each gesture sent, and for what.
type readBackend struct {
	apitest.Nop
	main    []domain.EventID
	threads [][2]domain.EventID
}

func (b *readBackend) MarkRead(_ context.Context, _ domain.RoomID, eventID domain.EventID, _ bool) error {
	b.main = append(b.main, eventID)
	return nil
}

func (b *readBackend) MarkThreadRead(_ context.Context, _ domain.RoomID, root, eventID domain.EventID, _ bool) error {
	b.threads = append(b.threads, [2]domain.EventID{root, eventID})
	return nil
}

func TestReadingARoomDoesNotReadItsThreads(t *testing.T) {
	t.Parallel()

	b := &readBackend{}
	m := inThread(t, b)
	_, cmd := m.markRead()
	deliver(t, m, cmd)

	if len(b.threads) != 0 {
		t.Errorf("opening a room sent a thread receipt: %v", b.threads)
	}
	if len(b.main) != 1 || b.main[0] != mx+"$loose" {
		t.Errorf("main receipts = %v, want the newest message outside a thread", b.main)
	}
}

// Opening a thread sends its receipt up to the newest reply; the room's receipt never
// does, and only this clears its summary badge.
func TestOpeningAThreadReadsIt(t *testing.T) {
	t.Parallel()

	b := &readBackend{}
	m := inThread(t, b)
	next, cmd := m.openThread()
	m = next
	deliver(t, m, cmd)

	if len(b.threads) != 1 || b.threads[0] != [2]domain.EventID{mx + "$root", mx + "$r2"} {
		t.Errorf("thread receipts = %v, want $root read to its newest reply", b.threads)
	}
}

// A thread with unread replies is badged on its summary row, in the room badge's glyph.
func TestAnUnreadThreadIsBadgedOnItsSummaryRow(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: threaded()})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{
		RoomID: mx + "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: mx + "$root", Unread: 2}},
	}})

	rows := ansi.Strip(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(rows, "2 replies") || !strings.Contains(rows, "●2") {
		t.Errorf("the summary row is not badged:\n%s", rows)
	}
}

// A read thread carries no badge; the row still shows the conversation happened.
func TestAReadThreadHasNoBadge(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: threaded()})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: mx + "!a:x", Counted: true}})

	rows := ansi.Strip(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(rows, "2 replies") {
		t.Fatalf("the summary row went missing:\n%s", rows)
	}
	if strings.Contains(rows, "●") {
		t.Errorf("a read thread is badged:\n%s", rows)
	}
}

// pagingBackend serves a thread's scrollback and records the requests, to tell the
// thread's endpoint from the room's.
type pagingBackend struct {
	apitest.Nop
	roomPages   []string
	threadPages [][2]string
	page        domain.TimelinePage
}

func (b *pagingBackend) Timeline(_ context.Context, _ domain.RoomID, from string, _ int) (domain.TimelinePage, error) {
	b.roomPages = append(b.roomPages, from)
	return domain.TimelinePage{}, nil
}

func (b *pagingBackend) ThreadPage(_ context.Context, _ domain.RoomID, root domain.EventID, from string, _ int) (domain.TimelinePage, error) {
	b.threadPages = append(b.threadPages, [2]string{string(root), from})
	return b.page, nil
}

func TestTheTopOfAThreadPagesTheThread(t *testing.T) {
	t.Parallel()

	b := &pagingBackend{page: domain.TimelinePage{
		Messages: []domain.Message{{
			ID: mx + "$r0", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", Body: "the first answer", Timestamp: at(0), ThreadRoot: mx + "$root",
		}},
		Next: "tok2",
	}}
	m := inThread(t, b)
	next, _ := m.openThread()
	m = next

	next, cmd := m.scrollToStart()
	m = next
	m = deliver(t, m, cmd)

	if len(b.roomPages) != 0 {
		t.Errorf("the room was paged from inside a thread: %v", b.roomPages)
	}
	if len(b.threadPages) != 1 || b.threadPages[0] != [2]string{mx + "$root", ""} {
		t.Fatalf("thread pages = %v, want one for $root with no token", b.threadPages)
	}
	if m.thread.token != "tok2" || m.thread.atStart {
		t.Errorf("token = %q atStart = %v, want the next page remembered", m.thread.token, m.thread.atStart)
	}
	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "the first answer") {
		t.Errorf("the fetched reply is not in the thread:\n%s", rows)
	}
	// A page with no next token is the start of the conversation; nothing more is asked.
	b.page = domain.TimelinePage{}
	m.thread.loading = false
	next, cmd = m.scrollToStart()
	m = next
	m = deliver(t, m, cmd)
	if !m.thread.atStart {
		t.Error("an empty page should mark the thread's start")
	}
}

// A room backfilled to its start may still have older thread replies.
func TestAFullyLoadedRoomDoesNotStopAThreadPaging(t *testing.T) {
	t.Parallel()

	b := &pagingBackend{}
	m := inThread(t, b)
	next, _ := m.openThread()
	m = next
	m.timeline.hist.atStart = true

	next, cmd := m.scrollToStart()
	m = next
	deliver(t, m, cmd)

	if len(b.threadPages) != 1 {
		t.Errorf("thread pages = %v, want one — the room's start is not the thread's", b.threadPages)
	}
}

// A thread's name replaces its root snippet everywhere the thread is named.
func TestNamingAThread(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	next, _ := m.openThread()
	m = next
	if got := m.threadTitle(); !strings.Contains(got, "ship the release notes?") {
		t.Fatalf("breadcrumb = %q, want the root's snippet by default", got)
	}

	next, _ = m.renameThread(m.thread.root)
	m = next
	if m.prompt.input != "ship the release notes?" {
		t.Errorf("prompt = %q, want it prefilled with what the thread is called now", m.prompt.input)
	}
	next, _ = m.submitThreadName("Release notes")
	m = next

	if got := m.threadName(mx + "$root"); got != "Release notes" {
		t.Errorf("threadName = %q, want the name it was given", got)
	}
	if got := m.threadTitle(); !strings.Contains(got, "Release notes") {
		t.Errorf("breadcrumb = %q, want the name", got)
	}
	// Clearing it falls back to the snippet, not a blank.
	next, _ = m.renameThread(mx + "$root")
	m = next
	next, _ = m.submitThreadName("  ")
	m = next
	if got := m.threadName(mx + "$root"); got != "ship the release notes?" {
		t.Errorf("threadName = %q, want the root's snippet back", got)
	}
}

// listedThreads opens the open room's thread picker, the daemon answering.
func listedThreads(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.listThreads()
	if cmd == nil {
		t.Fatal("listing the threads asked the daemon nothing")
	}
	return update(t, next, cmd())
}

// The picker lists the room's threads, newest activity first: the only way back to a
// read thread.
func TestThePickerListsTheRoomsThreads(t *testing.T) {
	t.Parallel()

	m := listedThreads(t, inThread(t, apitest.Nop{}))

	if m.picker.kind != pickerThread {
		t.Fatalf("picker = %v, want the thread picker", m.picker.kind)
	}
	items := m.picker.items
	if len(items) != 1 || items[0].value != mx+"$root" {
		t.Fatalf("items = %+v, want the room's one thread", items)
	}
	if items[0].label != "ship the release notes?" {
		t.Errorf("label = %q, want the thread's name", items[0].label)
	}
	if !strings.Contains(items[0].detail, "2 replies") || !strings.Contains(items[0].detail, "Bob") {
		t.Errorf("detail = %q, want the size and who last spoke", items[0].detail)
	}

	next, _ := m.acceptPick()
	m = next
	if m.thread.root != mx+"$root" {
		t.Errorf("thread root = %q, want the chosen conversation open", m.thread.root)
	}
	if m.picker.active() {
		t.Error("the picker should close when a thread is chosen")
	}
}

func TestThePickerSaysWhenThereAreNoThreads(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$one", RoomID: mx + "!a:x", Sender: "matrix:@alice:x", Body: "just a message", Timestamp: at(1)},
	}}})
	m = listedThreads(t, m)

	if m.picker.active() {
		t.Error("a room with no threads should not open a picker")
	}
	if !strings.Contains(m.status(), "no threads in") || !strings.Contains(m.status(), "start one with") {
		t.Errorf("status = %q, want it to say there are none", m.status())
	}
}

// r on a message already in a conversation answers it there.
func TestReplyingToARootAnswersInsideItsThread(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.timeline.selected = mx + "$root"

	acted, _, handled := m.timelineAction(actReply)
	if !handled {
		t.Fatal("reply was not handled")
	}
	m = acted
	if m.thread.root != mx+"$root" {
		t.Fatalf("thread.root = %q, want the pane showing the conversation being answered", m.thread.root)
	}
	if !m.compose.insertMode {
		t.Error("the composer is not open")
	}
	m.compose.input = "yes"
	sent, cmd := m.submit()
	m = sent
	if cmd != nil {
		runCmd(t, cmd)
	}

	if len(b.sent) != 1 {
		t.Fatalf("sent %d drafts, want 1", len(b.sent))
	}
	if b.sent[0].ThreadRoot != mx+"$root" || b.sent[0].ReplyTo != mx+"$root" {
		t.Errorf("draft = %+v, want a reply to $root inside $root", b.sent[0])
	}
}

// A message in no conversation is answered in place, with no thread invented and the
// pane left alone.
func TestReplyingToAnOrdinaryMessageStaysInTheRoom(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.timeline.selected = mx + "$loose"

	acted, _, handled := m.timelineAction(actReply)
	if !handled {
		t.Fatal("reply was not handled")
	}
	m = acted
	if m.thread.open() {
		t.Errorf("thread.root = %q, want the pane still on the room", m.thread.root)
	}
	m.compose.input = "noted"
	sent, cmd := m.submit()
	m = sent
	if cmd != nil {
		runCmd(t, cmd)
	}

	if len(b.sent) != 1 {
		t.Fatalf("sent %d drafts, want 1", len(b.sent))
	}
	if b.sent[0].ThreadRoot != "" || b.sent[0].ReplyTo != mx+"$loose" {
		t.Errorf("draft = %+v, want a plain reply to $loose", b.sent[0])
	}
}

// A plain reply to a message that later grew a thread is drawn in that conversation.
func TestAPlainReplyIntoAThreadIsDrawnInIt(t *testing.T) {
	t.Parallel()

	page := threaded()
	page.Messages = append(page.Messages, domain.Message{
		ID: mx + "$mine", RoomID: mx + "!a:x", Sender: "matrix:@me:x", SenderName: "Me",
		Body: "answering the question", Timestamp: at(5), ReplyTo: mx + "$root",
	})
	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: page})

	rows := strings.Join(m.layoutRows(), "\n")
	if strings.Contains(rows, "answering the question") {
		t.Errorf("the reply was drawn in the main timeline, outside the thread it answers:\n%s", rows)
	}
	if got := len(domain.ThreadMessages(m.timeline.messages, mx+"$root")); got != 4 {
		t.Errorf("thread holds %d messages, want 4 — the root, both replies and the plain one", got)
	}
}

// bridgedToSlack is a room of Slack bridge ghosts, from which roomProtocol reads the network.
func bridgedToSlack() domain.TimelinePage {
	return domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$root", RoomID: mx + "!a:x", Sender: "matrix:@slack_t0team-u0alice:x", SenderName: "Alice", Body: "did you fix it?", Timestamp: at(1)},
		{ID: mx + "$loose", RoomID: mx + "!a:x", Sender: "matrix:@slack_t0team-u0carol:x", SenderName: "Carol", Body: "unrelated", Timestamp: at(2)},
	}}
}

// Slack has no non-thread reply: the bridge opens a thread under what is answered, so
// answering here opens it too and both sides show the same exchange.
func TestReplyingInASlackRoomStartsTheThreadItWouldBecome(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inRoom(t, b, bridgedToSlack())
	m.timeline.selected = mx + "$loose"

	acted, _, handled := m.timelineAction(actReply)
	if !handled {
		t.Fatal("reply was not handled")
	}
	m = acted
	if m.thread.root != mx+"$loose" {
		t.Fatalf("thread.root = %q, want the message being answered", m.thread.root)
	}
	m.compose.input = "yes"
	sent, cmd := m.submit()
	m = sent
	if cmd != nil {
		runCmd(t, cmd)
	}

	if len(b.sent) != 1 {
		t.Fatalf("sent %d drafts, want 1", len(b.sent))
	}
	if b.sent[0].ThreadRoot != mx+"$loose" || b.sent[0].ReplyTo != mx+"$loose" {
		t.Errorf("draft = %+v, want a reply to $loose inside a thread rooted there", b.sent[0])
	}
}

// Elsewhere a reply stays a reply: Matrix has both kinds, and converting would invent
// a conversation where a quote was meant.
func TestReplyingOnMatrixStaysAPlainReply(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.timeline.selected = mx + "$loose"

	acted, _, _ := m.timelineAction(actReply)
	m = acted
	if m.thread.open() {
		t.Errorf("thread.root = %q, want the pane still on the room", m.thread.root)
	}
}

func TestAThreadIsNamedFromTheListWhileFiltering(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.picker = newPicker(pickerThread, []pickerItem{
		{label: "a thread", value: mx + "$root", match: "a thread"},
	})
	if m.picker.mode != pickerFilter {
		t.Fatal("the thread list is not filtering, so this test is not about the thing it says")
	}

	// A letter goes into the filter.
	typed, _, _ := m.pickerKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if typed.picker.filter != "a" {
		t.Errorf("filter = %q, want the letter typed into it", typed.picker.filter)
	}
	if typed.prompt.kind == promptThreadName {
		t.Fatal("a letter opened the rename prompt — the filter would be unusable")
	}

	// The chord names it.
	named, _, handled := m.pickerKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModAlt})
	if !handled {
		t.Fatal("the rename chord was not handled in the thread list")
	}
	if named.prompt.kind != promptThreadName {
		t.Errorf("prompt = %v, want the thread-name prompt open", named.prompt.kind)
	}
	if named.aimedAt.renamingThread != mx+"$root" {
		t.Errorf("renaming %q, want the thread under the cursor", named.aimedAt.renamingThread)
	}
}

// One chord names a thread from all three places it can be seen.
func TestAltRNamesAThreadFromEverywhereItIsVisible(t *testing.T) {
	t.Parallel()

	altR := tea.KeyPressMsg{Code: 'r', Mod: tea.ModAlt}

	base := func() Model {
		m := sized(t, withRooms(t, newModel()))
		next, _ := m.selectRoom(m.filteredRooms()[0])
		return next
	}

	t.Run("the open thread, from the timeline", func(t *testing.T) {
		t.Parallel()
		m := base()
		m.focus = paneTimeline
		m.thread.root = mx + "$root"

		named, _, handled := m.threadAction(actRenameThread)
		if !handled {
			t.Fatal("the timeline did not handle the rename chord")
		}
		if named.aimedAt.renamingThread != mx+"$root" {
			t.Errorf("renaming %q, want the open thread", named.aimedAt.renamingThread)
		}
	})

	t.Run("nothing to name is inert rather than inventive", func(t *testing.T) {
		t.Parallel()
		m := base()
		m.focus = paneTimeline

		named, _, _ := m.threadAction(actRenameThread)
		if named.aimedAt.renamingThread != "" {
			t.Errorf("renaming %q with no thread in view, want nothing",
				named.aimedAt.renamingThread)
		}
		if named.prompt.kind == promptThreadName {
			t.Error("a prompt opened for a thread that does not exist")
		}
	})

	// One key, one action, in every scope that can show a thread.
	t.Run("the same chord in every scope", func(t *testing.T) {
		t.Parallel()
		m := base()
		for _, scope := range []struct {
			name string
			in   scope
		}{
			{"timeline", scopeTimeline},
			{"room list", scopeRooms},
			{"thread list", scopePicker},
		} {
			if got := m.keys.lookup(altR.String(), scope.in); got != actRenameThread {
				t.Errorf("%s: alt+r resolves to %v, want actRenameThread", scope.name, got)
			}
		}
	})
}

// threadListBackend answers ListThreads for the room list's `ctrl+t`: there the client
// holds only the cursor room's preview, so the list comes from the daemon.
type threadListBackend struct {
	apitest.Nop
	asked   []domain.RoomID
	threads map[domain.RoomID][]domain.Thread
}

func (b *threadListBackend) ListThreads(_ context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	b.asked = append(b.asked, roomID)
	return b.threads[roomID], nil
}

// inRoomList is the room list with two rooms, the cursor on Alpha and the keyboard
// on the list.
func inRoomList(t *testing.T, b api.Backend) Model {
	t.Helper()
	m := sized(t, update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
		{ID: mx + "!b:x", Name: "Bravo"},
	}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus = paneRooms
	return m
}

func TestListingThreadsFromTheRoomListAsksAboutThatRoom(t *testing.T) {
	t.Parallel()

	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{
		mx + "!a:x": {{Root: mx + "$root", RoomID: mx + "!a:x", Count: 2, LatestAt: at(4), LatestSenderName: "Bob"}},
	}}
	m := inRoomList(t, b)

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	msg, ok := msgOf[scopeThreadsMsg](t, cmd)
	if !ok {
		t.Fatal("ctrl+t in the room list asked for nothing")
	}
	if len(b.asked) != 1 || b.asked[0] != mx+"!a:x" {
		t.Fatalf("asked about %v, want only the room under the cursor", b.asked)
	}
	m = update(t, m, msg)
	if m.picker.kind != pickerThread {
		t.Fatalf("picker kind = %v, want the thread list", m.picker.kind)
	}
	row := m.picker.all[0]
	// One room's list doesn't repeat the room on every row; the prefix tells rooms apart.
	if strings.Contains(row.label, "Alpha") {
		t.Errorf("label = %q, should not carry the room it is all in", row.label)
	}
	if row.room != mx+"!a:x" {
		t.Errorf("row room = %q, want the room the thread is in", row.room)
	}
}

// An empty answer names the room, not "no threads in scope".
func TestAnEmptyThreadListNamesTheRoomItAskedAbout(t *testing.T) {
	t.Parallel()

	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{}}
	m := inRoomList(t, b)

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	msg, _ := msgOf[scopeThreadsMsg](t, cmd)
	m = update(t, m, msg)

	if m.picker.active() {
		t.Error("nothing to show should open no picker")
	}
	if got := m.status(); !strings.Contains(got, "Alpha") {
		t.Errorf("status = %q, want it to name the room", got)
	}
}

// Choosing from the room list can't enter the thread at once: the room holds only its
// cache preview. The request is armed and taken when the history lands.
func TestChoosingAThreadFromTheRoomListWaitsForTheHistory(t *testing.T) {
	t.Parallel()

	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{
		mx + "!a:x": {{Root: mx + "$root", RoomID: mx + "!a:x", Count: 2, LatestAt: at(4), LatestSenderName: "Bob"}},
	}}
	m := inRoomList(t, b)
	_, cmd := press(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	msg, _ := msgOf[scopeThreadsMsg](t, cmd)
	m = update(t, m, msg)

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.thread.open() {
		t.Fatal("the thread cannot be open before its replies are loaded")
	}
	if m.rows.opening != mx+"$root" {
		t.Fatalf("pending thread = %q, want the chosen root", m.rows.opening)
	}

	// The backfill reads the cache past the network's last page before it settles.
	m, cmd = asModel(m.Update(timelineMsg{roomID: mx + "!a:x", page: threaded()}))
	m = deliver(t, m, cmd)
	if m.thread.root != mx+"$root" {
		t.Errorf("thread root = %q, want the history to have taken the request", m.thread.root)
	}
	if m.rows.opening != "" {
		t.Error("the request should be answered exactly once")
	}
}

// A list wider than one room carries the room on every row, so choosing a thread
// elsewhere opens that room first.
func TestChoosingAThreadInAnotherRoomOpensThatRoom(t *testing.T) {
	t.Parallel()

	m := inRoomList(t, apitest.Nop{})
	m = update(t, m, scopeThreadsMsg{threads: []scopeThread{
		{room: domain.Room{ID: mx + "!b:x", Name: "Bravo"}, thread: domain.Thread{Root: mx + "$elsewhere", RoomID: mx + "!b:x", Count: 2, LatestAt: at(4)}},
		{room: domain.Room{ID: mx + "!a:x", Name: "Alpha"}, thread: domain.Thread{Root: mx + "$root", RoomID: mx + "!a:x", Count: 2, LatestAt: at(2)}},
	}})
	if m.picker.kind != pickerThread {
		t.Fatalf("picker kind = %v, want the thread list", m.picker.kind)
	}
	if row := m.picker.all[0]; !strings.Contains(row.label, "Bravo") {
		t.Fatalf("label = %q, want the room in front of the name across rooms", row.label)
	}

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.openRoom != mx+"!b:x" {
		t.Fatalf("open room = %q, want the thread's own room", m.openRoom)
	}
	if m.rows.opening != mx+"$elsewhere" {
		t.Errorf("pending thread = %q, want it held until the room's history lands", m.rows.opening)
	}
}

// The last replier's name in a thread label obeys the space rule like every other surface.
func TestTheThreadListShortensNamesByTheSpaceRule(t *testing.T) {
	t.Parallel()

	m := sized(t, update(t, newModel(), roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}}))
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: mx + "!w:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x"}},
	}})
	m.prefs.display.SpaceRules = []config.SpaceRule{{Space: "space:Work", FirstNameOnly: true}}
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = update(t, next, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$root", RoomID: mx + "!a:x", Sender: "matrix:@noa:x", SenderName: "Noa Katz", Body: "ship it?", Timestamp: at(1)},
		{ID: mx + "$r1", RoomID: mx + "!a:x", Sender: "matrix:@noa:x", SenderName: "Noa Katz", Body: "on it", Timestamp: at(2), ThreadRoot: mx + "$root"},
	}}})

	items := listedThreads(t, m).picker.items
	if len(items) != 1 {
		t.Fatalf("thread items = %d, want the one thread", len(items))
	}
	if strings.Contains(items[0].detail, "Katz") {
		t.Errorf("detail = %q, want the space's first-name rule applied", items[0].detail)
	}
	if !strings.Contains(items[0].detail, "Noa") {
		t.Errorf("detail = %q, want it to still name who answered", items[0].detail)
	}
	// So does the timeline's summary row for the same thread.
	if row := m.threadRow(domain.Thread{
		RoomID: mx + "!a:x", Root: mx + "$root", Count: 1, LatestSender: "matrix:@noa:x", LatestSenderName: "Noa Katz",
	}, 12, 60); strings.Contains(row, "Katz") {
		t.Errorf("thread summary row = %q, want the first-name rule applied", row)
	}
}

// A forum's topics predate the timeline: the thread menu lists them as the daemon
// does, by title.
func TestATopicOlderThanTheTimelineIsNamedByItsTitle(t *testing.T) {
	t.Parallel()
	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{mx + "!a:x": {
		{RoomID: mx + "!a:x", Root: mx + "$topic", Title: "Flea market", Count: 40, LatestAt: at(9)},
	}}}
	m := listedThreads(t, inThread(t, b))
	i := slices.IndexFunc(m.picker.items, func(p pickerItem) bool { return p.value == mx+"$topic" })
	if i < 0 || m.picker.items[i].label != isolate("Flea market") {
		t.Fatalf("items = %+v, want the topic named by its title", m.picker.items)
	}
	if !slices.ContainsFunc(m.picker.items, func(p pickerItem) bool { return p.value == mx+"$root" }) {
		t.Error("the thread the timeline holds was left out")
	}
}
