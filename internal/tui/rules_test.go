package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// ruling returns a model in a room that belongs to a space, with one message from
// Alice, plus the config path rules are written to.
func ruling(t *testing.T, notifs config.Notifications) (Model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	base := config.Config{Homeserver: "https://x", User: "@me:x", Notifications: notifs, Tags: starter(t).Tags}
	base.Keys.FillDefaults()
	if err := config.Save(path, base); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	m := update(t, starterNew(apitest.Nop{}, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!standup:x", Name: "Standup"}}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!standup:x"}},
	}})
	m = sized(t, m).WithConfigFile(path, base)
	// Install the rules the same way startup does, so the status line describes the
	// real ones rather than a hand-assembled set.
	rules, err := setup.NotificationRules(notifs)
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	m = m.WithRules(rules, "@me:x", true)
	next, _ := m.selectRoom(domain.Room{ID: "!standup:x", Name: "Standup"})
	m = next
	m = update(t, m, timelineMsg{roomID: "!standup:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "Alice", Body: "standup in 5"},
	}}})
	m.focus, m.compose.insertMode = paneTimeline, false
	m = m.clearStatus()
	return m, path
}

// notifyAll is a notifications section whose account-wide rule shows everything,
// followed by rules.
func notifyAll(rules ...config.Rule) config.Notifications {
	return config.Notifications{Enabled: true, Rules: append([]config.Rule{{Name: "everything", Show: "all"}}, rules...)}
}

// lastRule is the most recently appended rule.
func lastRule(t *testing.T, rules []config.Rule) config.Rule {
	t.Helper()
	if len(rules) == 0 {
		t.Fatal("no rules")
	}
	return rules[len(rules)-1]
}

// resolveFor resolves msg against the TUI's current config exactly as the daemon will.
func resolveFor(t *testing.T, m Model, msg domain.Message) (notify.Resolved, notify.Event) {
	t.Helper()

	rules, err := setup.NotificationRules(m.conf.base.Notifications)
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	room, ok := m.roomByID(msg.RoomID)
	if !ok {
		t.Fatalf("no room %s in the model", msg.RoomID)
	}
	resolved := notify.Resolve(rules, notify.Scope{
		Room:   setup.Place{Room: m.factsFor(room)},
		Sender: msg.Sender,
	}, time.Now())
	return resolved, notify.Event{Mentioned: msg.Mentioned, Direct: room.IsDirect}
}

// notifies reports whether the authored rules would earn msg a notification.
func notifies(t *testing.T, m Model, msg domain.Message) bool {
	t.Helper()
	resolved, event := resolveFor(t, m, msg)
	return resolved.Decide(event).Notify
}

// pickLabel filters the open picker to a label and accepts it, running the command.
func pickLabel(t *testing.T, m Model, substr string) Model {
	t.Helper()
	if m.picker.spec.modal && m.picker.mode == pickerNavigate {
		m, _ = press(t, m, keyText("i")) // a modal picker filters after its filter key
	}
	for _, r := range substr {
		m, _ = press(t, m, keyText(string(r)))
	}
	if len(m.picker.items) == 0 {
		t.Fatalf("nothing matches %q in the picker", substr)
	}
	next, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	// deliver: closing the picker also asks for a repaint, so the command is a batch.
	return deliver(t, next, cmd)
}

// The scopes offered from a message are described in words.
func TestRuleScopesFromMessage(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{})
	m, _ = press(t, m, keyText("b"))
	if m.picker.kind != pickerRuleScope {
		t.Fatalf("b should offer the scopes, got %v", m.picker.kind)
	}
	labels := make([]string, 0, len(m.picker.items))
	for _, item := range m.picker.items {
		labels = append(labels, item.label)
	}
	want := []string{
		"Alice in Standup", "Alice, anywhere", "everyone in Standup",
		"Alice in Work", "everything in Work",
	}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Errorf("scopes = %v, want %v", labels, want)
	}
	if m.picker.items[0].detail != "@alice:x in !standup:x" {
		t.Errorf("detail = %q, should show the rule's own terms", m.picker.items[0].detail)
	}
}

// From the room list there is no sender, so the scopes are the room and its spaces.
func TestRuleScopesFromRoom(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{})
	m.focus = paneRooms
	m, _ = press(t, m, keyText("b"))
	if m.picker.kind != pickerRuleScope {
		t.Fatalf("kind = %v", m.picker.kind)
	}
	if len(m.picker.items) != 2 {
		t.Fatalf("scopes = %+v, want the room and its space", m.picker.items)
	}
	for _, item := range m.picker.items {
		if strings.Contains(item.detail, "@") {
			t.Errorf("a room rule should carry no sender, got %q", item.detail)
		}
	}
}

// From the rail there is only one scope, so it skips straight to the presets.
func TestRuleFromRailSkipsTheScope(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{})
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	m, _ = press(t, m, keyText("b"))
	if m.picker.kind != pickerRulePreset {
		t.Fatalf("kind = %v, want the presets directly", m.picker.kind)
	}
	if m.aimedAt.rule.match != domain.SpaceEntry("Work") || m.aimedAt.rule.sender != "" {
		t.Errorf("target = %+v, want the space", m.aimedAt.rule)
	}
}

// A tag's row is a place a rule can name, as tag:<name>.
func TestRuleOfferedForTagRows(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{})
	m.focus = paneRail
	for _, key := range []string{homeGroupKey, dmsGroupKey, unreadGroupKey} {
		m.rail.cursor = indexOfGroup(m.rail.groups, key)
		next, _ := press(t, m, keyText("b"))
		if !next.picker.active() || next.aimedAt.rule.match != key {
			t.Errorf("%s: aimed at %q, want a rule for the tag", key, next.aimedAt.rule.match)
		}
	}
}

// The three asks, authored through the UI and honored by the decision.
func TestRuleAuthoringChangesTheDecision(t *testing.T) {
	t.Parallel()

	t.Run("one person always gets through", func(t *testing.T) {
		t.Parallel()
		// Mention-only and quiet all day: a rule naming Alice is narrower than the
		// quiet-hours rule, so `show = all` gets her through.
		m, path := ruling(t, config.Notifications{Enabled: true, Rules: []config.Rule{
			{Name: "quiet hours", Show: "none", When: "00:00-23:59"},
		}})
		m, _ = press(t, m, keyText("b"))
		m = pickLabel(t, m, "anywhere")   // scope: Alice, anywhere
		m = pickLabel(t, m, "everything") // preset: notify for everything

		fromAlice := domain.Message{ID: "$2", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "A", Body: "hi"}
		if !notifies(t, m, fromAlice) {
			t.Error("Alice should get through the quiet hours")
		}
		fromBob := domain.Message{ID: "$3", RoomID: "!standup:x", Sender: "@bob:x", SenderName: "B", Body: "hi"}
		if notifies(t, m, fromBob) {
			t.Error("Bob should not")
		}
		// And it persisted.
		reloaded, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(reloaded.Notifications.Rules) != 2 {
			t.Fatalf("rules = %+v, want the quiet rule plus the one written", reloaded.Notifications.Rules)
		}
		if rule := lastRule(t, reloaded.Notifications.Rules); rule.Sender != "@alice:x" || rule.Show != "all" {
			t.Errorf("rule = %+v, want show = all for Alice", rule)
		}
	})

	t.Run("mute a room", func(t *testing.T) {
		t.Parallel()
		m, _ := ruling(t, notifyAll())
		m.focus = paneRooms
		m, _ = press(t, m, keyText("b"))
		m = pickLabel(t, m, "Standup")
		m = pickLabel(t, m, "Mute")

		msg := domain.Message{ID: "$2", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "A", Body: "hi"}
		if notifies(t, m, msg) {
			t.Error("a muted room should not notify")
		}
	})

	t.Run("only one person in a muted room", func(t *testing.T) {
		t.Parallel()
		m, _ := ruling(t, notifyAll())
		// Mute the room from the room list…
		m.focus = paneRooms
		m, _ = press(t, m, keyText("b"))
		m = pickLabel(t, m, "Standup")
		m = pickLabel(t, m, "Mute")
		// …then un-mute Alice within it, from her message.
		m.focus = paneTimeline
		m, _ = press(t, m, keyText("b"))
		m = pickLabel(t, m, "Alice in Standup")
		m = pickLabel(t, m, "everything")

		fromAlice := domain.Message{ID: "$2", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "A", Body: "hi"}
		if !notifies(t, m, fromAlice) {
			t.Error("Alice should notify despite the room being muted")
		}
		fromBob := domain.Message{ID: "$3", RoomID: "!standup:x", Sender: "@bob:x", SenderName: "B", Body: "hi"}
		if notifies(t, m, fromBob) {
			t.Error("everyone else should stay muted")
		}
	})
}

// One rule per scope, accumulating settings.
func TestRulesAccumulatePerScope(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{})
	for _, preset := range []string{"everything", "Seen but not heard"} {
		m, _ = press(t, m, keyText("b"))
		m = pickLabel(t, m, "Alice in Standup")
		m = pickLabel(t, m, preset)
	}
	rules := m.conf.base.Notifications.Rules
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want one per scope", rules)
	}
	rule := rules[0]
	if rule.Show != "all" || rule.Ring != "none" {
		t.Errorf("rule = %+v, want both axes on the one rule", rule)
	}
}

// The presets say which one is already in effect.
func TestRulePresetsMarkWhatIsSet(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{Rules: []config.Rule{
		{Match: "!standup:x", Sender: "@alice:x", Show: "none", Ring: "none"},
	}})
	m, _ = press(t, m, keyText("b"))
	m = pickLabel(t, m, "Alice in Standup")

	set := map[string]bool{}
	for _, item := range m.picker.items {
		set[item.label] = strings.Contains(item.detail, "currently set")
	}
	if !set["Mute — never notify"] {
		t.Error("the mute preset should be marked as in effect")
	}
	if !set["Seen but not heard"] {
		t.Error("the sound preset should be marked as in effect")
	}
	if set["Notify me for everything"] {
		t.Error("a preset that is not in effect should not be marked")
	}
	// Remove is offered because there is something to remove.
	if !set["Remove the rule"] && !hasLabel(m.picker.items, "Remove the rule") {
		t.Error("remove should be offered when a rule exists")
	}
}

// With no rule there is nothing to remove, so the option is not offered.
func TestRemoveNotOfferedWithoutARule(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{})
	m, _ = press(t, m, keyText("b"))
	m = pickLabel(t, m, "Alice in Standup")
	if hasLabel(m.picker.items, "Remove the rule") {
		t.Error("remove should not be offered when there is no rule")
	}
}

// Removing a rule returns the scope to the global settings.
func TestRemoveRule(t *testing.T) {
	t.Parallel()

	m, path := ruling(t, notifyAll(
		config.Rule{Match: "!standup:x", Show: "none"},
		config.Rule{Match: "space:Work", Show: "all"},
	))
	m.focus = paneRooms
	m, _ = press(t, m, keyText("b"))
	m = pickLabel(t, m, "Standup")
	m = pickLabel(t, m, "Remove")

	if len(m.conf.base.Notifications.Rules) != 2 {
		t.Fatalf("rules = %+v, want the room rule gone", m.conf.base.Notifications.Rules)
	}
	if lastRule(t, m.conf.base.Notifications.Rules).Match != "space:Work" {
		t.Errorf("the wrong rule was removed: %+v", m.conf.base.Notifications.Rules)
	}
	msg := domain.Message{ID: "$2", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "A", Body: "hi"}
	if !notifies(t, m, msg) {
		t.Error("with the mute removed the room should notify again")
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Notifications.Rules) != 2 {
		t.Error("the removal did not persist")
	}
}

// The sound preset prompts for a path; an empty path clears it.
func TestRuleSoundPreset(t *testing.T) {
	t.Parallel()

	m, path := ruling(t, config.Notifications{})
	m, _ = press(t, m, keyText("b"))
	m = pickLabel(t, m, "Alice in Standup")
	m = pickLabel(t, m, "own sound")
	if m.prompt.kind != promptRuleSound {
		t.Fatalf("the sound preset should prompt, got %v", m.prompt.kind)
	}
	for _, r := range "/s/boss.wav" {
		m, _ = press(t, m, keyText(string(r)))
	}
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		runCmd(t, cmd)
	}
	rules := m.conf.base.Notifications.Rules
	if len(rules) != 1 || rules[0].Sound != "/s/boss.wav" {
		t.Fatalf("rules = %+v", rules)
	}
	got, _ := resolveFor(t, m, domain.Message{
		ID: "$2", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "A", Body: "hi",
	})
	if got.Sound != "/s/boss.wav" {
		t.Errorf("resolved sound = %q", got.Sound)
	}

	// Clearing it: the prompt is prefilled with what is set, and emptying removes it.
	m, _ = press(t, m, keyText("b"))
	m = pickLabel(t, m, "Alice in Standup")
	m = pickLabel(t, m, "own sound")
	if m.prompt.input != "/s/boss.wav" {
		t.Errorf("prompt = %q, want it prefilled", m.prompt.input)
	}
	for range len(m.prompt.input) {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		runCmd(t, cmd)
	}
	// The rule set nothing else, so it is dropped.
	if len(m.conf.base.Notifications.Rules) != 0 {
		t.Errorf("rules = %+v, want the empty rule dropped", m.conf.base.Notifications.Rules)
	}
	if _, err := config.Load(path); err != nil {
		t.Errorf("written config does not load: %v", err)
	}
}

// Backing out at any step writes nothing.
func TestRuleFlowIsAbandonable(t *testing.T) {
	t.Parallel()

	steps := map[string]int{"at the scope": 1, "at the presets": 2, "at the sound prompt": 3}
	for name, depth := range steps {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, path := ruling(t, config.Notifications{})
			before, _ := os.ReadFile(path)
			m, _ = press(t, m, keyText("b"))
			if depth >= 2 {
				m = pickLabel(t, m, "Alice in Standup")
			}
			if depth >= 3 {
				m = pickLabel(t, m, "own sound")
			}
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			if len(m.conf.base.Notifications.Rules) != 0 {
				t.Errorf("backing out %s wrote a rule: %+v", name, m.conf.base.Notifications.Rules)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Errorf("backing out %s wrote to the config", name)
			}
		})
	}
}

// A rule takes effect on the next message, not on the next start.
func TestRuleAppliesWithoutRestart(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, notifyAll())
	msg := domain.Message{ID: "$2", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "A", Body: "hi"}
	if !notifies(t, m, msg) {
		t.Fatal("nothing is muted to start with")
	}
	m, _ = press(t, m, keyText("b"))
	m = pickLabel(t, m, "Alice in Standup")
	m = pickLabel(t, m, "Mute")
	if notifies(t, m, msg) {
		t.Error("the new rule should be in the running config at once, not at the next start")
	}
}

// A room in several spaces offers each of them.
func TestHomesOfSpaces(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!standup:x"}},
		{ID: "!e:x", Name: "Everything", Children: []domain.RoomID{"!standup:x", "!other:x"}},
		{ID: "!f:x", Name: "Friends", Children: []domain.RoomID{"!other:x"}},
	}})
	got := m.placeHomes("!standup:x")
	slices.Sort(got) // which comes first is the home order's business (domain.HomeOrder)
	if strings.Join(got, ",") != "Everything,Work" {
		t.Errorf("homesOf = %v, want both spaces containing it", got)
	}
	if len(m.placeHomes("!nowhere:x")) != 0 {
		t.Error("a room in no space should have none")
	}
}

// hasLabel reports whether the picker offers an item with that label.
func hasLabel(items []pickerItem, label string) bool {
	for _, item := range items {
		if item.label == label {
			return true
		}
	}
	return false
}

// The rule list resolves IDs back to names, or a rule is unreadable once written.
func TestRuleListReadsRulesBack(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{Enabled: true, Rules: []config.Rule{
		{Match: "!standup:x", Show: "none"},
		{Match: "space:Work", Sender: "@alice:x", Ring: "all", Sound: "/s/boss.wav"},
		{Sender: "@bob:x", Show: "mention"},
		{Match: "!gone:x", Show: "none"}, // a room we have left
	}})

	next, _ := m.openRuleList()
	m = next
	if m.picker.kind != pickerRuleList {
		t.Fatalf("picker = %v, want the rule list", m.picker.kind)
	}
	if len(m.picker.all) != 4 {
		t.Fatalf("%d rows, want one per rule", len(m.picker.all))
	}

	for _, want := range []string{"Standup", "@alice:x in Work", "@bob:x, anywhere"} {
		if !hasLabel(m.picker.all, want) {
			t.Errorf("the list should name %q: %+v", want, m.picker.all)
		}
	}
	// A rule for a room that is gone still shows, so it stays removable.
	if !hasLabel(m.picker.all, "!gone:x") {
		t.Errorf("a rule for a departed room should still be listed: %+v", m.picker.all)
	}
	if got := m.picker.all[0].detail; got != "show = none" {
		t.Errorf("detail = %q, want what the rule does", got)
	}
	if got := m.picker.all[1].detail; !strings.Contains(got, "ring = all") ||
		!strings.Contains(got, "/s/boss.wav") {
		t.Errorf("detail = %q, want both settings named", got)
	}
}

// Choosing a rule reopens it at the preset picker that wrote it.
func TestRuleListOpensARuleForEditing(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, notifyAll(config.Rule{Match: "!standup:x", Show: "none"}))
	next, _ := m.openRuleList()
	m = next
	m = pickLabel(t, m, "Standup")

	if m.picker.kind != pickerRulePreset {
		t.Fatalf("picker = %v, want the presets for that rule", m.picker.kind)
	}
	if !hasLabel(m.picker.all, "Remove the rule") {
		t.Error("an existing rule should offer removal")
	}
	msg := domain.Message{ID: "$2", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "A", Body: "hi"}
	m = pickLabel(t, m, "Remove")
	if notifies(t, m, msg) != true {
		t.Error("removing the mute should let the room notify again")
	}
}

// With nothing set, the list says so rather than opening an empty chooser.
func TestRuleListWithNoRules(t *testing.T) {
	t.Parallel()

	m, _ := ruling(t, config.Notifications{Enabled: true})
	next, _ := m.openRuleList()
	m = next
	if m.picker.active() {
		t.Error("an empty list is not a chooser")
	}
	if !strings.Contains(m.status(), "no notification rules") {
		t.Errorf("status = %q, should say there are none", m.status())
	}
}

// A space-scoped rule written from a message, the room list or the rail is a place
// entry (not the bare name), so it passes the startup check and reaches the space.
func TestSpaceRulesAreWrittenAsPlaceEntries(t *testing.T) {
	t.Parallel()

	for name, author := range map[string]func(*testing.T, Model) Model{
		"from a message": func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyText("b"))
			m = pickLabel(t, m, "everything in Work")
			return pickLabel(t, m, "Mute")
		},
		"from the room list": func(t *testing.T, m Model) Model {
			m.focus = paneRooms
			m, _ = press(t, m, keyText("b"))
			m = pickLabel(t, m, "everything in Work")
			return pickLabel(t, m, "Mute")
		},
		"from the rail": func(t *testing.T, m Model) Model {
			m.focus = paneRail
			m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
			m, _ = press(t, m, keyText("b"))
			return pickLabel(t, m, "Mute")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, path := ruling(t, notifyAll())
			m = author(t, m)

			reloaded, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(reloaded.Notifications.Rules) != 2 {
				t.Fatalf("rules = %+v, want the one space rule added", reloaded.Notifications.Rules)
			}
			match := lastRule(t, reloaded.Notifications.Rules).Match
			if match != domain.SpaceEntry("Work") {
				t.Errorf("match = %q, want %q", match, domain.SpaceEntry("Work"))
			}
			if kind, ok := domain.ParseEntry(match); !ok || kind != domain.EntryClass {
				t.Errorf("ParseEntry(%q) = %v, %v — want a class of rooms", match, kind, ok)
			}
			if err := setup.PlaceEntries(reloaded); err != nil {
				t.Errorf("the next startup would refuse the config: %v", err)
			}
			room, _ := m.roomByID("!standup:x")
			if got := (setup.Place{Room: m.factsFor(room)}).Reach(match); got != notify.ClassOfRooms {
				t.Errorf("Reach(%q) = %v, want the space to reach a room in it", match, got)
			}
			msg := domain.Message{ID: "$2", RoomID: "!standup:x", Sender: "@alice:x", SenderName: "A", Body: "hi"}
			if notifies(t, m, msg) {
				t.Error("a muted space should not notify for a room in it")
			}
			next, _ := m.openRuleList()
			if !hasLabel(next.picker.all, "Work") {
				t.Errorf("the rule list should name the space: %+v", next.picker.all)
			}
		})
	}
}
