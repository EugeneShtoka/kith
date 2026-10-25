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
	m := sized(t, update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}}))
	next, _ := m.selectRoom(domain.Room{ID: mx + "!a:x", Name: "Alpha"})
	m = next
	m = m.setMessages([]domain.Message{msg})
	m.timeline.selected = msg.ID
	m.focus = paneTimeline
	return m
}

func editedThenDeleted() domain.Message {
	return domain.Message{
		ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "", Timestamp: at(1),
		Edited: true, Redacted: true, RedactedBy: "matrix:@moderator:x", RedactedReason: "off topic",
	}
}

// Three versions with their times, then the deletion.
func TestTheHistoryShowsEveryVersionAndTheDeletion(t *testing.T) {
	t.Parallel()

	b := &historyBackend{revs: []domain.Revision{
		{ID: mx + "$m", Body: "seven", At: at(10)},
		{ID: mx + "$e1", Body: "seven thirty", At: at(20)},
		{ID: mx + "$e2", Body: "seven thirty, usual place", At: at(30)},
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

	if b.asked != mx+"$m" {
		t.Errorf("asked about %q, want $m", b.asked)
	}
	pane := strings.Join(m.historyLines(70, 40), "\n")
	for _, want := range []string{"seven", "seven thirty", "usual place"} {
		if !strings.Contains(pane, want) {
			t.Errorf("the history is missing %q:\n%s", want, pane)
		}
	}
	// Edits and the deletion in one view.
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

// It takes the pane rather than expanding the row, which would push the conversation around.
func TestTheHistoryTakesTheTimelinePane(t *testing.T) {
	t.Parallel()

	b := &historyBackend{revs: []domain.Revision{{ID: mx + "$m", Body: "only version", At: at(10)}}}
	m := browsing(t, b, domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "only version", Timestamp: at(1)})
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

// An empty history says which of the two reasons applies.
func TestTheHistorySaysWhyItIsEmpty(t *testing.T) {
	t.Parallel()

	plain := domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "once", Timestamp: at(1)}
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

// An answer for a message the user has moved on from is dropped.
func TestALateAnswerForAnotherMessageIsIgnored(t *testing.T) {
	t.Parallel()

	b := &historyBackend{revs: []domain.Revision{{ID: mx + "$m", Body: "mine", At: at(10)}}}
	m := browsing(t, b, domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "mine", Timestamp: at(1)})
	m, cmd := press(t, m, keyText("H"))
	m = deliver(t, m, cmd)

	next, _ := m.handleHistory(historyMsg{eventID: mx + "$somethingelse", revisions: []domain.Revision{{ID: mx + "$x", Body: "not this"}}})
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
		revs = append(revs, domain.Revision{ID: domain.EventID(mx + "$e" + string(rune('a'+i))), Body: "version", At: at(i + 1)})
	}
	b := &historyBackend{revs: revs}
	m := browsing(t, b, domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "v", Timestamp: at(1), Edited: true})
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
	// Clamped at the top.
	m, _ = press(t, m, keyText("k"))
	m, _ = press(t, m, keyText("k"))
	if m.history.scroll != 0 {
		t.Errorf("scroll = %d after scrolling up past the top, want 0", m.history.scroll)
	}
}

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

// The deletion is a row with a time in the same column as versions: an immediate retraction
// and a removal weeks later are different events.
func TestTheDeletionSaysWhen(t *testing.T) {
	t.Parallel()

	deleted := at(400)
	b := &historyBackend{
		revs:     []domain.Revision{{ID: mx + "$m", Body: "seven", At: at(10)}},
		deletion: domain.Deletion{At: deleted, By: "matrix:@moderator:x", Reason: "off topic"},
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

// The time can be missing; the server has to answer for it.
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

// A redaction seen live carries its own time, so no fetch is needed.
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
	keys.Bindings["nav.down"] = "n"
	keys.Bindings["nav.select_newest"] = "E"

	m := newModel()
	m.keys = newKeymap(keys)
	m.height = 40
	m.history = historyState{open: true, msg: domain.Message{ID: mx + "$1", RoomID: mx + "!a:x"}}

	scrolled, _ := m.handleHistoryKey(key('n'))
	if scrolled.history.scroll != 1 {
		t.Errorf("the rebound down key scrolled to %d, want 1", scrolled.history.scroll)
	}

	// And the end key.
	ended, _ := m.handleHistoryKey(key('E'))
	if ended.history.scroll <= 1 {
		t.Errorf("the rebound end key scrolled to %d, want the far end", ended.history.scroll)
	}

	// esc still closes: it is every overlay's exit, not a binding.
	closed, _ := m.handleHistoryKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if closed.history.active() {
		t.Error("esc did not close the history")
	}
}

// A Hebrew message's history reads as the timeline draws it: every version in visual order,
// flushed right, with the deletion reason and author reordered too.
func TestTheHistoryDrawsRightToLeftTextAsTheTimelineDoes(t *testing.T) {
	t.Parallel()

	const (
		first  = "נפגשים בשבע"
		second = "נפגשים בשבע וחצי ליד הים"
		reason = "ספאם גלוי"
		author = "דנה לוי"
	)
	msg := domain.Message{
		ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", SenderName: author, Body: second, Timestamp: at(1),
		Edited: true,
	}
	b := &historyBackend{revs: []domain.Revision{
		{ID: mx + "$m", Body: first, At: at(10)},
		{ID: mx + "$e1", Body: second, At: at(20)},
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
	gone.Redacted, gone.RedactedBy, gone.RedactedReason = true, "matrix:@moderator:x", reason
	m2 := browsing(t, &historyBackend{revs: b.revs}, gone)
	m2, cmd2 := press(t, m2, keyText("H"))
	m2 = deliver(t, m2, cmd2)
	if end := stripStyles(m2.deletionLine()); !strings.Contains(end, hebVisual(reason)) {
		t.Errorf("deletion line = %q, want the reason in visual order %q", end, hebVisual(reason))
	}
}

// A long RTL version wraps in reading order: the sentence starts on the first row at its right
// edge.
func TestALongRightToLeftVersionWrapsInReadingOrder(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("שלום עולם מה נשמע היום ", 6)
	msg := domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: body, Timestamp: at(1), Edited: true}
	m := browsing(t, &historyBackend{revs: []domain.Revision{{ID: mx + "$m", Body: body, At: at(10)}}}, msg)
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

// The history view has no close key of its own: the quit binding closes it (as a pager), and
// any other bound key does not.
func TestTheHistoryViewClosesOnTheQuitBindingOnly(t *testing.T) {
	t.Parallel()
	keys := config.DefaultKeys()
	keys.Bindings["quit"] = "ctrl+q"
	keys.Bindings["nav.down"] = "q"
	m := starterNew(apitest.Nop{}, config.Display{}).WithKeys(keys)
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
