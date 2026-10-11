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

// User commands: `/zoom` runs `<config dir>/commands/zoom` and its stdout lands in the
// composer (or another configured sink). Nothing is sent without a keypress unless configured;
// a failure leaves the composer as typed. The argument is one argv element (and $KITH_ARG);
// no shell is involved.

// commandEnvPrefix is the environment namespace scripts read (shared with the notification
// command hook).
const commandEnvPrefix = "KITH_"

// commandWaitDelay bounds the wait, after killing a command, for remaining writers to close.
// With captureFile it stops a forked grandchild holding stdout from stretching a timeout or
// success.
const commandWaitDelay = 500 * time.Millisecond

// commandOutputLimit caps what a script can put in the composer.
const commandOutputLimit = 8 << 10

type commandRanMsg struct {
	name string // as typed, "/zoom"
	// room is where it was typed; the output belongs there even if you moved on.
	room domain.RoomID
	out  domain.ScriptOutput // where stdout goes (config.Script.Output)
	body string              // stdout, trailing newline trimmed
	// why explains a command that produced nothing usable.
	why       string
	truncated bool // body hit commandOutputLimit
}

// userCommand resolves a name to an executable in the commands directory; false means an
// ordinary message. The name must be a single path segment: that is the whole sandbox.
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
	// Stat on the event loop deliberately: it decides whether submit() sends the line. The
	// executable bit separates a command from a note.
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	return path, true
}

// commandsDir is the configured scripts directory, else the one beside the config file.
func (m Model) commandsDir() (string, error) {
	if dir := strings.TrimSpace(m.conf.base.Commands.Dir); dir != "" {
		return expandHome(dir)
	}
	if m.conf.path == "" {
		return "", nil
	}
	return filepath.Join(filepath.Dir(m.conf.path), "commands"), nil
}

// runUserCommand starts a command once it has the context it declared (script.go). One at a
// time, so a double enter cannot book two meetings.
func (m Model) runUserCommand(name, path, arg string, room domain.Room) (Model, tea.Cmd) {
	if m.running != "" {
		return m.say(m.running + " is still running"), nil
	}
	m.running = name
	m.script = pendingScript{name: name, path: path, arg: arg, room: room, needs: m.scriptNeeds(name)}
	return m.resolveScript()
}

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
		commandEnvPrefix+"USER="+m.selfIn(room.ID),
	)
	env = append(env, m.scriptEnv()...)
	// Built on the loop: the goroutine must not read the model.
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

// captureFile is a temp file for a command's output, and its cleanup. A file, not a buffer:
// any other writer becomes a pipe whose EOF waits for every holder (see commandWaitDelay).
func captureFile() (*os.File, func(), error) {
	file, err := os.CreateTemp("", "kith-cmd-*")
	if err != nil {
		return nil, nil, fmt.Errorf("could not capture the output: %w", err)
	}
	return file, func() {
		// Cleanup of a temp file already read or abandoned; nothing to report.
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

// commandFailure describes a failed command: its first stderr line if any, else something
// better than the raw error.
func commandFailure(err error, stderr string) string {
	if line := strings.TrimSpace(firstLine(stderr)); line != "" {
		return line
	}
	// ENOEXEC almost always means a missing shebang, which a shell hides by retrying as a script.
	if errors.Is(err, syscall.ENOEXEC) {
		return "cannot run it — does the file start with a #! line, like #!/bin/sh?"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "cannot run it — is it executable? chmod +x the file"
	}
	return err.Error()
}

func firstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before
	}
	return s
}

// handleCommandRan routes output to its sink, or says why there is none (composer untouched).
func (m Model) handleCommandRan(msg commandRanMsg) (Model, tea.Cmd) {
	m.running = ""
	m.script = pendingScript{}
	if msg.why != "" {
		return m.say(msg.name + ": " + msg.why), nil
	}
	// Truncated output is never sent, copied or discarded unreviewed: it goes to a sink that shows
	// it.
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

// doneWithCommand clears the command text for sinks that put nothing in the composer, so the
// next enter does not rerun it.
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

// sendCommandOutput posts output to the room it was typed in, emptying the composer; no
// pending reply or mentions go with it.
func (m Model) sendCommandOutput(msg commandRanMsg) (Model, tea.Cmd) {
	m.compose.input, m.compose.drafted, m.compose.replyTo = "", nil, ""
	m = m.say("sending…")
	m, stop := m.stopTyping()
	return m, tea.Batch(m.sendCmd(msg.room, domain.Draft{Body: msg.body}), stop)
}
