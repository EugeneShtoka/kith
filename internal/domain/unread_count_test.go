package domain

import "testing"

// Count takes both numbers from one source: the cache when asked and able to answer,
// else the server's push-rule counts.
func TestUnreadCount(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		u     Unread
		local bool
		n, h  int
	}{
		{"muted room, local", Unread{Messages: 3, Mentions: 1, Counted: true}, true, 3, 1},
		{"muted room, server", Unread{Messages: 3, Mentions: 1, Counted: true}, false, 0, 0},
		{"uncounted falls back", Unread{Notifications: 12, Highlights: 2}, true, 12, 2},
		{"never mixes, local", Unread{Notifications: 9, Highlights: 4, Messages: 2, Counted: true}, true, 2, 0},
		{"never mixes, server", Unread{Notifications: 9, Highlights: 4, Messages: 2, Counted: true}, false, 9, 4},
	} {
		n, h := tc.u.Count(tc.local)
		if n != tc.n || h != tc.h {
			t.Errorf("%s: Count = %d, %d; want %d, %d", tc.name, n, h, tc.n, tc.h)
		}
		if got := tc.u.HasUnread(tc.local); got != (tc.n > 0) {
			t.Errorf("%s: HasUnread = %v, disagrees with Count", tc.name, got)
		}
	}
	if !(Unread{Marked: true}).HasUnread(true) {
		t.Error("a room marked unread reported nothing unread")
	}
}
