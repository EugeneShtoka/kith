package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// threaded is a room whose timeline holds a root, two replies to it, and one unrelated
// message sent between them — the braid a thread arrived as before the relation was read.
func threaded() domain.TimelinePage {
	return domain.TimelinePage{Messages: []domain.Message{
		{ID: "$root", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "ship the release notes?", Timestamp: at(1)},
		{ID: "$r1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "on it", Timestamp: at(2), ThreadRoot: "$root"},
		{ID: "$loose", RoomID: "!a:x", Sender: "@carol:x", SenderName: "Carol", Body: "unrelated", Timestamp: at(3)},
		{ID: "$r2", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "done", Timestamp: at(4), ThreadRoot: "$root"},
	}}
}

func TestAThreadCollapsesToOneSummaryRow(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: threaded()})

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

// The summary belongs to the root's span, so it is drawn immediately under it —
// not at the end of the room, and not where the replies used to be.
func TestTheSummaryRowSitsUnderItsRoot(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: threaded()})

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

// A thread reply has no row, so the message cursor must not stop on one: j/k walk
// what is drawn, or the selection disappears mid-room.
func TestTheCursorSkipsCollapsedReplies(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: threaded()})

	if got := m.selectedID(); got != "$loose" {
		t.Fatalf("selection = %q, want the newest *drawn* message", got)
	}
	next, _ := m.moveSelection(-1)
	if got := next.selectedID(); got != "$root" {
		t.Errorf("moving up selected %q, want $root — the replies between are not drawn", got)
	}
}

// A thread whose root aged out of the cached window still has to appear: its
// summary anchors at the newest message before its newest reply, saying what it
// cannot show rather than showing nothing.
func TestAnOlderThreadSaysSo(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$a", RoomID: "!a:x", Sender: "@alice:x", Body: "loaded", Timestamp: at(1)},
		{ID: "$r1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "reply", Timestamp: at(2), ThreadRoot: "$gone"},
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
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$root", RoomID: "!a:x", Sender: "@alice:x", Body: "question", Timestamp: at(1)},
		{ID: "$r1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "answer", Timestamp: at(2), ThreadRoot: "$root"},
	}}})

	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "1 reply ") {
		t.Errorf("summary does not read as one reply:\n%s", rows)
	}
}

// threadBackend records the drafts the client asks it to send, which is the only
// way to see what a composed message actually carried, and answers the one fetch a
// thread with no loaded root makes.
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

// inThread opens room !a:x with the braid from threaded() and puts the cursor on
// the root, ready for the thread keys.
func inThread(t *testing.T, b api.Backend) Model {
	t.Helper()
	return inRoom(t, b, threaded())
}

// inRoom is the same room holding whatever timeline a test needs — the network it
// is bridged to is read from who is talking in it (roomProtocol), so a fixture is
// how a test says "this is a Slack room".
func inRoom(t *testing.T, b api.Backend, page domain.TimelinePage) Model {
	t.Helper()
	m := sized(t, update(t, New(context.Background(), b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
	}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, timelineMsg{roomID: "!a:x", page: page})
	m.focus, m.compose.insertMode = paneTimeline, false
	m.timeline.selected = "$root"
	return m
}

func TestOpeningAThreadTakesThePaneOver(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	next, _ := m.openThread()
	m = next

	if m.thread.root != "$root" {
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

// Back climbs out of the thread rather than out of the pane, and puts the cursor
// on the root — the row the summary is on, which is where the conversation is from
// the room's side.
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
	if m.timeline.selected != "$root" {
		t.Errorf("selected = %q, want the thread's root", m.timeline.selected)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.focus != paneRooms {
		t.Error("back again should leave the pane")
	}
}

// The defect the whole slice exists for: an answer typed in a thread must land in
// the thread, not in the room's main timeline.
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
	if b.sent[0].ThreadRoot != "$root" {
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

// r inside a thread answers that message *within* the thread — both pointers, which
// is what makes it a real reply rather than the chain fallback.
func TestReplyingInsideAThreadKeepsBoth(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	next, _ := m.openThread()
	m = next
	m.timeline.selected = "$r1"

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
	if b.sent[0].ThreadRoot != "$root" || b.sent[0].ReplyTo != "$r1" {
		t.Errorf("draft = %+v, want a reply to $r1 inside $root", b.sent[0])
	}
}

// T on a message nobody has answered opens an empty thread rooted at it, with the
// composer ready — nothing exists on the server until that first reply.
func TestStartingAThread(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.timeline.selected = "$loose"
	next, _ := m.startThread()
	m = next

	if m.thread.root != "$loose" {
		t.Fatalf("thread root = %q, want the selected message", m.thread.root)
	}
	if !m.compose.insertMode {
		t.Error("starting a thread should open the composer")
	}
	msgs := m.shownMessages()
	if len(msgs) != 1 || msgs[0].ID != "$loose" {
		t.Errorf("thread shows %d messages, want just the root it was started on", len(msgs))
	}
}

// A thread whose root aged out of the cached window opens anyway: the replies are
// readable, and the missing first row is fetched rather than left blank.
func TestOpeningAThreadFetchesAMissingRoot(t *testing.T) {
	t.Parallel()

	b := &threadBackend{fetch: domain.Message{ID: "$gone", RoomID: "!a:x", Sender: "@alice:x", Body: "the original question", Timestamp: at(0)}}
	m := inThread(t, b)
	m = update(t, m, incomingMsg{message: domain.Message{
		ID: "$r9", RoomID: "!a:x", Sender: "@bob:x", Body: "still going", Timestamp: at(9), ThreadRoot: "$gone",
	}})
	// The cursor goes on the message the summary row hangs under — the only way in
	// to a thread whose root the cache has never held.
	m.timeline.selected = "$loose"

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
	if len(b.events) != 1 || b.events[0] != "$gone" {
		t.Errorf("fetched %v, want the missing root", b.events)
	}
	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "the original question") {
		t.Errorf("the fetched root is not shown:\n%s", rows)
	}
}

// t on a message in no conversation says so instead of inventing one.
func TestOpeningAThreadWhereThereIsNone(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	m.timeline.selected = "$loose"
	next, _ := m.openThread()
	m = next

	if m.thread.open() {
		t.Error("a message in no thread should not open one")
	}
	if !strings.Contains(m.status(), "no thread here") {
		t.Errorf("status = %q, should say there is nothing to open", m.status())
	}
}

// The regression collapsing would otherwise cause: a search hit inside a thread is
// not drawn in the room, so jumping to it has to open the thread it lives in.
func TestRevealingAMessageOpensItsThread(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	next, _ := m.revealMessage("$r2")
	m = next

	if m.thread.root != "$root" {
		t.Fatalf("open thread = %q, want the hit's thread", m.thread.root)
	}
	if m.timeline.selected != "$r2" {
		t.Errorf("selected = %q, want the revealed message", m.timeline.selected)
	}
	// And it is actually on screen, which is the half a bare selection would miss.
	if _, spans := m.layoutIndexed(); func() bool { _, ok := spans["$r2"]; return !ok }() {
		t.Error("the revealed message has no rows in the pane")
	}
}

// And back out again: revealing something in the room closes a thread that is in
// the way, rather than selecting a message the pane is not showing.
func TestRevealingLeavesAThreadWhenItHasTo(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	opened, _ := m.openThread()
	m = opened

	next, _ := m.revealMessage("$loose")
	m = next

	if m.thread.open() {
		t.Error("revealing a main-timeline message should close the thread")
	}
	if m.timeline.selected != "$loose" {
		t.Errorf("selected = %q, want $loose", m.timeline.selected)
	}
}

// Every client chains its thread replies to the previous message, so quoting all of them
// doubles the height of the conversation to repeat the row above.
func TestAThreadDropsTheQuoteOfTheRowAbove(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		// $r3 answers the root, three rows up — not the message before it.
		{ID: "$r3", RoomID: "!a:x", Sender: "@carol:x", SenderName: "Carol", Body: "back to the top", Timestamp: at(5), ThreadRoot: "$root", ReplyTo: "$root"},
	}}})
	// $r2 chains to $r1, the row directly above it.
	msgs := m.timeline.messages
	for i := range msgs {
		if msgs[i].ID == "$r2" {
			msgs[i].ReplyTo = "$r1"
		}
	}
	m = m.setMessages(msgs)

	opened, _ := m.openThread()
	m = opened
	// Styling puts escape sequences between the marker and the name, so the rows are
	// stripped before they are read as text.
	rows := ansi.Strip(strings.Join(m.layoutRows(), "\n"))

	if strings.Contains(rows, "↪ Bob: on it") {
		t.Errorf("a reply quoted the row directly above it:\n%s", rows)
	}
	if !strings.Contains(rows, "↪ Alice: ship the release notes?") {
		t.Errorf("a reply reaching further back lost its quote:\n%s", rows)
	}
}

// A room is a feed and sits at the bottom; a thread is a conversation with a
// beginning, so a short one starts at the top instead of floating under a void.
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

// The breadcrumb names the thread; it does not set the root's whole message across
// the top of the pane, where the root is already the first row.
func TestTheBreadcrumbIsALabelNotAParagraph(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	long := strings.Repeat("a very long opening message ", 10)
	msgs := m.timeline.messages
	for i := range msgs {
		if msgs[i].ID == "$root" {
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

// Opening a room reads its main timeline and nothing else.
func TestReadingARoomDoesNotReadItsThreads(t *testing.T) {
	t.Parallel()

	b := &readBackend{}
	m := inThread(t, b)
	_, cmd := m.markRead()
	deliver(t, m, cmd)

	if len(b.threads) != 0 {
		t.Errorf("opening a room sent a thread receipt: %v", b.threads)
	}
	if len(b.main) != 1 || b.main[0] != "$loose" {
		t.Errorf("main receipts = %v, want the newest message outside a thread", b.main)
	}
}

// Opening a thread reads that thread, up to its newest reply — the receipt the
// room's own never sends, and the only thing that clears the badge on its summary.
func TestOpeningAThreadReadsIt(t *testing.T) {
	t.Parallel()

	b := &readBackend{}
	m := inThread(t, b)
	next, cmd := m.openThread()
	m = next
	deliver(t, m, cmd)

	if len(b.threads) != 1 || b.threads[0] != [2]domain.EventID{"$root", "$r2"} {
		t.Errorf("thread receipts = %v, want $root read to its newest reply", b.threads)
	}
}

// A thread the daemon says has unread replies is badged where the summary is, in
// the room badge's glyph. Without it T4's counts are a number nobody sees.
func TestAnUnreadThreadIsBadgedOnItsSummaryRow(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: threaded()})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{
		RoomID: "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: "$root", Unread: 2}},
	}})

	rows := ansi.Strip(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(rows, "2 replies") || !strings.Contains(rows, "●2") {
		t.Errorf("the summary row is not badged:\n%s", rows)
	}
}

// A thread that has been read carries no badge — the row still says a conversation
// happened, which is what the summary is for.
func TestAReadThreadHasNoBadge(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: threaded()})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!a:x", Counted: true}})

	rows := ansi.Strip(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(rows, "2 replies") {
		t.Fatalf("the summary row went missing:\n%s", rows)
	}
	if strings.Contains(rows, "●") {
		t.Errorf("a read thread is badged:\n%s", rows)
	}
}

// pagingBackend serves a thread's own scrollback, and records what was asked for so
// a test can tell the thread's endpoint from the room's.
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

// Reaching the top of an open thread pages the *thread*, not the room.
func TestTheTopOfAThreadPagesTheThread(t *testing.T) {
	t.Parallel()

	b := &pagingBackend{page: domain.TimelinePage{
		Messages: []domain.Message{{
			ID: "$r0", RoomID: "!a:x", Sender: "@bob:x", Body: "the first answer", Timestamp: at(0), ThreadRoot: "$root",
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
	if len(b.threadPages) != 1 || b.threadPages[0] != [2]string{"$root", ""} {
		t.Fatalf("thread pages = %v, want one for $root with no token", b.threadPages)
	}
	if m.thread.token != "tok2" || m.thread.atStart {
		t.Errorf("token = %q atStart = %v, want the next page remembered", m.thread.token, m.thread.atStart)
	}
	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "the first answer") {
		t.Errorf("the fetched reply is not in the thread:\n%s", rows)
	}
	// And a page with no next token is the start of the conversation, after which
	// there is nothing left to ask for.
	b.page = domain.TimelinePage{}
	m.thread.loading = false
	next, cmd = m.scrollToStart()
	m = next
	m = deliver(t, m, cmd)
	if !m.thread.atStart {
		t.Error("an empty page should mark the thread's start")
	}
}

// A room that has been backfilled to its start is at *its* start; a thread inside it may
// still have older replies.
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

// Naming a thread replaces the snippet of its root everywhere the thread is named.
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

	if got := m.threadName("$root"); got != "Release notes" {
		t.Errorf("threadName = %q, want the name it was given", got)
	}
	if got := m.threadTitle(); !strings.Contains(got, "Release notes") {
		t.Errorf("breadcrumb = %q, want the name", got)
	}
	// And clearing it falls back to the snippet rather than to a blank.
	next, _ = m.renameThread("$root")
	m = next
	next, _ = m.submitThreadName("  ")
	m = next
	if got := m.threadName("$root"); got != "ship the release notes?" {
		t.Errorf("threadName = %q, want the root's snippet back", got)
	}
}

// The picker lists the room's conversations, newest activity first — the way back to
// a thread that has been read, which is otherwise nowhere.
func TestThePickerListsTheRoomsThreads(t *testing.T) {
	t.Parallel()

	m := inThread(t, apitest.Nop{})
	next, _ := m.listThreads()
	m = next

	if m.picker.kind != pickerThread {
		t.Fatalf("picker = %v, want the thread picker", m.picker.kind)
	}
	items := m.picker.items
	if len(items) != 1 || items[0].value != "$root" {
		t.Fatalf("items = %+v, want the room's one thread", items)
	}
	if items[0].label != "ship the release notes?" {
		t.Errorf("label = %q, want the thread's name", items[0].label)
	}
	if !strings.Contains(items[0].detail, "2 replies") || !strings.Contains(items[0].detail, "Bob") {
		t.Errorf("detail = %q, want the size and who last spoke", items[0].detail)
	}

	next, _ = m.acceptPick()
	m = next
	if m.thread.root != "$root" {
		t.Errorf("thread root = %q, want the chosen conversation open", m.thread.root)
	}
	if m.picker.active() {
		t.Error("the picker should close when a thread is chosen")
	}
}

// A room with no conversation in it says so rather than opening an empty picker.
func TestThePickerSaysWhenThereAreNoThreads(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$one", RoomID: "!a:x", Sender: "@alice:x", Body: "just a message", Timestamp: at(1)},
	}}})
	next, _ := m.listThreads()
	m = next

	if m.picker.active() {
		t.Error("a room with no threads should not open a picker")
	}
	if !strings.Contains(m.status(), "no threads in this room yet") {
		t.Errorf("status = %q, want it to say there are none", m.status())
	}
}

// r on a message that is already in a conversation answers it there.
func TestReplyingToARootAnswersInsideItsThread(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.timeline.selected = "$root"

	acted, _, handled := m.timelineAction(actReply)
	if !handled {
		t.Fatal("reply was not handled")
	}
	m = acted
	if m.thread.root != "$root" {
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
	if b.sent[0].ThreadRoot != "$root" || b.sent[0].ReplyTo != "$root" {
		t.Errorf("draft = %+v, want a reply to $root inside $root", b.sent[0])
	}
}

// And nowhere else: a message in no conversation is answered where it is, with no
// thread invented around it and the pane left alone.
func TestReplyingToAnOrdinaryMessageStaysInTheRoom(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.timeline.selected = "$loose"

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
	if b.sent[0].ThreadRoot != "" || b.sent[0].ReplyTo != "$loose" {
		t.Errorf("draft = %+v, want a plain reply to $loose", b.sent[0])
	}
}

// The reply a client sent before it knew better: a plain reply to a message a
// thread has since grown under is drawn in that conversation, not beside it.
func TestAPlainReplyIntoAThreadIsDrawnInIt(t *testing.T) {
	t.Parallel()

	page := threaded()
	page.Messages = append(page.Messages, domain.Message{
		ID: "$mine", RoomID: "!a:x", Sender: "@me:x", SenderName: "Me",
		Body: "answering the question", Timestamp: at(5), ReplyTo: "$root",
	})
	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: page})

	rows := strings.Join(m.layoutRows(), "\n")
	if strings.Contains(rows, "answering the question") {
		t.Errorf("the reply was drawn in the main timeline, outside the thread it answers:\n%s", rows)
	}
	if got := len(domain.ThreadMessages(m.timeline.messages, "$root")); got != 4 {
		t.Errorf("thread holds %d messages, want 4 — the root, both replies and the plain one", got)
	}
}

// bridgedToSlack is a room whose participants are the Slack bridge's ghosts, which
// is where roomProtocol reads the network from.
func bridgedToSlack() domain.TimelinePage {
	return domain.TimelinePage{Messages: []domain.Message{
		{ID: "$root", RoomID: "!a:x", Sender: "@slack_t0team-u0alice:x", SenderName: "Alice", Body: "did you fix it?", Timestamp: at(1)},
		{ID: "$loose", RoomID: "!a:x", Sender: "@slack_t0team-u0carol:x", SenderName: "Carol", Body: "unrelated", Timestamp: at(2)},
	}}
}

// Slack has no reply that is not a thread: the bridge opens one under whatever is
// answered, so answering here opens it on this side too, and the two windows show
// the same exchange in the same place.
func TestReplyingInASlackRoomStartsTheThreadItWouldBecome(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inRoom(t, b, bridgedToSlack())
	m.timeline.selected = "$loose"

	acted, _, handled := m.timelineAction(actReply)
	if !handled {
		t.Fatal("reply was not handled")
	}
	m = acted
	if m.thread.root != "$loose" {
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
	if b.sent[0].ThreadRoot != "$loose" || b.sent[0].ReplyTo != "$loose" {
		t.Errorf("draft = %+v, want a reply to $loose inside a thread rooted there", b.sent[0])
	}
}

// Everywhere else a reply is still a reply. Matrix has both kinds, and turning one
// into the other would invent a conversation where a quote was meant.
func TestReplyingOnMatrixStaysAPlainReply(t *testing.T) {
	t.Parallel()

	b := &threadBackend{}
	m := inThread(t, b)
	m.timeline.selected = "$loose"

	acted, _, _ := m.timelineAction(actReply)
	m = acted
	if m.thread.open() {
		t.Errorf("thread.root = %q, want the pane still on the room", m.thread.root)
	}
}

// The thread list can name the thread under the cursor, while it is filtering.
func TestAThreadIsNamedFromTheListWhileFiltering(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.picker = newPicker(pickerThread, []pickerItem{
		{label: "a thread", value: "$root", match: "a thread"},
	})
	if m.picker.mode != pickerFilter {
		t.Fatal("the thread list is not filtering, so this test is not about the thing it says")
	}

	// A letter goes into the filter, as it must.
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
	if named.aimedAt.renamingThread != "$root" {
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
		m.thread.root = "$root"

		named, _, handled := m.threadAction(actRenameThread)
		if !handled {
			t.Fatal("the timeline did not handle the rename chord")
		}
		if named.aimedAt.renamingThread != "$root" {
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

	// And the claim itself: one key, one action, in every scope that can show a thread.
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

// threadListBackend answers ListThreads, which is what the room list's `ctrl+t` asks:
// standing there the client holds only the cursor room's preview, so the list has to
// come from the daemon rather than from what is loaded.
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
// with the list rather than with the timeline.
func inRoomList(t *testing.T, b api.Backend) Model {
	t.Helper()
	m := sized(t, update(t, New(context.Background(), b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo"},
	}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus = paneRooms
	return m
}

func TestListingThreadsFromTheRoomListAsksAboutThatRoom(t *testing.T) {
	t.Parallel()

	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{
		"!a:x": {{Root: "$root", RoomID: "!a:x", Count: 2, LatestAt: at(4), LatestSenderName: "Bob"}},
	}}
	m := inRoomList(t, b)

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	msg, ok := msgOf[scopeThreadsMsg](t, cmd)
	if !ok {
		t.Fatal("ctrl+t in the room list asked for nothing")
	}
	if len(b.asked) != 1 || b.asked[0] != "!a:x" {
		t.Fatalf("asked about %v, want only the room under the cursor", b.asked)
	}
	m = update(t, m, msg)
	if m.picker.kind != pickerThread {
		t.Fatalf("picker kind = %v, want the thread list", m.picker.kind)
	}
	row := m.picker.all[0]
	// One room's list does not repeat the room on every row — the prefix is there to
	// tell conversations in *different* rooms apart.
	if strings.Contains(row.label, "Alpha") {
		t.Errorf("label = %q, should not carry the room it is all in", row.label)
	}
	if row.room != "!a:x" {
		t.Errorf("row room = %q, want the room the thread is in", row.room)
	}
}

// An empty answer names the room, because "no threads in scope" is a strange thing to
// be told about the one room you are pointing at.
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

// Choosing from the room list cannot enter the thread on the spot: the room holds
// only its cache preview, and the thread view is a filter over messages that are not
// there yet. The request is armed and taken when the history lands.
func TestChoosingAThreadFromTheRoomListWaitsForTheHistory(t *testing.T) {
	t.Parallel()

	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{
		"!a:x": {{Root: "$root", RoomID: "!a:x", Count: 2, LatestAt: at(4), LatestSenderName: "Bob"}},
	}}
	m := inRoomList(t, b)
	_, cmd := press(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	msg, _ := msgOf[scopeThreadsMsg](t, cmd)
	m = update(t, m, msg)

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.thread.open() {
		t.Fatal("the thread cannot be open before its replies are loaded")
	}
	if m.rows.opening != "$root" {
		t.Fatalf("pending thread = %q, want the chosen root", m.rows.opening)
	}

	m = update(t, m, timelineMsg{roomID: "!a:x", page: threaded()})
	if m.thread.root != "$root" {
		t.Errorf("thread root = %q, want the history to have taken the request", m.thread.root)
	}
	if m.rows.opening != "" {
		t.Error("the request should be answered exactly once")
	}
}

// A list wider than one room carries the room on every row, so choosing a thread
// somewhere else opens that room first.
func TestChoosingAThreadInAnotherRoomOpensThatRoom(t *testing.T) {
	t.Parallel()

	m := inRoomList(t, apitest.Nop{})
	m = update(t, m, scopeThreadsMsg{threads: []scopeThread{
		{room: domain.Room{ID: "!b:x", Name: "Bravo"}, thread: domain.Thread{Root: "$elsewhere", RoomID: "!b:x", Count: 2, LatestAt: at(4)}},
		{room: domain.Room{ID: "!a:x", Name: "Alpha"}, thread: domain.Thread{Root: "$root", RoomID: "!a:x", Count: 2, LatestAt: at(2)}},
	}})
	if m.picker.kind != pickerThread {
		t.Fatalf("picker kind = %v, want the thread list", m.picker.kind)
	}
	if row := m.picker.all[0]; !strings.Contains(row.label, "Bravo") {
		t.Fatalf("label = %q, want the room in front of the name across rooms", row.label)
	}

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.openRoom != "!b:x" {
		t.Fatalf("open room = %q, want the thread's own room", m.openRoom)
	}
	if m.rows.opening != "$elsewhere" {
		t.Errorf("pending thread = %q, want it held until the room's history lands", m.rows.opening)
	}
}

// A thread's label carries the name of whoever answered last, and that name is somebody's
// — so it obeys the same space rule every other surface does.
func TestTheThreadListShortensNamesByTheSpaceRule(t *testing.T) {
	t.Parallel()

	m := sized(t, update(t, newModel(), roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}}))
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	m.prefs.display.SpaceRules = []config.SpaceRule{{Space: "Work", FirstNameOnly: true}}
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = update(t, next, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$root", RoomID: "!a:x", Sender: "@noa:x", SenderName: "Noa Katz", Body: "ship it?", Timestamp: at(1)},
		{ID: "$r1", RoomID: "!a:x", Sender: "@noa:x", SenderName: "Noa Katz", Body: "on it", Timestamp: at(2), ThreadRoot: "$root"},
	}}})

	items := m.threadItems()
	if len(items) != 1 {
		t.Fatalf("thread items = %d, want the one thread", len(items))
	}
	if strings.Contains(items[0].detail, "Katz") {
		t.Errorf("detail = %q, want the space's first-name rule applied", items[0].detail)
	}
	if !strings.Contains(items[0].detail, "Noa") {
		t.Errorf("detail = %q, want it to still name who answered", items[0].detail)
	}
	// And the summary row the timeline draws for the same thread, which reads the
	// name the same way.
	if row := m.threadRow(domain.Thread{
		RoomID: "!a:x", Root: "$root", Count: 1, LatestSender: "@noa:x", LatestSenderName: "Noa Katz",
	}, 12, 60); strings.Contains(row, "Katz") {
		t.Errorf("thread summary row = %q, want the first-name rule applied", row)
	}
}
