package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Commands you write yourself: `/zoom` runs `<config dir>/commands/zoom` and its stdout
// lands in the composer (or another configured sink). Nothing is sent without a
// keypress unless configured, and a failure leaves the composer as typed. The argument
// is one argv element (and $KITH_ARG) — there is no shell anywhere in the chain.

// commandEnvPrefix is the environment namespace scripts read (shared with the
// notification command hook).
const commandEnvPrefix = "KITH_"

// commandWaitDelay bounds the wait, after killing a command, for anything still
// writing to close. Together with captureFile it stops a forked grandchild holding
// stdout (`sleep 30`, a backgrounded browser) from stretching a timeout or a success.
const commandWaitDelay = 500 * time.Millisecond

// commandOutputLimit caps what a script can put in the composer.
const commandOutputLimit = 8 << 10

// commandRanMsg carries a user command's result back into the model.
type commandRanMsg struct {
	name string // as typed, "/zoom"
	// room is where it was typed; the output belongs there even if you moved on.
	room domain.RoomID
	out  domain.ScriptOutput // where stdout goes (config.Script.Output)
	body string              // stdout, trailing newline trimmed
	// why is what to say when the command produced nothing usable.
	why       string
	truncated bool // body hit commandOutputLimit
}

// userCommand resolves a command name to an executable in the commands directory;
// false means the body is an ordinary message. The name must be a single path segment
// — that is the whole sandbox.
func (m Model) userCommand(name string) (string, bool) {
	name = strings.TrimPrefix(name, "/")
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", false
	}
	dir, err := m.commandsDir()
	if err != nil || dir == "" {
		return "", false
	}
	path := filepath.Join(dir, name)
	// A stat on the event loop, deliberately: it decides whether submit() sends the
	// line. The executable bit separates a command from a note in the same folder.
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	return path, true
}

// commandsDir is where the scripts live: the configured directory, else the one
// beside the config file.
func (m Model) commandsDir() (string, error) {
	if dir := strings.TrimSpace(m.conf.base.Commands.Dir); dir != "" {
		return expandHome(dir)
	}
	if m.conf.path == "" {
		return "", nil
	}
	return filepath.Join(filepath.Dir(m.conf.path), "commands"), nil
}

// runUserCommand starts a command once it has the context it declared (script.go).
// One at a time: a double enter must not book two meetings.
func (m Model) runUserCommand(name, path, arg string, room domain.Room) (Model, tea.Cmd) {
	if m.running != "" {
		return m.say(m.running + " is still running"), nil
	}
	m.running = name
	m.script = pendingScript{name: name, path: path, arg: arg, room: room, needs: m.scriptNeeds(name)}
	return m.resolveScript()
}

// launchScript runs the command now that every need is answered.
func (m Model) launchScript() (Model, tea.Cmd) {
	script := m.script
	m = m.doing(script.name + " …")
	return m, m.userCommandCmd(script.name, script.path, script.arg, script.room)
}

// userCommandCmd runs the script off the event loop and reports what it produced.
func (m Model) userCommandCmd(name, path, arg string, room domain.Room) tea.Cmd {
	timeout := m.conf.base.Commands.RunTimeout()
	env := append(os.Environ(),
		commandEnvPrefix+"ARG="+arg,
		commandEnvPrefix+"ROOM_ID="+string(room.ID),
		commandEnvPrefix+"ROOM="+m.roomLabel(room),
		commandEnvPrefix+"USER="+m.me,
	)
	env = append(env, m.scriptEnv()...)
	// Built here, on the loop: the goroutine must not read the model.
	payload, payloadErr := m.scriptJSON()
	out := m.scriptOutput(name)
	parent := m.ctx
	return func() tea.Msg {
		if payloadErr != nil {
			return commandRanMsg{name: name, room: room.ID, why: "could not assemble its context: " + payloadErr.Error()}
		}
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, arg) // #nosec G204 -- the path is a file the user made executable in their own commands directory
		cmd.Env = env
		// On stdin, never argv: /proc/<pid>/cmdline is world-readable.
		cmd.Stdin = bytes.NewReader(payload)
		cmd.WaitDelay = commandWaitDelay
		stdout, cleanOut, err := captureFile()
		if err != nil {
			return commandRanMsg{name: name, room: room.ID, why: err.Error()}
		}
		defer cleanOut()
		stderr, cleanErr, err := captureFile()
		if err != nil {
			return commandRanMsg{name: name, room: room.ID, why: err.Error()}
		}
		defer cleanErr()
		cmd.Stdout, cmd.Stderr = stdout, stderr

		runErr := cmd.Run()
		msg := commandRanMsg{name: name, room: room.ID, out: out}
		switch {
		case ctx.Err() != nil:
			msg.why = fmt.Sprintf("gave up after %s", timeout)
		case runErr != nil:
			msg.why = commandFailure(runErr, readCapture(stderr))
		}
		if msg.why != "" {
			return msg
		}
		body := strings.TrimRight(readCapture(stdout), "\n")
		if len(body) > commandOutputLimit {
			body, msg.truncated = body[:commandOutputLimit], true
		}
		if strings.TrimSpace(body) == "" {
			if msg.out != domain.OutputNone {
				msg.why = "printed nothing"
			}
			return msg
		}
		msg.body = body
		return msg
	}
}

// captureFile is a temporary file to collect a command's output in, and its cleanup.
// A file, not a buffer: any other writer becomes a pipe whose EOF waits for every
// holder, not for the command (see commandWaitDelay).
func captureFile() (*os.File, func(), error) {
	file, err := os.CreateTemp("", "kith-cmd-*")
	if err != nil {
		return nil, nil, fmt.Errorf("could not capture the output: %w", err)
	}
	return file, func() {
		// Cleanup of a temp file already read (or abandoned): nothing to report to.
		_ = file.Close()
		_ = os.Remove(file.Name())
	}, nil
}

// readCapture is everything a command wrote to one of its capture files.
func readCapture(file *os.File) string {
	data, err := os.ReadFile(file.Name())
	if err != nil {
		return ""
	}
	return string(data)
}

// commandFailure is what to say about a command that exited badly: its own first
// line of stderr where it wrote one, else something better than the raw error.
func commandFailure(err error, stderr string) string {
	if line := strings.TrimSpace(firstLine(stderr)); line != "" {
		return line
	}
	// ENOEXEC almost always means a missing shebang — which a shell hides by
	// retrying the file as a script, so it works by hand and fails here.
	if errors.Is(err, syscall.ENOEXEC) {
		return "cannot run it — does the file start with a #! line, like #!/bin/sh?"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "cannot run it — is it executable? chmod +x the file"
	}
	return err.Error()
}

// firstLine is s up to its first newline.
func firstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before
	}
	return s
}

// handleCommandRan routes a command's output to its sink, or says why there is none
// (leaving the composer as it was).
func (m Model) handleCommandRan(msg commandRanMsg) (Model, tea.Cmd) {
	m.running = ""
	m.script = pendingScript{}
	if msg.why != "" {
		return m.say(msg.name + ": " + msg.why), nil
	}
	// Truncated output is never sent, copied or discarded unreviewed: it goes to a
	// sink that shows it.
	if msg.truncated && msg.out != domain.OutputPager {
		return m.composeCommandOutput(msg)
	}
	switch msg.out {
	case domain.OutputPager:
		return m.openPager(msg.name, msg.body, msg.truncated)
	case domain.OutputSend:
		return m.sendCommandOutput(msg)
	case domain.OutputStatus:
		return m.doneWithCommand().say(msg.name + ": " + firstLine(msg.body)), nil
	case domain.OutputClipboard:
		return m.doneWithCommand().copy(msg.body, describeCopy(msg.name+"'s output", msg.body))
	case domain.OutputNone:
		return m.doneWithCommand().say(msg.name + " ran"), nil
	case domain.OutputCompose:
	}
	return m.composeCommandOutput(msg)
}

// doneWithCommand clears the command text from the composer, for sinks that put
// nothing there, so the next enter does not run it again.
func (m Model) doneWithCommand() Model {
	m = m.store(fieldComposer, newEditor("").end())
	m.compose.drafted = nil
	return m
}

// composeCommandOutput replaces the typed command in the composer with its output.
func (m Model) composeCommandOutput(msg commandRanMsg) (Model, tea.Cmd) {
	m = m.store(fieldComposer, newEditor(msg.body).end())
	m.compose.insertMode = true
	said := msg.name + " — " + m.keys.keyHint(scopeInsert, actSend) + " to send"
	if msg.truncated {
		said = msg.name + " printed more than fits; " + said
	}
	return m.doing(said), nil
}

// sendCommandOutput posts a command's output to the room it was typed in. The
// composer is emptied and no pending reply or mentions travel with it.
func (m Model) sendCommandOutput(msg commandRanMsg) (Model, tea.Cmd) {
	m.compose.input, m.compose.drafted, m.compose.replyTo = "", nil, ""
	m = m.say("sending…")
	m, stop := m.stopTyping()
	return m, tea.Batch(m.sendCmd(msg.room, domain.Draft{Body: msg.body}), stop)
}
