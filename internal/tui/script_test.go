package tui

import (
	"encoding/json"

	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// withContext is `scripted` plus a declared context: one script, its
// `[[command]]` block, and messages in the room.
func withContext(t *testing.T, name, body string, script config.Command, msgs ...domain.Message) Model {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, dir, name, body, 0o755)

	m, _ := attaching(t)
	script.Name = name
	cfg := config.Config{Commands: config.Commands{Dir: dir, Timeout: 5}, UserCommands: []config.Command{script}}
	m = m.WithConfigFile(filepath.Join(home, "config.toml"), asStarted(cfg)).keptIn()
	if len(msgs) > 0 {
		m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: msgs}})
	}
	m, _ = press(t, m, keyText("i"))
	return m
}

func runTyped(t *testing.T, m Model, typed string) Model {
	t.Helper()
	m = typeInto(t, m, typed)
	m, cmd := press(t, m, sendKey())
	return deliver(t, m, cmd)
}

func TestAScriptGetsTheHistoryItAskedFor(t *testing.T) {
	t.Parallel()

	m := withContext(t, "sum", "#!/bin/sh\ncat\n",
		config.Command{Needs: []string{"history:2"}},
		msgWith(mx+"$1", "first"), msgWith(mx+"$2", "second"), msgWith(mx+"$3", "third"))

	m = runTyped(t, m, "/sum")

	var got scriptContextJSON
	if err := json.Unmarshal([]byte(m.editorFor(fieldComposer).text), &got); err != nil {
		t.Fatalf("the script did not receive JSON: %v — got %q", err, m.editorFor(fieldComposer).text)
	}
	if len(got.History) != 2 {
		t.Fatalf("%d messages, want the last 2: %+v", len(got.History), got.History)
	}
	// Oldest first.
	if got.History[0].Body != "second" || got.History[1].Body != "third" {
		t.Errorf("history = %q, %q; want second then third", got.History[0].Body, got.History[1].Body)
	}
	if got.History[0].Sender != "matrix:@a:x" || got.History[0].SenderName != "Alice" {
		t.Errorf("who said it did not travel: %+v", got.History[0])
	}
}

// An undeclared key is absent rather than empty.
func TestAScriptGetsNothingItDidNotAskFor(t *testing.T) {
	t.Parallel()

	m := withContext(t, "bare", "#!/bin/sh\ncat\n", config.Command{}, msgWith(mx+"$1", "a https://one.org here"))

	m = runTyped(t, m, "/bare")

	if got := m.editorFor(fieldComposer).text; got != "{}" {
		t.Errorf("a script with no needs received %q, want an empty object", got)
	}
}

// /proc/<pid>/cmdline is world-readable, so argv carries only the typed argument.
func TestTheConversationNeverReachesTheArgv(t *testing.T) {
	t.Parallel()

	m := withContext(t, "argv", "#!/bin/sh\necho \"argc=$# argv=$*\"\n",
		config.Command{Needs: []string{"history:3", "message"}},
		msgWith(mx+"$1", "something secret"), msgWith(mx+"$2", "and more of it"))

	m = runTyped(t, m, "/argv typed")

	got := m.editorFor(fieldComposer).text
	if got != "argc=1 argv=typed" {
		t.Errorf("argv = %q, want only the typed argument", got)
	}
	if strings.Contains(got, "secret") {
		t.Error("a message body reached the command line")
	}
}

func TestAScriptAsksWhichLinkBeforeItRuns(t *testing.T) {
	t.Parallel()

	m := withContext(t, "save", "#!/bin/sh\necho \"got $KITH_URL\"\n",
		config.Command{Needs: []string{"url"}},
		msgWith(mx+"$1", "read https://one.org and https://two.org"))

	m = typeInto(t, m, "/save")
	m, cmd := press(t, m, sendKey())
	m = deliver(t, m, cmd)

	// Nothing has run yet.
	if got := m.editorFor(fieldComposer).text; got != "/save" {
		t.Fatalf("composer = %q — the script ran before it was told which link", got)
	}
	if m.picker.kind != pickerContext || m.picker.then != actRunScript {
		t.Fatalf("picker = %v/%v, want the script's chooser", m.picker.kind, m.picker.then)
	}
	if !strings.Contains(m.picker.spec.title, "save") || !strings.Contains(m.picker.spec.title, "link") {
		t.Errorf("title = %q, want it to name the command and the thing", m.picker.spec.title)
	}
	if len(m.picker.items) != 2 {
		t.Fatalf("%d rows, want 2", len(m.picker.items))
	}

	// Arrow, not `j`: this chooser filters from the first keystroke.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, accept := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliver(t, m, accept)
	if got := m.editorFor(fieldComposer).text; got != "got https://two.org" {
		t.Errorf("script received %q, want the link that was chosen", got)
	}
}

func TestASingleLinkIsNotAQuestion(t *testing.T) {
	t.Parallel()

	m := withContext(t, "save", "#!/bin/sh\necho \"got $KITH_URL\"\n",
		config.Command{Needs: []string{"url"}},
		msgWith(mx+"$1", "read https://one.org today"))

	m = runTyped(t, m, "/save")

	if m.picker.active() {
		t.Error("one link should not raise a chooser")
	}
	if got := m.editorFor(fieldComposer).text; got != "got https://one.org" {
		t.Errorf("script received %q", got)
	}
}

func TestAScriptThatNeedsALinkAndFindsNoneSaysSo(t *testing.T) {
	t.Parallel()

	m := withContext(t, "save", "#!/bin/sh\necho ran\n",
		config.Command{Needs: []string{"url"}},
		msgWith(mx+"$1", "no links in here at all"))

	m = typeInto(t, m, "/save")
	m, cmd := press(t, m, sendKey())
	m = deliver(t, m, cmd)

	if got := m.editorFor(fieldComposer).text; got != "/save" {
		t.Fatalf("composer = %q — the script should not run without what it declared", got)
	}
	if !strings.Contains(m.status(), "save") || !strings.Contains(m.status(), "no link") {
		t.Errorf("status = %q, want it to name the command and the missing need", m.status())
	}
	if m.running != "" {
		t.Errorf("still holding %q", m.running)
	}
}

func TestOutputSinks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		output  string
		wants   string
		inPlace bool // the composer keeps what was typed
	}{
		{output: "status", wants: "printed this"},
		{output: "none", wants: "ran"},
	} {
		m := withContext(t, "side", "#!/bin/sh\necho 'printed this'\n", config.Command{Output: tc.output})
		m = runTyped(t, m, "/side")

		if !strings.Contains(m.status(), tc.wants) {
			t.Errorf("output %q: status = %q, want %q", tc.output, m.status(), tc.wants)
		}
		if got := m.editorFor(fieldComposer).text; got != "" {
			t.Errorf("output %q put %q in the composer", tc.output, got)
		}
	}
}

func TestASilentScriptIsFineWhenItsOutputIsDiscarded(t *testing.T) {
	t.Parallel()

	m := withContext(t, "quiet", "#!/bin/sh\nexit 0\n", config.Command{Output: "none"})
	m = runTyped(t, m, "/quiet")

	if strings.Contains(m.status(), "printed nothing") {
		t.Errorf("status = %q, want it not to call a discarded output a failure", m.status())
	}
}

func TestAScriptRunsFromItsKey(t *testing.T) {
	t.Parallel()

	m := withContext(t, "sum", "#!/bin/sh\ncat\n",
		config.Command{Needs: []string{"history:1"}, Keys: "alt+t"},
		msgWith(mx+"$1", "only this one"))
	// Script keys never fire while typing.
	m.focus, m.compose.insertMode = paneTimeline, false

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModAlt})
	m = deliver(t, m, cmd)

	if !strings.Contains(m.editorFor(fieldComposer).text, "only this one") {
		t.Errorf("composer = %q, want the script's output", m.editorFor(fieldComposer).text)
	}
}

func TestAKeyBoundScriptStillAsks(t *testing.T) {
	t.Parallel()

	m := withContext(t, "save", "#!/bin/sh\necho \"got $KITH_URL\"\n",
		// Not alt+s: that is spell.open_insert, and shadowing a built-in is refused.
		config.Command{Needs: []string{"url"}, Keys: "alt+g"},
		msgWith(mx+"$1", "https://one.org and https://two.org"))
	m.focus, m.compose.insertMode = paneTimeline, false

	m, _ = press(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})

	if m.picker.kind != pickerContext || m.picker.then != actRunScript {
		t.Fatalf("picker = %v/%v, want the script's chooser", m.picker.kind, m.picker.then)
	}
	if !strings.Contains(m.picker.spec.title, "save") {
		t.Errorf("title = %q, want it to name the command", m.picker.spec.title)
	}
}

func TestAScriptKeyCannotShadowABuiltIn(t *testing.T) {
	t.Parallel()

	km := keymapFor(config.Config{Keys: config.DefaultKeys(), UserCommands: []config.Command{
		{Name: "save", Keys: "gx"},
	}})

	if _, bound := km.scriptFor("g x"); bound {
		t.Error("a script took gx, which opens a link")
	}
	issues := strings.Join(km.issues, "\n")
	if !strings.Contains(issues, "save") || !strings.Contains(issues, "unbind") {
		t.Errorf("issues = %q, want the collision named with the way out", issues)
	}
}

func TestTwoScriptsCannotShareAKey(t *testing.T) {
	t.Parallel()

	km := keymapFor(config.Config{Keys: config.DefaultKeys(), UserCommands: []config.Command{
		{Name: "first", Keys: "alt+t"},
		{Name: "second", Keys: "alt+t"},
	}})

	if name, _ := km.scriptFor("alt+t"); name != "first" {
		t.Errorf("alt+t runs %q, want the first block to hold it", name)
	}
	if !strings.Contains(strings.Join(km.issues, "\n"), `command "second"`) {
		t.Errorf("issues = %+v, want the second reported", km.issues)
	}
}

func TestTheHelpOverlayListsYourScriptKeys(t *testing.T) {
	t.Parallel()

	m := withContext(t, "sum", "#!/bin/sh\ncat\n",
		config.Command{Needs: []string{"history:20"}, Keys: "alt+t"})

	help := strings.Join(m.helpLines(), "\n")

	if !strings.Contains(help, "alt+t") || !strings.Contains(help, "/sum") {
		t.Errorf("the overlay does not list the binding:\n%s", help)
	}
	// And what it is given.
	if !strings.Contains(help, "history:20") {
		t.Error("the overlay does not say what the command is given")
	}
}

func TestAScriptCanPrintIntoTheReader(t *testing.T) {
	t.Parallel()

	m := withContext(t, "whois", "#!/bin/sh\nprintf 'Alice\\nBob\\nCarol\\n'\n",
		config.Command{Output: "pager"})

	m = runTyped(t, m, "/whois")

	if !m.reader.showing(readerScript) {
		t.Fatalf("reader = %v, want the script's", m.reader.kind)
	}
	title, lines, paint := m.readerBody()
	if title != "/whois" {
		t.Errorf("title = %q, want the command that produced it", title)
	}
	if len(lines) != 3 || lines[0] != "Alice" || lines[2] != "Carol" {
		t.Errorf("lines = %+v", lines)
	}
	// No sender palette on script output.
	if paint != nil {
		t.Error("a script's output should not be painted as if it named people")
	}
	// The typed command is cleared.
	if got := m.editorFor(fieldComposer).text; got != "" {
		t.Errorf("composer = %q, want the command taken out of it", got)
	}
}

func TestEachReaderScrollsAgainstItsOwnLength(t *testing.T) {
	t.Parallel()

	m := withContext(t, "long", "#!/bin/sh\nseq 1 200\n", config.Command{Output: "pager"})
	m = sized(t, m)
	m = runTyped(t, m, "/long")

	if len(m.readerContent()) < 200 {
		t.Fatalf("the reader's own content is %d lines, want the script's 200", len(m.readerContent()))
	}
	// Paging to the end lands on the last screenful of this document.
	m = m.scrollHelp(10_000)
	want := len(m.readerContent()) - m.helpRows()
	if m.reader.scroll != want {
		t.Errorf("scrolled to %d, want %d — the limit is this reader's, not the help list's", m.reader.scroll, want)
	}
	if m.reader.scroll <= len(m.helpLines()) && len(m.helpLines()) < 200 {
		t.Error("the limit still looks like the keybinding list's")
	}
}

func TestTheFocusKeyCanBeReplacedByAScript(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Bindings["timeline.open_url_focus"] = "-" // the built-in gives up gX
	script := config.Command{Name: "go", Needs: []string{"url"}, Output: "none", Keys: "gX"}

	m := withContext(t, "go", "#!/bin/sh\necho \"would open $KITH_URL\" >&2\n", script,
		msgWith(mx+"$1", "read https://one.org today"))
	m.keys = keymapFor(config.Config{
		Keys:         keys,
		UserCommands: []config.Command{script},
	})
	m.focus, m.compose.insertMode = paneTimeline, false

	if name, bound := m.keys.scriptFor("g X"); !bound || name != "go" {
		t.Fatalf("gX runs %q (%v); want the script — issues: %+v", name, bound, m.keys.issues)
	}
	if len(m.keys.issues) != 0 {
		t.Errorf("issues = %+v, want none", m.keys.issues)
	}

	m, _ = press(t, m, keyText("g"))
	m, cmd := press(t, m, keyText("X"))
	m = deliver(t, m, cmd)

	if !strings.Contains(m.status(), "go ran") {
		t.Errorf("status = %q, want the script to have run", m.status())
	}
	// output = "none": nothing lands in the composer.
	if got := m.editorFor(fieldComposer).text; got != "" {
		t.Errorf("composer = %q, want nothing", got)
	}
}

func TestTheReplacementStillAsksWhichLink(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Bindings["timeline.open_url_focus"] = "-"
	script := config.Command{Name: "go", Needs: []string{"url"}, Output: "none", Keys: "gX"}

	m := withContext(t, "go", "#!/bin/sh\nexit 0\n", script,
		msgWith(mx+"$1", "https://one.org and https://two.org"))
	m.keys = keymapFor(config.Config{
		Keys:         keys,
		UserCommands: []config.Command{script},
	})
	m.focus, m.compose.insertMode = paneTimeline, false

	m, _ = press(t, m, keyText("g"))
	m, _ = press(t, m, keyText("X"))

	if m.picker.kind != pickerContext || m.picker.then != actRunScript {
		t.Fatalf("picker = %v/%v, want the script's chooser", m.picker.kind, m.picker.then)
	}
	if len(m.picker.items) != 2 {
		t.Errorf("%d rows, want both links", len(m.picker.items))
	}
}

// A sequence bound to a script completes like any chord, without waiting for a third key.
func TestASequenceBoundToAScriptCompletes(t *testing.T) {
	t.Parallel()

	km := keymapFor(config.Config{Keys: config.DefaultKeys(), UserCommands: []config.Command{
		{Name: "go", Keys: "z s"},
	}})

	if !km.boundIn("z s", scopeGlobal, scopeTimeline) {
		t.Error("the resolver does not consider a script sequence finished, so it never fires")
	}
	if !km.incomplete("z", scopeGlobal) {
		t.Error("the first step does not wait for the second")
	}
}
