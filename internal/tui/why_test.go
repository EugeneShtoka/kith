package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/notify"
)

// The explanation lists every rule in force, in resolution order, and marks the decider.
func TestWhyExplainsTheChain(t *testing.T) {
	t.Parallel()

	night, err := notify.ParseWhen("22:00-08:00")
	if err != nil {
		t.Fatalf("ParseWhen: %v", err)
	}
	m, _ := silencing(t) // room !a:x ("Alpha"), in the Work space
	m.notifications.rules = []notify.Rule{
		{Name: "everything", Show: new(notify.LevelMention)},
		{Name: "Quiet hours", When: &night, Show: new(notify.LevelNone)},
		{Name: "Work", Match: "space:Work", Show: new(notify.LevelAll)},
		{Name: "Alice", Sender: "@alice:x", Show: new(notify.LevelAll)},
	}

	raw := m.whyLines(time.Date(2026, 8, 23, 23, 30, 0, 0, time.Local))
	lines := strings.Join(raw, "\n")
	for _, want := range []string{"everything", "Quiet hours", "Work"} {
		if !strings.Contains(lines, want) {
			t.Errorf("the explanation should list %q:\n%s", want, lines)
		}
	}
	// Work is the narrowest rule that speaks without a sender.
	deciding := ""
	for _, line := range raw {
		if strings.Contains(line, "decides") {
			deciding = line
		}
	}
	if !strings.Contains(deciding, "Work") {
		t.Errorf("the deciding rule is %q, want Work — the narrowest one awake", deciding)
	}
	// Alice's rule needs a sender, so it is reported as pending.
	if !strings.Contains(lines, "depends on who is writing") {
		t.Errorf("a sender rule should be named as unresolved:\n%s", lines)
	}
	if !strings.Contains(lines, "all notifies") {
		t.Errorf("the explanation should state the outcome:\n%s", lines)
	}
}

// A temporary mute is explained with its remaining time.
func TestWhyExplainsAMute(t *testing.T) {
	t.Parallel()

	m, _ := silencing(t)
	m.notifications.rules = []notify.Rule{{Name: "everything", Show: new(notify.LevelAll)}}
	m.notifications.temps = notify.Temps{}.Add(until(hidden("!a:x", "", "Alpha"), time.Now().Add(90*time.Minute)))

	lines := strings.Join(m.whyLines(time.Now()), "\n")
	if !strings.Contains(lines, "muted") || !strings.Contains(lines, "1h") {
		t.Errorf("a mute should be explained with its remaining time:\n%s", lines)
	}
	if !strings.Contains(lines, "nothing notifies here") {
		t.Errorf("the outcome should be stated plainly:\n%s", lines)
	}
}

// The overlay opens from /why and closes on any key.
func TestWhyOverlayOpensAndCloses(t *testing.T) {
	t.Parallel()

	m, _ := silencing(t)
	room, _ := m.currentRoom()
	_, m, _ = m.composerCommand("/why", room)
	if !m.reader.showing(readerWhy) {
		t.Fatal("/why should open the explanation")
	}
	m, _ = press(t, m, keyText("j"))
	if m.reader.showing(readerWhy) {
		t.Error("any key should close it")
	}
}

func TestWhyWithNoRoom(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	if got := strings.Join(m.whyLines(time.Now()), "\n"); !strings.Contains(got, "no room open") {
		t.Errorf("lines = %q, want it to say there is nothing to explain", got)
	}
}

// With notifications off the explanation leads with the switch, then the rules anyway.
func TestWhySaysNotificationsAreOff(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.notifications.rules = []notify.Rule{{Name: "all rooms", Show: new(notify.LevelMention)}}
	m.notifications.on = false

	lines := strings.Join(m.whyLines(time.Now()), "\n")
	if !strings.Contains(lines, "notifications are off") {
		t.Errorf("explanation = %q, want it to lead with the switch", lines)
	}
	if !strings.Contains(lines, "all rooms") {
		t.Errorf("explanation = %q, want the rules shown anyway", lines)
	}
}
