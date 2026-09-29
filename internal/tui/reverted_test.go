package tui

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The daemon sends a message back when the edit it showed was deleted. That copy
// replaces the shown words, although it is older than the edit; and it never brings
// words back to a message deleted meanwhile.
func TestARevertedCopyReplacesTheDeletedEdit(t *testing.T) {
	t.Parallel()
	edited := domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@dana:x", Body: "v1 secret", Edited: true,
		Timestamp: time.UnixMilli(5000), EditedAt: time.UnixMilli(7000), RevisionID: "$e1"}
	back := domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@dana:x", Body: "v0",
		Timestamp: time.UnixMilli(5000), Reverted: true}

	for _, tc := range []struct {
		name  string
		shown domain.Message
		want  string
	}{
		{"an edited message goes back", edited, "v0"},
		{"a deleted message stays empty", domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@dana:x",
			Redacted: true, Timestamp: time.UnixMilli(5000)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := quoting(t, &quoteBackend{})
			m, _ = routed(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{tc.shown}}})
			m, _ = routed(t, m, incomingMsg{message: back})
			if len(m.timeline.messages) != 1 {
				t.Fatalf("%d messages, want the one row", len(m.timeline.messages))
			}
			got := m.timeline.messages[0]
			if got.Body != tc.want {
				t.Errorf("shows %q, want %q", got.Body, tc.want)
			}
			if got.Reverted {
				t.Error("the marker stuck to the row; it is only an instruction to replace")
			}
		})
	}
}

// A reverted copy of a message this client does not hold is an ordinary row.
func TestARevertedCopyOfAnUnheldMessageIsAPlainRow(t *testing.T) {
	t.Parallel()
	got := domain.MergeMessages(nil, []domain.Message{{ID: "$m", Body: "v0", Reverted: true}})
	if len(got) != 1 || got[0].Reverted || got[0].Body != "v0" {
		t.Errorf("merged %+v, want one plain row with v0", got)
	}
}
