package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A row's cache entry is keyed by everything it draws. Each case changes one input
// of a message the cursor is not on (only the selected row is drawn fresh), and the
// frame must match one drawn from an empty cache.
func TestTheRowCacheFollowsEveryRowInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		msgs   []domain.Message
		change func(t *testing.T, m Model) Model
	}{
		{
			name: "a star set arrives",
			msgs: []domain.Message{{ID: "$one", Body: "first"}, {ID: "$two", Body: "second"}},
			change: func(t *testing.T, m Model) Model {
				m, _ = routed(t, m, starredInMsg{roomID: "!a:x", ids: []domain.EventID{"$one"}})
				return m
			},
		},
		{
			name: "a star is toggled, then the cursor moves on",
			msgs: []domain.Message{{ID: "$one", Body: "first"}, {ID: "$two", Body: "second"}},
			change: func(t *testing.T, m Model) Model {
				m.timeline.selected = "$one"
				m, _ = settled(m).toggleStar()
				m = settled(m)
				m.timeline.selected = "$two"
				return m
			},
		},
		{
			name: "a deleted message is revealed, then the cursor moves on",
			msgs: []domain.Message{{ID: "$one", Body: "secret words", Redacted: true}, {ID: "$two", Body: "second"}},
			change: func(t *testing.T, m Model) Model {
				m.timeline.selected = "$one"
				m, _ = settled(m).toggleReveal()
				if !m.timeline.revealed["$one"] {
					t.Fatal("nothing was revealed; the case proves nothing")
				}
				m = settled(m)
				m.timeline.selected = "$two"
				return m
			},
		},
		{
			name: "a quoted message is fetched",
			msgs: []domain.Message{{ID: "$one", Body: "test", ReplyTo: "$old"}, {ID: "$two", Body: "later"}},
			change: func(t *testing.T, m Model) Model {
				m, _ = routed(t, m, fetchedQuoteMsg{roomID: "!a:x", message: domain.Message{
					ID: "$old", RoomID: "!a:x", Sender: "@dana:x", SenderName: "Dana", Body: "the message being answered"}})
				return m
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for i := range tc.msgs {
				tc.msgs[i].RoomID, tc.msgs[i].Sender = "!a:x", "@me:x"
			}
			m := quoting(t, &quoteBackend{})
			m, _ = routed(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: tc.msgs}})
			m.timeline.selected = "$two"
			m = settled(m)
			_ = m.View() // the first frame fills the cache

			m = settled(tc.change(t, m))
			if err := staleRow(m); err != nil {
				t.Fatal(err)
			}
			cached := stripStyles(m.View().Content)
			m.derived.valid = false
			if fresh := stripStyles(settled(m).View().Content); cached != fresh {
				t.Errorf("the frame drawn from the cache differs from a fresh one:\ncached:\n%s\nfresh:\n%s", cached, fresh)
			}
		})
	}
}
