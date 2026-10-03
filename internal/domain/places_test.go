package domain_test

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// One room as every scope reads it: your name for it, its spaces in your order, the
// first bridge holding it, and pins that may name any of those.
func TestARoomsFactsAreReadOneWay(t *testing.T) {
	t.Parallel()
	room := domain.Room{ID: "!r:x", Name: "Standup", IsDirect: false}
	holders := []domain.Space{
		{ID: "!w:x", Name: "Work"},
		{ID: "!wa:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp},
		{ID: "!tg:x", Name: "Telegram", Bridge: domain.ProtocolTelegram},
	}
	places := domain.Places{
		Names:    map[domain.RoomID]string{"!r:x": "Daily"},
		Priority: []string{"Telegram"},
		Tags:     tagSet(t, domain.Tag{Name: "Pinned", Picked: []string{"room:Daily"}}),
	}
	got := places.Facts(room, holders)
	if got.Name != "Daily" {
		t.Errorf("Name = %q, want the one you gave it", got.Name)
	}
	if !slices.Equal(got.Spaces, []string{"Telegram", "Work", "WhatsApp"}) {
		t.Errorf("Spaces = %v, want your priority first", got.Spaces)
	}
	if got.Protocol != domain.ProtocolWhatsApp {
		t.Errorf("Protocol = %q, want the first bridge holding it", got.Protocol)
	}
	if !slices.Equal(got.Tags, []string{"Pinned"}) {
		t.Errorf("Tags = %v: a tag picking the room by your name for it did not hold it", got.Tags)
	}

	plain := domain.Places{}.Facts(domain.Room{ID: "!anon:x"}, nil)
	if plain.Name != "!anon:x" || plain.Protocol != domain.ProtocolMatrix || plain.Tags != nil || plain.Spaces != nil {
		t.Errorf("an unnamed room in no space = %+v", plain)
	}
	native := domain.Places{}.Facts(domain.Room{ID: "whatsapp:359/1203@g.us", Name: "Choir"}, nil)
	if native.Protocol != domain.ProtocolWhatsApp {
		t.Errorf("a native room's network = %q", native.Protocol)
	}
	// A tag whose rule names a space holds what the space holds.
	bySpace := domain.Places{Tags: tagSet(t, domain.Tag{Name: "Work", Rule: []string{"space:Work"}})}.Facts(room, holders)
	if !slices.Equal(bySpace.Tags, []string{"Work"}) {
		t.Errorf("Tags = %v: a tag naming a space did not hold its room", bySpace.Tags)
	}
}

func TestHoldersAreTheSpacesHoldingTheRoom(t *testing.T) {
	t.Parallel()
	spaces := []domain.Space{
		{ID: "!a:x", Children: []domain.RoomID{"!r:x"}},
		{ID: "!b:x", Children: []domain.RoomID{"!other:x"}},
		{ID: "!c:x", Children: []domain.RoomID{"!other:x", "!r:x"}},
	}
	got := domain.HoldersOf("!r:x", spaces)
	if len(got) != 2 || got[0].ID != "!a:x" || got[1].ID != "!c:x" {
		t.Errorf("HoldersOf = %+v", got)
	}
}

// A tag is decided by the space hierarchy as much as space: is.
func TestTagsNeedThePlaces(t *testing.T) {
	t.Parallel()
	if !(domain.ModelScope{Except: []string{"tag:Pinned"}}).NeedsPlaces() {
		t.Error("a tag does not need the spaces read, but its rule may name a space")
	}
	if (domain.ModelScope{Only: []string{"room:Daily", "dm"}}).NeedsPlaces() {
		t.Error("room and dm entries need no spaces")
	}
}

func tagSet(t *testing.T, tags ...domain.Tag) domain.TagSet {
	t.Helper()
	set, _, err := domain.NewTagSet(tags)
	if err != nil {
		t.Fatal(err)
	}
	return set
}
