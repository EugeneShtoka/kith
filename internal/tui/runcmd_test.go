package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// scripted gives a model whose commands directory holds one script, plus the
// directory so a test can add more.
func scripted(t *testing.T, name, body string) (Model, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, dir, name, body, 0o755)

	m, _ := attaching(t)
	cfg := config.Config{Commands: config.Commands{Dir: dir, Timeout: 5}}
	m = m.WithConfigFile(filepath.Join(home, "config.toml"), cfg).keptIn()
	m, _ = press(t, m, keyText("i"))
	return m, dir
}

func write(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// The whole point: a command you wrote produces text, and the text lands in the
// composer for you to send — or not.
func TestUserCommandProposesItsOutput(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "zoom", "#!/bin/sh\necho \"Zoom: https://example.org/j/$KITH_ARG\"\n")
	m = typeInto(t, m, "/zoom standup")
	m, cmd := press(t, m, sendKey())
	m = deliver(t, m, cmd)

	if got := m.editorFor(fieldComposer).text; got != "Zoom: https://example.org/j/standup" {
		t.Errorf("composer = %q, want the script's output", got)
	}
	if !m.compose.insertMode {
		t.Error("the proposal should leave you in the composer, ready to send")
	}
	if !strings.Contains(m.status(), "to send") {
		t.Errorf("status = %q, want it to say the output is a proposal", m.status())
	}
}

// Nothing is sent. That is the rule the whole design turns on: a script that
// misfires costs a keystroke, not a message in a room with other people in it.
func TestUserCommandSendsNothingByItself(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "gif", "#!/bin/sh\necho 'a cat falling over'\n")
	backend, ok := m.backend.(*sendingBackend)
	if !ok {
		t.Fatal("the test backend is not the recording one")
	}
	m = typeInto(t, m, "/gif cat")
	m, cmd := press(t, m, sendKey())
	m = deliver(t, m, cmd)

	if len(backend.sent) != 0 {
		t.Errorf("sent %+v, want nothing until enter is pressed again", backend.sent)
	}
	// And the second enter sends what was proposed.
	_, sendCmd := press(t, m, sendKey())
	deliver(t, m, sendCmd)
	if len(backend.sent) != 1 || backend.sent[0].Body != "a cat falling over" {
		t.Errorf("sent %+v, want the proposal", backend.sent)
	}
}

// The room's context reaches the script, which is what lets one script behave
// differently in different rooms.
func TestUserCommandGetsTheRoomInItsEnvironment(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "where", "#!/bin/sh\necho \"$KITH_ROOM_ID in $KITH_ROOM\"\n")
	m = typeInto(t, m, "/where")
	m, cmd := press(t, m, sendKey())
	m = deliver(t, m, cmd)

	if got := m.editorFor(fieldComposer).text; got != "!a:x in Alpha" {
		t.Errorf("composer = %q, want the room it ran in", got)
	}
}

// A failing script changes nothing and explains itself in its own words.
func TestFailingCommandLeavesTheComposerAlone(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "broken", "#!/bin/sh\necho 'no credentials' >&2\nexit 1\n")
	m = typeInto(t, m, "/broken")
	m, cmd := press(t, m, sendKey())
	m = deliver(t, m, cmd)

	if got := m.editorFor(fieldComposer).text; got != "/broken" {
		t.Errorf("composer = %q, want what was typed, untouched", got)
	}
	if !strings.Contains(m.status(), "no credentials") {
		t.Errorf("status = %q, want the script's own explanation", m.status())
	}
}

// A script that prints nothing is not a message.
func TestSilentCommandSaysSo(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "quiet", "#!/bin/sh\nexit 0\n")
	m = typeInto(t, m, "/quiet")
	m, cmd := press(t, m, sendKey())
	m = deliver(t, m, cmd)

	if got := m.editorFor(fieldComposer).text; got != "/quiet" {
		t.Errorf("composer = %q, want what was typed", got)
	}
	if !strings.Contains(m.status(), "printed nothing") {
		t.Errorf("status = %q", m.status())
	}
}

// A hung script is killed and reported, not left holding the client.
func TestHungCommandIsKilled(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, dir, "hang", "#!/bin/sh\nsleep 30\n", 0o755)

	m, _ := attaching(t)
	// One second, so the test waits about that long rather than the default fifteen.
	m = m.WithConfigFile(filepath.Join(home, "config.toml"),
		config.Config{Commands: config.Commands{Dir: dir, Timeout: 1}})
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/hang")
	m, cmd := press(t, m, sendKey())
	started := time.Now()
	m = deliver(t, m, cmd)

	// The property that broke: killing the shell leaves `sleep 30` holding the pipe,
	// so reading to end-of-file waited the whole thirty seconds and reported the
	// timeout long after it had passed. See commandWaitDelay.
	if waited := time.Since(started); waited > 5*time.Second {
		t.Errorf("waited %s for a 1s timeout — the kill is not closing the pipes", waited)
	}
	if !strings.Contains(m.status(), "gave up") {
		t.Errorf("status = %q, want the timeout reported", m.status())
	}
	if got := m.editorFor(fieldComposer).text; got != "/hang" {
		t.Errorf("composer = %q, want what was typed", got)
	}
}

// A name is one path segment, and that is the whole of the sandbox: "run what is in
// my commands folder" must not be able to mean anything else.
func TestCommandNameCannotClimbOut(t *testing.T) {
	t.Parallel()

	m, dir := scripted(t, "ok", "#!/bin/sh\necho fine\n")
	// A real executable one level up, which a climbing name would reach.
	write(t, filepath.Dir(dir), "outside", "#!/bin/sh\necho reached\n", 0o755)

	for _, name := range []string{"/../outside", "/..", "/.", "//outside", "/sub/outside"} {
		if path, ok := m.userCommand(name); ok {
			t.Errorf("%q resolved to %q, want no command", name, path)
		}
	}
	if _, ok := m.userCommand("/ok"); !ok {
		t.Error("the ordinary name stopped working")
	}
}

// A file nobody made executable is a note in the same folder, not a command.
func TestNonExecutableFileIsNotACommand(t *testing.T) {
	t.Parallel()

	m, dir := scripted(t, "ok", "#!/bin/sh\necho fine\n")
	write(t, dir, "notes", "just a reminder\n", 0o644)
	if _, ok := m.userCommand("/notes"); ok {
		t.Error("a non-executable file was offered as a command")
	}
}

// A built-in wins where the names collide, so a script cannot quietly change what
// /me means.
func TestBuiltInBeatsAScriptOfTheSameName(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "me", "#!/bin/sh\necho 'not this'\n")
	backend, ok := m.backend.(*sendingBackend)
	if !ok {
		t.Fatal("the test backend is not the recording one")
	}
	m = typeInto(t, m, "/me waves")
	m, cmd := press(t, m, sendKey())
	deliver(t, m, cmd)

	if len(backend.sent) != 1 || !backend.sent[0].Emote || backend.sent[0].Body != "waves" {
		t.Errorf("sent %+v, want the built-in emote", backend.sent)
	}
}

// The completion offers your scripts and stops offering a deleted one. The name is
// typed before the async scan lands: the popup must not close while it is in flight.
func TestCompletionOffersYourScripts(t *testing.T) {
	t.Parallel()

	m, dir := scripted(t, "zoom", "#!/bin/sh\necho hi\n")
	m, scan := press(t, m, keyText("/"))
	m = typeInto(t, m, "zo")
	if !m.completion.active {
		t.Fatal("the command list closed while its directory scan was in flight")
	}
	m = deliver(t, m, scan)
	if len(m.completion.candidates) != 1 || m.completion.candidates[0].text != "/zoom" {
		t.Errorf("candidates = %+v, want your script", m.completion.candidates)
	}

	if err := os.Remove(filepath.Join(dir, "zoom")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	gone, _ := attaching(t)
	gone = gone.WithConfigFile(filepath.Join(filepath.Dir(dir), "config.toml"),
		config.Config{Commands: config.Commands{Dir: dir}})
	gone, _ = press(t, gone, keyText("i"))
	gone, goneScan := press(t, gone, keyText("/"))
	gone = typeInto(t, gone, "zo")
	gone = deliver(t, gone, goneScan)
	if gone.completion.active {
		t.Error("a deleted script is still offered")
	}
}

// The directory is scanned once per popup open, not once per keystroke.
func TestCommandScanRunsOncePerOpenNotPerKeystroke(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "zoom", "#!/bin/sh\necho hi\n")

	m, scan := press(t, m, keyText("/"))
	if scan == nil {
		t.Fatal("opening the command popup did not start a directory scan")
	}
	m = deliver(t, m, scan)

	// Other commands (draft writes) may appear; another scan must not.
	for _, r := range "zoo" {
		var cmd tea.Cmd
		m, cmd = press(t, m, keyText(string(r)))
		for _, msg := range msgsOf(t, cmd) {
			if _, rescanned := msg.(userCommandsMsg); rescanned {
				t.Fatalf("typing %q rescanned the directory — the scan is back on the keystroke path", r)
			}
		}
	}
	if len(m.completion.candidates) != 1 || m.completion.candidates[0].text != "/zoom" {
		t.Errorf("candidates = %+v, want the script still offered", m.completion.candidates)
	}
}

// One at a time: a second enter while /zoom is still talking to Zoom would book a
// second meeting.
func TestOneCommandAtATime(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "slow", "#!/bin/sh\nsleep 5\n")
	m = typeInto(t, m, "/slow")
	m, cmd := press(t, m, sendKey())
	if m.running != "/slow" {
		t.Fatalf("running = %q, want the command in flight", m.running)
	}
	again, _ := press(t, m, sendKey())
	if !strings.Contains(again.status(), "still running") {
		t.Errorf("status = %q, want it to refuse the second run", again.status())
	}
	_ = cmd // killed with the test's context
}

// A backgrounded child inheriting stdout (a launched browser) must not keep the
// command running.
func TestBackgroundedChildDoesNotHoldTheCommand(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "launch", "#!/bin/sh\nsleep 20 &\necho 'Zoom: https://example.org/j/1'\n")
	m = typeInto(t, m, "/launch")
	m, cmd := press(t, m, sendKey())
	started := time.Now()
	m = deliver(t, m, cmd)

	if waited := time.Since(started); waited > 5*time.Second {
		t.Errorf("waited %s for a script that finished at once", waited)
	}
	if got := m.editorFor(fieldComposer).text; got != "Zoom: https://example.org/j/1" {
		t.Errorf("composer = %q, want the output", got)
	}
}

// A script with no `#!` line fails with ENOEXEC (a shell hides this); the status says
// why and the composer keeps what was typed.
func TestScriptWithNoShebangSaysWhy(t *testing.T) {
	t.Parallel()

	m, _ := scripted(t, "noshebang", "echo 'this file has no shebang'\n")
	m = typeInto(t, m, "/noshebang")
	m, cmd := press(t, m, sendKey())
	m = deliver(t, m, cmd)

	if !strings.Contains(m.status(), "#!") {
		t.Errorf("status = %q, want it to name the missing shebang", m.status())
	}
	if got := m.editorFor(fieldComposer).text; got != "/noshebang" {
		t.Errorf("composer = %q, want what was typed", got)
	}
}

// deliverTwice runs a command and then the command its handler returned.
func deliverTwice(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	next, followUp := asModel(m.Update(msg))
	return deliver(t, next, followUp)
}

// A command whose output is "send" posts on the first enter.
func TestSendOutputPostsStraightAway(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, dir, "zoom", "#!/bin/sh\nprintf 'Zoom meeting started\\nMeeting ID: 653-052-4978\\n'\n", 0o755)

	m, backend := attaching(t)
	m = m.WithConfigFile(filepath.Join(home, "config.toml"), config.Config{
		Commands: config.Commands{Dir: dir, Timeout: 5, Scripts: []config.Script{{Name: "zoom", Output: "send"}}},
	})
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/zoom")
	m, cmd := press(t, m, sendKey())
	m = deliverTwice(t, m, cmd)

	if len(backend.sent) != 1 {
		t.Fatalf("sent %+v, want the output posted without a second press", backend.sent)
	}
	if !strings.Contains(backend.sent[0].Body, "Meeting ID: 653-052-4978") {
		t.Errorf("sent %q, want the script's whole output", backend.sent[0].Body)
	}
	// Nothing is left waiting to confirm.
	if got := m.editorFor(fieldComposer).text; got != "" {
		t.Errorf("composer = %q, want it emptied", got)
	}
	if strings.Contains(m.status(), "to send") {
		t.Errorf("status = %q, want no proposal left over", m.status())
	}
}

// The output posts to the room it was typed in, even after you moved on.
func TestSentOutputGoesToTheRoomItWasTypedIn(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, dir, "zoom", "#!/bin/sh\necho 'a link'\n", 0o755)

	m, backend := attaching(t)
	m = m.WithConfigFile(filepath.Join(home, "config.toml"), config.Config{
		Commands: config.Commands{Dir: dir, Timeout: 5, Scripts: []config.Script{{Name: "zoom", Output: "send"}}},
	})
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/zoom")
	m, cmd := press(t, m, sendKey())

	// Moved to the other room while the script was running.
	next, _ := m.selectRoom(domain.Room{ID: "!b:x", Name: "Bravo"})
	moved := next
	deliverTwice(t, moved, cmd)

	if backend.roomID != "!a:x" {
		t.Errorf("sent to %q, want the room the command was typed in", backend.roomID)
	}
}

// A truncated output is never sent unreviewed, whatever the policy says: the message
// would end mid-sentence, which is exactly the case for looking first.
func TestTruncatedOutputIsNeverSentUnreviewed(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// More than commandOutputLimit.
	write(t, dir, "flood", "#!/bin/sh\nyes 'aaaaaaaaaaaaaaaaaaaa' | head -c 20000\n", 0o755)

	m, backend := attaching(t)
	m = m.WithConfigFile(filepath.Join(home, "config.toml"), config.Config{
		Commands: config.Commands{Dir: dir, Timeout: 5, Scripts: []config.Script{{Name: "flood", Output: "send"}}},
	})
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/flood")
	m, cmd := press(t, m, sendKey())
	m = deliverTwice(t, m, cmd)

	if len(backend.sent) != 0 {
		t.Errorf("sent %d messages, want none — truncated output waits to be looked at", len(backend.sent))
	}
	if !strings.Contains(m.status(), "more than fits") {
		t.Errorf("status = %q, want it to say the output was cut", m.status())
	}
}
