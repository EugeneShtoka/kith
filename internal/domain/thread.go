package domain

import (
	"sort"
	"time"
)

// Thread is one thread's summary, enough to draw and badge its row in the main timeline.
type Thread struct {
	Root   EventID
	RoomID RoomID
	// Count is the number of replies, excluding the root.
	Count int
	// Latest is the newest reply; its sender as an MXID (for color) and resolved name.
	Latest           EventID
	LatestAt         time.Time
	LatestSender     string
	LatestSenderName string
	// Anchor is the main-timeline message the row is drawn beneath: the root when
	// loaded, else the newest main-timeline message before the thread's newest reply.
	Anchor EventID
	// RootLoaded is whether Root is among the messages this summary was built from.
	RootLoaded bool
	// Title is the root's text, for naming the thread where the root is not on screen.
	Title string
	// Unread and Mentions count since our receipt for this thread, not the room's.
	Unread   int
	Mentions int
}

// WithUnread stamps each thread with its unread counts from u.
func WithUnread(threads []Thread, u Unread) []Thread {
	for i := range threads {
		threads[i].Unread, threads[i].Mentions = u.ThreadCount(threads[i].Root)
	}
	return threads
}

// Conversations maps each loaded message in a thread to its root. A root maps to
// itself, and a plain reply to a threaded message joins that thread.
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

// ThreadRootOf is the message's thread root per Conversations, "" when in none.
func ThreadRootOf(msgs []Message, id EventID) EventID {
	if id == "" {
		return ""
	}
	return Conversations(msgs)[id]
}

// CollapseThreads splits a room's messages (oldest first, as MergeMessages leaves
// them) into the main timeline and one summary per thread.
func CollapseThreads(msgs []Message) ([]Message, []Thread) {
	in := Conversations(msgs)
	main := make([]Message, 0, len(msgs))
	byRoot := make(map[EventID]*Thread)
	order := make([]EventID, 0)
	for i := range msgs {
		msg := msgs[i]
		root := in[msg.ID]
		// A root stays in the main timeline; its summary row hangs off it.
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
		// Input is ordered, so the last reply seen is the newest; the anchor follows it,
		// keeping a rootless thread's row where it last moved.
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

// ThreadMessages returns a thread's root (when loaded) and its replies, oldest first.
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
