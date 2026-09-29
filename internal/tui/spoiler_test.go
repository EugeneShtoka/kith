package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// spoilerMsg is a message with a covered run in it.
func spoilerMsg() domain.Message {
	return domain.Message{
		ID: "$1", RoomID: "!a:x", Sender: "@her:x",
		Body: "he dies at the end",
		HTML: `he dies at the <span data-mx-spoiler>end</span>`,
	}
}

// Covered, the words keep their width but are drawn unreadable; revealed, readable.
func TestASpoilerIsCoveredUntilItIsAskedFor(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	msg := spoilerMsg()

	covered := strings.Join(m.messageRows(msg, 80, 12, nil, false, ""), "\n")
	if !strings.Contains(stripStyles(covered), "he dies at the end") {
		t.Fatalf("the row lost the text it is hiding:\n  %q", stripStyles(covered))
	}
	if !strings.Contains(covered, "\x1b[") {
		t.Errorf("nothing was styled, so nothing was covered:\n  %q", covered)
	}

	m.timeline.revealed = map[domain.EventID]bool{"$1": true}
	open := strings.Join(m.messageRows(msg, 80, 12, nil, false, ""), "\n")
	if open == covered {
		t.Error("revealing changed nothing about how the row is drawn")
	}
}

// The key toggles, and toggles one message; one with nothing hidden is not revealed.
func TestRevealingTogglesOneMessage(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = m.setMessages([]domain.Message{spoilerMsg(), {ID: "$2", RoomID: "!a:x", Sender: "@her:x", Body: "plain"}})
	m.timeline.selected = "$1"

	m, _ = m.toggleReveal()
	if !m.timeline.revealed["$1"] {
		t.Fatal("the message under the cursor was not revealed")
	}
	if m.timeline.revealed["$2"] {
		t.Error("revealing one message revealed another")
	}
	m, _ = m.toggleReveal()
	if m.timeline.revealed["$1"] {
		t.Error("pressing it again did not cover it back")
	}
	m.timeline.selected = "$2"
	before := m.status()
	m, _ = m.toggleReveal()
	if m.timeline.revealed["$2"] {
		t.Error("a message with nothing covered was marked revealed")
	}
	if m.status() != before {
		t.Errorf("status changed to %q on a message with nothing hidden", m.status())
	}
}

// A kept deletion ([display.deleted] keep) is uncovered by the same key, behind the
// placeholder.
func TestAKeptDeletionIsUncoveredByTheSameKey(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	kept := domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@her:x",
		Body: "what she said", Redacted: true, RedactedBy: "@her:x"}

	body, _, _ := m.messageBody(kept, nil)
	if body != redactedBody {
		t.Fatalf("a kept deletion drew %q before it was asked for", body)
	}
	m.timeline.revealed = map[domain.EventID]bool{"$1": true}
	body, _, _ = m.messageBody(kept, nil)
	if !strings.Contains(body, "what she said") || !strings.Contains(body, redactedBody) {
		t.Errorf("revealed, it drew %q — want the placeholder and the words", body)
	}
}

// A deletion with nothing kept says so rather than silently doing nothing.
func TestAnErasedDeletionSaysThereIsNothingToUncover(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	erased := domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@her:x", Redacted: true}
	m = m.setMessages([]domain.Message{erased})
	m.timeline.selected = "$1"

	m, _ = m.toggleReveal()
	if !strings.Contains(m.status(), "nothing was kept") {
		t.Errorf("status = %q, want it to say the words were not kept", m.status())
	}
	if m.timeline.revealed["$1"] {
		t.Error("a deletion with nothing kept was marked revealed")
	}
}
