package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// `#` completes a room: the name is inserted and the link recorded.

func roomsForCompletion(t *testing.T) Model {
	t.Helper()
	rooms := []domain.Room{
		{ID: "!war:example.org", Name: "Warroom"},
		{ID: "!wardrobe:example.org", Name: "Wardrobe"},
		{ID: "!quiet:example.org", Name: "Reading"},
	}
	return sized(t, update(t, newModel(), roomsMsg{rooms: rooms}))
}

func TestHashOffersRoomsRankedByMatch(t *testing.T) {
	t.Parallel()

	m := roomsForCompletion(t)
	got := m.roomCandidates("war")
	if len(got) != 2 {
		t.Fatalf("offered %d rooms for \"war\", want the two that match: %+v", len(got), got)
	}
	if got[0].text != "Warroom" && got[0].text != "Wardrobe" {
		t.Errorf("first row = %q, want a room whose name starts with the query", got[0].text)
	}
	if !strings.HasPrefix(got[0].detail, "!") {
		t.Errorf("row detail = %q, want the room id", got[0].detail)
	}
	if got[0].roomID == "" {
		t.Error("the candidate carries no room to link to")
	}
}

func TestHashMatchesTheRoomIDToo(t *testing.T) {
	t.Parallel()

	m := roomsForCompletion(t)
	got := m.roomCandidates("quiet")
	if len(got) != 1 || got[0].text != "Reading" {
		t.Fatalf("matching by id offered %+v, want the room whose id contains it", got)
	}
}

func TestAcceptingARoomRecordsItAsAMention(t *testing.T) {
	t.Parallel()

	m := roomsForCompletion(t)
	m.focus, m.compose.insertMode = paneTimeline, true
	m = typeInto(t, m, "see #war")

	if !m.completion.active || m.completion.trigger != "#" {
		t.Fatalf("typing #war opened %+v, want the room popup", m.completion)
	}
	m, _ = m.acceptCompletion()

	text := m.editorFor(fieldComposer).text
	if !strings.Contains(text, "War") || strings.Contains(text, "matrix.to") {
		t.Errorf("composer = %q, want the room's name and no url", text)
	}
	if len(m.compose.drafted) != 1 {
		t.Fatalf("drafted %+v, want one mention", m.compose.drafted)
	}
	got := m.compose.drafted[0]
	if got.RoomID == "" || got.UserID != "" {
		t.Errorf("mention = %+v, want a room and nobody to notify", got)
	}
	if got.Name != strings.TrimSpace(strings.TrimPrefix(text, "see ")) {
		t.Errorf("mention name = %q, composer says %q — the link is written against the name", got.Name, text)
	}
}

// A room mention notifies nobody.
func TestARoomMentionNotifiesNobody(t *testing.T) {
	t.Parallel()

	person := domain.Mention{UserID: "@alice:example.org", Name: "Alice"}
	room := domain.Mention{RoomID: "!war:example.org", Name: "Warroom"}
	if !person.Notifies() {
		t.Error("a person's mention does not notify")
	}
	if room.Notifies() {
		t.Error("a room's mention notifies, which would ping everybody in it")
	}
	if room.Target() != "!war:example.org" || person.Target() != "@alice:example.org" {
		t.Errorf("targets = %q and %q", room.Target(), person.Target())
	}
}
