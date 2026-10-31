package tui

import (
	"context"
	"fmt"
	"image/color"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/theme"
)

// A name in its own color lays out exactly as one in the row's color: across short, long,
// wide and RTL names, with or without lead, trail and badge, wide and narrow rows, selected
// or not.
func TestAColoredNameLaysOutAsAPlainOne(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), apitest.Nop{}, config.Display{}, config.DeviceDisplay{})
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

// A place that asks for it names each room in its network's color (by its facts; a bridged
// room's is its bridge's), configured or default, none for a network without one; other rooms
// keep the row's color. The rail, when asked, colors a space or tag whose rooms are all on one
// network, and no other.
func TestRoomsAreNamedInTheirNetworksColors(t *testing.T) {
	t.Parallel()
	rooms := []domain.Room{
		{ID: "whatsapp:1/g@g.us", Name: "Family"},
		{ID: "telegram:1/-11", Name: "Book club"},
		{ID: mx + "!bridged:x", Name: "Bridged"},
		{ID: mx + "!plain:x", Name: "Plain"},
	}
	display := config.Display{
		SpaceRules: []config.SpaceRule{{Space: "space:Mixed", NetworkColors: new(true)}, {Space: "space:Only WhatsApp", NetworkColors: new(false)}},
		Rail:       config.Rail{NetworkColors: true},
	}
	dev := config.Device{Keys: config.DefaultKeys(), TUI: config.DeviceTUI{Theme: config.Theme{Networks: config.NetworkColors{Telegram: "orange", Matrix: "none"}}}}
	m := update(t, New(context.Background(), apitest.Nop{}, display, dev.Display).WithDevice("", dev), roomsMsg{rooms: rooms})
	m = sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: mx + "!mixed:x", Name: "Mixed", Children: []domain.RoomID{"whatsapp:1/g@g.us", "telegram:1/-11", mx + "!bridged:x", mx + "!plain:x"}},
		{ID: mx + "!quiet:x", Name: "Quiet", Children: []domain.RoomID{"whatsapp:1/g@g.us", "telegram:1/-11"}},
		{ID: mx + "!onlywa:x", Name: "Only WhatsApp", Children: []domain.RoomID{"whatsapp:1/g@g.us"}},
		{ID: mx + "!slack:x", Name: "Workspace", Bridge: domain.ProtocolSlack, Children: []domain.RoomID{mx + "!bridged:x"}},
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
	// Everywhere: a place with no rule follows; one set off does not.
	everywhere := m
	everywhere.prefs.device.NetworkColors = true
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
	// The rail row of a one-network space is in its color while the rail asks, else the row's own.
	only := m.rail.groups[indexOfGroup(m.rail.groups, "Only WhatsApp")]
	if row := m.railRow(only, false, false, 20); !strings.Contains(row, "38;2;29;233;182") {
		t.Errorf("Only WhatsApp's rail row is not teal: %q", row)
	}
	off := m
	off.rail.cfg.NetworkColors = false
	if row := off.railRow(only, false, false, 20); strings.Contains(row, "38;2;29;233;182") {
		t.Errorf("with the rail not asking, Only WhatsApp's row is green: %q", row)
	}
	if networks["Only WhatsApp"] != domain.ProtocolWhatsApp || networks["Workspace"] != domain.ProtocolSlack ||
		networks["Quiet"] != "" || networks["Mixed"] != "" {
		t.Errorf("the rail's networks = %v, want Only WhatsApp's and Workspace's alone", networks)
	}
}

// A place's two settings share its [[display.space_rule]]: setting one keeps the other, and the
// rule goes only when neither is set. network_colors off counts as set and overrides [display].
func TestAPlacesSwitchesKeepEachOther(t *testing.T) {
	t.Parallel()
	d := config.Display{SpaceRules: []config.SpaceRule{{Space: "space:Other", FirstNameOnly: true}}}
	friends := setup.HomeNamer(d, "Friends")
	d.SpaceRules = withFirstNames(d, "Friends", true)
	d.SpaceRules = withNetworkColors(d, "Friends", new(true))
	c := config.Settings{Bridge: config.Config{Display: d}}
	if !firstNamesIn(c, "Friends") || !c.Bridge.Display.NetworkColorsIn(c.Device.Display, friends) || len(d.SpaceRules) != 2 ||
		d.SpaceRules[1].Space != "space:Friends" {
		t.Fatalf("both on: %+v, want one space:Friends rule", d.SpaceRules)
	}
	d.SpaceRules = withFirstNames(d, "Friends", false)
	c.Bridge.Display = d
	if firstNamesIn(c, "Friends") || !c.Bridge.Display.NetworkColorsIn(c.Device.Display, friends) {
		t.Fatalf("first names off took the colors with it: %+v", d.SpaceRules)
	}
	d.SpaceRules = withNetworkColors(d, "Friends", new(false))
	c.Device.Display.NetworkColors = true
	c.Bridge.Display = d
	if c.Bridge.Display.NetworkColorsIn(c.Device.Display, friends) || len(d.SpaceRules) != 2 {
		t.Fatalf("off is not kept against [display] network_colors: %+v", d.SpaceRules)
	}
	d.SpaceRules = withNetworkColors(d, "Friends", nil)
	if len(d.SpaceRules) != 1 || d.SpaceRules[0].Space != "space:Other" || !d.SpaceRules[0].FirstNameOnly {
		t.Fatalf("both off: %+v, want only Other's rule", d.SpaceRules)
	}
}

// A room's thread rows and its overflow row take the room's network color; in a place that does
// not ask, none do.
func TestAThreadRowTakesItsRoomsColor(t *testing.T) {
	t.Parallel()
	room := domain.Room{ID: "telegram:1/-11", Name: "Book club"}
	dev := config.Device{
		Keys:    config.DefaultKeys(),
		Display: config.DeviceDisplay{NetworkColors: true},
		TUI:     config.DeviceTUI{Theme: config.Theme{Networks: config.NetworkColors{Telegram: "orange"}}},
	}
	m := update(t, New(context.Background(), apitest.Nop{}, config.Display{}, dev.Display).WithDevice("", dev), roomsMsg{rooms: []domain.Room{room}})
	m = sized(t, m)
	orange := sgrOf(t, m.roomNameColor(room))
	rows := []roomRow{
		{room: room},
		{room: room, thread: domain.ThreadUnread{Root: mx + "$root", Unread: 1}},
		{room: room, more: 3},
	}
	for _, r := range rows {
		if line := m.roomListLine(r, 30, false, true); !strings.Contains(line, orange) {
			t.Errorf("row %+v is not in its room's color: %q", r, line)
		}
	}
	off := m
	off.prefs.device.NetworkColors = false
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

// A device file's [tui.theme] is the window's palette and [tui.theme.networks] its
// network colors, at start and when the device's settings change.
func TestTheWindowDrawsWithTheDeviceFilesTUITheme(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), config.DeviceFile)
	text := "[tui.theme]\npreset = \"gruvbox\"\naccent = \"#ff8800\"\n\n[tui.theme.networks]\ntelegram = \"#123456\"\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	dev, err := config.LoadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	m := starterNew(apitest.Nop{}, config.Display{}, dev.Display).WithDevice(path, dev)
	accent, _ := theme.ParseColor("#ff8800")
	if sgrOf(t, m.theme.Palette.Accent) != sgrOf(t, accent) {
		t.Errorf("accent %v, want the device file's #ff8800", m.theme.Palette.Accent)
	}
	gruvbox, _ := theme.Resolve("gruvbox", nil)
	if sgrOf(t, m.theme.Palette.Border) != sgrOf(t, gruvbox.Border) {
		t.Errorf("border %v, want gruvbox's %v", m.theme.Palette.Border, gruvbox.Border)
	}
	telegram, _ := theme.ParseColor("#123456")
	if sgrOf(t, m.networkColor(domain.ProtocolTelegram)) != sgrOf(t, telegram) {
		t.Errorf("Telegram's color %v, want the device file's #123456", m.networkColor(domain.ProtocolTelegram))
	}

	changed := dev.Clone()
	changed.TUI.Theme.Accent = "#00ff00"
	changed.TUI.Theme.Networks.Telegram = "none"
	m, _ = m.applyDevice(changed, "")
	green, _ := theme.ParseColor("#00ff00")
	if sgrOf(t, m.theme.Palette.Accent) != sgrOf(t, green) || m.networkColor(domain.ProtocolTelegram) != nil {
		t.Errorf("after the change: accent %v, Telegram %v; want #00ff00 and none", m.theme.Palette.Accent, m.networkColor(domain.ProtocolTelegram))
	}
}

// A config applied from the app rebuilds the rail with its networks, keeping a one-network
// space's color.
func TestTheRailKeepsItsColorsWhenTheConfigIsApplied(t *testing.T) {
	t.Parallel()
	display := config.Display{Rail: config.Rail{NetworkColors: true}}
	m := update(t, New(context.Background(), apitest.Nop{}, display, config.DeviceDisplay{}), roomsMsg{rooms: []domain.Room{{ID: "whatsapp:1/g@g.us", Name: "Family"}}})
	m = sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: mx + "!onlywa:x", Name: "Only WhatsApp", Children: []domain.RoomID{"whatsapp:1/g@g.us"}},
	}}))
	network := func(m Model) domain.Protocol {
		return m.rail.groups[indexOfGroup(m.rail.groups, "Only WhatsApp")].network
	}
	if network(m) != domain.ProtocolWhatsApp {
		t.Fatalf("before: Only WhatsApp's network = %q", network(m))
	}
	renamed := m.prefs.display
	renamed.Names = config.SetName(renamed.Names, "whatsapp:1/g@g.us", "Kin")
	m, _ = m.applyDisplay(renamed, "")
	if network(m) != domain.ProtocolWhatsApp {
		t.Errorf("after a room was renamed, Only WhatsApp's network = %q, want WhatsApp", network(m))
	}
}

// A network's own space (an account's, a bridge's) is in that network's color when the
// network has one, before any room is in it and whatever rooms it holds; with no color for
// the network, its rooms decide as for any space. A space you made has no network of its own.
func TestANetworksOwnSpaceTakesItsColor(t *testing.T) {
	t.Parallel()
	display := config.Display{Rail: config.Rail{NetworkColors: true}}
	dev := config.Device{Keys: config.DefaultKeys(), TUI: config.DeviceTUI{Theme: config.Theme{Networks: config.NetworkColors{GoogleMessages: "coral", LinkedIn: "none"}}}}
	rooms := []domain.Room{{ID: "whatsapp:1/g@g.us", Name: "Family"}}
	m := update(t, New(context.Background(), apitest.Nop{}, display, dev.Display).WithDevice("", dev), roomsMsg{rooms: rooms})
	m = sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "gmessages:a/account", Name: "SMS", Bridge: domain.ProtocolGMessages, Original: true},
		{ID: "gmessages:b/account", Name: "SMS mixed", Bridge: domain.ProtocolGMessages, Original: true,
			Children: []domain.RoomID{"whatsapp:1/g@g.us"}},
		{ID: "linkedin:c/account", Name: "LinkedIn", Bridge: domain.ProtocolLinkedIn, Original: true},
		{ID: mx + "!mine:x", Name: "Mine", Bridge: domain.ProtocolMatrix},
	}}))
	networks := map[string]domain.Protocol{}
	for _, g := range m.rail.groups {
		networks[g.key] = g.network
	}
	if networks["SMS"] != domain.ProtocolGMessages || networks["SMS mixed"] != domain.ProtocolGMessages {
		t.Errorf("an account's space with its network colored: %q, %q", networks["SMS"], networks["SMS mixed"])
	}
	if networks["LinkedIn"] != "" || networks["Mine"] != "" {
		t.Errorf("no color for the network, or a space of your own: %q, %q", networks["LinkedIn"], networks["Mine"])
	}
	sms := m.rail.groups[indexOfGroup(m.rail.groups, "SMS")]
	coral, _ := theme.ParseColor("coral")
	r, g, b, _ := coral.RGBA()
	if row := m.railRow(sms, false, false, 20); !strings.Contains(row, fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)) {
		t.Errorf("the empty SMS space's rail row is not coral: %q", row)
	}
}
