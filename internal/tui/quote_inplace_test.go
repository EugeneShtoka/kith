package tui

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A reply to a message folded into a thread quotes that message as it is now: an edit
// of it shows in the quote, where a copy taken when the quote was first drawn did not.
func TestAQuotedThreadReplyShowsItsEdit(t *testing.T) {
	t.Parallel()
	ts := func(s int) time.Time { return time.Unix(int64(1_700_000_000+s), 0) }
	msgs := []domain.Message{
		{ID: "$root", RoomID: "!a:x", Sender: "@dana:x", Body: "a thread", Timestamp: ts(0)},
		{ID: "$inner", RoomID: "!a:x", Sender: "@dana:x", Body: "the old words", ThreadRoot: "$root", Timestamp: ts(1)},
		{ID: "$answer", RoomID: "!a:x", Sender: "@me:x", Body: "about that", ReplyTo: "$inner", Timestamp: ts(2)},
	}
	m := update(t, starterNew(&quoteBackend{}, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	m, _ = m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m, _ = routed(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: msgs}})
	m, _ = m.loadQuotes()
	if got, ok := m.quotedTarget(m.derivedFor(), "$inner"); !ok || got.Body != "the old words" {
		t.Fatalf("quote = %q, %v before the edit", got.Body, ok)
	}
	edited := append([]domain.Message(nil), msgs...)
	edited[1].Body, edited[1].Edited = "the new words", true
	m = m.setMessages(edited)
	m, _ = m.loadQuotes()
	if got, _ := m.quotedTarget(m.derivedFor(), "$inner"); got.Body != "the new words" {
		t.Fatalf("quote = %q after the edit, want the new words", got.Body)
	}
}
