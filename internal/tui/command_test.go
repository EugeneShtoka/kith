package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Lists have both a `/` form (this room) and a `:` form (the pane's scope);
// commands about the session or every room are `:` only; commands acting on the
// room you write in are `/` only.
func TestEveryCommandLineCommandIsGlobal(t *testing.T) {
	t.Parallel()

	lists := map[string]bool{
		"files": true, "mentions": true, "search": true,
		"starred": true, "tracked": true, "threads": true, "scheduled": true,
		"caught": true,
	}
	clientWide := map[string]bool{
		"verify": true, "todo": true, "help": true,
		"settings": true, "dnd": true, "join": true, "go": true, "new": true,
	}
	// both act on the room written in as /, and on the open room or the rail's place
	// as : (tab moves between them).
	both := map[string]bool{"shortcut": true}
	want := map[string]bool{}
	for name := range lists {
		want[name] = true
	}
	for name := range both {
		want[name] = true
	}
	for name := range clientWide {
		want[name] = true
	}
	for _, cmd := range commands {
		if !want[cmd.name] {
			t.Errorf(":%s is on the command line — is it really about the client rather "+
				"than about one room? A room-scoped command belongs beside /me.", cmd.name)
		}
		delete(want, cmd.name)
		if strings.HasPrefix(cmd.name, ":") || strings.HasPrefix(cmd.name, "/") {
			t.Errorf("%q carries its own prefix; the name is bare and the colon is the label", cmd.name)
		}
		if cmd.summary == "" {
			t.Errorf(":%s has no summary, so it is invisible in the help overlay", cmd.name)
		}
		if cmd.run == nil {
			t.Errorf(":%s has no handler", cmd.name)
		}
	}
	for name := range want {
		t.Errorf(":%s is missing from the command line", name)
	}

	// Symmetry, so neither table can gain half of a pair unnoticed.
	slashNames := map[string]bool{}
	for _, cmd := range slashCommands {
		slashNames[strings.TrimPrefix(cmd.name, "/")] = true
	}
	for name := range lists {
		if !slashNames[name] {
			t.Errorf(":%s opens a list and has no /%s — a list command has both forms", name, name)
		}
	}
	for name := range both {
		if !slashNames[name] {
			t.Errorf(":%s has no /%s, which it is the room-free half of", name, name)
		}
	}
	for name := range clientWide {
		if slashNames[name] {
			t.Errorf("/%s exists, but :%s has no per-room meaning — one of the two is in the wrong table", name, name)
		}
	}
}

func TestAnUnknownCommandListsWhatThereIs(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	got, cmd := m.runCommandLine("schedulled")
	if cmd != nil {
		t.Error("a typo ran something")
	}
	if !strings.Contains(got.status(), "schedulled") {
		t.Errorf("status %q does not name what was typed", got.status())
	}
	if !strings.Contains(got.status(), ":scheduled") {
		t.Errorf("status %q does not list what is available", got.status())
	}
}

func TestCommandLineParsing(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = m.clearStatus()
	if next, cmd := m.runCommandLine("   "); cmd != nil || next.status() != m.status() {
		t.Errorf("an empty command line ran something or said %q", next.status())
	}
	for _, in := range []string{":scheduled", "scheduled"} {
		if _, cmd := m.runCommandLine(in); cmd == nil {
			t.Errorf("%q did not run", in)
		}
	}
}

// On the command line, accepting a completion runs it — one enter, not two.
func TestAcceptingACommandRunsItImmediately(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m, _ = press(t, m, keyText(":"))
	if m.prompt.kind != promptCommand {
		t.Fatalf("prompt = %d, want the command line", m.prompt.kind)
	}
	if !m.completion.active {
		t.Fatal("the command line opens with its table showing")
	}

	// Narrow to :files, then accept once.
	for _, r := range "files" {
		m, _ = press(t, m, keyText(string(r)))
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.completion.active {
		t.Error("the popup should be closed")
	}
	if m.search.showing() != (filesList{}) {
		t.Errorf("results pane showing %q, want the file list — the command did not run",
			m.search.showing().name())
	}
	// :files opens its own narrowing prompt, so the command line itself is gone.
	if m.prompt.kind != promptFiles {
		t.Errorf("prompt = %d, want the file list's own prompt", m.prompt.kind)
	}
}

func TestCommandLineNarrowsAsYouType(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m, _ = press(t, m, keyText(":"))
	if len(m.completion.candidates) < 2 {
		t.Fatalf("the table opens with %d rows; the test needs at least two to narrow",
			len(m.completion.candidates))
	}
	for _, r := range "sched" {
		m, _ = press(t, m, keyText(string(r)))
	}
	if got := len(m.completion.candidates); got != 1 {
		t.Fatalf("%d candidates after typing \"sched\", want just :scheduled", got)
	}
	// The inserted text is the bare name: the colon is the prompt's label.
	if got := m.completion.candidates[0].text; got != "scheduled" {
		t.Errorf("candidate = %q, want scheduled", got)
	}
}

// The command menu is drawn below the composer, beside the status line it completes.
func TestCommandMenuIsDrawnBelowTheComposer(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m, _ = press(t, m, keyText(":"))

	lines := strings.Split(stripStyles(m.View().Content), "\n")
	first := m.completion.candidates[0].text
	menu, composer := -1, -1
	for i, line := range lines {
		if strings.Contains(line, first) {
			menu = i
		}
		if strings.Contains(line, "NORMAL") {
			composer = i
		}
	}
	if menu < 0 || composer < 0 {
		t.Fatalf("menu=%d composer=%d; expected both on screen", menu, composer)
	}
	if menu < composer {
		t.Errorf("the command menu is above the composer (menu row %d, composer row %d) — "+
			"it belongs beside the status line it is completing", menu, composer)
	}
}

// The shortcut shown comes from the live keymap, so a rebind follows.
func TestCommandsShowTheirShortcut(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	rows := m.commandLineCandidates(":go")
	if len(rows) == 0 {
		t.Fatal(":go is not offered at all")
	}
	if !strings.Contains(rows[0].detail, "ctrl+k") {
		t.Errorf(":go detail = %q, want it to name the shortcut", rows[0].detail)
	}
	for _, row := range m.commandLineCandidates(":scheduled") {
		if strings.Contains(row.detail, "·") {
			t.Errorf(":scheduled detail = %q, but it has no shortcut to name", row.detail)
		}
	}
	keys := config.DefaultKeys()
	keys.JumpTo = "ctrl+g"
	m.keys = newKeymap(keys)
	rows = m.commandLineCandidates(":go")
	if len(rows) == 0 || !strings.Contains(rows[0].detail, "ctrl+g") {
		t.Errorf("after a rebind, detail = %+v, want the new key", rows)
	}
}
