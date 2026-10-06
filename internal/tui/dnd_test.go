package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// fakeNotifications stands in for the daemon's notification state, one temporary
// rule per named thing.
type fakeNotifications struct {
	mu     sync.Mutex
	temps  notify.Temps
	setErr error
}

func (f *fakeNotifications) SetDND(_ context.Context, rule notify.Rule) (notify.Temps, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return nil, f.setErr
	}
	f.temps = f.temps.Add(rule)
	return f.temps, nil
}

func (f *fakeNotifications) ClearDND(_ context.Context, match, sender string) (notify.Temps, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.temps = f.temps.Remove(match, sender)
	return f.temps, nil
}

func (f *fakeNotifications) ClearAllDND(context.Context) (notify.Temps, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.temps = nil
	return nil, nil
}

func (f *fakeNotifications) DND(context.Context) (notify.Temps, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.temps, nil
}

func (f *fakeNotifications) ReloadConfig(context.Context) error { return nil }

func (f *fakeNotifications) held() notify.Temps {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append(notify.Temps(nil), f.temps...)
}

// silencing gives a model attached to a fake daemon, in Alpha (in the Work space)
// with one of Alice's messages selected, so every mute scope applies.
func silencing(t *testing.T) (Model, *fakeNotifications) {
	t.Helper()

	daemon := &fakeNotifications{}
	m := sized(t, withRooms(t, newModel())).
		WithNotifications(daemon).
		WithRules([]notify.Rule{{Name: "all rooms", Show: new(notify.LevelAll)}}, true)
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "hi"},
	}}})
	m.focus, m.compose.insertMode = paneTimeline, false
	m = m.clearStatus()
	return m, daemon
}

// drain is deliver: run a command and fold what it produced into the model.
func drain(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	return deliver(t, m, cmd)
}

// ctrl+n asks what to silence and for how long.
func TestDNDAsksWhatAndForHowLong(t *testing.T) {
	t.Parallel()

	m, daemon := silencing(t)
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.picker.kind != pickerDNDScope {
		t.Fatalf("ctrl+n opened %v, want the scope picker", m.picker.kind)
	}
	// Narrowest first, short names only.
	wantScopes := []string{"Alpha", "Alice", "Work", "all notifications"}
	gotScopes := make([]string, 0, len(m.picker.all))
	for _, item := range m.picker.all {
		gotScopes = append(gotScopes, item.label)
	}
	if strings.Join(gotScopes, " | ") != strings.Join(wantScopes, " | ") {
		t.Errorf("the scope picker offered %v, want %v in that order", gotScopes, wantScopes)
	}
	// Typing still narrows against the long form.
	for _, item := range m.picker.all {
		if item.label == "Work" && !strings.Contains(item.match, "everything in Work") {
			t.Errorf("the space row should still match on %q: %q", "everything in Work", item.match)
		}
		if item.label == "Alice" && !strings.Contains(item.match, "anywhere") {
			t.Errorf("the person row should still match on %q: %q", "anywhere", item.match)
		}
	}

	m = pickLabel(t, m, "Alpha")
	if m.picker.kind != pickerDNDFor {
		t.Fatalf("choosing a scope opened %v, want the length picker", m.picker.kind)
	}
	for _, want := range []string{"1 hour", "2 hours", "4 hours", "8 hours", "24 hours", "Until I turn it off"} {
		if !hasLabel(m.picker.all, want) {
			t.Errorf("the length picker should offer %q", want)
		}
	}

	before := time.Now()
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 1 hour, the first row
	m = drain(t, m, cmd)

	held := daemon.held()
	if len(held) != 1 {
		t.Fatalf("the daemon holds %+v, want one mute", held)
	}
	got := held[0]
	if got.Match != "!a:x" || got.Sender != "" {
		t.Errorf("rule = %+v, want the room that was chosen", got)
	}
	if !got.Temp {
		t.Error("a mute must be a temporary rule")
	}
	if got.Show == nil || *got.Show != notify.LevelNone {
		t.Errorf("Show = %v, want none — ctrl+n takes the notification away", got.Show)
	}
	if got.Name != "Alpha" {
		t.Errorf("Name = %q, want the name the UI shows", got.Name)
	}
	if d := got.Until.Sub(before); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("deadline is %v away, want about an hour", d)
	}
	if !strings.Contains(m.status(), "Alpha") {
		t.Errorf("status = %q, should name what was silenced", m.status())
	}
}

// With only the account to choose, the scope question is skipped.
func TestDNDSkipsTheScopeWhenOnlyGlobalApplies(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel()).WithNotifications(&fakeNotifications{})
	m.focus = paneRail
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.picker.kind != pickerDNDFor {
		t.Fatalf("picker = %v, want the length picker straight away", m.picker.kind)
	}
}

// The badge counts down from the deadline and lapses; scoped mutes show a count.
func TestSilenceBadge(t *testing.T) {
	t.Parallel()

	m, _ := silencing(t)
	if got := m.silenceBadge(); got != "" {
		t.Errorf("badge = %q, want none while notifications are flowing", got)
	}

	m.notifications.temps = notify.Temps{}.Add(until(hidden("", "", "all notifications"), time.Now().Add(90*time.Minute)))
	if got := m.silenceBadge(); !strings.Contains(got, "dnd") || !strings.Contains(got, "1h") {
		t.Errorf("badge = %q, want a dnd badge counting down", got)
	}

	m.notifications.temps = notify.Temps{}.Add(hidden("", "", "all notifications"))
	if got := m.silenceBadge(); !strings.Contains(got, "dnd") || strings.Contains(got, "h") {
		t.Errorf("badge = %q, want a plain dnd badge with no countdown", got)
	}

	m.notifications.temps = notify.Temps{}.
		Add(hidden("!a:x", "", "Alpha")).
		Add(hidden("", "@alice:x", "Alice"))
	if got := m.silenceBadge(); !strings.Contains(got, "×2") {
		t.Errorf("badge = %q, want a count rather than a claim of general silence", got)
	}

	m.notifications.temps = notify.Temps{until(hidden("", "", "all notifications"), time.Now().Add(-time.Minute))}
	if got := m.silenceBadge(); got != "" {
		t.Errorf("badge = %q, want none for a lapsed mute", got)
	}
}

// A standing rule silencing the room is named in the badge.
func TestSilenceBadgeNamesTheStandingRule(t *testing.T) {
	t.Parallel()

	allDay := notify.Window{Start: 0, End: 24*time.Hour - time.Nanosecond}
	m, _ := silencing(t)
	m.notifications.rules = append(m.notifications.rules, notify.Rule{
		Name: "Quiet hours", When: &allDay, Show: new(notify.LevelNone),
	})
	got := m.silenceBadge()
	if !strings.Contains(got, "Quiet hours") {
		t.Errorf("badge = %q, want it to name the rule holding notifications back", got)
	}

	// An unnamed rule falls back to the narrowest thing it names.
	m.notifications.rules = append(m.notifications.rules[:len(m.notifications.rules)-1], notify.Rule{
		Match: "!a:x", When: &allDay, Show: new(notify.LevelNone),
	})
	if matched := m.silenceBadge(); !strings.Contains(matched, "!a:x") {
		t.Errorf("badge = %q, want the unnamed rule identified by what it matches", matched)
	}

	// The account-wide default reads "quiet: all rooms".
	m.notifications.rules = append(m.notifications.rules[:len(m.notifications.rules)-1], notify.Rule{Show: new(notify.LevelNone)})
	got = m.silenceBadge()
	if !strings.Contains(got, "quiet:") {
		t.Errorf("badge = %q, want it to say what it is doing, not just name a rule", got)
	}
	if !strings.Contains(got, "all rooms") {
		t.Errorf("badge = %q, want the account-wide rule to say what it covers", got)
	}
}

// With notifications off the badge says so and names no rule; an empty rule set
// must not resolve to a phantom silencing rule.
func TestSilenceBadgeWithNotificationsOff(t *testing.T) {
	t.Parallel()

	m, _ := silencing(t)
	m.notifications.on, m.notifications.rules = false, nil

	got := m.silenceBadge()
	if !strings.Contains(got, "notifications off") {
		t.Errorf("badge = %q, want it to say notifications are off", got)
	}
	for _, invented := range []string{"quiet:", "all rooms"} {
		if strings.Contains(got, invented) {
			t.Errorf("badge = %q, want no rule named — there is none", got)
		}
	}

	m.notifications.on = true
	if quiet := m.silenceBadge(); quiet != "" {
		t.Errorf("badge = %q, want nothing: an empty rule set silences nothing", quiet)
	}
}

// A rule outside its schedule silences nothing.
func TestSilenceBadgeIgnoresASleepingRule(t *testing.T) {
	t.Parallel()

	m, _ := silencing(t)
	night, err := notify.ParseWhen("22:00-08:00")
	if err != nil {
		t.Fatalf("ParseWhen: %v", err)
	}
	m.notifications.rules = append(m.notifications.rules, notify.Rule{Name: "Quiet hours", When: &night, Show: new(notify.LevelNone)})
	noon := time.Date(2026, 8, 23, 12, 0, 0, 0, time.Local)
	if name, ok := m.silencedBy(noon); ok {
		t.Errorf("silencedBy = %q at midday, want nothing: the rule is asleep", name)
	}
	night23 := time.Date(2026, 8, 23, 23, 0, 0, 0, time.Local)
	if name, ok := m.silencedBy(night23); !ok || name != "Quiet hours" {
		t.Errorf("silencedBy = %q, %v at 23:00, want the quiet-hours rule", name, ok)
	}
}

// A daemon failure is reported and leaves the model unmuted.
func TestDNDFailureIsReported(t *testing.T) {
	t.Parallel()

	m, daemon := silencing(t)
	daemon.setErr = errors.New("socket closed")
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	m = pickLabel(t, m, "notifications")
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drain(t, m, cmd)
	if !strings.Contains(m.status(), "socket closed") {
		t.Errorf("status = %q, should carry the failure", m.status())
	}
	if m.notifications.temps.Any(time.Now()) {
		t.Error("a failed call must not leave the model believing it is muted")
	}
}

// With no daemon attached the key says so.
func TestDNDWithoutADaemon(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if !strings.Contains(m.status(), "kithd") {
		t.Errorf("status = %q, should say where do-not-disturb lives", m.status())
	}
	if m.picker.active() {
		t.Error("there is nothing to ask about with no daemon to ask")
	}
}

// ctrl+n hides notifications; alt+n silences only their sound.
func TestTwoKeysTwoKindsOfSilence(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		key    tea.KeyPressMsg
		title  string
		writes func(notify.Rule) *notify.Level
		leaves func(notify.Rule) *notify.Level
	}{
		"ctrl+n hides entirely": {
			key: tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}, title: "Do not disturb for what?",
			writes: func(r notify.Rule) *notify.Level { return r.Show },
			leaves: func(r notify.Rule) *notify.Level { return r.Ring },
		},
		"alt+n silences the sound": {
			key: tea.KeyPressMsg{Code: 'n', Mod: tea.ModAlt}, title: "Mute sound for what?",
			writes: func(r notify.Rule) *notify.Level { return r.Ring },
			leaves: func(r notify.Rule) *notify.Level { return r.Show },
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, daemon := silencing(t)
			m, _ = press(t, m, tc.key)
			if got := m.picker.spec.title; got != tc.title {
				t.Errorf("picker title = %q, want %q", got, tc.title)
			}
			m = pickLabel(t, m, "notifications")
			m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 1 hour
			m = drain(t, m, cmd)

			held := daemon.held()
			if len(held) != 1 {
				t.Fatalf("the daemon holds %+v, want one mute", held)
			}
			if got := tc.writes(held[0]); got == nil || *got != notify.LevelNone {
				t.Errorf("the key wrote %v, want the axis it owns set to none", got)
			}
			if got := tc.leaves(held[0]); got != nil {
				t.Errorf("the other axis is %v, want it left alone: a rule says only what it changes", got)
			}
		})
	}
}

// Either key lifts whatever is in force, in one keystroke.
func TestEitherKeyLiftsAnySilence(t *testing.T) {
	t.Parallel()

	for name, key := range map[string]tea.KeyPressMsg{
		"ctrl+n": {Code: 'n', Mod: tea.ModCtrl},
		"alt+n":  {Code: 'n', Mod: tea.ModAlt},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, daemon := silencing(t)
			daemon.temps = notify.Temps{}.Add(silenced("", "", "all notifications"))
			m = drain(t, m, m.readDNDCmd())

			m, cmd := press(t, m, key)
			if m.picker.active() {
				t.Error("with something already muted, either key should lift it rather than ask")
			}
			m = drain(t, m, cmd)
			if len(daemon.held()) != 0 {
				t.Errorf("the daemon still holds %+v, want everything lifted", daemon.held())
			}
			if m.status() != "do not disturb off" {
				t.Errorf("status = %q", m.status())
			}
		})
	}
}

// The badge says which axis is in force.
func TestBadgeNamesTheAxis(t *testing.T) {
	t.Parallel()

	m, _ := silencing(t)
	m.notifications.temps = notify.Temps{}.Add(silenced("", "", "all notifications"))
	if got := m.silenceBadge(); !strings.Contains(got, "muted") {
		t.Errorf("badge = %q, want it to say the sound is off, not that nothing is arriving", got)
	}
	m.notifications.temps = notify.Temps{}.Add(hidden("", "", "all notifications"))
	if got := m.silenceBadge(); !strings.Contains(got, "dnd") {
		t.Errorf("badge = %q, want the do-not-disturb badge", got)
	}
}

// hidden and until build temporary rules.
func hidden(match, sender, name string) notify.Rule {
	return notify.Rule{Name: name, Match: match, Sender: sender, Show: new(notify.LevelNone), Temp: true}
}

func until(r notify.Rule, deadline time.Time) notify.Rule { r.Until = deadline; return r }

// silenced is the sound-only rule.
func silenced(match, sender, name string) notify.Rule {
	return notify.Rule{Name: name, Match: match, Sender: sender, Ring: new(notify.LevelNone), Temp: true}
}

// An abandoned do-not-disturb flow leaves no scopes, target or axis behind.
func TestAnAbandonedDNDFlowLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	m, _ := silencing(t)
	m.focus = paneRail

	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if !m.picker.active() {
		t.Fatal("ctrl+n did not open the scope picker; this room has more than one scope")
	}
	if len(m.notifications.scopes) == 0 {
		t.Fatal("the flow opened with no scopes to choose from")
	}

	next, _ := m.chooseDNDScope(-1)
	m = next

	if m.picker.active() {
		t.Error("the picker is still open after backing out")
	}
	if m.notifications.scopes != nil {
		t.Errorf("scopes survived an abandoned flow: %+v", m.notifications.scopes)
	}
	if m.notifications.target != (muteTarget{}) {
		t.Errorf("target survived an abandoned flow: %+v", m.notifications.target)
	}
	if m.notifications.axis.silence != nil {
		t.Error("axis survived an abandoned flow")
	}
}

// A completed flow leaves nothing behind either.
func TestACompletedDNDFlowLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	m, _ := silencing(t)
	m.focus = paneRail

	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	next, _ := m.chooseDNDScope(0)
	m = next
	if m.notifications.target == (muteTarget{}) {
		t.Fatal("choosing a scope recorded nothing")
	}

	next, cmd := m.chooseDNDFor(dndDurations[0].key)
	m = next
	if cmd == nil {
		t.Fatal("choosing a length asked the daemon for nothing")
	}
	if m.notifications.scopes != nil || m.notifications.target != (muteTarget{}) {
		t.Errorf("the flow's state survived it completing: %+v / %+v",
			m.notifications.scopes, m.notifications.target)
	}
	if m.notifications.axis.silence != nil {
		t.Error("the axis survived the flow completing")
	}
}

// A space mute matches as a place entry, so it reaches the space's rooms.
func TestDNDOnASpaceReachesItsRooms(t *testing.T) {
	t.Parallel()

	m, daemon := silencing(t)
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	m = pickLabel(t, m, "everything in Work")
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 1 hour
	m = drain(t, m, cmd)

	held := daemon.held()
	if len(held) != 1 {
		t.Fatalf("the daemon holds %+v, want one mute", held)
	}
	match := held[0].Match
	if _, ok := domain.ParseEntry(match); !ok {
		t.Fatalf("the mute's match %q is not a place entry", match)
	}
	room, _ := m.roomByID("!a:x")
	if got := (setup.Place{Room: m.factsFor(room)}).Reach(match); got != notify.ClassOfRooms {
		t.Errorf("Reach(%q) = %v, want the space to reach a room in it", match, got)
	}
	if !strings.Contains(m.status(), "Work") {
		t.Errorf("status = %q, should name the space", m.status())
	}
}
