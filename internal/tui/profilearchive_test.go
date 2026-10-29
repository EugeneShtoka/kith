package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The rooms the profile archive tests file, and a config whose profiles archive
// different ones: home archives Signal and puts the archive first on its rail, work
// archives Slack and keeps the shared rail (the archive last).
var (
	signalRoom = domain.Room{ID: mx + "!s:x", Name: "Bridge chat", Network: domain.ProtocolSignal}
	slackRoom  = domain.Room{ID: mx + "!k:x", Name: "Standup", Network: domain.ProtocolSlack}
)

func archivingConfig() config.Config {
	return config.Config{
		Tags:    []config.Tag{{Name: "Archived", Rule: []string{"space:Nowhere"}}, {Name: "All", Rule: []string{"*"}}},
		Display: config.Display{Rail: config.Rail{Order: []string{"tag:All", "*", "tag:Archived"}}},
		Profiles: []config.Profile{
			{
				Name: "home", Include: []string{"*"}, Archive: config.ProfileArchive{Rooms: []string{"protocol:signal"}},
				Rail: config.Rail{Order: []string{"tag:Archived", "tag:All", "*"}},
			},
			{Name: "work", Include: []string{"*"}, Archive: config.ProfileArchive{Rooms: []string{"protocol:slack"}}},
		},
	}
}

// archivingWindow is a window over both rooms in profile ("" the first).
func archivingWindow(t *testing.T, cfg config.Config, profile string) (Model, *profiledBackend) {
	t.Helper()
	rooms := []domain.Room{signalRoom, slackRoom}
	b := &profiledBackend{in: profile, rooms: map[string][]domain.Room{"": rooms, "home": rooms, "work": rooms}}
	m := sized(t, starterNew(b, config.Display{}))
	m = m.WithConfigFile("", cfg).WithProfiles(b, profile)
	return update(t, m, roomsMsg{rooms: rooms}), b
}

// archivedIn reports whether the window's archive tag holds room, as the rail judges.
func archivedIn(t *testing.T, m Model, room domain.Room) bool {
	t.Helper()
	view := m.unreadView()
	i, ok := view.tags.Index("Archived")
	if !ok {
		t.Fatal("no Archived tag")
	}
	return view.tagsOf(room).In[i]
}

// A window shows its profile's archive and rail; switching re-reads both, though the
// rail had already judged every room under the first profile's archive.
func TestASwitchShowsTheProfilesArchiveAndRail(t *testing.T) {
	t.Parallel()
	m, _ := archivingWindow(t, archivingConfig(), "")
	if !archivedIn(t, m, signalRoom) || archivedIn(t, m, slackRoom) {
		t.Errorf("home archives Signal %v, Slack %v; want Signal only", archivedIn(t, m, signalRoom), archivedIn(t, m, slackRoom))
	}
	if at := indexOfGroupExact(m.rail.groups, "tag:Archived"); at != 0 {
		t.Errorf("home's archive row is at %d, want first, as its rail orders", at)
	}

	m, cmd := m.switchProfile("work")
	m = settle(t, m, cmd)
	if archivedIn(t, m, signalRoom) || !archivedIn(t, m, slackRoom) {
		t.Errorf("after the switch work archives Signal %v, Slack %v; want Slack only", archivedIn(t, m, signalRoom), archivedIn(t, m, slackRoom))
	}
	if at := indexOfGroupExact(m.rail.groups, "tag:Archived"); at != len(m.rail.groups)-1 || at < 1 {
		t.Errorf("work's archive row is at %d of %d, want last, as the shared rail orders", at, len(m.rail.groups))
	}
}

// A window started in a profile (kith --profile) runs its archive and rail at once.
func TestAWindowStartsInItsProfilesArchive(t *testing.T) {
	t.Parallel()
	m, _ := archivingWindow(t, archivingConfig(), "work")
	if archivedIn(t, m, signalRoom) || !archivedIn(t, m, slackRoom) {
		t.Errorf("started in work, archives Signal %v, Slack %v; want Slack only", archivedIn(t, m, signalRoom), archivedIn(t, m, slackRoom))
	}
}

// Filing by hand writes the archive tag's shared lists, decided by the window's own
// archive: home's rule holds Signal, so filing it out excludes it; work's does not, so
// filing it in picks it. No profile's rule, and not the tag's, is written.
func TestFilingInAProfileWritesTheSharedLists(t *testing.T) {
	t.Parallel()
	home, _ := archivingWindow(t, archivingConfig(), "home")
	home, _ = home.toggleTag("Archived", signalRoom)
	got := home.conf.base
	if !slices.Equal(got.Tags[0].Excluded, []string{string(signalRoom.ID)}) || len(got.Tags[0].Picked) != 0 {
		t.Errorf("home filing Signal out wrote picked %v, excluded %v; want it excluded", got.Tags[0].Picked, got.Tags[0].Excluded)
	}
	if archivedIn(t, home, signalRoom) {
		t.Error("home still archives Signal after filing it out")
	}

	work, _ := archivingWindow(t, archivingConfig(), "work")
	work, _ = work.toggleTag("Archived", signalRoom)
	got = work.conf.base
	if !slices.Equal(got.Tags[0].Picked, []string{string(signalRoom.ID)}) || len(got.Tags[0].Excluded) != 0 {
		t.Errorf("work filing Signal in wrote picked %v, excluded %v; want it picked", got.Tags[0].Picked, got.Tags[0].Excluded)
	}
	for _, cfg := range []config.Config{home.conf.base, work.conf.base} {
		if !slices.Equal(cfg.Tags[0].Rule, []string{"space:Nowhere"}) ||
			!slices.Equal(cfg.Profiles[0].Archive.Rooms, []string{"protocol:signal"}) ||
			!slices.Equal(cfg.Profiles[1].Archive.Rooms, []string{"protocol:slack"}) {
			t.Errorf("filing rewrote a rule: tag %v, home %v, work %v", cfg.Tags[0].Rule, cfg.Profiles[0].Archive.Rooms, cfg.Profiles[1].Archive.Rooms)
		}
	}
}

// Two windows of different profiles filing the same room at once, each on the config
// as it was: one filing is saved, and the other window is told its change was not
// and runs the saved config. Neither is lost without a word.
func TestTwoProfilesFilingTheSameRoomAtOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, archivingConfig()); err != nil {
		t.Fatal(err)
	}
	window := func(profile string) Model {
		m, _ := archivingWindow(t, archivingConfig(), profile)
		return m.WithConfigFile(path, m.conf.base).WithProfiles(m.profiles.switcher, profile).keptIn()
	}
	home, work := window("home"), window("work")
	home, saveHome := home.toggleTag("Archived", signalRoom) // out: excluded
	work, saveWork := work.toggleTag("Archived", signalRoom) // in: picked
	home = settle(t, home, saveHome)
	work = settle(t, work, saveWork)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := config.Decode(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved.Tags[0].Excluded, []string{string(signalRoom.ID)}) || len(saved.Tags[0].Picked) != 0 {
		t.Errorf("saved picked %v, excluded %v; want home's filing, the first saved", saved.Tags[0].Picked, saved.Tags[0].Excluded)
	}
	if !strings.Contains(work.st.event, "not saved") {
		t.Errorf("work said %q, want that its change was not saved", work.st.event)
	}
	if !slices.Equal(work.conf.base.Tags[0].Excluded, saved.Tags[0].Excluded) || len(work.conf.base.Tags[0].Picked) != 0 {
		t.Errorf("work runs picked %v, excluded %v; want the saved config", work.conf.base.Tags[0].Picked, work.conf.base.Tags[0].Excluded)
	}
	if !archivedIn(t, work, slackRoom) || archivedIn(t, work, signalRoom) {
		t.Error("work no longer runs its own archive after taking the saved config")
	}
	_ = home
}
