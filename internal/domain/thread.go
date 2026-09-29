package domain

import (
	"sort"
	"time"
)

// Thread is one thread's summary: enough to draw the row that stands in for a
// conversation in the main timeline, and — once thread receipts exist — to badge it.
type Thread struct {
	Root   EventID
	RoomID RoomID
	// Count is the number of replies, excluding the root.
	Count int
	// Latest is the newest reply, with its sender both as an MXID (for color) and as
	// the name the timeline resolved for it (so the summary row can name a person
	// without a second lookup).
	Latest           EventID
	LatestAt         time.Time
	LatestSender     string
	LatestSenderName string
	// Anchor is the main-timeline message the summary row is drawn beneath: the root
	// itself when it is loaded, and otherwise the newest main-timeline message before
	// the thread's newest reply — the place in the conversation where the thread was
	// last spoken in.
	Anchor EventID
	// RootLoaded is whether Root is among the messages this summary was built from.
	RootLoaded bool
	// Title is the root's text, for naming the conversation where the root itself is
	// not on screen — a row in the room list, a line in the picker.
	Title string
	// Unread and Mentions are what has been said here since our receipt for this thread
	// — the thread's own position, not the room's.
	Unread   int
	Mentions int
}

// WithUnread stamps each thread with what the room's unread state says about it, which
// is what turns a summary row into a badged one.
func WithUnread(threads []Thread, u Unread) []Thread {
	for i := range threads {
		threads[i].Unread, threads[i].Mentions = u.ThreadCount(threads[i].Root)
	}
	return threads
}

// Conversations maps each loaded message onto the thread it belongs to: the root of the
// conversation it is part of, and nothing for a message that is in none.
func Conversations(msgs []Message) map[EventID]EventID {
	roots := make(map[EventID]bool)
	for i := range msgs {
		if msgs[i].ThreadRoot != "" {
			roots[msgs[i].ThreadRoot] = true
		}
	}
	if len(roots) == 0 {
		return nil
	}
	in := make(map[EventID]EventID, len(roots)*4)
	for i := range msgs {
		msg := msgs[i]
		switch {
		case msg.ThreadRoot != "":
			in[msg.ID] = msg.ThreadRoot
		case roots[msg.ID]:
			in[msg.ID] = msg.ID
		case msg.ReplyTo != "":
			if root, ok := in[msg.ReplyTo]; ok {
				in[msg.ID] = root
			}
		}
	}
	return in
}

// ThreadRootOf is the conversation one message belongs to, by the reading above: its
// thread's root, itself when it is a root something has answered, and "" when it is in
// none.
func ThreadRootOf(msgs []Message, id EventID) EventID {
	if id == "" {
		return ""
	}
	return Conversations(msgs)[id]
}

// CollapseThreads splits a room's messages — oldest→newest, as MergeMessages leaves
// them — into what the main timeline draws and one summary per thread.
func CollapseThreads(msgs []Message) ([]Message, []Thread) {
	in := Conversations(msgs)
	main := make([]Message, 0, len(msgs))
	byRoot := make(map[EventID]*Thread)
	order := make([]EventID, 0)
	for i := range msgs {
		msg := msgs[i]
		root := in[msg.ID]
		// A root belongs to its own conversation and is still drawn here: the summary
		// row hangs off it, which is the whole shape of a collapsed thread.
		if root == "" || root == msg.ID {
			main = append(main, msg)
			continue
		}
		t, ok := byRoot[root]
		if !ok {
			t = &Thread{Root: root, RoomID: msg.RoomID}
			byRoot[root] = t
			order = append(order, root)
		}
		t.Count++
		// The input is ordered, so the last reply seen is the newest one; the anchor
		// follows it, which keeps an orphaned thread's row where the conversation
		// actually last moved.
		t.Latest, t.LatestAt = msg.ID, msg.Timestamp
		t.LatestSender, t.LatestSenderName = msg.Sender, msg.SenderName
		if len(main) > 0 {
			t.Anchor = main[len(main)-1].ID
		} else {
			t.Anchor = ""
		}
	}
	if len(byRoot) == 0 {
		return main, nil
	}
	loaded := make(map[EventID]bool, len(main))
	for i := range main {
		loaded[main[i].ID] = true
	}
	threads := make([]Thread, 0, len(byRoot))
	for _, root := range order {
		t := byRoot[root]
		if loaded[t.Root] {
			t.RootLoaded, t.Anchor = true, t.Root
		}
		threads = append(threads, *t)
	}
	sort.SliceStable(threads, func(i, j int) bool {
		if !threads[i].LatestAt.Equal(threads[j].LatestAt) {
			return threads[i].LatestAt.Before(threads[j].LatestAt)
		}
		return threads[i].Root < threads[j].Root
	})
	return main, threads
}

// ThreadMessages returns the messages belonging to one thread — the root, when it is
// loaded, followed by its replies oldest→newest.
func ThreadMessages(msgs []Message, root EventID) []Message {
	if root == "" {
		return nil
	}
	out := make([]Message, 0, 8)
	for i := range msgs {
		if msgs[i].ID == root {
			out = append(out, msgs[i])
			break
		}
	}
	in := Conversations(msgs)
	for i := range msgs {
		if msgs[i].ID != root && in[msgs[i].ID] == root {
			out = append(out, msgs[i])
		}
	}
	return out
}
