package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
	"github.com/EugeneShtoka/kith/internal/keystore"
)

// The commands folder, as the TUI runs it (internal/tui/runcmd.go, script.go): `/zoom`
// runs <commands dir>/zoom with what follows it as its one argument and $KITH_ARG, the
// context its [[command]] block declares on stdin and in KITH_* variables, its secrets
// from the keyring, and its output goes where the block says. One runs at a time; outside
// its `in` places it is refused, never sent as words. The window runs it itself, as the
// TUI does, not through kith-relay as the desktop's renderer must.

// commandWaitDelay bounds the wait, after killing a command, for writers still holding
// its output open (the TUI's).
const commandWaitDelay = 500 * time.Millisecond

// commandOutputLimit caps what a command can put in the composer (the TUI's).
const commandOutputLimit = 8 << 10

// commandsAt is where the folder is and the keyring its secrets are in.
type commandsAt struct{ dir, keyring string }

// commandRun is one command on its way: what it runs, where, and the context gathered.
type commandRun struct {
	name, path, arg string
	ed              *widget.Editor // the composer it was typed in
	room            domain.RoomID
	block           config.Command
	link            string // the link it was given, once one is picked
}

// commandRan is what a command came to: its output and where it goes, or why none.
type commandRan struct {
	run       *commandRun
	out       domain.ScriptOutput
	body, why string
	truncated bool
}

// commandsDir is the TUI's: [commands] dir, else `commands` beside the device file.
func commandsDir(dir, devicePath string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return filepath.Join(filepath.Dir(devicePath), "commands")
	}
	if home, err := os.UserHomeDir(); err == nil && (dir == "~" || strings.HasPrefix(dir, "~/")) {
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/"))
	}
	return dir
}

// commandPath is the file a command name runs, as the TUI's userCommand finds it: one
// path segment (that is the whole sandbox), an executable file and not a directory.
func commandPath(dir, name string) (string, bool) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsRune(name, 0) {
		return "", false
	}
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	return path, true
}

// commandsHere are the folder's commands that run in the open room, for the `/` list.
func (u *ui) commandsHere() []string {
	entries, err := os.ReadDir(u.cmds.dir)
	if err != nil {
		return nil // no folder is no commands
	}
	var names []string
	for _, e := range entries {
		block, _ := u.device.CommandFor(e.Name())
		if _, ok := commandPath(u.cmds.dir, e.Name()); ok && u.appliesIn(block, u.current) {
			names = append(names, e.Name())
		}
	}
	return names
}

// appliesIn is whether block's command runs in room: its `in` names it, or it has none.
func (u *ui) appliesIn(block config.Command, room domain.RoomID) bool {
	return len(block.In) == 0 || slices.ContainsFunc(block.In, u.rail.served[room].Facts.Names)
}

// userCommand runs the folder's command name, typed in ed with arg after it; false when
// the folder has none of that name, and the words are a message.
func (u *ui) userCommand(ctx context.Context, ed *widget.Editor, name, arg string) bool {
	path, ok := commandPath(u.cmds.dir, name)
	if !ok {
		return false
	}
	block, _ := u.device.CommandFor(name)
	switch {
	case !u.appliesIn(block, u.current):
		u.note = "/" + name + " is only for " + strings.Join(block.In, ", ")
	case u.running != "":
		u.note = u.running + " is still running"
	default:
		u.prepareCommand(ctx, &commandRun{name: name, path: path, arg: arg, ed: ed, room: u.current, block: block})
	}
	u.noteAt = time.Now()
	return true
}

// prepareCommand gathers the context c's block declares, as the TUI's scriptJSON does:
// the message (the one replied to, else the room's newest), its link (the only one, or
// the one picked from several), the room's last messages; then runs it.
func (u *ui) prepareCommand(ctx context.Context, c *commandRun) {
	say := func(why string) { u.note, u.noteAt = "/"+c.name+": "+why, time.Now() }
	shown, msg := u.commandMessage(c)
	var stdin domain.ScriptContext
	env := []string{"KITH_ARG=" + c.arg, "KITH_ROOM_ID=" + string(c.room), "KITH_ROOM=" + u.roomNamed(c.room), "KITH_USER=" + u.selfIn(c.room)}
	for _, entry := range c.block.Needs {
		need, ok := domain.ParseNeed(entry)
		if !ok {
			continue
		}
		if msg == nil && (need.Kind == domain.NeedMessage || need.Kind == domain.NeedURL) {
			say("needs a message and there is none here")
			return
		}
		switch need.Kind {
		case domain.NeedMessage:
			row := domain.ScriptRow(*msg, u.selves[msg.Sender])
			stdin.Message = &row
			env = append(env, "KITH_MESSAGE_ID="+string(msg.ID), "KITH_SENDER="+msg.Sender, "KITH_SENDER_NAME="+msg.SenderName)
		case domain.NeedURL:
			if !u.commandLink(ctx, c, msg) {
				return
			}
			stdin.URL = c.link
			env = append(env, "KITH_URL="+c.link)
		case domain.NeedHistory:
			last := shown[max(0, len(shown)-need.Count):]
			stdin.History = make([]domain.ScriptMessage, len(last))
			for i := range last {
				stdin.History[i] = domain.ScriptRow(last[i], u.selves[last[i].Sender])
			}
		case domain.NeedNone:
		}
	}
	payload, err := json.Marshal(stdin)
	if err != nil {
		say("could not assemble its context: " + err.Error())
		return
	}
	u.running, u.note, u.noteAt = "/"+c.name, "/"+c.name+" …", time.Now()
	timeout := u.device.Commands.RunTimeout()
	go func() {
		ran := u.execute(ctx, c, payload, env, timeout)
		u.mu.Lock()
		u.running, u.ran = "", &ran
		u.mu.Unlock()
		u.w.Invalidate()
	}()
}

// commandLink picks c's link in msg: the one picked already, else its only one; false
// when it has none (said), or asks which of several and runs c once one is picked.
func (u *ui) commandLink(ctx context.Context, c *commandRun, msg *domain.Message) bool {
	if c.link != "" {
		return true
	}
	links := messageLinks(*msg)
	switch len(links) {
	case 0:
		u.note, u.noteAt = "/"+c.name+": no link in that message", time.Now()
		return false
	case 1:
		c.link = links[0]
		return true
	}
	u.linking = &linkChoice{links: links, btns: make([]widget.Clickable, len(links)), take: func(link string) {
		c.link = link
		u.prepareCommand(ctx, c)
	}}
	return false
}

// commandMessage is the room's main timeline as loaded, oldest first, and the message c
// is run on: the one its composer replies to, else the newest; nil for none.
func (u *ui) commandMessage(c *commandRun) ([]domain.Message, *domain.Message) {
	var shown []domain.Message
	if u.timeline.room == c.room {
		for i := range u.timeline.msgs {
			if m := &u.timeline.msgs[i]; m.ThreadRoot == "" || m.ThreadRoot == m.ID {
				shown = append(shown, *m)
			}
		}
	}
	if i := slices.IndexFunc(shown, func(m domain.Message) bool { return m.ID == u.replyTo && c.ed == &u.composer }); i >= 0 {
		return shown, &shown[i]
	}
	if len(shown) == 0 {
		return nil, nil
	}
	return shown, &shown[len(shown)-1]
}

// selfIn is the account of yours on room's network, "" for none.
func (u *ui) selfIn(room domain.RoomID) string {
	for id := range u.selves {
		if domain.NetworkOf(id) == domain.NetworkOf(string(room)) {
			return id
		}
	}
	return ""
}

// execute runs c's program with its context and secrets, under the timeout.
func (u *ui) execute(ctx context.Context, c *commandRun, payload []byte, env []string, timeout time.Duration) commandRan {
	out, _ := domain.ParseScriptOutput(c.block.Output) // checked as the device file loaded
	ran := commandRan{run: c, out: out}
	secrets, err := commandSecrets(u.cmds.keyring, c.block)
	if err != nil {
		ran.why = err.Error()
		return ran
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.path, c.arg) // #nosec G204 -- a file the user made executable in their own commands directory, by name
	cmd.Env = slices.Concat(os.Environ(), env, secrets)
	cmd.Stdin = bytes.NewReader(payload) // on stdin, never argv: /proc/<pid>/cmdline is world-readable
	cmd.WaitDelay = commandWaitDelay
	stdout, cleanOut, err := captureFile()
	if err != nil {
		ran.why = err.Error()
		return ran
	}
	defer cleanOut()
	stderr, cleanErr, err := captureFile()
	if err != nil {
		ran.why = err.Error()
		return ran
	}
	defer cleanErr()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	switch {
	case ctx.Err() != nil:
		ran.why = "gave up after " + human.Duration(timeout)
	case runErr != nil:
		ran.why = commandFailure(runErr, readCapture(stderr))
	}
	if ran.why != "" {
		return ran
	}
	ran.body = strings.TrimRight(readCapture(stdout), "\n")
	if len(ran.body) > commandOutputLimit {
		ran.body, ran.truncated = ran.body[:commandOutputLimit], true
	}
	if strings.TrimSpace(ran.body) == "" && out != domain.OutputNone {
		ran.why = "printed nothing"
	}
	return ran
}

// commandSecrets are block's declared secrets as its environment, from the keyring, as
// the TUI's commandSecretEnv reads them: a missing one is an error naming it, so the
// program never runs with an empty variable.
func commandSecrets(keyring string, block config.Command) ([]string, error) {
	env := make([]string, 0, len(block.Secrets))
	for _, name := range block.Secrets {
		value, err := keystore.Secret(keyring, config.SecretRef(block.Name, name))
		switch {
		case err != nil:
			return nil, fmt.Errorf("could not read its secret %s: %w", name, err)
		case value == "":
			return nil, fmt.Errorf("its secret %s is not set: the TUI's Settings → Advanced → Commands", name)
		}
		env = append(env, config.SecretEnv(name)+"="+value)
	}
	return env, nil
}

// landCommand puts what a command printed where its block says, as the TUI's
// handleCommandRan does. Truncated output is never sent, copied or dropped unread: it
// goes to the composer, or to be read; so does output for the composer of a room left
// meanwhile, rather than into another room's.
func (u *ui) landCommand(ctx context.Context, gtx C) {
	r, c := u.ran, u.ran.run
	u.ran = nil
	said := "/" + c.name
	if r.why != "" {
		u.note, u.noteAt = said+": "+r.why, time.Now()
		return
	}
	out := r.out
	if r.truncated && out != domain.OutputPager {
		out = domain.OutputCompose
	}
	if out == domain.OutputCompose && c.room != u.current {
		out = domain.OutputPager
	}
	done := func(note string) {
		if c.room == u.current {
			c.ed.SetText("")
			delete(u.mentions, c.ed)
		}
		u.note, u.noteAt = note, time.Now()
	}
	switch out {
	case domain.OutputPager:
		done("")
		u.openReading(said, r.body, r.truncated)
	case domain.OutputSend:
		done("Sending")
		if c.ed == &u.composer {
			u.replyTo = ""
		}
		go u.send(ctx, c.room, domain.Draft{Body: r.body})
	case domain.OutputStatus:
		line, _, _ := strings.Cut(r.body, "\n")
		done(said + ": " + line)
	case domain.OutputClipboard:
		done("")
		u.copy(gtx, r.body, said+"'s output is copied")
	case domain.OutputNone:
		done(said + " ran")
	case domain.OutputCompose:
		u.composeOutput(r)
	}
}

// composeOutput puts what a command printed in the composer it was typed in, to send.
func (u *ui) composeOutput(r *commandRan) {
	c := r.run
	c.ed.SetText(r.body)
	n := len([]rune(r.body))
	c.ed.SetCaret(n, n)
	u.focus = c.ed
	binding, _ := u.keyConf.Binding("insert.send")
	send, _, _ := strings.Cut(binding, ",")
	said := "/" + c.name + ": " + cmp.Or(strings.TrimSpace(send), "Send") + " to send"
	if r.truncated {
		said = "/" + c.name + " printed more than fits; " + said
	}
	u.note, u.noteAt = said, time.Now()
}

// captureFile is a temp file for a command's output, and its cleanup: a file, not a
// pipe, so a forked grandchild holding it open cannot hold the run (the TUI's).
func captureFile() (*os.File, func(), error) {
	file, err := os.CreateTemp("", "kith-cmd-*")
	if err != nil {
		return nil, nil, fmt.Errorf("could not capture the output: %w", err)
	}
	return file, func() {
		_ = file.Close()           // read, or abandoned: nothing to report
		_ = os.Remove(file.Name()) // as above
	}, nil
}

func readCapture(file *os.File) string {
	data, err := os.ReadFile(file.Name())
	if err != nil {
		return ""
	}
	return string(data)
}

// commandFailure is a failed command's first stderr line, else the TUI's hints.
func commandFailure(err error, stderr string) string {
	line, _, _ := strings.Cut(stderr, "\n")
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	if errors.Is(err, syscall.ENOEXEC) {
		return "cannot run it — does the file start with a #! line, like #!/bin/sh?"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "cannot run it — is it executable? chmod +x the file"
	}
	return err.Error()
}
