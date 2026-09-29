package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The rule these tests exist for: a name stays logical until it is drawn, and a
// *sentence* built from names is drawn left to right with each name isolated and
// reordered in its own direction.
const (
	hebRoom   = "משפחה"
	hebPerson = "דנה לוי"
)

func hebVisual(s string) string { return reorder(s, bidi.RightToLeft) }

// displayTitle is how a sentence of ours is drawn — a title, a status line — without
// the width or the styling.
func displayTitle(s string) string { return drawLine(s, lineSpec{sentence: true}) }

// namedRoom is a model in a Hebrew-named room with one message from a Hebrew-named
// person — enough for every surface that names either of them.
func namedRoom(t *testing.T) Model {
	t.Helper()
	m := sized(t, update(t, newModel(), roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: hebRoom}}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{
			ID: "$1", RoomID: "!a:x", Sender: "@d:x", SenderName: hebPerson,
			Body: "hi", Timestamp: at(1),
		}},
	}})
	m.focus, m.compose.insertMode = paneTimeline, false
	return m.clearStatus()
}

// A description that names a person and a room reads as one sentence, with the English
// words in the order they were written and each name reversed in place.
func TestRTLNamesInSentences(t *testing.T) {
	t.Parallel()

	m := namedRoom(t)
	ruled, _ := m.openRuleForSender()
	rm := ruled

	logical := []string{
		isolate(hebPerson) + " in " + isolate(hebRoom),
		isolate(hebPerson) + ", anywhere",
		"everyone in " + isolate(hebRoom),
	}
	want := []string{
		hebVisual(hebPerson) + " in " + hebVisual(hebRoom),
		hebVisual(hebPerson) + ", anywhere",
		"everyone in " + hebVisual(hebRoom),
	}
	got := make([]string, 0, len(rm.aimedAt.ruleScopes))
	for _, scope := range rm.aimedAt.ruleScopes {
		got = append(got, scope.what)
	}
	if strings.Join(got, "|") != strings.Join(logical, "|") {
		t.Errorf("rule scopes =\n  %q\nwant\n  %q", got, logical)
	}

	// Drawn, the rows say it in visual order: the picker draws what it is given.
	body := stripStyles(strings.Join(rm.pickerLines(60, 6), "\n"))
	for _, sentence := range want {
		if !strings.Contains(body, sentence) {
			t.Errorf("the picker should show %q, got:\n%s", sentence, body)
		}
	}
	// And so does the status line, which draws its sentence the same way.
	said := m.say("do not disturb: " + logical[2])
	status := strings.Split(stripStyles(said.View().Content), "\n")[m.height-1]
	if !strings.Contains(status, want[2]) {
		t.Errorf("status line = %q, want it to contain %q", status, want[2])
	}
}

// The mute picker had both bugs in one list: its room row was reordered twice and its
// person row not at all.
func TestRTLNamesInMuteTargets(t *testing.T) {
	t.Parallel()

	m := namedRoom(t)
	targets := m.muteTargets()
	if len(targets) < 2 {
		t.Fatalf("got %d targets", len(targets))
	}
	if displayTitle(targets[0].label) != hebVisual(hebRoom) {
		t.Errorf("room target draws as %q, want %q", displayTitle(targets[0].label), hebVisual(hebRoom))
	}
	if displayTitle(targets[1].what) != hebVisual(hebPerson)+", anywhere" {
		t.Errorf("person target draws as %q", displayTitle(targets[1].what))
	}
	// match is what gets written to the config, and it is an ID either way.
	if targets[0].match != "!a:x" {
		t.Errorf("room target matches on %q, want the room ID", targets[0].match)
	}
}

// A mention row shows the reordered name and inserts the logical one.
func TestRTLMentionRowShowsVisualInsertsLogical(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, domain.Member{UserID: "@d:x", DisplayName: hebPerson})
	m = typeInto(t, m, "@")
	row, ok := m.selectedCandidate()
	if !ok {
		t.Fatal("no candidate")
	}
	if row.label != isolate(hebPerson) {
		t.Errorf("label = %q, want the logical name, isolated — the popup draws it", row.label)
	}
	if drawn := stripStyles(m.candidateRow(row, false, 40)); !strings.Contains(drawn, hebVisual(hebPerson)) {
		t.Errorf("row = %q, want the name drawn reordered", drawn)
	}
	if row.text != hebPerson {
		t.Errorf("text = %q, want the logical name — it goes into the message", row.text)
	}
	m, _ = press(t, m, keyText("d"))
	m, _ = press(t, m, keyCode(tea.KeyTab))
	if !strings.Contains(m.compose.input, hebPerson) {
		t.Errorf("composer holds %q, want the logical name", m.compose.input)
	}
}
