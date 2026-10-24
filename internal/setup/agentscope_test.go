package setup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func agentWith(readRooms, readExcept, writeRooms, send []string) config.Agent {
	return config.Agent{
		Read:  config.AgentRead{Rooms: readRooms, Except: readExcept},
		Write: config.AgentWrite{Rooms: writeRooms, Send: send},
	}
}

// An empty read list shares nothing; an empty write list means whatever reading allows.
func TestAgentScopesReadLeastPrivilegeWriteSameAsRead(t *testing.T) {
	t.Parallel()

	room := domain.RoomFacts{ID: mx + "!a:x", Name: "Standup", Spaces: []string{"Work"}}
	allowed := func(scope domain.ModelScope) bool {
		return domain.AllowModel(scope, room, "configured", false).Allowed
	}
	if allowed(setup.AgentReadScope(config.Agent{})) {
		t.Error("an empty [agent.read] shared a room")
	}
	if !allowed(setup.AgentWriteScope(config.Agent{})) {
		t.Error("an empty [agent.write] refused a room — it should defer to reading")
	}
	withWork := agentWith([]string{"space:Work"}, []string{"room:Standup"}, nil, nil)
	if allowed(setup.AgentReadScope(withWork)) {
		t.Error("[agent.read] except did not subtract from rooms")
	}
}

// Reading a space and sending to a room ID is undecidable without rooms: no warning.
func TestStaticWarningsStaySilentWhenUndecidable(t *testing.T) {
	t.Parallel()

	cases := map[string]config.Agent{
		"room ID sent, space read":   agentWith([]string{"space:Work"}, nil, nil, []string{mx + "!dbg:x"}),
		"same entry in both":         agentWith([]string{"room:Notes"}, nil, []string{"room:Notes"}, []string{"room:notes"}),
		"id listed with room: form":  agentWith([]string{"room:" + mx + "!a:x"}, nil, nil, []string{mx + "!a:x"}),
		"room name, ids read":        agentWith([]string{mx + "!a:x"}, nil, nil, []string{"room:Notes"}),
		"dm written, protocol read":  agentWith([]string{"protocol:WhatsApp"}, nil, []string{"dm"}, nil),
		"nothing written":            agentWith(nil, nil, nil, nil),
		"space written, other space": agentWith([]string{"space:A"}, nil, []string{"space:B"}, nil),
	}
	for name, agent := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := setup.AgentStaticWarnings(agent); len(got) != 0 {
				t.Errorf("warned where it cannot decide: %q", got)
			}
		})
	}
}

// Entries ruled out by spelling are warned about, naming the list and entry.
func TestStaticWarningsNameDeadEntries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		agent config.Agent
		entry string
	}{
		{"nothing read", agentWith(nil, nil, nil, []string{mx + "!a:x"}), "[agent.read] rooms is empty"},
		{"excepted from reading", agentWith([]string{"group"}, []string{"room:HR"}, []string{"room:hr"}, nil), `[agent.write] rooms names "room:hr"`},
		{"an id reading does not list", agentWith([]string{mx + "!a:x", "room:" + mx + "!b:x"}, nil, nil, []string{mx + "!c:x"}), `[agent.write] send names "` + mx + `!c:x"`},
		{"another network", agentWith([]string{"protocol:Slack"}, nil, []string{"protocol:WhatsApp"}, nil), `"protocol:WhatsApp"`},
		{"dm while only groups are read", agentWith([]string{"group"}, nil, nil, []string{"dm"}), `send names "dm"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := setup.AgentStaticWarnings(tc.agent)
			if len(got) != 1 || !strings.Contains(got[0], tc.entry) {
				t.Fatalf("warnings = %q, want one containing %q", got, tc.entry)
			}
		})
	}
}

// places is a room list to resolve against.
type places struct {
	rooms     []domain.Room
	spaces    []domain.Space
	encrypted map[domain.RoomID]bool
	err       error
}

func (p places) Rooms(context.Context) ([]domain.Room, error)   { return p.rooms, p.err }
func (p places) Spaces(context.Context) ([]domain.Space, error) { return p.spaces, nil }
func (p places) RoomEncryption(context.Context, []domain.RoomID) (map[domain.RoomID]bool, error) {
	return p.encrypted, nil
}

func account() places {
	return places{
		rooms: []domain.Room{
			{ID: mx + "!dbg:x", Name: "Notes"},
			{ID: mx + "!ops:x", Name: "Ops"},
			{ID: mx + "!home:x", Name: "Family"},
			{ID: mx + "!dm:x", Name: "Dana", IsDirect: true},
		},
		spaces: []domain.Space{{ID: mx + "!s:x", Name: "Work", Children: []domain.RoomID{mx + "!dbg:x", mx + "!ops:x"}}},
	}
}

// A send room ID inside a read space stays silent, resolved or not.
func TestResolvedWarningsSilentForARoomInAReadSpace(t *testing.T) {
	t.Parallel()

	agent := agentWith([]string{"space:Work"}, nil, nil, []string{mx + "!dbg:x"})
	if got := setup.AgentWarnings(t.Context(), account(), domain.Places{}, agent); len(got) != 0 {
		t.Fatalf("warned about a readable room: %q", got)
	}
}

// Resolved, an entry matching rooms outside reading is reported with examples.
func TestResolvedWarningsNameRoomsOutsideReading(t *testing.T) {
	t.Parallel()

	agent := agentWith([]string{"space:Work"}, nil, []string{"group"}, []string{mx + "!home:x", mx + "!ops:x"})
	got := setup.AgentWarnings(t.Context(), account(), domain.Places{}, agent)
	if len(got) != 2 {
		t.Fatalf("warnings = %q, want two: group and !home:x", got)
	}
	if !strings.Contains(got[0], `rooms names "group"`) || !strings.Contains(got[0], "1 of the 3") || !strings.Contains(got[0], "Family") {
		t.Errorf("group warning = %q", got[0])
	}
	if !strings.Contains(got[1], `send names "`+mx+`!home:x"`) || !strings.Contains(got[1], "Family") {
		t.Errorf("send warning = %q", got[1])
	}
}

// An encrypted room reading cannot open is as unwritable as one outside its lists.
func TestResolvedWarningsNameEncryptedRooms(t *testing.T) {
	t.Parallel()

	p := account()
	p.encrypted = map[domain.RoomID]bool{mx + "!dbg:x": true}
	got := setup.AgentWarnings(t.Context(), p, domain.Places{}, agentWith([]string{"space:Work"}, nil, nil, []string{mx + "!dbg:x"}))
	if len(got) != 1 || !strings.Contains(got[0], "[agent.read] encrypted is false") {
		t.Fatalf("warnings = %q, want the encryption named", got)
	}
}

// Without rooms (no backend, a failing one, no match) the static check applies.
func TestResolvedWarningsFallBackToStatic(t *testing.T) {
	t.Parallel()

	agent := agentWith([]string{mx + "!a:x"}, nil, nil, []string{mx + "!gone:x"})
	for name, src := range map[string]setup.AgentPlaces{
		"no backend":       nil,
		"failing backend":  places{err: errors.New("down")},
		"matches no rooms": account(),
	} {
		got := setup.AgentWarnings(t.Context(), src, domain.Places{}, agent)
		if len(got) != 1 || !strings.Contains(got[0], `"`+mx+`!gone:x"`) {
			t.Errorf("%s: warnings = %q, want the static one", name, got)
		}
	}
}

// A prefixed room ID is on its network without a bridge space; a bare one is Matrix.
func TestFactsOfANativeRoom(t *testing.T) {
	t.Parallel()

	native := domain.Room{ID: domain.RoomID("whatsapp:44000000001/120363000000000001@g.us")}
	if got := (domain.Places{}).Facts(native, nil).Protocol; got != domain.ProtocolWhatsApp {
		t.Errorf("Facts(native).Protocol = %q", got)
	}
	if got := (domain.Places{}).Facts(domain.Room{ID: mx + "!a:x"}, nil).Protocol; got != domain.ProtocolMatrix {
		t.Errorf("Facts(!a:x).Protocol = %q", got)
	}
}

// A display name may be given to a native room by its ID.
func TestANativeRoomCanBeNamed(t *testing.T) {
	t.Parallel()

	target := "whatsapp:44000000001/120363000000000001@g.us"
	if err := setup.NameTargets(config.Display{Names: []config.DisplayName{{Target: target, Name: "Choir"}}}); err != nil {
		t.Errorf("NameTargets refused a native room ID: %v", err)
	}
}
