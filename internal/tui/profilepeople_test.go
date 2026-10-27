package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// A window's profile hides the people its people.exclude names, by ID or by a card
// field, with the direct replies to them, and never you; another profile shows them,
// the show key shows them here, and /why names the profile. The cards arriving later
// (a read of the folder) hides whom they name at once.
func TestAProfileHidesItsPeopleInTheTimeline(t *testing.T) {
	t.Parallel()
	const ann, cat, bob = "matrix:@ann:example.org", "telegram:1001", "matrix:@bob:example.org"
	minute := func(n int) time.Time { return time.Unix(1700000000+int64(n)*60, 0) }
	c := markedCase{msgs: []domain.Message{
		{ID: mx + "$0", RoomID: mx + "!a:x", Sender: ann, Body: "hi", Timestamp: minute(0)},
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: bob, Body: "yo", ReplyTo: mx + "$0", Timestamp: minute(1)},
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: cat, Body: "meeting", Timestamp: minute(2)},
		{ID: mx + "$3", RoomID: mx + "!a:x", Sender: bob, Body: "sure", ReplyTo: mx + "$2", Timestamp: minute(3)},
		{ID: mx + "$4", RoomID: mx + "!a:x", Sender: markedMe, Body: "me", Timestamp: minute(4)},
		{ID: mx + "$5", RoomID: mx + "!a:x", Sender: bob, Body: "plain", Timestamp: minute(5)},
	}}
	m := markedModel(t, c)
	cfg := m.conf.base.Clone()
	cfg.Profiles = []config.Profile{
		{Name: "first", Include: []string{"*"}},
		{Name: "home", Include: []string{"*"}, People: config.ProfilePeople{Exclude: []string{ann, "role:colleagues"}}},
	}
	m.conf.base = cfg
	m.conf.rev++
	m.profiles.current = "home"
	everyone := []domain.EventID{mx + "$0", mx + "$1", mx + "$2", mx + "$3", mx + "$4", mx + "$5"}
	if got, want := shownIDs(m), []domain.EventID{mx + "$2", mx + "$3", mx + "$4", mx + "$5"}; !slices.Equal(got, want) {
		t.Fatalf("home with no cards read drew %v, want %v: Ann and the reply to her hidden", got, want)
	}

	cards := []phonebook.Card{
		{Path: "Cat.md", Name: "Cat", IDs: phonebook.Identifiers{"telegram": {"1001"}}, Fields: []phonebook.Field{{Key: "role", Values: []string{"Colleagues"}}}},
		{Path: "Me.md", Name: "Me", IDs: phonebook.Identifiers{"matrix": {"@me:example.org"}}, Fields: []phonebook.Field{{Key: "role", Values: []string{"colleagues"}}}},
	}
	m.cards.index, m.cards.read = phonebook.NewIndex(cards), m.cards.read+1
	if got, want := shownIDs(m), []domain.EventID{mx + "$4", mx + "$5"}; !slices.Equal(got, want) {
		t.Fatalf("home with the cards read drew %v, want %v: Cat by her card too, never you", got, want)
	}

	m.timeline.selected = mx + "$2"
	m.markedShownIn = m.openRoom
	if got := strings.Join(m.whyLines(time.Now()), "\n"); !strings.Contains(got, "hidden in this profile") ||
		!strings.Contains(got, "excluded by the") || !strings.Contains(got, "home") {
		t.Errorf("why on Cat's message:\n%s", got)
	}
	if got := shownIDs(m); !slices.Equal(got, everyone) {
		t.Errorf("the show key drew %v, want everyone", got)
	}
	m.markedShownIn = ""

	m.profiles.current = "first"
	if got := shownIDs(m); !slices.Equal(got, everyone) {
		t.Errorf("first drew %v, want everyone", got)
	}
}
