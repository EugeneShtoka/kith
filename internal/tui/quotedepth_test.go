package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A reply to the message directly above draws no quote (timeline and thread); a reply
// reaching further back still does.
func TestAQuoteOfTheRowAboveIsNotDrawn(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", SenderName: "Ada", Body: "how did it go", Timestamp: at(1)},
		{ID: "$2", RoomID: "!a:x", Sender: "@b:x", SenderName: "Bo", Body: "landed it", ReplyTo: "$1", Timestamp: at(2)},
	}}})

	rows := strings.Join(m.layoutRows(), "\n")
	if strings.Contains(rows, "↪") {
		t.Errorf("a reply to the row above should carry no quote:\n%s", rows)
	}
	if !strings.Contains(rows, "landed it") || !strings.Contains(rows, "how did it go") {
		t.Errorf("both messages should still be there:\n%s", rows)
	}

	// One message further along, and the quote is worth drawing again.
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$3", RoomID: "!a:x", Sender: "@a:x", SenderName: "Ada", Body: "anything else", Timestamp: at(3)},
		{ID: "$4", RoomID: "!a:x", Sender: "@b:x", SenderName: "Bo", Body: "about that", ReplyTo: "$1", Timestamp: at(4)},
	}}})
	if rows = strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "↪") {
		t.Errorf("a reply reaching further back should quote:\n%s", rows)
	}
}
