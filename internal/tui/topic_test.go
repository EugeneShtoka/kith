package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// topicRoom is a room with a topic, open and focused.
func topicRoom(t *testing.T, topic string) Model {
	t.Helper()
	m := sized(t, withRooms(t, newModel()))
	joined := append([]domain.Room(nil), m.rooms.joined...)
	for i := range joined {
		if joined[i].ID == "!a:x" {
			joined[i].Topic = topic
		}
	}
	m.rooms = m.rooms.withJoined(joined)
	m.openRoom = "!a:x"
	m.focus, m.compose.insertMode = paneTimeline, false
	return m
}

// The topic rides in the title when there is room for it.
func TestTopicAppearsInTheTitle(t *testing.T) {
	t.Parallel()

	m := topicRoom(t, "standups 10:00, docs in the wiki")
	if got := m.topicSuffix("Alpha", 100); !strings.Contains(got, "standups 10:00") {
		t.Errorf("suffix = %q, want the topic", got)
	}
}

// It yields rather than truncating the rest: the room and the day answer questions
// you asked by being here, and a shred of a topic answers none.
func TestTopicYieldsWhenTheTitleIsLong(t *testing.T) {
	t.Parallel()

	m := topicRoom(t, "standups 10:00, docs in the wiki")
	wide := strings.Repeat("x", 90)
	if got := m.topicSuffix(wide, 100); got != "" {
		t.Errorf("suffix = %q, want nothing left for a topic", got)
	}
	// And nothing at all rather than an ellipsis, which would advertise a keystroke
	// instead of answering.
	if got := m.topicSuffix(strings.Repeat("x", 80), 100); strings.Contains(got, "standups") {
		t.Errorf("suffix = %q, want it to yield before it shortens to a word", got)
	}
}

// A room without one says nothing, and neither does a thread — a thread has its own
// subject, and the room's is not it.
func TestTopicAbsentWhereItWouldMislead(t *testing.T) {
	t.Parallel()

	if got := topicRoom(t, "").topicSuffix("Alpha", 100); got != "" {
		t.Errorf("suffix = %q for a room with no topic", got)
	}
	m := topicRoom(t, "standups 10:00")
	m.thread.root = domain.EventID("$t1")
	if got := m.topicSuffix("Alpha", 100); got != "" {
		t.Errorf("suffix = %q inside a thread, want the thread's own subject to stand", got)
	}
}

// `gt` opens the whole thing; with no topic it says so instead of opening an empty box.
func TestTopicReaderOpensOnlyWithATopic(t *testing.T) {
	t.Parallel()

	m := topicRoom(t, "line one\nline two")
	opened, _ := m.openTopic()
	if !opened.reader.showing(readerTopic) {
		t.Fatal("gt did not open the topic reader")
	}
	if lines := opened.topicLines(); len(lines) != 2 {
		t.Errorf("lines = %v, want the author's own two", lines)
	}

	empty := topicRoom(t, "")
	quiet, _ := empty.openTopic()
	if quiet.reader.showing(readerTopic) {
		t.Error("an empty topic opened a reader with nothing in it")
	}
	if quiet.status() == "" {
		t.Error("nothing was said about why nothing opened")
	}
}
