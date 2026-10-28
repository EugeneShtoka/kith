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
	set  func(c *config.Settings, entry string)
	ok   []string // accepted entries
}

var (
	homeForms  = []string{"space:Work", "tag:Pinned", "place:Work", "PLACE: work "}
	roomForms  = []string{mx + "!abc:example.org", "telegram:1/-100"}
	placeForms = append(append([]string{"room:Standup", "protocol:WhatsApp", "dm", "group"}, homeForms...), roomForms...)
)

// placeSettings is every setting that names a place.
func placeSettings() []placeSetting {
	one := func(entry string) []string { return []string{entry} }
	return []placeSetting{
		{"display.priority", func(c *config.Settings, e string) { c.Bridge.Display.Priority = one(e) }, homeForms},
		{"display.rail.order", func(c *config.Settings, e string) { c.Bridge.Display.Rail.Order = one(e) },
			append([]string{"*", "-", "---"}, homeForms...)},
		{"display.rail.hidden", func(c *config.Settings, e string) { c.Bridge.Display.Rail.Hidden = one(e) }, homeForms},
		{"display.rail.hide_when_empty", func(c *config.Settings, e string) { c.Bridge.Display.Rail.HideWhenEmpty = one(e) }, homeForms},
		{`profile "default": rail.order`, func(c *config.Settings, e string) { c.Bridge.Profiles[0].Rail.Order = one(e) },
			append([]string{"*", "-", "---"}, homeForms...)},
		{`profile "default": rail.hidden`, func(c *config.Settings, e string) { c.Bridge.Profiles[0].Rail.Hidden = one(e) }, homeForms},
		{`profile "default": rail.hide_when_empty`, func(c *config.Settings, e string) { c.Bridge.Profiles[0].Rail.HideWhenEmpty = one(e) }, homeForms},
		{"display.filing_spaces", func(c *config.Settings, e string) { c.Device.Display.FilingSpaces = one(e) },
			[]string{"space:Work", "place:Work", mx + "!abc:example.org"}},
		{"display.space_rule[0].space", func(c *config.Settings, e string) {
			c.Bridge.Display.SpaceRules = []config.SpaceRule{{Space: e, FirstNameOnly: true}}
		}, homeForms},
		{"display.rooms.rule[0].group", func(c *config.Settings, e string) {
			c.Device.Display.Rooms.Rules = []config.RoomsRule{{Group: e, Sort: []string{"name"}}}
		}, homeForms},
		{"display.read_rule[0].match", func(c *config.Settings, e string) {
			c.Device.Display.ReadRules = []config.ReadRule{{Match: e, Send: new(false)}}
		}, append(append([]string{mx + "$thread:example.org"}, homeForms...), roomForms...)},
		{"display.threads.rule[0].match", func(c *config.Settings, e string) {
			c.Device.Display.Threads.Rules = []config.ThreadRule{{Match: e, MaxInRoomList: 3}}
		}, append(cloned(homeForms), roomForms...)},
		{"display.media.rule[0].match", func(c *config.Settings, e string) {
			c.Bridge.Display.Media.Rules = []config.MediaRule{{Match: e, Auto: new(false)}}
		}, append(cloned(homeForms), roomForms...)},
		{`keys.jump "g w"`, func(c *config.Settings, e string) {
			c.Device.Keys.Jump = config.Jumps{{Chord: "g w", Target: e}}
		}, []string{"space:Work", "tag:Pinned", "place:Work", "room:" + mx + "!abc:example.org"}},
		{"codes.include", func(c *config.Settings, e string) { c.Bridge.Codes.Include = one(e) }, placeForms},
		{"codes.exclude", func(c *config.Settings, e string) { c.Bridge.Codes.Exclude = one(e) }, placeForms},
		{"display.direction.rtl", func(c *config.Settings, e string) { c.Device.Display.Direction.RTL = one(e) }, placeForms},
		{"display.direction.ltr", func(c *config.Settings, e string) { c.Device.Display.Direction.LTR = one(e) }, placeForms},
		{"agent.read.rooms", func(c *config.Settings, e string) { c.Bridge.Agent.Read.Rooms = one(e) }, placeForms},
		{"agent.read.except", func(c *config.Settings, e string) { c.Bridge.Agent.Read.Except = one(e) }, placeForms},
		{"agent.write.rooms", func(c *config.Settings, e string) { c.Bridge.Agent.Write.Rooms = one(e) }, placeForms},
		{"agent.write.except", func(c *config.Settings, e string) { c.Bridge.Agent.Write.Except = one(e) }, placeForms},
		{"agent.write.send", func(c *config.Settings, e string) { c.Bridge.Agent.Write.Send = one(e) }, placeForms},
		{"filter[0].in", func(c *config.Settings, e string) {
			c.Bridge.Filters = []config.Filter{{Name: "x", Tag: "Pinned", Words: one("w"), In: one(e)}}
		}, placeForms},
		{"command[0].in", func(c *config.Settings, e string) {
			c.Device.UserCommands = []config.Command{{Name: "zoom", In: one(e)}}
		}, placeForms},
		{"assist.rooms", func(c *config.Settings, e string) { c.Bridge.Assist.Rooms = one(e) }, placeForms},
		{"assist.except", func(c *config.Settings, e string) { c.Bridge.Assist.Except = one(e) }, placeForms},
		{"tracked.rule[0].in", func(c *config.Settings, e string) {
			c.Bridge.Display.Tracked.Rules = []config.TrackedRule{{Words: one("deploy"), In: one(e)}}
		}, placeForms},
		{"tracked.rule[0].except", func(c *config.Settings, e string) {
			c.Bridge.Display.Tracked.Rules = []config.TrackedRule{{Words: one("deploy"), Except: one(e)}}
		}, placeForms},
		{"notifications.rule[0].match", func(c *config.Settings, e string) {
			c.Bridge.Notifications.Rules = []config.Rule{{Match: e, Show: "all"}}
		}, placeForms},
		{"storage.rule[0].match", func(c *config.Settings, e string) {
			c.Bridge.Storage.Rules = []config.StorageRule{{Match: e, Messages: new(10)}}
		}, placeForms},
		{`profile "p": include`, func(c *config.Settings, e string) {
			c.Bridge.Profiles = []config.Profile{{Name: "p", Include: one(e)}}
		}, placeForms},
		{`profile "p": exclude`, func(c *config.Settings, e string) {
			c.Bridge.Profiles = []config.Profile{{Name: "p", Include: []string{"*"}, Exclude: one(e)}}
		}, placeForms},
		{`tag "Busy": rule`, func(c *config.Settings, e string) {
			c.Bridge.Tags = append(c.Bridge.Tags, config.Tag{Name: "Busy", Rule: []string{e}})
		}, append([]string{"unread", "not dm", "*"}, placeForms...)},
	}
}

func cloned(s []string) []string { return append([]string(nil), s...) }

// valid is a config every setting's entries are checked against: the tag the entries
// name exists.
func valid() config.Config {
	return config.Config{Matrix: config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://x", User: "@me:x"}}}, Tags: []config.Tag{{Name: "Pinned"}},
		Profiles: []config.Profile{{Name: "default", Include: []string{"*"}}}}
}

// validate checks both files, as a client does.
func validate(s config.Settings) error {
	if err := setup.Validate(s.Bridge); err != nil {
		return err
	}
	return setup.ValidateDevice(s.Device, s.Bridge)
}

// Every setting naming a place refuses a bare name at load, saying which setting, which
// entry, and the prefixes to choose from; it takes each prefixed form it lists.
func TestEverySettingNamingAPlaceRefusesABareName(t *testing.T) {
	t.Parallel()
	for _, s := range placeSettings() {
		for _, bare := range []string{"Work", "work chat"} {
			cfg := config.Settings{Bridge: valid()}
			s.set(&cfg, bare)
			err := validate(cfg)
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
			cfg := config.Settings{Bridge: valid()}
			s.set(&cfg, entry)
			if err := validate(cfg); err != nil {
				t.Errorf("%s: %q refused: %v", s.what, entry, err)
			}
		}
	}
}

// A setting holding rail rows takes no room-only form, and filing_spaces no tag.
func TestAPlaceSettingRefusesAFormItCannotUse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		set   func(*config.Settings)
		entry string
	}{
		{func(c *config.Settings) { c.Bridge.Display.Priority = []string{"room:Standup"} }, "room:Standup"},
		{func(c *config.Settings) { c.Bridge.Display.Rail.Order = []string{mx + "!abc:example.org"} }, mx + "!abc:example.org"},
		{func(c *config.Settings) { c.Bridge.Display.Rail.Hidden = []string{"dm"} }, "dm"},
		{func(c *config.Settings) { c.Device.Display.FilingSpaces = []string{"tag:Pinned"} }, "tag:Pinned"},
		{func(c *config.Settings) { c.Device.Display.ReadRules = []config.ReadRule{{Match: "room:Standup"}} }, "room:Standup"},
		{func(c *config.Settings) { c.Bridge.Display.Priority = []string{"place:"} }, "place:"},
		{func(c *config.Settings) { c.Device.Keys.Jump = config.Jumps{{Chord: "g w", Target: "Work"}} }, "Work"},
	} {
		cfg := config.Settings{Bridge: valid()}
		c.set(&cfg)
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), `"`+c.entry+`"`) {
			t.Errorf("%q: Validate = %v, want it refused", c.entry, err)
		}
	}
}

// What kith writes into a fresh config passes its own check: the starter.
func TestTheStarterNamesPlacesWithPrefixes(t *testing.T) {
	t.Parallel()
	cfg, err := config.Decode(config.Annotated())
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Validate(cfg); err != nil {
		t.Errorf("the starter config is refused: %v", err)
	}
}
