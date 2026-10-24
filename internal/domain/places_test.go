package domain_test

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room's facts: local name, spaces in your order, first bridge, and matching tags.
func TestARoomsFactsAreReadOneWay(t *testing.T) {
	t.Parallel()
	room := domain.Room{ID: mx + "!r:x", Name: "Standup", IsDirect: false}
	holders := []domain.Space{
		{ID: mx + "!w:x", Name: "Work"},
		{ID: mx + "!wa:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp},
		{ID: mx + "!tg:x", Name: "Telegram", Bridge: domain.ProtocolTelegram},
	}
	places := domain.Places{
		Names: map[domain.RoomID]string{mx + "!r:x": "Daily"},
		Order: domain.HomeOrder{Priority: []string{"space:Telegram"}},
		Tags:  tagSet(t, domain.Tag{Name: "Pinned", Picked: []string{"room:Daily"}}),
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

	plain := domain.Places{}.Facts(domain.Room{ID: mx + "!anon:x"}, nil)
	if plain.Name != mx+"!anon:x" || plain.Protocol != domain.ProtocolMatrix || plain.Tags != nil || plain.Spaces != nil {
		t.Errorf("an unnamed room in no space = %+v", plain)
	}
	native := domain.Places{}.Facts(domain.Room{ID: "whatsapp:44/1203@g.us", Name: "Choir"}, nil)
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
		{ID: mx + "!a:x", Children: []domain.RoomID{mx + "!r:x"}},
		{ID: mx + "!b:x", Children: []domain.RoomID{mx + "!other:x"}},
		{ID: mx + "!c:x", Children: []domain.RoomID{mx + "!other:x", mx + "!r:x"}},
	}
	got := domain.HoldersOf(mx+"!r:x", spaces)
	if len(got) != 2 || got[0].ID != mx+"!a:x" || got[1].ID != mx+"!c:x" {
		t.Errorf("HoldersOf = %+v", got)
	}
}

// A tag reads the space hierarchy as space: does.
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
