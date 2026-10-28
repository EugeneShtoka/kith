package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
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
	// taken are the profiles another window runs in.
	taken []string
	// told is every room Present was told, in order.
	told []domain.RoomID
}

func (b *profiledBackend) SwitchProfile(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if slices.Contains(b.taken, name) {
		return fmt.Errorf("%w: pid 7", api.ErrSeatTaken)
	}
	b.in = name
	b.switches = append(b.switches, name)
	return nil
}

func (b *profiledBackend) Present(_ context.Context, open domain.RoomID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.told = append(b.told, open)
	return nil
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
	m := inProfiles(t, b, config.Profile{Name: "work", Include: []string{"*"}}, config.Profile{Name: "home", Include: []string{"*"}})
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
	m := inProfiles(t, b, config.Profile{Name: "work", Include: []string{"*"}}, config.Profile{Name: "home", Include: []string{"*"}})
	for _, line := range []string{"profile WORK", "profile gym"} {
		next, _ := m.clearStatus().runCommandLine(line)
		if next.status() == "" {
			t.Errorf(":%s: status is empty, want it to say why nothing switched", line)
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

	one := inProfiles(t, &profiledBackend{rooms: map[string][]domain.Room{}}, starter(t).Profiles...)
	if got := one.renderStatus(); strings.Contains(got, "default") {
		t.Errorf("with one profile the status names it: %q", got)
	}
	next, _ = one.clearStatus().runCommandLine("profile")
	if next.picker.active() || next.status() == "" {
		t.Errorf("with one profile :profile = %q, picker %v", next.renderStatus(), next.picker.kind)
	}
}

// A switch into a profile another window runs in is refused, says so, and leaves the
// window where it was.
func TestASwitchRefusedLeavesTheWindowWhereItWas(t *testing.T) {
	t.Parallel()
	b := &profiledBackend{rooms: map[string][]domain.Room{}, taken: []string{"home"}}
	m := inProfiles(t, b, config.Profile{Name: "work", Include: []string{"*"}}, config.Profile{Name: "home", Include: []string{"*"}})
	m, cmd := m.clearStatus().switchProfile("home")
	m = settle(t, m, cmd)
	if got := m.renderStatus(); m.status() == "" || !strings.Contains(got, " work ") {
		t.Errorf("status = %q, want the refusal, and work still named", got)
	}
	if m.currentProfile() != "work" {
		t.Errorf("current = %q, want work", m.currentProfile())
	}
}

// The window tells the daemon it is in use: at once on opening a room, and on a key
// no more often than presenceEvery.
func TestAWindowSaysWhatItIsDoing(t *testing.T) {
	t.Parallel()
	b := &profiledBackend{rooms: map[string][]domain.Room{"": {{ID: mx + "!a:x", Name: "Alpha"}}}}
	m := inProfiles(t, b, config.Profile{Name: "work", Include: []string{"*"}}).WithPresence(b)
	key := keyText("j")
	tell := func(m Model, msg tea.Msg) Model {
		next, cmd := m.armPresence(msg)
		if cmd != nil {
			cmd()
		}
		return next
	}
	m.openRoom = ""
	m = tell(m, key) // a key, never told before
	m.openRoom = mx + "!a:x"
	m = tell(m, roomsMsg{}) // a room opened: at once, key or not
	m = tell(m, key)        // within presenceEvery: nothing
	m.presence.at = m.presence.at.Add(-presenceEvery)
	m = tell(m, roomsMsg{}) // no key: nothing
	tell(m, key)            // a key after presenceEvery
	want := []domain.RoomID{"", mx + "!a:x", mx + "!a:x"}
	if !slices.Equal(b.told, want) {
		t.Errorf("told %v, want %v", b.told, want)
	}
}
