package tui

import (
	tea "charm.land/bubbletea/v2"

	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// What a message used to say, and the one thing the view has to get right: it answers
// the whole question — edits *and* the deletion — because that is one question.

// historyBackend answers with a fixed history and records what was asked about.
type historyBackend struct {
	apitest.Nop
	asked    domain.EventID
	revs     []domain.Revision
	deletion domain.Deletion
	err      error
}

func (b *historyBackend) MessageHistory(
	_ context.Context, _ domain.RoomID, event domain.EventID,
) ([]domain.Revision, domain.Deletion, error) {
	b.asked = event
	return b.revs, b.deletion, b.err
}

// browsing puts the cursor on a message in a room, with a backend that can answer.
func browsing(t *testing.T, b *historyBackend, msg domain.Message) Model {
	t.Helper()
	m := sized(t, update(t, New(context.Background(), b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}}))
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	m = m.setMessages([]domain.Message{msg})
	m.timeline.selected = msg.ID
	m.focus = paneTimeline
	return m
}

func editedThenDeleted() domain.Message {
	return domain.Message{
		ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "", Timestamp: at(1),
		Edited: true, Redacted: true, RedactedBy: "@moderator:x", RedactedReason: "off topic",
	}
}

// The whole feature in one test: three versions with their times, and the deletion
// after them.
func TestTheHistoryShowsEveryVersionAndTheDeletion(t *testing.T) {
	t.Parallel()

	b := &historyBackend{revs: []domain.Revision{
		{ID: "$m", Body: "seven", At: at(10)},
		{ID: "$e1", Body: "seven thirty", At: at(20)},
		{ID: "$e2", Body: "seven thirty, usual place", At: at(30)},
	}}
	m := browsing(t, b, editedThenDeleted())

	m, cmd := press(t, m, keyText("H"))
	if !m.history.active() {
		t.Fatal("H did not open the history")
	}
	if !m.history.loading {
		t.Error("the pane did not say it was reading — a blank one reads as no history")
	}
	m = deliver(t, m, cmd)

	if b.asked != "$m" {
		t.Errorf("asked about %q, want $m", b.asked)
	}
	pane := strings.Join(m.historyLines(70, 40), "\n")
	for _, want := range []string{"seven", "seven thirty", "usual place"} {
		if !strings.Contains(pane, want) {
			t.Errorf("the history is missing %q:\n%s", want, pane)
		}
	}
	// Edits and the deletion in one view, which is the point: the question is what
	// happened to this message, and this message had both happen to it.
	if !strings.Contains(pane, "deleted by") || !strings.Contains(pane, "off topic") {
		t.Errorf("the deletion is not on the end of the history:\n%s", pane)
	}
	if !strings.Contains(pane, "sent") || !strings.Contains(pane, "edited") {
		t.Errorf("versions are not labeled by what they were:\n%s", pane)
	}
	if got := m.historyTitle(); !strings.Contains(got, "3 versions") {
		t.Errorf("title = %q, want the count", got)
	}
}

// It takes the pane rather than expanding the row: a growing row would push the
// conversation around while you read it.
func TestTheHistoryTakesTheTimelinePane(t *testing.T) {
	t.Parallel()

	b := &historyBackend{revs: []domain.Revision{{ID: "$m", Body: "only version", At: at(10)}}}
	m := browsing(t, b, domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "only version", Timestamp: at(1)})
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)

	if pane := m.renderTimeline(70, 20); !strings.Contains(stripStyles(pane), "only version") {
		t.Errorf("the history is not in the timeline's pane:\n%s", pane)
	}
	// And gives it back.
	m, _ = press(t, m, keyText("esc"))
	if m.history.active() {
		t.Error("esc did not close the history")
	}
}

// A message with no history says which of the two reasons applies, because the user can
// do something about one of them and nothing about the other.
func TestTheHistorySaysWhyItIsEmpty(t *testing.T) {
	t.Parallel()

	plain := domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "once", Timestamp: at(1)}
	m := browsing(t, &historyBackend{}, plain)
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)
	if got := strings.Join(m.historyLines(70, 10), "\n"); !strings.Contains(got, "never been edited") {
		t.Errorf("lines = %q, want it to say the message was never edited", got)
	}

	edited := plain
	edited.Edited = true
	m2 := browsing(t, &historyBackend{}, edited)
	m2, cmd2 := press(t, m2, keyText("H"))
	m2 = deliver(t, m2, cmd2)
	if got := strings.Join(m2.historyLines(70, 10), "\n"); !strings.Contains(got, "before its versions were being kept") {
		t.Errorf("lines = %q, want it to say why an edited message has no versions", got)
	}
}

// An answer for a message the user has moved on from is dropped rather than replacing
// what they are reading now.
func TestALateAnswerForAnotherMessageIsIgnored(t *testing.T) {
	t.Parallel()

	b := &historyBackend{revs: []domain.Revision{{ID: "$m", Body: "mine", At: at(10)}}}
	m := browsing(t, b, domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "mine", Timestamp: at(1)})
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)

	next, _ := m.handleHistory(historyMsg{eventID: "$somethingelse", revisions: []domain.Revision{{ID: "$x", Body: "not this"}}})
	got := next
	if pane := strings.Join(got.historyLines(70, 10), "\n"); strings.Contains(pane, "not this") {
		t.Errorf("a stale answer replaced the open history:\n%s", pane)
	}
}

// The view owns its keyboard.
func TestTheHistoryKeysDoNotReachTheTimeline(t *testing.T) {
	t.Parallel()

	revs := make([]domain.Revision, 0, 12)
	for i := range 12 {
		revs = append(revs, domain.Revision{ID: domain.EventID("$e" + string(rune('a'+i))), Body: "version", At: at(i + 1)})
	}
	b := &historyBackend{revs: revs}
	m := browsing(t, b, domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "v", Timestamp: at(1), Edited: true})
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)

	was := m.timeline.selected
	m, _ = press(t, m, keyText("j"))
	if m.history.scroll != 1 {
		t.Errorf("scroll = %d after j, want 1", m.history.scroll)
	}
	if m.timeline.selected != was {
		t.Errorf("the message cursor moved to %q while the history was open", m.timeline.selected)
	}
	// Clamped at the top rather than scrolling into blank space above it.
	m, _ = press(t, m, keyText("k"))
	m, _ = press(t, m, keyText("k"))
	if m.history.scroll != 0 {
		t.Errorf("scroll = %d after scrolling up past the top, want 0", m.history.scroll)
	}
}

// How much each edit changed, which is what makes a long history skimmable.
func TestChangeNoteCountsWhatMoved(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ before, after, want string }{
		{"seven", "seven thirty", "+7"},
		{"seven thirty", "seven", "−7"},
		{"same", "diff", "reworded"},
	} {
		if got := changeNote(tc.before, tc.after); !strings.Contains(got, tc.want) {
			t.Errorf("changeNote(%q, %q) = %q, want it to contain %q", tc.before, tc.after, got, tc.want)
		}
	}
}

// The deletion is a row in the sequence like a version is, and it carries a time in the
// same column: a message taken back in the next breath and one removed three weeks
// later are different events, and the clock is what says which happened.
func TestTheDeletionSaysWhen(t *testing.T) {
	t.Parallel()

	deleted := at(400)
	b := &historyBackend{
		revs:     []domain.Revision{{ID: "$m", Body: "seven", At: at(10)}},
		deletion: domain.Deletion{At: deleted, By: "@moderator:x", Reason: "off topic"},
	}
	m := browsing(t, b, editedThenDeleted())
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)

	pane := stripStyles(strings.Join(m.historyLines(70, 40), "\n"))
	if !strings.Contains(pane, deleted.Local().Format("2006-01-02 15:04:05")) {
		t.Errorf("the deletion carries no time:\n%s", pane)
	}
	if !strings.Contains(pane, "deleted by") || !strings.Contains(pane, "off topic") {
		t.Errorf("the deletion lost who or why:\n%s", pane)
	}
}

// The time is the one fact that can be missing — the server has to answer for it.
func TestADeletionWithNoKnownTimeStillSaysWhatHappened(t *testing.T) {
	t.Parallel()

	m := browsing(t, &historyBackend{}, editedThenDeleted())
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)

	pane := stripStyles(strings.Join(m.historyLines(70, 40), "\n"))
	if !strings.Contains(pane, "deleted by") {
		t.Errorf("a deletion with no time lost its line entirely:\n%s", pane)
	}
	if strings.Contains(pane, "1970") {
		t.Errorf("an unknown deletion time was rendered as the epoch:\n%s", pane)
	}
}

// A redaction that arrived while this client was watching carries its own time, so the
// view can answer with no fetch at all.
func TestALiveRedactionsTimeIsUsedWhenTheFetchSaysNothing(t *testing.T) {
	t.Parallel()

	msg := editedThenDeleted()
	msg.RedactedAt = at(500)
	m := browsing(t, &historyBackend{}, msg)
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)

	pane := stripStyles(strings.Join(m.historyLines(70, 40), "\n"))
	if !strings.Contains(pane, at(500).Local().Format("2006-01-02 15:04:05")) {
		t.Errorf("the redaction's own time was not used:\n%s", pane)
	}
}

// A rebound movement key works inside the history overlay too.
func TestTheHistoryHonorsReboundKeys(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Nav.Down = "n"
	keys.Nav.SelectNewest = "E"

	m := newModel()
	m.keys = newKeymap(keys)
	m.height = 40
	m.history = historyState{open: true, msg: domain.Message{ID: "$1", RoomID: "!a:x"}}

	scrolled, _ := m.handleHistoryKey(key('n'))
	if scrolled.history.scroll != 1 {
		t.Errorf("the rebound down key scrolled to %d, want 1", scrolled.history.scroll)
	}

	// And the end key, which the literal switch never had a case for at all.
	ended, _ := m.handleHistoryKey(key('E'))
	if ended.history.scroll <= 1 {
		t.Errorf("the rebound end key scrolled to %d, want the far end", ended.history.scroll)
	}

	// esc still closes, because that is every overlay's exit and not a binding.
	closed, _ := m.handleHistoryKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if closed.history.active() {
		t.Error("esc did not close the history")
	}
}

// A Hebrew message's history reads the way the timeline draws it: every version in
// visual order and flushed to the right edge, the deletion reason and the author's name
// reordered too.
func TestTheHistoryDrawsRightToLeftTextAsTheTimelineDoes(t *testing.T) {
	t.Parallel()

	const (
		first  = "נפגשים בשבע"
		second = "נפגשים בשבע וחצי ליד הים"
		reason = "ספאם גלוי"
		author = "דנה לוי"
	)
	msg := domain.Message{
		ID: "$m", RoomID: "!a:x", Sender: "@her:x", SenderName: author, Body: second, Timestamp: at(1),
		Edited: true,
	}
	b := &historyBackend{revs: []domain.Revision{
		{ID: "$m", Body: first, At: at(10)},
		{ID: "$e1", Body: second, At: at(20)},
	}}
	m := browsing(t, b, msg)

	// The timeline's rendering of the current body is the reference.
	const width = 70
	timeline := stripStyles(strings.Join(m.messageRows(msg, width, 8, m.senderColorMap(), false, ""), "\n"))
	if !strings.Contains(timeline, hebVisual(second)) {
		t.Fatalf("precondition: the timeline does not draw %q in visual order:\n%s", second, timeline)
	}

	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)
	lines := m.historyLines(width, 40)
	pane := stripStyles(strings.Join(lines, "\n"))
	for _, body := range []string{first, second} {
		if strings.Contains(pane, body) {
			t.Errorf("the history draws %q in logical order — backwards on a terminal:\n%s", body, pane)
		}
		row, ok := historyRowContaining(lines, hebVisual(body))
		if !ok {
			t.Errorf("the history does not draw %q in visual order:\n%s", body, pane)
			continue
		}
		// Flushed right, as the timeline flushes an RTL body to the pane's edge.
		if got := strings.TrimRight(row, " "); ansi.StringWidth(got) != width-1 {
			t.Errorf("RTL version row is not flushed right (ends at column %d, want %d): %q",
				ansi.StringWidth(got), width-1, row)
		}
	}
	if title := displayTitle(m.historyTitle()); !strings.Contains(title, hebVisual(author)) {
		t.Errorf("title = %q, want the author's name in visual order %q", title, hebVisual(author))
	}

	// The deletion reason, drawn after the versions, reads the same way.
	gone := msg
	gone.Redacted, gone.RedactedBy, gone.RedactedReason = true, "@moderator:x", reason
	m2 := browsing(t, &historyBackend{revs: b.revs}, gone)
	m2, cmd2 := press(t, m2, keyText("H"))
	m2 = deliver(t, m2, cmd2)
	if end := stripStyles(m2.deletionLine()); !strings.Contains(end, hebVisual(reason)) {
		t.Errorf("deletion line = %q, want the reason in visual order %q", end, hebVisual(reason))
	}
}

// A long right-to-left version wraps in reading order: the sentence starts on the first
// row, at its right edge, the way the timeline wraps one.
func TestALongRightToLeftVersionWrapsInReadingOrder(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("שלום עולם מה נשמע היום ", 6)
	msg := domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: body, Timestamp: at(1), Edited: true}
	m := browsing(t, &historyBackend{revs: []domain.Revision{{ID: "$m", Body: body, At: at(10)}}}, msg)
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)

	lines := m.historyLines(40, 40)
	if len(lines) < 3 {
		t.Fatalf("expected the version to wrap, got %q", lines)
	}
	firstRow := strings.TrimRight(stripStyles(lines[1]), " ")
	if !strings.HasSuffix(firstRow, hebVisual("שלום")) {
		t.Errorf("the first wrapped row should end (on the right) with the first word, got %q", firstRow)
	}
}

func historyRowContaining(lines []string, want string) (string, bool) {
	for _, l := range lines {
		if s := stripStyles(l); strings.Contains(s, want) {
			return s, true
		}
	}
	return "", false
}

// The history view has no key of its own: the quit key closes it (as it closes a
// pager), and a key bound to something else does that, whatever it is.
func TestTheHistoryViewClosesOnTheQuitBindingOnly(t *testing.T) {
	t.Parallel()
	keys := config.DefaultKeys()
	keys.Quit = "ctrl+q"
	keys.Nav.Down = "q"
	m := New(context.Background(), apitest.Nop{}, config.Display{}).WithKeys(keys)
	m = sized(t, withRooms(t, m))
	m.focus = paneTimeline
	m.history = historyState{open: true}

	next, _ := press(t, m, keyText("q"))
	if !next.history.active() {
		t.Fatal("q, bound to nav.down, closed the history view")
	}
	if next.history.scroll != 1 {
		t.Errorf("q, bound to nav.down, left the history at %d, want one line down", next.history.scroll)
	}
	closed, _ := press(t, m, tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if closed.history.active() {
		t.Error("the quit binding (ctrl+q) did not close the history view")
	}
}
