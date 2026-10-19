package setup_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// placeSetting is one setting that names places: how to put an entry in it, what its
// refusal names, and the forms it takes besides a bare name.
type placeSetting struct {
	what string // as the error names it
	set  func(c *config.Config, entry string)
	ok   []string // accepted entries
}

var (
	homeForms  = []string{"space:Work", "tag:Pinned", "place:Work", "PLACE: work "}
	roomForms  = []string{"!abc:example.org", "telegram:1/-100"}
	placeForms = append(append([]string{"room:Standup", "protocol:WhatsApp", "dm", "group"}, homeForms...), roomForms...)
)

// placeSettings is every setting that names a place.
func placeSettings() []placeSetting {
	one := func(entry string) []string { return []string{entry} }
	return []placeSetting{
		{"display.priority", func(c *config.Config, e string) { c.Display.Priority = one(e) }, homeForms},
		{"display.rail.order", func(c *config.Config, e string) { c.Display.Rail.Order = one(e) },
			append([]string{"*", "-", "---"}, homeForms...)},
		{"display.rail.hidden", func(c *config.Config, e string) { c.Display.Rail.Hidden = one(e) }, homeForms},
		{"display.rail.hide_when_empty", func(c *config.Config, e string) { c.Display.Rail.HideWhenEmpty = one(e) }, homeForms},
		{"display.filing_spaces", func(c *config.Config, e string) { c.Display.FilingSpaces = one(e) },
			[]string{"space:Work", "place:Work", "!abc:example.org"}},
		{"display.space_rule[0].space", func(c *config.Config, e string) {
			c.Display.SpaceRules = []config.SpaceRule{{Space: e, FirstNameOnly: true}}
		}, homeForms},
		{"display.rooms.rule[0].group", func(c *config.Config, e string) {
			c.Display.Rooms.Rules = []config.RoomsRule{{Group: e, Sort: []string{"name"}}}
		}, homeForms},
		{"display.read_rule[0].match", func(c *config.Config, e string) {
			c.Display.ReadRules = []config.ReadRule{{Match: e, Send: new(false)}}
		}, append(append([]string{"$thread:example.org"}, homeForms...), roomForms...)},
		{"display.threads.rule[0].match", func(c *config.Config, e string) {
			c.Display.Threads.Rules = []config.ThreadRule{{Match: e, MaxInRoomList: 3}}
		}, append(cloned(homeForms), roomForms...)},
		{"display.media.rule[0].match", func(c *config.Config, e string) {
			c.Display.Media.Rules = []config.MediaRule{{Match: e, Auto: new(false)}}
		}, append(cloned(homeForms), roomForms...)},
		{`keys.jump "g w"`, func(c *config.Config, e string) {
			c.Keys.Jump = config.Jumps{{Chord: "g w", Target: e}}
		}, []string{"space:Work", "tag:Pinned", "place:Work", "room:!abc:example.org"}},
		{"codes.include", func(c *config.Config, e string) { c.Codes.Include = one(e) }, placeForms},
		{"codes.exclude", func(c *config.Config, e string) { c.Codes.Exclude = one(e) }, placeForms},
		{"display.direction.rtl", func(c *config.Config, e string) { c.Display.Direction.RTL = one(e) }, placeForms},
		{"display.direction.ltr", func(c *config.Config, e string) { c.Display.Direction.LTR = one(e) }, placeForms},
		{"agent.read.rooms", func(c *config.Config, e string) { c.Agent.Read.Rooms = one(e) }, placeForms},
		{"agent.read.except", func(c *config.Config, e string) { c.Agent.Read.Except = one(e) }, placeForms},
		{"agent.write.rooms", func(c *config.Config, e string) { c.Agent.Write.Rooms = one(e) }, placeForms},
		{"agent.write.except", func(c *config.Config, e string) { c.Agent.Write.Except = one(e) }, placeForms},
		{"agent.write.send", func(c *config.Config, e string) { c.Agent.Write.Send = one(e) }, placeForms},
		{"filter[0].in", func(c *config.Config, e string) {
			c.Filters = []config.Filter{{Name: "x", Tag: "Pinned", Words: one("w"), In: one(e)}}
		}, placeForms},
		{"command[0].in", func(c *config.Config, e string) {
			c.UserCommands = []config.Command{{Name: "zoom", In: one(e)}}
		}, placeForms},
		{"assist.rooms", func(c *config.Config, e string) { c.Assist.Rooms = one(e) }, placeForms},
		{"assist.except", func(c *config.Config, e string) { c.Assist.Except = one(e) }, placeForms},
		{"tracked.rule[0].in", func(c *config.Config, e string) {
			c.Display.Tracked.Rules = []config.TrackedRule{{Words: one("deploy"), In: one(e)}}
		}, placeForms},
		{"tracked.rule[0].except", func(c *config.Config, e string) {
			c.Display.Tracked.Rules = []config.TrackedRule{{Words: one("deploy"), Except: one(e)}}
		}, placeForms},
		{"notifications.rule[0].match", func(c *config.Config, e string) {
			c.Notifications.Rules = []config.Rule{{Match: e, Show: "all"}}
		}, placeForms},
		{"storage.rule[0].match", func(c *config.Config, e string) {
			c.Storage.Rules = []config.StorageRule{{Match: e, Messages: new(10)}}
		}, placeForms},
		{`tag "Busy": rule`, func(c *config.Config, e string) {
			c.Tags = append(c.Tags, config.Tag{Name: "Busy", Rule: []string{e}})
		}, append([]string{"unread", "not dm", "*"}, placeForms...)},
	}
}

func cloned(s []string) []string { return append([]string(nil), s...) }

// valid is a config every setting's entries are checked against: the tag the entries
// name exists.
func valid() config.Config {
	return config.Config{Homeserver: "https://x", User: "@me:x", Tags: []config.Tag{{Name: "Pinned"}}}
}

// Every setting naming a place refuses a bare name at load, saying which setting, which
// entry, and the prefixes to choose from; it takes each prefixed form it lists.
func TestEverySettingNamingAPlaceRefusesABareName(t *testing.T) {
	t.Parallel()
	for _, s := range placeSettings() {
		for _, bare := range []string{"Work", "work chat"} {
			cfg := valid()
			s.set(&cfg, bare)
			err := setup.Validate(cfg)
			if err == nil {
				t.Errorf("%s: bare %q accepted", s.what, bare)
				continue
			}
			msg := err.Error()
			if !strings.Contains(msg, s.what) || !strings.Contains(msg, `"`+bare+`" needs a prefix: `) ||
				!strings.Contains(msg, "place:") {
				t.Errorf("%s: bare %q refused with %q, want the setting, the entry and the prefixes", s.what, bare, msg)
			}
		}
		for _, entry := range s.ok {
			cfg := valid()
			s.set(&cfg, entry)
			if err := setup.Validate(cfg); err != nil {
				t.Errorf("%s: %q refused: %v", s.what, entry, err)
			}
		}
	}
}

// The refusal in full, for the setting most often written by hand.
func TestABareNameIsRefusedInSoManyWords(t *testing.T) {
	t.Parallel()
	cfg := valid()
	cfg.Display.Priority = []string{"space:Friends", "Work"}
	want := `setup: display.priority: "Work" needs a prefix: space:, tag:, place:`
	if err := setup.Validate(cfg); err == nil || err.Error() != want {
		t.Errorf("Validate = %v, want %q", err, want)
	}
	cfg = valid()
	cfg.Display.Rail.Order = []string{"tag:Pinned", "-", "Friends"}
	want = `setup: display.rail.order: "Friends" needs a prefix: space:, tag:, place: (or * or -)`
	if err := setup.Validate(cfg); err == nil || err.Error() != want {
		t.Errorf("Validate = %v, want %q", err, want)
	}
	cfg = valid()
	cfg.Agent.Read.Rooms = []string{"Work"}
	want = `setup: agent.read.rooms: "Work" needs a prefix: room:, space:, tag:, place:, protocol: (or a room ID, dm, group)`
	if err := setup.Validate(cfg); err == nil || err.Error() != want {
		t.Errorf("Validate = %v, want %q", err, want)
	}
}

// A setting holding rail rows takes no room-only form, and filing_spaces no tag.
func TestAPlaceSettingRefusesAFormItCannotUse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		set   func(*config.Config)
		entry string
	}{
		{func(c *config.Config) { c.Display.Priority = []string{"room:Standup"} }, "room:Standup"},
		{func(c *config.Config) { c.Display.Rail.Order = []string{"!abc:example.org"} }, "!abc:example.org"},
		{func(c *config.Config) { c.Display.Rail.Hidden = []string{"dm"} }, "dm"},
		{func(c *config.Config) { c.Display.FilingSpaces = []string{"tag:Pinned"} }, "tag:Pinned"},
		{func(c *config.Config) { c.Display.ReadRules = []config.ReadRule{{Match: "room:Standup"}} }, "room:Standup"},
		{func(c *config.Config) { c.Display.Priority = []string{"place:"} }, "place:"},
		{func(c *config.Config) { c.Keys.Jump = config.Jumps{{Chord: "g w", Target: "Work"}} }, "Work"},
	} {
		cfg := valid()
		c.set(&cfg)
		if err := setup.Validate(cfg); err == nil || !strings.Contains(err.Error(), `"`+c.entry+`" needs a prefix`) {
			t.Errorf("%q: Validate = %v, want it refused", c.entry, err)
		}
	}
}

// What kith writes into a fresh config passes its own check: the starter.
func TestTheStarterNamesPlacesWithPrefixes(t *testing.T) {
	t.Parallel()
	cfg, err := config.Decode(strings.NewReplacer(
		`homeserver = ""`, `homeserver = "https://x"`, `user       = ""`, `user       = "@me:x"`,
	).Replace(config.Annotated()))
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Validate(cfg); err != nil {
		t.Errorf("the starter config is refused: %v", err)
	}
}
