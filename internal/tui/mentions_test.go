package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The mentions list shows the messages naming you, newest first, across rooms.
func TestMentionsListShowsTheMessages(t *testing.T) {
	t.Parallel()

	backend := &searchBackend{hits: []domain.SearchHit{
		hit("!a:x", "$new", "dana", "@you can you look at this", 1),
		hit("!b:x", "$old", "sam", "thanks @you", 30),
	}}
	m := update(t, sized(t, starterNew(backend, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = m.clearStatus()

	m, cmd := press(t, m, keyText("@"))
	m = deliver(t, m, cmd)

	if !m.search.active || m.search.showing() != (mentionsList{}) {
		t.Fatal("@ did not open the mentions list")
	}
	// The prompt stays open, labelled for narrowing, not searching.
	if m.prompt.kind != promptMentions {
		t.Errorf("prompt = %d, want the mentions prompt", m.prompt.kind)
	}
	if got := m.prompt.label(); !strings.Contains(got, "narrow") {
		t.Errorf("label = %q, want it to say typing narrows the list", got)
	}
	if len(backend.requests) == 0 {
		t.Fatal("nothing was asked of the cache")
	}
	req := backend.requests[len(backend.requests)-1]
	if !req.Filter.Mentioned {
		t.Error("the request did not ask for mentions")
	}
	if req.Filter.Terms != "" {
		t.Errorf("terms = %q, want none", req.Filter.Terms)
	}
	if !req.Rooms.All {
		t.Errorf("rooms = %+v, want every room", req.Rooms)
	}
	if len(m.search.hits) != 2 {
		t.Fatalf("hits = %d, want both mentions", len(m.search.hits))
	}

	// The pane names what it shows, and rows carry the room.
	frame := stripStyles(m.View().Content)
	for _, want := range []string{"Mentions", "2 mentions", "Alpha", "Bravo", "@you can you"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not show %q:\n%s", want, frame)
		}
	}
}

// An empty list is reported as good news, not "no matches".
func TestNoMentionsSaysSoKindly(t *testing.T) {
	t.Parallel()

	m := update(t, sized(t, starterNew(&searchBackend{}, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m, cmd := press(t, m, keyText("@"))
	m = deliver(t, m, cmd)

	if got := m.searchSummary(); !strings.Contains(got, "nobody has named you") {
		t.Errorf("summary = %q", got)
	}
}

// Typing and the scope key narrow the list, as in search.
func TestMentionsCanBeNarrowed(t *testing.T) {
	t.Parallel()

	backend := &searchBackend{hits: []domain.SearchHit{hit("!a:x", "$1", "dana", "ping", 1)}}
	m := update(t, sized(t, starterNew(backend, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m, cmd := press(t, m, keyText("@"))
	m = deliver(t, m, cmd)

	// tab cycles the scope, and the mentions filter survives it.
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = deliver(t, m, cmd)
	req := backend.requests[len(backend.requests)-1]
	if !req.Filter.Mentioned {
		t.Error("cycling the scope dropped the mentions filter")
	}
	if !strings.Contains(stripStyles(m.View().Content), "Mentions") {
		t.Error("the pane stopped saying it is a list of mentions")
	}
}
