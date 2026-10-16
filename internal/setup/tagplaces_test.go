package setup

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// tag:<name> is a place everywhere places are read; a missing tag is refused.
func TestTagPlacesMustNameATag(t *testing.T) {
	t.Parallel()
	base := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	for name, set := range map[string]func(*config.Config){
		"a notification rule": func(c *config.Config) { c.Notifications.Rules = []config.Rule{{Match: "tag:Nope"}} },
		"agent read":          func(c *config.Config) { c.Agent.Read.Rooms = []string{"tag:Nope"} },
		"codes":               func(c *config.Config) { c.Codes.Include = []string{"tag:Nope"} },
		"a spam filter's in": func(c *config.Config) {
			c.Filters = []config.Filter{{Name: "x", Tag: "Family", Words: []string{"w"}, In: []string{"tag:Nope"}}}
		},
	} {
		cfg := base
		set(&cfg)
		if err := PlaceEntries(cfg); err == nil || !strings.Contains(err.Error(), "names no [[tag]]") {
			t.Errorf("%s naming no tag: %v, want refused", name, err)
		}
	}
	ok := base
	ok.Agent.Read.Rooms = []string{"tag:family"}
	ok.Notifications.Rules = []config.Rule{{Match: "tag:Family"}}
	if err := PlaceEntries(ok); err != nil {
		t.Errorf("places naming a tag that exists: %v", err)
	}
}

// Places carry the tags and the home order ([display] priority, rail order, tags).
func TestPlacesCarryTagsAndPriority(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	cfg.Display.Priority = []string{"tag:Family", "space:Work"}
	places := PlacesOf(cfg)
	if !slices.Equal(places.Order.Priority, []string{"tag:Family", "space:Work"}) || !slices.Equal(places.Order.Tags, []string{"family"}) {
		t.Errorf("home order = %+v, want [display] priority and the tags", places.Order)
	}
	if facts := places.Facts(domain.Room{ID: "!mom:x", IsDirect: true}, nil); !facts.Names("tag:Family") {
		t.Error("a scope's facts do not know the room's tags")
	}
}

// The daemon's and the client's home orders, built in different steps, agree.
func TestTheHomeOrderIsTheSameBuiltInEitherStep(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}, {Name: "All", Rule: []string{"*"}}}}
	cfg.Display.Priority = []string{"space:Work"}
	cfg.Display.Rail.Order = []string{"tag:Family", "-", "place:Desk", "*"}
	cfg.Display.Names = []config.DisplayName{{Target: "space:Work", Name: "Desk"}}
	spaces := []domain.Space{
		{ID: "!w:x", Name: "Work"},
		{ID: "!tip:x", Name: "Acme", Original: true},
		{ID: "!wa:x", Name: "WhatsApp Home", Bridge: domain.ProtocolWhatsApp},
	}
	tags, _, err := Tags(cfg)
	if err != nil {
		t.Fatal(err)
	}
	daemon := PlacesOf(cfg).Order.WithManaged(spaces)
	client := domain.NewHomeOrder(cfg.Display.Priority, cfg.Display.Rail.Order, tags, spaces, cfg.Display.SpaceLabels())
	if !reflect.DeepEqual(daemon, client) {
		t.Errorf("daemon %+v\nclient %+v", daemon, client)
	}
	if !client.Managed["acme"] || !client.Managed["whatsapp home"] || client.Managed["work"] || !client.Every["all"] {
		t.Errorf("order = %+v, want Acme and WhatsApp Home the network's own, All every room", client)
	}
}

// A word exclusive does not know is refused at load.
func TestExclusiveRefusesAnUnknownWord(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{{Name: "Out", Rule: []string{"dm"}, Exclusive: []string{"rooms"}}}}
	if _, _, err := Tags(cfg); err == nil || !strings.Contains(err.Error(), `"rooms"`) {
		t.Errorf("Tags = %v, want exclusive's unknown word refused", err)
	}
}
