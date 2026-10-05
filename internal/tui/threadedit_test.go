package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Editing a formatted message in an open thread shows the new words at once, both from
// the optimistic fold and when the edit comes back from the homeserver.
func TestAnEditInAThreadShowsTheNewTextAtOnce(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	m := benchTimelineModel(0)
	m = m.setMessages([]domain.Message{
		{ID: "$root", RoomID: m.openRoom, Sender: benchSenders[1], Body: "the plan", Timestamp: at},
		{ID: "$mine", RoomID: m.openRoom, Sender: m.selves[0], ThreadRoot: "$root", Timestamp: at.Add(time.Minute),
			Body: "see you at seven", Format: richtext.FromMarkup("see you at <b>seven</b>")},
	})
	m.thread = threadState{root: "$root"}
	m = settled(m)
	if !strings.Contains(stripStyles(m.View().Content), "see you at seven") {
		t.Fatal("the thread does not show the message to begin with")
	}

	room, _ := m.currentRoom()
	m.compose.editing = "$mine"
	next, _ := m.submitEdit(room, "see you at eight")
	m, _ = asModel(next.Update(settleMsg{}))
	frame := stripStyles(m.View().Content)
	if !strings.Contains(frame, "see you at eight") || strings.Contains(frame, "seven") {
		t.Fatalf("after the edit the thread shows:\n%s", frame)
	}

	// The homeserver's copy of the edit, formatted again.
	m, _ = asModel(m.Update(incomingMsg{message: domain.Message{
		ID: "$mine", RoomID: m.openRoom, Sender: m.selves[0], Body: "see you at nine",
		Format: richtext.FromMarkup("see you at <i>nine</i>"), Edited: true, RevisionID: "$e2",
	}}))
	frame = stripStyles(m.View().Content)
	if !strings.Contains(frame, "see you at nine") || strings.Contains(frame, "eight") {
		t.Fatalf("after the second edit the thread shows:\n%s", frame)
	}
}
