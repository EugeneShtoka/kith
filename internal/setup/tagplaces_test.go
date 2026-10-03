package setup

import (
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// tag:<name> is a place wherever places are read, and naming a tag that does not
// exist is refused there, as a word that names nothing is.
func TestTagPlacesMustNameATag(t *testing.T) {
	t.Parallel()
	base := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	for name, set := range map[string]func(*config.Config){
		"a notification rule": func(c *config.Config) { c.Notifications.Rules = []config.Rule{{Match: "tag:Nope"}} },
		"agent read":          func(c *config.Config) { c.Agent.Read.Rooms = []string{"tag:Nope"} },
		"codes":               func(c *config.Config) { c.Codes.Include = []string{"tag:Nope"} },
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

// Every scope's places carry the tags, and [display] priority.
func TestPlacesCarryTagsAndPriority(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	cfg.Display.Priority = []string{"tag:Family", "Work"}
	places := PlacesOf(cfg)
	if !slices.Equal(places.Priority, []string{"tag:Family", "Work"}) {
		t.Errorf("priority = %v, want [display] priority", places.Priority)
	}
	if facts := places.Facts(domain.Room{ID: "!mom:x", IsDirect: true}, nil); !facts.Names("tag:Family") {
		t.Error("a scope's facts do not know the room's tags")
	}
}
