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

// Reading is least privilege and writing is not: an empty read list shares nothing, an
// empty write list is "whatever reading allows".
func TestAgentScopesReadLeastPrivilegeWriteSameAsRead(t *testing.T) {
	t.Parallel()

	room := domain.RoomFacts{ID: "!a:x", Name: "Standup", Spaces: []string{"Work"}}
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

// The user's own shape — read a space, send to a room ID — says nothing statically,
// because whether that room is in the space is not decidable without rooms.
func TestStaticWarningsStaySilentWhenUndecidable(t *testing.T) {
	t.Parallel()

	cases := map[string]config.Agent{
		"room ID sent, space read":   agentWith([]string{"space:Work"}, nil, nil, []string{"!dbg:x"}),
		"same entry in both":         agentWith([]string{"room:Notes"}, nil, []string{"room:Notes"}, []string{"room:notes"}),
		"id listed with room: form":  agentWith([]string{"room:!a:x"}, nil, nil, []string{"!a:x"}),
		"room name, ids read":        agentWith([]string{"!a:x"}, nil, nil, []string{"room:Notes"}),
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

// What the spelling alone rules out is said, naming the list and the entry.
func TestStaticWarningsNameDeadEntries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		agent config.Agent
		entry string
	}{
		{"nothing read", agentWith(nil, nil, nil, []string{"!a:x"}), "[agent.read] rooms is empty"},
		{"excepted from reading", agentWith([]string{"group"}, []string{"room:HR"}, []string{"room:hr"}, nil), `[agent.write] rooms names "room:hr"`},
		{"an id reading does not list", agentWith([]string{"!a:x", "room:!b:x"}, nil, nil, []string{"!c:x"}), `[agent.write] send names "!c:x"`},
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
			{ID: "!dbg:x", Name: "Notes"},
			{ID: "!ops:x", Name: "Ops"},
			{ID: "!home:x", Name: "Family"},
			{ID: "!dm:x", Name: "Dana", IsDirect: true},
		},
		spaces: []domain.Space{{ID: "!s:x", Name: "Work", Children: []domain.RoomID{"!dbg:x", "!ops:x"}}},
	}
}

// The user's config: a room ID in send that is a member of the space reading lists. It
// must be silent, resolved or not.
func TestResolvedWarningsSilentForARoomInAReadSpace(t *testing.T) {
	t.Parallel()

	agent := agentWith([]string{"space:Work"}, nil, nil, []string{"!dbg:x"})
	if got := setup.AgentWarnings(t.Context(), account(), domain.Places{}, agent); len(got) != 0 {
		t.Fatalf("warned about a readable room: %q", got)
	}
}

// Resolved, an entry is reported when rooms it matches are outside reading, with a few of
// them named.
func TestResolvedWarningsNameRoomsOutsideReading(t *testing.T) {
	t.Parallel()

	agent := agentWith([]string{"space:Work"}, nil, []string{"group"}, []string{"!home:x", "!ops:x"})
	got := setup.AgentWarnings(t.Context(), account(), domain.Places{}, agent)
	if len(got) != 2 {
		t.Fatalf("warnings = %q, want two: group and !home:x", got)
	}
	if !strings.Contains(got[0], `rooms names "group"`) || !strings.Contains(got[0], "1 of the 3") || !strings.Contains(got[0], "Family") {
		t.Errorf("group warning = %q", got[0])
	}
	if !strings.Contains(got[1], `send names "!home:x"`) || !strings.Contains(got[1], "Family") {
		t.Errorf("send warning = %q", got[1])
	}
}

// An encrypted room reading cannot open is as unwritable as one outside its lists.
func TestResolvedWarningsNameEncryptedRooms(t *testing.T) {
	t.Parallel()

	p := account()
	p.encrypted = map[domain.RoomID]bool{"!dbg:x": true}
	got := setup.AgentWarnings(t.Context(), p, domain.Places{}, agentWith([]string{"space:Work"}, nil, nil, []string{"!dbg:x"}))
	if len(got) != 1 || !strings.Contains(got[0], "[agent.read] encrypted is false") {
		t.Fatalf("warnings = %q, want the encryption named", got)
	}
}

// Without rooms — no backend, a failing one, an entry matching nothing — the static check
// is the answer.
func TestResolvedWarningsFallBackToStatic(t *testing.T) {
	t.Parallel()

	agent := agentWith([]string{"!a:x"}, nil, nil, []string{"!gone:x"})
	for name, src := range map[string]setup.AgentPlaces{
		"no backend":       nil,
		"failing backend":  places{err: errors.New("down")},
		"matches no rooms": account(),
	} {
		got := setup.AgentWarnings(t.Context(), src, domain.Places{}, agent)
		if len(got) != 1 || !strings.Contains(got[0], `"!gone:x"`) {
			t.Errorf("%s: warnings = %q, want the static one", name, got)
		}
	}
}

// `pinned` moves with every pin in the client, and kith-mcp reads the config once, so
// it cannot bound what an assistant reaches: every assistant list refuses it, and a
// list elsewhere (codes) still takes it.
func TestPinnedCannotBoundAnAssistant(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]config.Config{
		"agent.read.except":  {Agent: config.Agent{Read: config.AgentRead{Rooms: []string{"group"}, Except: []string{" Pinned "}}}},
		"agent.write.rooms":  {Agent: config.Agent{Write: config.AgentWrite{Rooms: []string{"pinned"}}}},
		"agent.write.send":   {Agent: config.Agent{Write: config.AgentWrite{Send: []string{"pinned"}}}},
		"assist.except":      {Assist: config.Assist{Except: []string{"pinned"}}},
		"agent.write.except": {Agent: config.Agent{Write: config.AgentWrite{Except: []string{"pinned"}}}},
	} {
		if err := setup.PlaceEntries(cfg); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: pinned was accepted (%v)", name, err)
		}
	}
	if err := setup.PlaceEntries(config.Config{Codes: config.Codes{Include: []string{"pinned"}}}); err != nil {
		t.Errorf("codes.include refused pinned: %v", err)
	}
}

// A room whose ID names its network is on that network, though no bridge space holds
// it; a bare one in no space is Matrix.
func TestFactsOfANativeRoom(t *testing.T) {
	t.Parallel()

	native := domain.Room{ID: domain.RoomID("whatsapp:359000000001/120363000000000001@g.us")}
	if got := (domain.Places{}).Facts(native, nil).Protocol; got != domain.ProtocolWhatsApp {
		t.Errorf("Facts(native).Protocol = %q", got)
	}
	if got := (domain.Places{}).Facts(domain.Room{ID: "!a:x"}, nil).Protocol; got != domain.ProtocolMatrix {
		t.Errorf("Facts(!a:x).Protocol = %q", got)
	}
}

// A display name may be given to a native room by its ID.
func TestANativeRoomCanBeNamed(t *testing.T) {
	t.Parallel()

	target := "whatsapp:359000000001/120363000000000001@g.us"
	if err := setup.NameTargets(config.Display{Names: []config.DisplayName{{Target: target, Name: "Choir"}}}); err != nil {
		t.Errorf("NameTargets refused a native room ID: %v", err)
	}
}
