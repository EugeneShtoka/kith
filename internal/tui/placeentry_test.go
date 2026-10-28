package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// placeSpaces are a space renamed Desk, a Telegram forum, a Slack workspace, and a space
// named like the DMs tag.
var placeSpaces = []domain.Space{
	{ID: mx + "!w:x", Name: "Work"},
	{ID: mx + "!b:x", Name: "Board", Bridge: domain.ProtocolTelegram, Leaving: domain.LeftWhole},
	{ID: mx + "!a:x", Name: "Acme", Bridge: domain.ProtocolSlack, Leaving: domain.LeftBySigningOut},
	{ID: mx + "!d:x", Name: "DMs"},
}

var placeNames = []config.DisplayName{
	{Target: config.NameTargetSpace + "Work", Name: "Desk"},
	{Target: config.NameTargetSpace + "Board", Name: "Club"},
}

// The rail's order, hidden and hide_when_empty name rows only by prefix: place: every row
// shown so (label or name, any case; several placed together, in rail order), space: and
// tag: their own kind by name; a bare name nothing.
func TestRailEntriesNameRowsOnlyByPrefix(t *testing.T) {
	t.Parallel()
	three := threeTags(t)
	rail := func(r config.Rail) []string {
		return keys(railGroups(placeSpaces, r, placeNames, three, nil))
	}
	// Unnamed: tags, then spaces.
	def := []string{"tag:All", "tag:DMs", "tag:Unread", "Work", "Board", "Acme", "DMs"}
	for _, c := range []struct {
		order []string
		want  []string
	}{
		{[]string{"place:dms", "*"}, []string{"tag:DMs", "DMs", "tag:All", "tag:Unread", "Work", "Board", "Acme"}},
		{[]string{"space:dms", "*"}, []string{"DMs", "tag:All", "tag:DMs", "tag:Unread", "Work", "Board", "Acme"}},
		{[]string{"tag:DMs", "*"}, []string{"tag:DMs", "tag:All", "tag:Unread", "Work", "Board", "Acme", "DMs"}},
		{[]string{"place:DESK", "place:club", "place:Acme", "*"}, []string{"Work", "Board", "Acme", "tag:All", "tag:DMs", "tag:Unread", "DMs"}},
		{[]string{"place:Board", "*"}, []string{"Board", "tag:All", "tag:DMs", "tag:Unread", "Work", "Acme", "DMs"}},
		{[]string{"space:Desk", "*"}, def}, // space: reads the name
		{[]string{"DMs", "Work", "*"}, def},
		{[]string{"unread", "*"}, def},
	} {
		if got := rail(config.Rail{Order: c.order}); !slices.Equal(got, c.want) {
			t.Errorf("order %q = %v, want %v", c.order, got, c.want)
		}
	}
	for _, c := range []struct {
		hidden []string
		gone   []string
	}{
		{[]string{"place:dms"}, []string{"tag:DMs", "DMs"}},
		{[]string{"space:DMs"}, []string{"DMs"}},
		{[]string{"tag:dms"}, []string{"tag:DMs"}},
		{[]string{"place:desk", "place:CLUB"}, []string{"Work", "Board"}},
		{[]string{"DMs", "Desk"}, nil},
	} {
		got := rail(config.Rail{Hidden: c.hidden})
		want := slices.DeleteFunc(slices.Clone(def), func(k string) bool { return slices.Contains(c.gone, k) })
		if !slices.Equal(got, want) {
			t.Errorf("hidden %q = %v, want %v", c.hidden, got, want)
		}
	}
	// A divider after a place: entry follows the last row it placed.
	sep := railGroups(placeSpaces, config.Rail{Order: []string{"place:DMs", "-", "*"}}, placeNames, three, nil)
	for _, g := range sep {
		if g.sepAfter != (g.key == "DMs") {
			t.Errorf("divider on %q = %t, want it after the space DMs alone", g.key, g.sepAfter)
		}
	}
	// hide_when_empty names rows as order does: every room is elsewhere, so each row it
	// names is empty.
	rooms := []domain.Room{{ID: mx + "!r:x", Name: "Elsewhere"}}
	for _, c := range []struct {
		entries []string
		gone    []string
	}{
		{[]string{"place:Desk"}, []string{"Work"}},
		{[]string{"space:DMs"}, []string{"DMs"}},
		{[]string{"Desk", "Acme"}, nil},
	} {
		got := keys(railGroups(placeSpaces, config.Rail{HideWhenEmpty: c.entries}, placeNames, three, rooms))
		for _, k := range def {
			if slices.Contains(got, k) == slices.Contains(c.gone, k) {
				t.Errorf("hide_when_empty %q: %s shown = %t", c.entries, k, slices.Contains(got, k))
			}
		}
	}
}

func keys(groups []group) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.key
	}
	return out
}

// A jump target names a rail row by prefix: place: the first row shown so, space: and
// tag: their own kind.
func TestAJumpNamesARailRowByPrefix(t *testing.T) {
	t.Parallel()
	m := configured(config.Config{Display: config.Display{Names: placeNames}})
	m = update(t, m, spacesMsg{spaces: placeSpaces})
	for _, c := range []struct{ target, want string }{
		{"place:desk", "Work"},
		{"place:Club", "Board"},
		{"space:Work", "Work"},
		{"space:Desk", ""},
		{"Desk", ""},
		{"Work", ""},
	} {
		got, _ := m.groupNamed(c.target)
		if got != c.want {
			t.Errorf("%q goes to %q, want %q", c.target, got, c.want)
		}
	}
}

// :shortcut on a rail row writes it by its kind: a tag as tag:<name>, a space as
// space:<name>, so the binding reads back to the same row.
func TestAShortcutWritesARailRowByItsKind(t *testing.T) {
	t.Parallel()
	m := configured(config.Config{Tags: []config.Tag{{Name: "Pinned"}}, Display: config.Display{Names: placeNames}})
	m = update(t, m, spacesMsg{spaces: placeSpaces})
	m.focus = paneRail
	for _, c := range []struct{ key, want string }{{"tag:Pinned", "tag:Pinned"}, {"Work", "space:Work"}} {
		m.rail.cursor = indexOfGroup(m.rail.groups, c.key)
		targets := m.bindingTargets(nil)
		if len(targets) == 0 || targets[0].String() != c.want {
			t.Fatalf("on %s: targets %+v, want %s first", c.key, targets, c.want)
		}
		if got, _ := m.groupNamed(targets[0].String()); got != c.key {
			t.Errorf("%s reads back to %q, want %q", c.want, got, c.key)
		}
	}
}

// The settings screen's list editors refuse a bare entry as the config does, saying so,
// and take a prefixed one.
func TestASettingsListRefusesABareName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		key  string
		read func(config.Settings) []string
		want string
	}{
		{"display.priority", func(c config.Settings) []string { return c.Bridge.Display.Priority },
			`display.priority: "Work" needs a prefix: space:, tag:, place:`},
		{"display.rail.hidden", func(c config.Settings) []string { return c.Bridge.Display.Rail.Hidden },
			`display.rail.hidden: "Work" needs a prefix: space:, tag:, place:`},
		{"display.filing_spaces", func(c config.Settings) []string { return c.Device.Display.FilingSpaces },
			`display.filing_spaces: "Work" needs a prefix: space:, place: (or a space's ID)`},
		{"agent.read.rooms", func(c config.Settings) []string { return c.Bridge.Agent.Read.Rooms },
			`agent.read.rooms: "Work" needs a prefix: room:, space:, tag:, place:, protocol: (or a room ID, dm, group)`},
	} {
		m, _ := opened(t, config.Notifications{})
		before := c.read(m.settings())
		m = m.settingEntriesOpen(c.key, settingAdd)
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m = typeIn(t, m, "Work")
		if got := c.read(m.settings()); !slices.Equal(got, before) {
			t.Errorf("%s: a bare name was written: %v", c.key, got)
		}
		if !strings.Contains(m.status(), c.want) {
			t.Errorf("%s: status %q, want %q", c.key, m.status(), c.want)
		}
		m = m.settingEntriesOpen(c.key, settingAdd)
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m = typeIn(t, m, "place:Work")
		if got := c.read(m.settings()); !slices.Contains(got, "place:Work") {
			t.Errorf("%s: place:Work not written: %v", c.key, got)
		}
	}
}
