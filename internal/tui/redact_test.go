package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// redactBackend records what was asked to be deleted, and can refuse.
type redactBackend struct {
	apitest.Nop
	room domain.RoomID
	dead domain.EventID
	err  error
}

func (b *redactBackend) Redact(_ context.Context, room domain.RoomID, event domain.EventID, _ string) error {
	b.room, b.dead = room, event
	return b.err
}

// deleting opens a room holding one of our messages and one of somebody else's.
func deleting(t *testing.T, backend *redactBackend) Model {
	t.Helper()
	m := update(t, starterNew(backend, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
	}})
	m = sized(t, m)
	m.me = "@me:x"
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$theirs", RoomID: "!a:x", Sender: "@dana:x", Body: "hers", Timestamp: at(1)},
		{ID: "$mine", RoomID: "!a:x", Sender: "@me:x", Body: "the wrong room", Timestamp: at(2)},
	}}})
	m.focus, m.compose.insertMode = paneTimeline, false
	return m.clearStatus()
}

// Deleting your own message asks, folds the row at once, then redacts.
func TestRedactingYourOwnMessage(t *testing.T) {
	t.Parallel()

	backend := &redactBackend{}
	m := deleting(t, backend)
	m, _ = press(t, m, keyText("x"))

	if !m.confirm.active() {
		t.Fatal("x did not ask")
	}
	if got := m.confirmPrompt(); !strings.Contains(got, "your message") {
		t.Errorf("prompt = %q, want it to say the message is yours", got)
	}

	m, cmd := press(t, m, keyText("y"))
	if idx := indexIn(m.timeline.messages, "$mine"); idx < 0 || !m.timeline.messages[idx].Redacted {
		t.Errorf("the row was not folded: %+v", m.timeline.messages)
	}
	deliver(t, m, cmd)
	if backend.dead != "$mine" || backend.room != "!a:x" {
		t.Errorf("asked to delete %q in %q, want $mine in !a:x", backend.dead, backend.room)
	}
}

// Somebody else's message gets a different question.
func TestRedactingSomebodyElsesAsksDifferently(t *testing.T) {
	t.Parallel()

	m := deleting(t, &redactBackend{})
	m.timeline.selected = "$theirs"
	if m.selectedID() != "$theirs" {
		t.Fatalf("the cursor is on %q, want somebody else's message", m.selectedID())
	}
	m, _ = press(t, m, keyText("x"))
	if got := m.confirmPrompt(); !strings.Contains(got, "somebody else") {
		t.Errorf("prompt = %q, want it to say the message is not yours", got)
	}
}

func TestDecliningLeavesTheMessage(t *testing.T) {
	t.Parallel()

	backend := &redactBackend{}
	m := deleting(t, backend)
	m, _ = press(t, m, keyText("x"))
	m, cmd := press(t, m, keyText("n"))
	deliver(t, m, cmd)

	if backend.dead != "" {
		t.Errorf("deleted %q after being told no", backend.dead)
	}
	if idx := indexIn(m.timeline.messages, "$mine"); idx >= 0 && m.timeline.messages[idx].Redacted {
		t.Error("the row was folded anyway")
	}
}

// A refused deletion is said out loud (and the row reloaded).
func TestAFailedDeletionRestoresTheRow(t *testing.T) {
	t.Parallel()

	backend := &redactBackend{err: context.DeadlineExceeded}
	m := deleting(t, backend)
	m, _ = press(t, m, keyText("x"))
	m, cmd := press(t, m, keyText("y"))
	m = deliver(t, m, cmd)

	if !strings.Contains(m.status(), "could not delete") {
		t.Errorf("status = %q, want the failure said out loud", m.status())
	}
}

func TestRedactingADeletedMessageSaysSo(t *testing.T) {
	t.Parallel()

	m := deleting(t, &redactBackend{})
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$mine", RoomID: "!a:x", Redacted: true},
	}}})
	m, _ = press(t, m, keyText("x"))

	if m.confirm.active() {
		t.Error("it asked about a message that is already gone")
	}
	if !strings.Contains(m.status(), "already deleted") {
		t.Errorf("status = %q", m.status())
	}
}

// editing opens a room holding one message of ours, behind a sending backend.
func editing(t *testing.T, body string) (Model, *sendingBackend) {
	t.Helper()
	backend := &sendingBackend{}
	m := update(t, starterNew(backend, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	m.me = "@me:x"
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$mine", RoomID: "!a:x", Sender: "@me:x", Body: body, Timestamp: at(1)},
	}}})
	m.focus, m.compose.insertMode = paneTimeline, false
	return m, backend
}

// Editing puts the message's text in the composer and enter sends a replacement.
func TestEditingYourOwnMessage(t *testing.T) {
	t.Parallel()

	m, backend := editing(t, "half a thought")
	m = m.store(fieldComposer, newEditor("a different thought").end())

	m, _ = press(t, m, keyText("E"))
	if !m.compose.isEditing() {
		t.Fatal("E did not start an edit")
	}
	if got := m.editorFor(fieldComposer).text; got != "half a thought" {
		t.Errorf("composer = %q, want the message's own text", got)
	}
	if _, prompt := m.composerPrefix(); !strings.Contains(stripStyles(prompt), "editing") {
		t.Errorf("the gutter says %q, want it to say an edit is in progress", stripStyles(prompt))
	}

	m = m.store(fieldComposer, newEditor("the whole thought").end())
	m, cmd := press(t, m, sendKey())
	idx := indexIn(m.timeline.messages, "$mine")
	if idx < 0 || m.timeline.messages[idx].Body != "the whole thought" || !m.timeline.messages[idx].Edited {
		t.Errorf("the row was not folded: %+v", m.timeline.messages)
	}
	deliver(t, m, cmd)

	if len(backend.sent) != 1 {
		t.Fatalf("sent %+v, want one revision", backend.sent)
	}
	if backend.sent[0].Edits != "$mine" || backend.sent[0].Body != "the whole thought" {
		t.Errorf("sent %+v, want a replacement of $mine", backend.sent[0])
	}
	if m.compose.isEditing() {
		t.Error("the edit is still in progress after sending it")
	}
}

// esc abandons the edit and gives back the draft.
func TestCancellingAnEditRestoresTheDraft(t *testing.T) {
	t.Parallel()

	m := deleting(t, &redactBackend{})
	m = m.store(fieldComposer, newEditor("a different thought").end())
	m.timeline.selected = "$mine"
	m, _ = press(t, m, keyText("E"))
	if !m.compose.isEditing() {
		t.Fatal("E did not start an edit")
	}
	m, _ = press(t, m, keyCode(27)) // esc

	if m.compose.isEditing() {
		t.Error("esc did not abandon the edit")
	}
	if got := m.editorFor(fieldComposer).text; got != "a different thought" {
		t.Errorf("composer = %q, want the draft back", got)
	}
}

func TestEditingSomebodyElsesIsRefused(t *testing.T) {
	t.Parallel()

	m := deleting(t, &redactBackend{})
	m.timeline.selected = "$theirs"
	m, _ = press(t, m, keyText("E"))

	if m.compose.isEditing() {
		t.Error("it started editing somebody else's message")
	}
	if !strings.Contains(m.status(), "your own") {
		t.Errorf("status = %q", m.status())
	}
}

// An empty edit is refused, not turned into a deletion.
func TestAnEmptyEditIsRefused(t *testing.T) {
	t.Parallel()

	m, backend := editing(t, "something")
	m, _ = press(t, m, keyText("E"))
	m = m.store(fieldComposer, newEditor("").end())
	m, cmd := press(t, m, sendKey())
	deliver(t, m, cmd)

	if len(backend.sent) != 0 {
		t.Errorf("sent %+v, want nothing", backend.sent)
	}
	if !strings.Contains(m.status(), "would delete") {
		t.Errorf("status = %q, want it to point at the delete key", m.status())
	}
	if !m.compose.isEditing() {
		t.Error("the edit was abandoned rather than refused")
	}
}

// A deletion whose edit history could not all be removed still counts as deleted:
// the message is gone, so the row must not come back.
func TestAPartlyRemovedHistoryStillCountsAsDeleted(t *testing.T) {
	t.Parallel()

	backend := &redactBackend{err: fmt.Errorf("%w: 2 of 3 could not be removed", api.ErrEditsRemain)}
	m := deleting(t, backend)
	m, _ = press(t, m, keyText("x"))
	m, cmd := press(t, m, keyText("y"))
	m = deliver(t, m, cmd)

	if strings.Contains(m.status(), "could not delete") {
		t.Errorf("status = %q — that reads as the deletion having failed", m.status())
	}
	if !strings.Contains(m.status(), "deleted") || !strings.Contains(m.status(), "could not be removed") {
		t.Errorf("status = %q, want it to say deleted *and* what is left", m.status())
	}
}
