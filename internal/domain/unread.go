package domain

import "time"

// Unread is a room's unread state, pulled from /sync: the server-computed notification
// counts (which honor the user's push rules) plus our own read position.
type Unread struct {
	RoomID RoomID
	// Notifications and Highlights are the server's unread_notifications counts.
	Notifications int
	Highlights    int
	// ReadEvent is where our m.read receipt points; "" when unknown.
	ReadEvent EventID
	// Messages counts cached messages after the later of ReadEvent and our newest
	// message, excluding ours and redacted ones; Mentions is the subset naming us.
	Messages int
	Mentions int
	// Counted reports that the read position is known (ReadEvent is cached, or its
	// time was fetched), so Messages is meaningful.
	Counted bool
	// Threads is the per-thread breakdown of Messages.
	Threads []ThreadUnread
	// Marked is MSC2867's m.marked_unread.
	Marked bool
}

// ThreadCount is what one thread of this room has unread, and how much of it names us.
func (u Unread) ThreadCount(root EventID) (count, mentions int) {
	for i := range u.Threads {
		if u.Threads[i].Root == root {
			return u.Threads[i].Unread, u.Threads[i].Mentions
		}
	}
	return 0, 0
}

// Count is what to badge: how much is unread, and how much of that names us.
func (u Unread) Count(local bool) (count, highlights int) {
	if local && u.Counted {
		return u.Messages, u.Mentions
	}
	return u.Notifications, u.Highlights
}

// HasUnread reports whether the room has anything to badge (per Count), or is marked
// unread.
func (u Unread) HasUnread(local bool) bool {
	if u.Marked {
		return true
	}
	n, _ := u.Count(local)
	return n > 0
}

// ReadResult is the outcome of marking rooms read without opening them: receipts
// sent, rooms skipped for lack of a known event, and receipts the server refused.
type ReadResult struct {
	Marked  int
	Skipped int
	Failed  int
	// FirstError is why the first failed room failed, so a report can say more than
	// a count. Empty when nothing failed.
	FirstError string
}

// ThreadUnread is one thread's unread since our receipt for that thread.
type ThreadUnread struct {
	Root     EventID
	Unread   int // as Unread.Messages, for this thread
	Mentions int
	LatestAt time.Time // newest unread reply's time
	Latest   EventID   // newest unread reply, where a mark-read receipt points
	Title    string    // the root's text
}
