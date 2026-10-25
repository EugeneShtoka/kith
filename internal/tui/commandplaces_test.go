package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A command's `in` is enforced at every entry point: typed, its key, and the `/` list.
// The generator covers every command with zero, one or two place entries (case and
// slash variants of its block's name included) in every kind of room, and checks each
// entry point against an oracle written out by hand rather than computed by the matcher.

// placeRooms are one room of each kind the entries tell apart.
var placeRooms = []domain.Room{
	{ID: mx + "!s:x", Name: "Standup", Network: domain.ProtocolSlack},
	{ID: mx + "!w:x", Name: "Dana", Network: domain.ProtocolWhatsApp, IsDirect: true},
	{ID: mx + "!m:x", Name: "Hall"},
}

// placeOracle is, per entry, the rooms it names.
var placeOracle = map[string][]domain.RoomID{
	"protocol:slack":    {mx + "!s:x"},
	"PROTOCOL:Slack":    {mx + "!s:x"},
	"protocol:whatsapp": {mx + "!w:x"},
	"dm":                {mx + "!w:x"},
	"group":             {mx + "!s:x", mx + "!m:x"},
	"room:Hall":         {mx + "!m:x"},
	mx + "!m:x":         {mx + "!m:x"},
}

type placedCommand struct {
	file string // the executable, and what is typed after the slash
	key  string // its second key after "z"
	in   []string
}

// placedCommands is every command with up to two entries.
func placedCommands() []placedCommand {
	entries := slices.Sorted(func(yield func(string) bool) {
		for e := range placeOracle {
			if !yield(e) {
				return
			}
		}
	})
	sets := [][]string{nil}
	for i := range entries {
		sets = append(sets, []string{entries[i]})
		for j := i + 1; j < len(entries); j++ {
			sets = append(sets, []string{entries[i], entries[j]})
		}
	}
	var keys []rune
	for r := '0'; r <= 'x'; r++ {
		if (r <= '9' || r >= 'a') && r != 'h' && r != 'o' { // z h and z o are built-ins
			keys = append(keys, r)
		}
	}
	out := make([]placedCommand, 0, len(sets))
	for i, in := range sets {
		out = append(out, placedCommand{file: fmt.Sprintf("c%02d", i), key: string(keys[i]), in: in})
	}
	return out
}

// allowed is the oracle: no entries is everywhere, else some entry names the room.
func (c placedCommand) allowed(room domain.RoomID) bool {
	if len(c.in) == 0 {
		return true
	}
	return slices.ContainsFunc(c.in, func(e string) bool { return slices.Contains(placeOracle[e], room) })
}

// blockName spells the block's name three ways; all mean the file when typed.
// The upper-case spelling gets no key: a key runs the file its block spells, case and all.
func (c placedCommand) blockName(i int) string {
	switch i % 3 {
	case 1:
		return "/" + c.file
	case 2:
		return strings.ToUpper(c.file)
	}
	return c.file
}

// placedModel opens room with every command configured, keys bound, and /me fenced off
// everywhere by a block that must not hide the built-in.
func placedModel(t *testing.T, commands []placedCommand, room domain.Room) (Model, *sendingBackend) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	blocks := []config.Command{{Name: "me", In: []string{"room:nowhere"}}}
	for i, c := range commands {
		write(t, dir, c.file, "#!/bin/sh\necho ran\n", 0o755)
		block := config.Command{Name: c.blockName(i), In: c.in}
		if placedKeyed(i) {
			block.Keys = "z " + c.key
		}
		blocks = append(blocks, block)
	}
	backend := &sendingBackend{}
	m := sized(t, update(t, starterNew(backend, config.Display{}), roomsMsg{rooms: placeRooms}))
	m, _ = m.selectRoom(room)
	m = m.WithConfigFile(filepath.Join(home, "config.toml"),
		config.Config{Commands: config.Commands{Dir: dir, Timeout: 5}, UserCommands: blocks}).keptIn()
	if len(m.keys.issues) > 0 {
		t.Fatalf("the fixture's keys collide: %v", m.keys.issues)
	}
	m.focus, m.compose.insertMode = paneTimeline, false
	return m.clearStatus(), backend
}

func placedKeyed(i int) bool { return i%3 != 2 }

func TestACommandRunsOnlyInItsPlaces(t *testing.T) {
	t.Parallel()

	commands := placedCommands()
	for _, room := range placeRooms {
		base, backend := placedModel(t, commands, room)
		for i, c := range commands {
			want := c.allowed(room.ID)
			slash := "/" + c.file

			// Typed: run, or refused with its places named and nothing sent.
			m, _ := press(t, base, keyText("i"))
			m = typeInto(t, m, slash)
			m, cmd := press(t, m, sendKey())
			if ran := m.running == slash; ran != want {
				t.Errorf("%s typed in %s (in %v): ran = %v, want %v", slash, room.Name, c.in, ran, want)
			}
			if !want {
				deliver(t, m, cmd)
				if len(backend.sent) > 0 {
					t.Fatalf("%s typed in %s (in %v) was sent as text: %+v", slash, room.Name, c.in, backend.sent)
				}
				if !strings.Contains(m.status(), "only for") || !strings.Contains(m.status(), c.in[0]) {
					t.Errorf("%s typed in %s: status = %q, want its places named", slash, room.Name, m.status())
				}
			}

			// Its key: runs, or does nothing at all.
			if !placedKeyed(i) {
				continue
			}
			k, _ := press(t, base, keyText("z"))
			k, _ = press(t, k, keyText(c.key))
			if ran := k.running == slash; ran != want {
				t.Errorf("%s by key in %s (in %v): ran = %v, want %v", slash, room.Name, c.in, ran, want)
			}
			if !want && k.status() != "" {
				t.Errorf("%s by key in %s: status = %q, want nothing", slash, room.Name, k.status())
			}
		}

		// The `/` list: exactly the commands allowed here, and the built-in /me.
		m, _ := press(t, base, keyText("i"))
		m, scan := press(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
		m = deliver(t, m, scan)
		var offered []string
		for _, cand := range m.completion.candidates {
			offered = append(offered, cand.text)
		}
		for _, c := range commands {
			if got := slices.Contains(offered, "/"+c.file); got != c.allowed(room.ID) {
				t.Errorf("/%s offered in %s (in %v) = %v, want %v", c.file, room.Name, c.in, got, c.allowed(room.ID))
			}
		}
		if !slices.Contains(offered, "/me") {
			t.Errorf("a [[command]] block named me hid the built-in /me in %s", room.Name)
		}
	}
}

// A block's `in` never fences off the built-in of the same name.
func TestABlockNamedLikeABuiltInLeavesItRunning(t *testing.T) {
	t.Parallel()

	m, backend := placedModel(t, nil, placeRooms[0])
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/me waves")
	m, cmd := press(t, m, sendKey())
	deliver(t, m, cmd)
	if len(backend.sent) != 1 || !backend.sent[0].Emote {
		t.Errorf("sent %+v, want the built-in emote", backend.sent)
	}
}

// `?` says where a keyed command applies.
func TestHelpSaysWhereACommandApplies(t *testing.T) {
	t.Parallel()

	m, _ := placedModel(t, []placedCommand{{file: "zoom", key: "q", in: []string{"protocol:slack"}}}, placeRooms[0])
	help := strings.Join(m.scriptHelp(12), "\n")
	if !strings.Contains(help, "/zoom") || !strings.Contains(help, "only in protocol:slack") {
		t.Errorf("help = %q, want /zoom with its places", help)
	}
}
