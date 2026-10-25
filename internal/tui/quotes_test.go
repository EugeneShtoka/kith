package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// quoting is a client in a room whose backend answers FetchEvent; messages arrive via Update
// so the triggered fetch runs.
func quoting(t *testing.T, backend *quoteBackend) Model {
	t.Helper()
	m := update(t, starterNew(backend, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(domain.Room{ID: mx + "!a:x", Name: "Alpha"})
	m = next
	m.focus, m.compose.insertMode = paneTimeline, false
	return m.clearStatus()
}

// quoteBackend answers FetchEvent, counting the asks.
type quoteBackend struct {
	apitest.Nop
	asks int
	msg  domain.Message
	err  error
}

func (q *quoteBackend) FetchEvent(context.Context, domain.RoomID, domain.EventID) (domain.Message, error) {
	q.asks++
	return q.msg, q.err
}

// A reply whose target is not loaded fetches it, instead of drawing `↪ …`.
func TestAQuoteIsFetchedWhenItsTargetIsNotLoaded(t *testing.T) {
	t.Parallel()

	backend := &quoteBackend{msg: domain.Message{
		ID: mx + "$old", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", SenderName: "Dana",
		Body: "the message being answered",
	}}
	m := quoting(t, backend)
	m, cmd := routed(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$reply", RoomID: mx + "!a:x", Sender: "matrix:@me:x", Body: "test", ReplyTo: mx + "$old"},
	}}})
	m = settle(t, m, cmd)

	if backend.asks == 0 {
		t.Fatal("the unloaded target was never asked for")
	}
	if _, known := m.quotes.known[mx+"$old"]; !known {
		t.Fatal("the fetched target was not filed as a quote")
	}
	// Drawn in the reply's row, not as a row of its own.
	frame := stripStyles(m.View().Content)
	if !strings.Contains(frame, "the message being answered") {
		t.Errorf("the quote is not drawn:\n%s", frame)
	}
	for i := range m.timeline.messages {
		if m.timeline.messages[i].ID == mx+"$old" {
			t.Error("the fetched target was merged into the timeline as a row")
		}
	}
}

// An unobtainable target is asked for once, not every frame.
func TestAnUnfetchableQuoteIsAskedForOnce(t *testing.T) {
	t.Parallel()

	backend := &quoteBackend{err: errors.New("not found")}
	m := quoting(t, backend)
	for range 3 {
		next, cmd := routed(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
			{ID: mx + "$reply", RoomID: mx + "!a:x", Sender: "matrix:@me:x", Body: "test", ReplyTo: mx + "$old"},
		}}})
		m = settle(t, next, cmd)
	}
	if backend.asks != 1 {
		t.Errorf("asked %d times, want once", backend.asks)
	}
	// And the row says why, not an arrow and an ellipsis.
	if frame := stripStyles(m.View().Content); !strings.Contains(frame, "quoted message") {
		t.Errorf("no placeholder for the unavailable quote:\n%s", frame)
	}
}

// A target loaded but folded into a thread needs no fetch.
func TestACollapsedTargetIsNotFetched(t *testing.T) {
	t.Parallel()

	backend := &quoteBackend{msg: domain.Message{ID: mx + "$root", Body: "should not be needed"}}
	m := quoting(t, backend)
	m, cmd := routed(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$root", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", Body: "the thread's first message"},
		{ID: mx + "$in-thread", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", Body: "a reply inside it",
			ThreadRoot: mx + "$root"},
		{ID: mx + "$answer", RoomID: mx + "!a:x", Sender: "matrix:@me:x", Body: "answering the threaded one",
			ThreadRoot: mx + "$root", ReplyTo: mx + "$in-thread"},
	}}})
	settle(t, m, cmd)

	if backend.asks != 0 {
		t.Errorf("asked the daemon %d times for a message already loaded", backend.asks)
	}
}

// Following a reply goes to the message it answers.
func TestGoingToTheMessageAReplyAnswers(t *testing.T) {
	t.Parallel()

	m := quoting(t, &quoteBackend{})
	m, cmd := routed(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$asked", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", Body: "the question"},
		{ID: mx + "$filler", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", Body: "something else"},
		{ID: mx + "$answer", RoomID: mx + "!a:x", Sender: "matrix:@me:x", Body: "the answer", ReplyTo: mx + "$asked"},
	}}})
	m = settle(t, m, cmd)
	m.timeline.selected = mx + "$answer"

	jumped, _ := press(t, m, keyCode(tea.KeyEnter))
	if jumped.timeline.selected != mx+"$asked" {
		t.Errorf("cursor on %s, want the message the reply answers", jumped.timeline.selected)
	}

	// A message that answers nothing says so, rather than silently doing nothing.
	m.timeline.selected = mx + "$filler"
	quiet, _ := press(t, m.clearStatus(), keyCode(tea.KeyEnter))
	if !strings.Contains(quiet.status(), "not a reply") {
		t.Errorf("status = %q, want it to say the message answers nothing", quiet.status())
	}
}

// r replies, enter follows the reply.
func TestReplyIsBoundToR(t *testing.T) {
	t.Parallel()

	m := quoting(t, &quoteBackend{})
	m, cmd := routed(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$one", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", Body: "hello"},
	}}})
	m = settle(t, m, cmd)
	m.timeline.selected = mx + "$one"

	replying, _ := press(t, m, keyText("r"))
	if replying.compose.replyTo != mx+"$one" || !replying.compose.insertMode {
		t.Errorf("r gave replyTo=%q insert=%v, want the reply started",
			replying.compose.replyTo, replying.compose.insertMode)
	}
	// And enter does not reply.
	notReplying, _ := press(t, m, keyCode(tea.KeyEnter))
	if notReplying.compose.replyTo != "" {
		t.Errorf("enter started a reply to %q", notReplying.compose.replyTo)
	}
}
