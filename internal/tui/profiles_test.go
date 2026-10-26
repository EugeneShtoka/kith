package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// profiledBackend lists each profile's rooms, in the profile it was last switched to.
type profiledBackend struct {
	apitest.Nop
	rooms map[string][]domain.Room

	mu       sync.Mutex
	in       string
	switches []string
}

func (b *profiledBackend) SwitchProfile(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.in = name
	b.switches = append(b.switches, name)
}

func (b *profiledBackend) Rooms(context.Context) ([]domain.Room, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rooms[b.in], nil
}

// inProfiles is a window over b, configured with profiles, running in the first.
func inProfiles(t *testing.T, b *profiledBackend, profiles ...config.Profile) Model {
	t.Helper()
	m := sized(t, starterNew(b, config.Display{}))
	m = m.WithConfigFile("", config.Config{Profiles: profiles}).WithProfiles(b, "")
	rooms, _ := b.Rooms(t.Context())
	return update(t, m, roomsMsg{rooms: rooms})
}

func joinedIDs(m Model) []domain.RoomID {
	var ids []domain.RoomID
	for i := range m.rooms.joined {
		ids = append(ids, m.rooms.joined[i].ID)
	}
	slices.Sort(ids)
	return ids
}

// The profile key lists the profiles, the current one marked; picking another switches
// the window and reads that profile's rooms, and the status line names it.
func TestAWindowSwitchesProfile(t *testing.T) {
	t.Parallel()
	work, home := domain.RoomID(mx+"!w:x"), domain.RoomID(mx+"!h:x")
	b := &profiledBackend{in: "", rooms: map[string][]domain.Room{
		"": {{ID: work, Name: "Standup"}}, "home": {{ID: home, Name: "Family"}},
	}}
	m := inProfiles(t, b, config.Profile{Name: "work"}, config.Profile{Name: "home"})
	if got := m.renderStatus(); !strings.Contains(got, " work ") {
		t.Errorf("status = %q, want the profile named", got)
	}

	m, _ = chord(t, m, "g", "P")
	if m.picker.kind != pickerProfile || len(m.picker.items) != 2 || m.picker.items[0].detail != "current" {
		t.Fatalf("picker = %v %+v, want both profiles with work current", m.picker.kind, m.picker.items)
	}
	m, cmd := m.switchProfile("home")
	m = settle(t, m, cmd)
	if !slices.Equal(b.switches, []string{"home"}) {
		t.Errorf("switched to %v, want home", b.switches)
	}
	if got := joinedIDs(m); !slices.Equal(got, []domain.RoomID{home}) {
		t.Errorf("rooms = %v, want home's", got)
	}
	if got := m.renderStatus(); !strings.Contains(got, " home ") {
		t.Errorf("status = %q, want home named", got)
	}
}

// :profile goes straight to a profile in any case, says when it is already there or
// does not exist, and with one profile there is nothing to switch to or name.
func TestTheProfileCommand(t *testing.T) {
	t.Parallel()
	b := &profiledBackend{rooms: map[string][]domain.Room{}}
	m := inProfiles(t, b, config.Profile{Name: "work"}, config.Profile{Name: "home"})
	for line, want := range map[string]string{
		"profile WORK": "already in work",
		"profile gym":  "no profile gym — the config has work, home",
	} {
		next, _ := m.runCommandLine(line)
		if got := next.renderStatus(); !strings.Contains(got, want) {
			t.Errorf(":%s: status = %q, want %q", line, got, want)
		}
	}
	if len(b.switches) != 0 {
		t.Errorf("switched to %v, want nothing", b.switches)
	}
	next, cmd := m.runCommandLine("profile Home")
	settle(t, next, cmd)
	if !slices.Equal(b.switches, []string{"home"}) {
		t.Errorf(":profile Home switched to %v, want home", b.switches)
	}

	one := inProfiles(t, &profiledBackend{rooms: map[string][]domain.Room{}})
	if got := one.renderStatus(); strings.Contains(got, "default") {
		t.Errorf("with one profile the status names it: %q", got)
	}
	next, _ = one.runCommandLine("profile")
	if next.picker.active() || !strings.Contains(next.renderStatus(), "one profile, default") {
		t.Errorf("with one profile :profile = %q, picker %v", next.renderStatus(), next.picker.kind)
	}
}
