package tui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// opened returns a model with the settings screen opened by its key, plus the config path.
func opened(t *testing.T, notifs config.Notifications) (Model, string) {
	t.Helper()
	m, path := ruling(t, notifs)
	m, _ = press(t, m, keyText(","))
	if m.picker.kind != pickerSetting {
		t.Fatalf("\",\" should open the settings, got picker %v", m.picker.kind)
	}
	return m, path
}

// typeIn types into an open prompt and submits, running the resulting command.
func typeIn(t *testing.T, m Model, text string) Model {
	t.Helper()
	if !m.prompt.active() {
		t.Fatal("no prompt is open")
	}
	m.prompt.input = ""
	for _, r := range text {
		m, _ = press(t, m, keyText(string(r)))
	}
	next, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliver(t, next, cmd)
	return m
}

// rowFor finds a settings row by its label.
func rowFor(t *testing.T, m Model, label string) pickerItem {
	t.Helper()
	for _, item := range m.picker.items {
		if item.label == label {
			return item
		}
	}
	t.Fatalf("no row labeled %q in %+v", label, m.picker.items)
	return pickerItem{}
}

// Every row shows what the setting is currently set to.
func TestSettingsShowCurrentValues(t *testing.T) {
	t.Parallel()

	m, _ := opened(t, config.Notifications{Enabled: true})
	for _, want := range []struct{ label, value string }{
		{"Notifications", "on"},
		{"Notify me for", "mention"},
		{"Play a sound for", "same"},
		{"Quiet hours", "none"},
		{"During quiet hours, notify for", "none"},
		{"Desktop popups", "off"},
		{"Sender name width", "no limit"},
		{"Images", "placeholder"},
		{"React palette ranking", "room"},
	} {
		if got := rowFor(t, m, want.label).detail; got != want.value {
			t.Errorf("%s shows %q, want %q", want.label, got, want.value)
		}
	}
}

// Choosing a level is written to the file, the live config and the decision.
func TestSettingChoiceChangesTheDecision(t *testing.T) {
	t.Parallel()

	m, path := opened(t, config.Notifications{Enabled: true})
	chat := domain.Message{ID: "$2", RoomID: "!standup:x", Sender: "@bob:x", SenderName: "B", Body: "morning"}
	if notifies(t, m, chat) {
		t.Fatal("mention-only should not notify for ordinary chat")
	}

	m = pickLabel(t, m, "notify") // the row: Notify me for
	if m.picker.kind != pickerSettingValue {
		t.Fatalf("a choice setting should offer its values, got %v", m.picker.kind)
	}
	m = pickLabel(t, m, "all")

	if !notifies(t, m, chat) {
		t.Error("on=all should notify for ordinary chat, live")
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Notifications.Rules) != 1 || reloaded.Notifications.Rules[0].Show != "all" {
		t.Errorf("saved rules = %+v, want the account-wide rule at show = all", reloaded.Notifications.Rules)
	}
	if !strings.Contains(m.status(), "all") {
		t.Errorf("status = %q, should report the new value", m.status())
	}
}

// A choice picker marks the current value.
func TestSettingChoiceMarksTheCurrentValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ row, current string }{
		{"notify", "mention"},
		{"images", "placeholder"},
		{"react", "room"},
	} {
		m, _ := opened(t, config.Notifications{Enabled: true})
		m = pickLabel(t, m, tc.row)
		marked := ""
		for _, item := range m.picker.items {
			if strings.HasPrefix(item.detail, "current") {
				marked = item.label
			}
		}
		if marked != tc.current {
			t.Errorf("%s: marked %q as current, want %q", tc.row, marked, tc.current)
		}
	}
}

// A toggle takes effect in the running app, not at the next start.
func TestSettingToggleTakesEffectImmediately(t *testing.T) {
	t.Parallel()

	m, path := opened(t, config.Notifications{Enabled: true})
	if !m.prefs.openInsert {
		t.Fatal("rooms open in insert mode by default")
	}
	m = pickLabel(t, m, "ready") // Open rooms ready to type
	if m.prefs.openInsert {
		t.Error("the toggle did not reach the live model")
	}
	if m.picker.active() {
		t.Error("a toggle has nothing more to ask; the picker should close")
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Display.OpenInInsertMode() {
		t.Error("the toggle was not saved")
	}
}

// A value the parser will not take is reported and changes nothing.
func TestSettingRejectsAValueThatWillNotParse(t *testing.T) {
	t.Parallel()

	m, path := opened(t, config.Notifications{Enabled: true})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m = pickLabel(t, m, "width") // Sender name width
	m = typeIn(t, m, "wide")
	if !strings.Contains(m.status(), "not a number") {
		t.Errorf("status = %q, should say what was wrong", m.status())
	}
	if m.conf.base.Display.MaxNameLength != 0 {
		t.Errorf("width = %d, should be untouched", m.conf.base.Display.MaxNameLength)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Error("a rejected value should not have rewritten the file")
	}
}

// A number typed in is applied and saved.
func TestSettingNumberIsAppliedAndSaved(t *testing.T) {
	t.Parallel()

	m, path := opened(t, config.Notifications{Enabled: true})
	m = pickLabel(t, m, "width")
	m = typeIn(t, m, "24")
	if m.conf.base.Display.MaxNameLength != 24 {
		t.Errorf("width = %d, want 24", m.conf.base.Display.MaxNameLength)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Display.MaxNameLength != 24 {
		t.Errorf("saved width = %d, want 24", reloaded.Display.MaxNameLength)
	}
}

// Quiet hours are a scheduled rule, validated by the rules' own parser.
func TestSettingQuietHoursUseTheRuleParser(t *testing.T) {
	t.Parallel()

	m, path := opened(t, config.Notifications{Enabled: true})
	m = pickLabel(t, m, "quiet")
	m = typeIn(t, m, "22:00-08:00")
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Notifications.Rules) != 1 || reloaded.Notifications.Rules[0].When != "22:00-08:00" {
		t.Errorf("saved rules = %+v, want one scheduled rule", reloaded.Notifications.Rules)
	}
	if _, ok := m.silencedBy(time.Date(2026, 8, 23, 23, 30, 0, 0, time.Local)); !ok {
		t.Error("the live rules have no quiet window")
	}

	// A time that is not a time is refused, with the parser's own complaint.
	m, _ = press(t, m, keyText(","))
	m = pickLabel(t, m, "quiet")
	m = typeIn(t, m, "22-08")
	if !strings.Contains(m.status(), "HH:MM") {
		t.Errorf("status = %q, should carry the parser's complaint", m.status())
	}
	if got := quietWindowText(m.conf.base); got != "22:00-08:00" {
		t.Errorf("window = %q, a rejected time should leave the working one alone", got)
	}
}

// Quiet hours can be cleared: the prompt opens on the range, and emptying it removes the rule.
func TestSettingQuietHoursCanBeCleared(t *testing.T) {
	t.Parallel()

	m, _ := opened(t, config.Notifications{Enabled: true, Rules: []config.Rule{
		{Name: "quiet hours", Show: "none", When: "22:00-08:00"},
	}})
	if rowFor(t, m, "Quiet hours").detail != "22:00-08:00" {
		t.Fatalf("row = %q", rowFor(t, m, "Quiet hours").detail)
	}
	m = pickLabel(t, m, "quiet")
	if m.prompt.input != "22:00-08:00" {
		t.Errorf("the prompt starts with %q, want the range so it can be edited", m.prompt.input)
	}
	m = typeIn(t, m, "")
	if rules := m.conf.base.Notifications.Rules; len(rules) != 0 {
		t.Errorf("rules = %+v, want the quiet rule removed", rules)
	}
}

// An edit never writes through to the config it started from: a save of that config
// may still be encoding it on a command goroutine.
func TestASettingLeavesThePreviousConfigUntouched(t *testing.T) {
	t.Parallel()

	m, _ := opened(t, config.Notifications{Enabled: true, Rules: []config.Rule{
		{Name: "quiet hours", Show: "none", When: "22:00-08:00"},
		{Name: "work", Match: "space:Work", Show: "all"},
	}})
	before := m.conf.base // what an in-flight save holds
	m = pickLabel(t, m, "quiet")
	m = typeIn(t, m, "")
	if rules := m.conf.base.Notifications.Rules; len(rules) != 1 || rules[0].Name != "work" {
		t.Fatalf("rules = %+v, want only the work rule", rules)
	}
	if rules := before.Notifications.Rules; len(rules) != 2 || rules[0].Name != "quiet hours" || rules[1].Name != "work" {
		t.Fatalf("the earlier config changed under its save: %+v", rules)
	}
}

// Code length is one range row; rows are picked by config key since labels share "length".
func TestSettingCodeShape(t *testing.T) {
	t.Parallel()

	m, path := opened(t, config.Notifications{Enabled: true})
	if got := rowFor(t, m, "Code length").detail; got != "4-8" {
		t.Errorf("length shows %q, want 4-8", got)
	}
	m = pickLabel(t, m, "codes.leng")
	if m.prompt.input != "4-8" {
		t.Errorf("prompt starts with %q, want the range", m.prompt.input)
	}
	m = typeIn(t, m, "6")
	if m.conf.base.Codes.Min() != 6 || m.conf.base.Codes.Max() != 6 {
		t.Errorf("length = %d-%d, want 6-6", m.conf.base.Codes.Min(), m.conf.base.Codes.Max())
	}
	if m.prefs.codes.rules.MinLength != 6 {
		t.Errorf("live rules are %+v — the change did not reach them", m.prefs.codes.rules)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Codes.MaxLength != 6 {
		t.Errorf("saved max = %d, want 6", reloaded.Codes.MaxLength)
	}
}

// A code shape that could never match is refused when applied, naming what to change.
func TestSettingCodeShapeRefusesTheImpossible(t *testing.T) {
	t.Parallel()

	m, _ := opened(t, config.Notifications{Enabled: true})

	// Digits off while a digit is still required: refused, and it says which.
	before := m.prefs.codes.rules
	m = pickLabel(t, m, "codes.d")
	if !strings.Contains(m.status(), "require_digit") {
		t.Errorf("status = %q, should name the setting in the way", m.status())
	}
	if m.prefs.codes.rules != before {
		t.Errorf("rules changed to %+v despite the refusal", m.prefs.codes.rules)
	}

	// Drop the requirement first, and the same toggle is then fine.
	m, _ = press(t, m, keyText(","))
	m = pickLabel(t, m, "codes.r")
	if m.prefs.codes.rules.RequireDigit {
		t.Fatalf("rules = %+v, want the digit requirement off", m.prefs.codes.rules)
	}
	m, _ = press(t, m, keyText(","))
	m = pickLabel(t, m, "codes.d")
	if m.prefs.codes.rules.Digits {
		t.Fatalf("rules = %+v, want digits off", m.prefs.codes.rules)
	}

	// Now letters are the only characters left, so turning them off too is refused.
	m, _ = press(t, m, keyText(","))
	before = m.prefs.codes.rules
	m = pickLabel(t, m, "codes.let")
	if !strings.Contains(m.status(), "could not apply") {
		t.Errorf("status = %q, should refuse a shape that matches nothing", m.status())
	}
	if m.prefs.codes.rules != before {
		t.Errorf("rules changed to %+v despite the refusal", m.prefs.codes.rules)
	}
}

// Every row of the settings table is complete and self-consistent.
func TestSettingsTableIsWellFormed(t *testing.T) {
	t.Parallel()

	var cfg config.Config
	seen := make(map[string]bool, len(settingsList))
	for _, s := range settingsList {
		if seen[s.key] {
			t.Errorf("%s is listed twice", s.key)
		}
		seen[s.key] = true
		if s.label == "" || s.show == nil {
			t.Errorf("%s is incomplete: %+v", s.key, s)
			continue
		}
		// Only a settingOpen row writes nothing.
		if wantsSet := s.kind != settingOpen; wantsSet != (s.set != nil) {
			t.Errorf("%s: set != nil is %v, want %v for kind %v", s.key, s.set != nil, wantsSet, s.kind)
			continue
		}
		if s.show(cfg) == "" {
			t.Errorf("%s shows nothing", s.key)
		}
		switch s.kind {
		case settingChoice:
			if len(s.choices) < 2 {
				t.Errorf("%s is a choice with %d options", s.key, len(s.choices))
			}
			current := s.show(cfg)
			found := false
			for _, c := range s.choices {
				if c.value == current {
					found = true
				}
				if c.label == "" || c.detail == "" {
					t.Errorf("%s: option %q is undescribed", s.key, c.value)
				}
			}
			if !found {
				t.Errorf("%s shows %q, which is not one of its choices", s.key, current)
			}
		case settingOpen:
			if len(s.choices) != 0 || s.edit != nil {
				t.Errorf("%s leads somewhere; it should hold no value of its own", s.key)
			}
		case settingToggle, settingText:
			if len(s.choices) != 0 {
				t.Errorf("%s has choices but is not a choice setting", s.key)
			}
		}
		// What the prompt offers for editing must be accepted back.
		if s.kind == settingText {
			round := cfg
			if err := s.set(&round, editText(s, cfg)); err != nil {
				t.Errorf("%s cannot re-accept its own %q: %v", s.key, editText(s, cfg), err)
			}
		}
	}
}

// Auto-copy refuses to turn on without a clipboard command and an include, saying which is missing.
func TestAutoCopyToggleRefusesWhatCannotRun(t *testing.T) {
	t.Parallel()

	const row = "Auto-copy codes as they arrive"

	m, _ := opened(t, config.Notifications{Enabled: true})
	if got := rowFor(t, m, row).detail; got != "off" {
		t.Errorf("row shows %q, want off — it ships off", got)
	}

	// No clipboard command: refused, and the status says which piece is missing.
	m = pickLabel(t, m, "Auto-copy")
	if m.conf.base.Clipboard.AutoCopy {
		t.Error("the toggle should have refused, not switched on into doing nothing")
	}
	if !strings.Contains(m.status(), "clipboard") {
		t.Errorf("status = %q, should name the missing clipboard command", m.status())
	}

	// A command but nowhere named: still refused, and now for the other reason.
	cfg := m.conf.base.Clone()
	cfg.Clipboard.Command = "wl-copy"
	next, _ := m.applyConfig(cfg, "", "")
	m = next
	m = m.closePicker()
	m, _ = press(t, m, keyText(","))
	m = pickLabel(t, m, "Auto-copy")
	if m.conf.base.Clipboard.AutoCopy {
		t.Error("nowhere named means everywhere; the toggle should still refuse")
	}
	if !strings.Contains(m.status(), "include") {
		t.Errorf("status = %q, should name the missing place", m.status())
	}

	// Both in place: it switches on, and reads on.
	cfg = m.conf.base
	cfg.Codes.Include = []string{"space:Bridges"}
	next, _ = m.applyConfig(cfg, "", "")
	m = next
	m = m.closePicker()
	m, _ = press(t, m, keyText(","))
	m = pickLabel(t, m, "Auto-copy")
	if !m.conf.base.Clipboard.AutoCopy {
		t.Fatal("with both prerequisites met the toggle should switch it on")
	}
	m = m.closePicker()
	m, _ = press(t, m, keyText(","))
	if got := rowFor(t, m, row).detail; got != "on" {
		t.Errorf("row shows %q, want on", got)
	}

	// And off again, unconditionally — turning something off never needs prerequisites.
	m = pickLabel(t, m, "Auto-copy")
	if m.conf.base.Clipboard.AutoCopy {
		t.Error("the toggle should switch it back off")
	}
}

// The popup timeout row reads its sentinels back in words.
func TestPopupTimeoutRowReadsBackInWords(t *testing.T) {
	t.Parallel()

	seconds := func(i int) *int { return &i }
	for name, tc := range map[string]struct {
		set  *int
		want string
	}{
		"unset shows the default": {nil, "15s"},
		"negative is never":       {seconds(-1), "until dismissed"},
		"zero defers":             {seconds(0), "your desktop's default"},
		"a positive is seconds":   {seconds(30), "30s"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := timeoutNote(config.Notifications{Timeout: tc.set}); got != tc.want {
				t.Errorf("timeoutNote() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Any negative timeout is normalized to -1.
func TestPopupTimeoutRowTakesTheNegative(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		typed string
		want  *int
	}{
		"empty unsets":       {"", nil},
		"-1 is kept":         {"-1", func() *int { i := -1; return &i }()},
		"-30 is normalized":  {"-30", func() *int { i := -1; return &i }()},
		"0 is kept":          {"0", func() *int { i := 0; return &i }()},
		"a positive is kept": {"45", func() *int { i := 45; return &i }()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var cfg config.Config
			if err := setPopupTimeout(&cfg, tc.typed); err != nil {
				t.Fatalf("setPopupTimeout(%q) = %v, want it accepted", tc.typed, err)
			}
			switch {
			case tc.want == nil && cfg.Notifications.Timeout != nil:
				t.Errorf("timeout = %d, want it unset", *cfg.Notifications.Timeout)
			case tc.want != nil && cfg.Notifications.Timeout == nil:
				t.Errorf("timeout is unset, want %d", *tc.want)
			case tc.want != nil && *cfg.Notifications.Timeout != *tc.want:
				t.Errorf("timeout = %d, want %d", *cfg.Notifications.Timeout, *tc.want)
			}
		})
	}

	var cfg config.Config
	err := setPopupTimeout(&cfg, "soon")
	if err == nil {
		t.Fatal("a value that is not a number was accepted")
	}
	if !strings.Contains(err.Error(), "-1") {
		t.Errorf("error = %q, want it to name -1 as the way to say until-dismissed", err)
	}
}
