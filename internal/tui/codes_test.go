package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// The scope governs what kith volunteers, never what a keypress answers.
func TestCodeScopeGovernsTheHintNotTheKey(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "Your code is 482910"))
	m = m.WithConfigFile("", config.Config{Codes: config.Codes{Exclude: []string{"room:Alpha"}}})
	if m.hasCode() {
		t.Error("an excluded room should not advertise the code key")
	}
	if !strings.Contains(m.hints(), "reply") {
		t.Fatalf("legend = %q, expected the ordinary timeline legend", m.hints())
	}
	if strings.Contains(m.hints(), "copy code") {
		t.Errorf("legend = %q, should not advertise the code key here", m.hints())
	}
	// Pressing the key still answers.
	pressed, cmd := chord(t, m, "y", "c")
	if cmd == nil || !strings.Contains(pressed.status(), "482910") {
		t.Errorf("status = %q — the key must still work in an excluded room", pressed.status())
	}
}

// The rules reach the running model without a restart.
func TestCodeRulesReachTheModel(t *testing.T) {
	t.Parallel()

	six := 6
	m := yanking(t, msgWith("$1", "Your PIN is 4821"))
	m = m.WithConfigFile("", config.Config{Codes: config.Codes{MinLength: six, MaxLength: 8}})
	if m.hasCode() {
		t.Error("a four-digit PIN should not qualify under a six-character minimum")
	}
	m, _ = chord(t, m, "y", "c")
	if !strings.Contains(m.status(), "no code") {
		t.Errorf("status = %q, want no code found", m.status())
	}
}

// An impossible shape is reported, and the model keeps the defaults.
func TestCodeRulesRejectAnImpossibleShape(t *testing.T) {
	t.Parallel()

	no := false
	m := yanking(t, msgWith("$1", "Your code is 482910"))
	m = m.WithConfigFile("", config.Config{Codes: config.Codes{Letters: &no, Digits: &no}})
	if !strings.Contains(m.status(), "codes:") {
		t.Errorf("status = %q, should report the bad section", m.status())
	}
	if !m.hasCode() {
		t.Error("the model should fall back to the default shape, not to nothing")
	}
}
