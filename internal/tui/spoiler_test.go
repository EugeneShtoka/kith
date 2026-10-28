package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

func spoilerMsg() domain.Message {
	return domain.Message{
		ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@her:x",
		Body:   "he dies at the end",
		Format: richtext.FromMarkup(`he dies at the <span data-mx-spoiler>end</span>`),
	}
}

// Covered words keep their width but are drawn unreadable.
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

	m.timeline.revealed = map[domain.EventID]bool{mx + "$1": true}
	open := strings.Join(m.messageRows(msg, 80, 12, nil, false, ""), "\n")
	if open == covered {
		t.Error("revealing changed nothing about how the row is drawn")
	}
}

// The key toggles one message; one with nothing hidden is not revealed.
func TestRevealingTogglesOneMessage(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = m.setMessages([]domain.Message{spoilerMsg(), {ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "plain"}})
	m.timeline.selected = mx + "$1"

	m, _ = m.toggleReveal()
	if !m.timeline.revealed[mx+"$1"] {
		t.Fatal("the message under the cursor was not revealed")
	}
	if m.timeline.revealed[mx+"$2"] {
		t.Error("revealing one message revealed another")
	}
	m, _ = m.toggleReveal()
	if m.timeline.revealed[mx+"$1"] {
		t.Error("pressing it again did not cover it back")
	}
	m.timeline.selected = mx + "$2"
	before := m.status()
	m, _ = m.toggleReveal()
	if m.timeline.revealed[mx+"$2"] {
		t.Error("a message with nothing covered was marked revealed")
	}
	if m.status() != before {
		t.Errorf("status changed to %q on a message with nothing hidden", m.status())
	}
}

// A kept deletion ([display.deleted] keep) is uncovered by the same key.
func TestAKeptDeletionIsUncoveredByTheSameKey(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	kept := domain.Message{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@her:x",
		Body: "what she said", Redacted: true, RedactedBy: "matrix:@her:x"}

	body, _, _ := m.messageBody(kept, nil)
	if body != redactedBody {
		t.Fatalf("a kept deletion drew %q before it was asked for", body)
	}
	m.timeline.revealed = map[domain.EventID]bool{mx + "$1": true}
	body, _, _ = m.messageBody(kept, nil)
	if !strings.Contains(body, "what she said") || !strings.Contains(body, redactedBody) {
		t.Errorf("revealed, it drew %q — want the placeholder and the words", body)
	}
}

func TestAnErasedDeletionSaysThereIsNothingToUncover(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	erased := domain.Message{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Redacted: true}
	m = m.setMessages([]domain.Message{erased})
	m.timeline.selected = mx + "$1"

	m, _ = m.toggleReveal()
	if m.status() == "" {
		t.Errorf("status = %q, want it to say the words were not kept", m.status())
	}
	if m.timeline.revealed[mx+"$1"] {
		t.Error("a deletion with nothing kept was marked revealed")
	}
}
