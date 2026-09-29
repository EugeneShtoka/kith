package tui

import (
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// deletedRoom is a timeline with one deletion of yours, one of somebody else's, and a
// message from each that is still there.
func deletedRoom(t *testing.T, deleted config.Deleted) Model {
	t.Helper()
	m := sized(t, newModel())
	m.prefs.display.Deleted = deleted
	m.me = "@me:x"
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	minute := func(i int) time.Time { return time.Unix(1700000000+int64(i)*60, 0) }
	return m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@me:x", Body: "mine, still here", Timestamp: minute(1)},
		{ID: "$2", RoomID: "!a:x", Sender: "@me:x", Redacted: true, Timestamp: minute(2)},
		{ID: "$3", RoomID: "!a:x", Sender: "@her:x", Body: "hers, still here", Timestamp: minute(3)},
		{ID: "$4", RoomID: "!a:x", Sender: "@her:x", Redacted: true, Timestamp: minute(4)},
	})
}

// shownIDs is what the pane would draw, in order.
func shownIDs(m Model) []domain.EventID {
	msgs := m.shownMessages()
	out := make([]domain.EventID, 0, len(msgs))
	for i := range msgs {
		out = append(out, msgs[i].ID)
	}
	return out
}

// Yours and others' deletions are hidden separately.
func TestDeletionsAreHiddenSeparately(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.Deleted
		want []domain.EventID
	}{
		{"both shown, the default", config.Deleted{}, []domain.EventID{"$1", "$2", "$3", "$4"}},
		{"mine hidden", config.Deleted{Mine: config.DeletedHide},
			[]domain.EventID{"$1", "$3", "$4"}},
		{"theirs hidden", config.Deleted{Others: config.DeletedHide},
			[]domain.EventID{"$1", "$2", "$3"}},
		{"both hidden", config.Deleted{Mine: config.DeletedHide, Others: config.DeletedHide},
			[]domain.EventID{"$1", "$3"}},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			m := deletedRoom(t, c.cfg)
			if got := shownIDs(m); !slices.Equal(got, c.want) {
				t.Fatalf("drew %v, want %v", got, c.want)
			}
		})
	}
}

// A deletion from an account grouped with yours by identity counts as yours.
func TestAMessageFromYourOtherAccountCountsAsYours(t *testing.T) {
	t.Parallel()

	m := deletedRoom(t, config.Deleted{Mine: config.DeletedHide})
	m.prefs.identities = map[string]resolvedIdentity{
		"@me:x":       {alias: "Eugene"},
		"@phone:x":    {alias: "Eugene"},
		"@stranger:x": {alias: "Somebody"},
	}
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@phone:x", Redacted: true, Timestamp: time.Unix(1700000060, 0)},
		{ID: "$2", RoomID: "!a:x", Sender: "@stranger:x", Redacted: true, Timestamp: time.Unix(1700000120, 0)},
	})
	got := shownIDs(m)
	if len(got) != 1 || got[0] != "$2" {
		t.Errorf("drew %v, want only the stranger's deletion — the bridged one is yours", got)
	}
}

// The deleted placeholder names who removed it, and why, when not the sender.
func TestTheDeletedPlaceholderNamesWhoAndWhy(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	tests := []struct {
		name string
		msg  domain.Message
		want string
	}{
		{
			name: "taken back by its own sender",
			msg:  domain.Message{Sender: "@her:x", Redacted: true, RedactedBy: "@her:x"},
			want: "(deleted)",
		},
		{
			name: "remover unknown",
			msg:  domain.Message{Sender: "@her:x", Redacted: true},
			want: "(deleted)",
		},
		{
			name: "removed by somebody else",
			msg:  domain.Message{Sender: "@her:x", Redacted: true, RedactedBy: "@mod:x"},
			want: "(deleted by mod)",
		},
		{
			name: "removed with a reason",
			msg:  domain.Message{Sender: "@her:x", Redacted: true, RedactedBy: "@mod:x", RedactedReason: "spam"},
			want: "(deleted by mod — spam)",
		},
		{
			name: "a reason that is several lines is one line here",
			msg:  domain.Message{Sender: "@her:x", Redacted: true, RedactedBy: "@mod:x", RedactedReason: "spam\nand worse"},
			want: "(deleted by mod — spam and worse)",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := m.deletedBody(c.msg); got != c.want {
				t.Errorf("drew %q, want %q", got, c.want)
			}
		})
	}
}
