package tui

import (
	"context"
	"fmt"
	"image/color"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/theme"
)

// A name drawn in its own color lays out exactly as one drawn in the row's: over
// names short and long, wide and right-to-left, with and without a lead, a trail and
// a badge, in rows wide and too narrow for the name, selected or not.
func TestAColoredNameLaysOutAsAPlainOne(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), apitest.Nop{}, config.Display{})
	green, _ := theme.ParseColor("green")
	r := rand.New(rand.NewPCG(1, 2))
	names := []string{"Dana", "A rather long room name that will not fit", "שלום עולם", "日本語のチャット", "🇧🇬 Болгария", ""}
	for i := range 500 {
		label := rowLabel{name: names[r.IntN(len(names))]}
		if r.IntN(2) == 0 {
			label.lead = inviteMark
		}
		if r.IntN(2) == 0 {
			label.trail = draftMark
		}
		badge := ""
		if r.IntN(2) == 0 {
			badge = "●12"
		}
		inner, selected, active := 8+r.IntN(40), r.IntN(2) == 0, r.IntN(2) == 0
		plain := m.listRow(label, badge, false, inner, selected, active)
		label.color = green
		colored := m.listRow(label, badge, false, inner, selected, active)
		if ansi.Strip(plain) != ansi.Strip(colored) {
			t.Fatalf("case %d: %q (badge %q, inner %d)\nplain   %q\ncolored %q", i, label.name, badge, inner, ansi.Strip(plain), ansi.Strip(colored))
		}
		if label.name != "" && !strings.Contains(colored, "38;2;58;217;0") { // green, #3ad900
			t.Fatalf("case %d: the name is not drawn in its color: %q", i, colored)
		}
	}
}

// A place that asks for it names each room in its network's color, by its facts (a
// bridged Matrix room's bridge's), as configured or by default, none for
// a network with none; a place that does not, and every room elsewhere, keep the row's
// color. The rail, when asked, colors a space or tag whose rooms are all on one
// network, and no other.
func TestRoomsAreNamedInTheirNetworksColors(t *testing.T) {
	t.Parallel()
	rooms := []domain.Room{
		{ID: "whatsapp:1/g@g.us", Name: "Family"},
		{ID: "telegram:1/-11", Name: "Book club"},
		{ID: "!bridged:x", Name: "Bridged"},
		{ID: "!plain:x", Name: "Plain"},
	}
	display := config.Display{
		SpaceRules: []config.SpaceRule{{Space: "Mixed", NetworkColors: new(true)}, {Space: "Only WhatsApp", NetworkColors: new(false)}},
		Theme:      config.Theme{Networks: config.NetworkColors{Telegram: "orange", Matrix: "none"}},
		Rail:       config.Rail{NetworkColors: true},
	}
	m := update(t, New(context.Background(), apitest.Nop{}, display), roomsMsg{rooms: rooms})
	m = sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!mixed:x", Name: "Mixed", Children: []domain.RoomID{"whatsapp:1/g@g.us", "telegram:1/-11", "!bridged:x", "!plain:x"}},
		{ID: "!quiet:x", Name: "Quiet", Children: []domain.RoomID{"whatsapp:1/g@g.us", "telegram:1/-11"}},
		{ID: "!onlywa:x", Name: "Only WhatsApp", Children: []domain.RoomID{"whatsapp:1/g@g.us"}},
		{ID: "!slack:x", Name: "Workspace", Bridge: domain.ProtocolSlack, Children: []domain.RoomID{"!bridged:x"}},
	}}))
	color := func(name string) string {
		c, _ := theme.ParseColor(name)
		if c == nil {
			return ""
		}
		r, g, b, _ := c.RGBA()
		return strings.Join([]string{string(rune(r)), string(rune(g)), string(rune(b))}, ",")
	}
	got := func(m Model, room domain.Room) string {
		c := m.roomNameColor(room)
		if c == nil {
			return ""
		}
		r, g, b, _ := c.RGBA()
		return strings.Join([]string{string(rune(r)), string(rune(g)), string(rune(b))}, ",")
	}
	m.rail.cursor = indexOfGroup(m.rail.groups, "Mixed")
	for i, want := range []string{color("teal"), color("orange"), color("magenta"), ""} {
		if g := got(m, rooms[i]); g != want {
			t.Errorf("in Mixed, %s's color = %v, want %v", rooms[i].Name, g, want)
		}
	}
	m.rail.cursor = indexOfGroup(m.rail.groups, "Quiet")
	for i := range rooms {
		if got(m, rooms[i]) != "" {
			t.Errorf("in Quiet, which does not ask, %s is colored", rooms[i].Name)
		}
	}
	// Everywhere: a place with no rule of its own follows, one that says off does not.
	everywhere := m
	everywhere.prefs.display.NetworkColors = true
	if g := got(everywhere, rooms[1]); g != color("orange") {
		t.Errorf("with every room list asking, Quiet's %s = %v, want orange", rooms[1].Name, g)
	}
	everywhere.rail.cursor = indexOfGroup(everywhere.rail.groups, "Only WhatsApp")
	if g := got(everywhere, rooms[0]); g != "" {
		t.Errorf("Only WhatsApp says off, yet %s is colored", rooms[0].Name)
	}
	networks := map[string]domain.Protocol{}
	for _, g := range m.rail.groups {
		networks[g.key] = g.network
	}
	// Drawn: the rail row of a one-network space is in its color while the rail asks,
	// and in the row's own when it does not.
	only := m.rail.groups[indexOfGroup(m.rail.groups, "Only WhatsApp")]
	if row := m.railRow(only, false, false, 20); !strings.Contains(row, "38;2;29;233;182") {
		t.Errorf("Only WhatsApp's rail row is not teal: %q", row)
	}
	off := m
	off.prefs.display.Rail.NetworkColors = false
	if row := off.railRow(only, false, false, 20); strings.Contains(row, "38;2;29;233;182") {
		t.Errorf("with the rail not asking, Only WhatsApp's row is green: %q", row)
	}
	if networks["Only WhatsApp"] != domain.ProtocolWhatsApp || networks["Workspace"] != domain.ProtocolSlack ||
		networks["Quiet"] != "" || networks["Mixed"] != "" {
		t.Errorf("the rail's networks = %v, want Only WhatsApp's and Workspace's alone", networks)
	}
}

// A place's two settings share its [[display.space_rule]]: setting one keeps the
// other, and the rule goes only when neither is set. Network colors set off is set: it
// overrides [display] network_colors.
func TestAPlacesSwitchesKeepEachOther(t *testing.T) {
	t.Parallel()
	rules := []config.SpaceRule{{Space: "Other", FirstNameOnly: true}}
	rules = withFirstNames(rules, "Friends", true)
	rules = withNetworkColors(rules, "friends", new(true)) // the place, in any case
	c := config.Config{Display: config.Display{SpaceRules: rules}}
	if !firstNamesIn(c, "Friends") || !c.Display.NetworkColorsIn("Friends") || len(rules) != 2 {
		t.Fatalf("both on: %+v", rules)
	}
	rules = withFirstNames(rules, "Friends", false)
	c.Display.SpaceRules = rules
	if firstNamesIn(c, "Friends") || !c.Display.NetworkColorsIn("Friends") {
		t.Fatalf("first names off took the colors with it: %+v", rules)
	}
	rules = withNetworkColors(rules, "Friends", new(false))
	c.Display.SpaceRules, c.Display.NetworkColors = rules, true
	if c.Display.NetworkColorsIn("Friends") || len(rules) != 2 {
		t.Fatalf("off is not kept against [display] network_colors: %+v", rules)
	}
	rules = withNetworkColors(rules, "Friends", nil)
	if len(rules) != 1 || rules[0].Space != "Other" || !rules[0].FirstNameOnly {
		t.Fatalf("both off: %+v, want only Other's rule", rules)
	}
}

// A room's thread rows, and the row counting the threads left out, are drawn in the
// room's network color, so a room and its threads read as one; in a place that does
// not ask, none of them is.
func TestAThreadRowTakesItsRoomsColor(t *testing.T) {
	t.Parallel()
	room := domain.Room{ID: "telegram:1/-11", Name: "Book club"}
	display := config.Display{
		NetworkColors: true,
		Theme:         config.Theme{Networks: config.NetworkColors{Telegram: "orange"}},
	}
	m := update(t, New(context.Background(), apitest.Nop{}, display), roomsMsg{rooms: []domain.Room{room}})
	m = sized(t, m)
	orange := sgrOf(t, m.roomNameColor(room))
	rows := []roomRow{
		{room: room},
		{room: room, thread: domain.ThreadUnread{Root: "$root", Unread: 1}},
		{room: room, more: 3},
	}
	for _, r := range rows {
		if line := m.roomListLine(r, 30, false, true); !strings.Contains(line, orange) {
			t.Errorf("row %+v is not in its room's color: %q", r, line)
		}
	}
	off := m
	off.prefs.display.NetworkColors = false
	for _, r := range rows {
		if line := off.roomListLine(r, 30, false, true); strings.Contains(line, orange) {
			t.Errorf("with network colors off, row %+v is colored: %q", r, line)
		}
	}
}

// sgrOf is the truecolor foreground sequence c is drawn with.
func sgrOf(t *testing.T, c color.Color) string {
	t.Helper()
	if c == nil {
		t.Fatal("no color")
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}
