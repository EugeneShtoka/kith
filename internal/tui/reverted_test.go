package tui

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// When the edit a message showed is deleted, the cache holds the message with its
// words before the edit, and sends that: it replaces the shown words, although it is
// older than the edit. A message deleted meanwhile comes as deleted, never with words.
func TestTheCachesCopyReplacesADeletedEdit(t *testing.T) {
	t.Parallel()
	edited := domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", Body: "v1 secret", Edited: true,
		Timestamp: time.UnixMilli(5000), EditedAt: time.UnixMilli(7000), RevisionID: mx + "$e1"}

	for _, tc := range []struct {
		name string
		back domain.Message
		want string
	}{
		{"an edited message goes back", domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", Body: "v0",
			Timestamp: time.UnixMilli(5000)}, "v0"},
		{"a deleted message stays empty", domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@dana:x",
			Redacted: true, Edited: true, Timestamp: time.UnixMilli(5000)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := quoting(t, &quoteBackend{})
			m, _ = routed(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{edited}}})
			m, _ = routed(t, m, incomingMsg{message: tc.back})
			if len(m.timeline.messages) != 1 {
				t.Fatalf("%d messages, want the one row", len(m.timeline.messages))
			}
			if got := m.timeline.messages[0].Body; got != tc.want {
				t.Errorf("shows %q, want %q", got, tc.want)
			}
		})
	}
}

// A reverted copy of a message this client does not hold is an ordinary row.
func TestARevertedCopyOfAnUnheldMessageIsAPlainRow(t *testing.T) {
	t.Parallel()
	got := domain.MergeMessages(nil, []domain.Message{{ID: mx + "$m", Body: "v0", Reverted: true}})
	if len(got) != 1 || got[0].Reverted || got[0].Body != "v0" {
		t.Errorf("merged %+v, want one plain row with v0", got)
	}
}
