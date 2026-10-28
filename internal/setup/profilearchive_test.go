package setup_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// archiving is a config whose archive tag holds Slack rooms and one picked room, with
// a profile setting its own archive and rail, and one setting neither.
func archiving() config.Config {
	cfg := valid()
	cfg.Tags = []config.Tag{
		{Name: "Pinned"},
		{Name: "Archived", Rule: []string{"protocol:slack"}, Picked: []string{mx + "!picked:x"}},
	}
	cfg.Display.Rail = config.Rail{Order: []string{"*", "tag:Archived"}}
	cfg.Profiles = []config.Profile{
		{Name: "default", Include: []string{"*"}},
		{
			Name: "phone", Include: []string{"*"}, Exclude: []string{"tag:Archived"},
			Archive: config.ProfileArchive{Rooms: []string{"protocol:signal"}, Push: new(true)},
			Rail:    config.Rail{Order: []string{"tag:Archived", "*"}, NetworkColors: true},
		},
		{Name: "plain", Include: []string{"*"}},
	}
	return cfg
}

// A profile's archive.rooms is the archive tag's rule there and archive.push its
// push_archive; its rail is the whole rail. The tag's hand-filed rooms, the other tags
// and the other profiles keep what the config says, and the config is not changed.
func TestAProfileHasItsOwnArchiveAndRail(t *testing.T) {
	t.Parallel()
	cfg := archiving()
	before := cfg.Clone()

	phone := setup.ProfileConfig(cfg, "Phone ")
	archived := phone.Tags[1]
	if !slices.Equal(archived.Rule, []string{"protocol:signal"}) || !archived.PushArchive ||
		!slices.Equal(archived.Picked, []string{mx + "!picked:x"}) {
		t.Errorf("phone's Archived = %+v, want its own rule and push, the shared picked", archived)
	}
	if !reflect.DeepEqual(phone.Tags[0], cfg.Tags[0]) {
		t.Errorf("phone's Pinned = %+v, want the shared one", phone.Tags[0])
	}
	if want := (config.Rail{Order: []string{"tag:Archived", "*"}, NetworkColors: true}); !reflect.DeepEqual(phone.Display.Rail, want) {
		t.Errorf("phone's rail = %+v, want %+v", phone.Display.Rail, want)
	}
	for _, name := range []string{"", "default", "plain", "gone"} {
		if got := setup.ProfileConfig(cfg, name); !reflect.DeepEqual(got, cfg) {
			t.Errorf("profile %q runs %+v, want the shared config", name, got.Tags)
		}
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Error("ProfileConfig changed the config it was given")
	}
	phone.Tags[1].Rule[0] = "dm"
	if cfg.Profiles[1].Archive.Rooms[0] != "protocol:signal" {
		t.Error("the profile's view shares its rule with the config")
	}
}

// kithd judges a profile's rooms with its own archive: phone excludes what it archives
// (Signal), not what the shared archive holds (Slack). A profile without its own
// archive judges with the shared tags.
func TestAProfileHoldsRoomsByItsOwnArchive(t *testing.T) {
	t.Parallel()
	profiles, err := setup.Profiles(archiving())
	if err != nil {
		t.Fatal(err)
	}
	tags, _, err := setup.Tags(archiving())
	if err != nil {
		t.Fatal(err)
	}
	signal := domain.RoomFacts{ID: "signal:+1/a", Protocol: domain.ProtocolSignal}
	slack := domain.RoomFacts{ID: "slack:T/C", Protocol: domain.ProtocolSlack}
	signal.Tags, slack.Tags = tags.Of(signal), tags.Of(slack) // as kithd's scope judges them
	if profiles[0].Tags != nil || profiles[2].Tags != nil || profiles[1].Tags == nil {
		t.Fatalf("own tags: default %v, phone %v, plain %v; want phone's only", profiles[0].Tags, profiles[1].Tags, profiles[2].Tags)
	}
	phone := profiles[1]
	if phone.Holds(signal) || !phone.Holds(slack) {
		t.Errorf("phone holds Signal %v, Slack %v; want what it archives left out, not the shared archive",
			phone.Holds(signal), phone.Holds(slack))
	}
}

// Whatever names the archive tag is what archive.rooms sets: the default Archived and a
// network's own archive tag alike.
func TestAProfileSetsEveryArchiveTag(t *testing.T) {
	t.Parallel()
	cfg := archiving()
	cfg.Tags = append(cfg.Tags, config.Tag{Name: "Shelf", Rule: []string{"dm"}})
	cfg.WhatsApp.Archive.Tag = "shelf"
	view := setup.ProfileConfig(cfg, "phone")
	for _, i := range []int{1, 2} {
		if !slices.Equal(view.Tags[i].Rule, []string{"protocol:signal"}) {
			t.Errorf("phone's %s rule = %v, want archive.rooms", view.Tags[i].Name, view.Tags[i].Rule)
		}
	}
	if !slices.Equal(view.Tags[0].Rule, cfg.Tags[0].Rule) {
		t.Errorf("phone's Pinned rule = %v, want the shared one", view.Tags[0].Rule)
	}
}

// What a profile's archive cannot mean is refused at load, saying which profile.
func TestAProfilesArchiveIsRefusedWhatItCannotMean(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		edit    func(*config.Config)
		refusal string // "" when accepted
	}{
		"no archive tag": {func(c *config.Config) { c.Tags = c.Tags[:1] }, `profile "phone": archive`},
		"a missing tag": {func(c *config.Config) { c.Profiles[1].Archive.Rooms = []string{"tag:Nope"} },
			`profile "phone": archive.rooms`},
		// phone excludes tag:Archived, so its own archive must not read a room's state.
		"a state word": {func(c *config.Config) { c.Profiles[1].Archive.Rooms = []string{"unread"} },
			`profile "phone": exclude: "tag:Archived"`},
		"a state word it does not name": {func(c *config.Config) {
			c.Profiles[1].Exclude = nil
			c.Profiles[1].Archive.Rooms = []string{"unread"}
		}, ""},
		"a negated rule": {func(c *config.Config) { c.Profiles[1].Archive.Rooms = []string{"not tag:Pinned", "not dm"} }, ""},
		"push alone":     {func(c *config.Config) { c.Profiles[1].Archive = config.ProfileArchive{Push: new(false)} }, ""},
		"a rail alone":   {func(c *config.Config) { c.Profiles[1].Archive = config.ProfileArchive{} }, ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := archiving()
			tc.edit(&cfg)
			err := setup.Validate(cfg)
			switch {
			case tc.refusal == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
				t.Errorf("Validate = %v, want a refusal holding %q", err, tc.refusal)
			}
		})
	}
}

// Deleting a tag takes it out of every profile's rail as out of the shared one.
func TestDeletingATagTakesItOffEveryRail(t *testing.T) {
	t.Parallel()
	cfg := archiving()
	cfg.Profiles[1].Rail.Hidden = []string{"tag:Pinned"}
	cfg.Profiles[1].Rail.HideWhenEmpty = []string{"tag:pinned", "tag:Archived"}
	cfg.Profiles[1].Rail.Order = append(cfg.Profiles[1].Rail.Order, "tag:Pinned")
	got := setup.DeleteTag(cfg, "pinned").Profiles[1].Rail
	if slices.Contains(got.Order, "tag:Pinned") || len(got.Hidden) != 0 || !slices.Equal(got.HideWhenEmpty, []string{"tag:Archived"}) {
		t.Errorf("phone's rail = %+v, want Pinned gone and the rest kept", got)
	}
}
