package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The list of mentions themselves: every message that names you, newest first, across
// every room — what was said and when, rather than which rooms hold one.
func TestMentionsListShowsTheMessages(t *testing.T) {
	t.Parallel()

	backend := &searchBackend{hits: []domain.SearchHit{
		hit("!a:x", "$new", "dana", "@you can you look at this", 1),
		hit("!b:x", "$old", "sam", "thanks @you", 30),
	}}
	m := update(t, sized(t, New(context.Background(), backend, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = m.clearStatus()

	m, cmd := press(t, m, keyText("@"))
	m = deliver(t, m, cmd)

	if !m.search.active || m.search.showing() != (mentionsList{}) {
		t.Fatal("@ did not open the mentions list")
	}
	// The prompt stays open, and its label says what typing will do — the list is
	// already there, so typing narrows it rather than starting a search.
	if m.prompt.kind != promptMentions {
		t.Errorf("prompt = %d, want the mentions prompt", m.prompt.kind)
	}
	if got := m.prompt.label(); !strings.Contains(got, "narrow") {
		t.Errorf("label = %q, want it to say typing narrows the list", got)
	}
	// The filter did the asking, with no terms at all.
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

	// The pane says what it is showing, and the rows carry the room — which is what
	// makes a cross-room list readable.
	frame := stripStyles(m.View().Content)
	for _, want := range []string{"Mentions", "2 mentions", "Alpha", "Bravo", "@you can you"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not show %q:\n%s", want, frame)
		}
	}
}

// An empty list is the good outcome, not a failed question — so it says so in those
// terms rather than "no matches".
func TestNoMentionsSaysSoKindly(t *testing.T) {
	t.Parallel()

	m := update(t, sized(t, New(context.Background(), &searchBackend{}, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m, cmd := press(t, m, keyText("@"))
	m = deliver(t, m, cmd)

	if got := m.searchSummary(); !strings.Contains(got, "nobody has named you") {
		t.Errorf("summary = %q", got)
	}
}

// Typing narrows the list, and the scope key still narrows it to one room — both free,
// because this is the search machinery wearing a different title.
func TestMentionsCanBeNarrowed(t *testing.T) {
	t.Parallel()

	backend := &searchBackend{hits: []domain.SearchHit{hit("!a:x", "$1", "dana", "ping", 1)}}
	m := update(t, sized(t, New(context.Background(), backend, config.Display{})),
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
