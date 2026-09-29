package tui

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Writing a message in $EDITOR. Finishing an edit only replaces the draft — nothing
// is sent — and a failed edit leaves the draft exactly as it was.

// externalEditMsg carries the result of an editor session back into the model: the
// editor's failure, else the text it left, read (and the file removed) by the
// callback, so the handler does no I/O.
type externalEditMsg struct {
	err       error  // the editor failed; the draft stays
	readErr   error  // the file could not be read back; the draft stays
	text      string // what the editor left, trailing newlines trimmed
	path      string // the temp file, for the log
	removeErr error  // the temp file could not be removed (a draft copy left behind)
}

// draftFileMsg is the draft written to a temp file for the editor, or why it was not.
type draftFileMsg struct {
	path string
	err  error
}

// openExternalEditor writes the draft to a temporary file, off the Update loop; the
// editor is run on it when that lands (handleDraftFile).
func (m Model) openExternalEditor() (Model, tea.Cmd) {
	draft := m.editorFor(fieldComposer).text
	suffix := m.conf.base.Composer.FileSuffixOrDefault()
	return m, func() tea.Msg { return writeDraftFile(draft, suffix) }
}

// writeDraftFile puts draft in a new temp file; nothing is left behind on failure.
func writeDraftFile(draft, suffix string) draftFileMsg {
	file, err := os.CreateTemp("", "kith-*"+suffix)
	if err != nil {
		return draftFileMsg{err: fmt.Errorf("open a draft file: %w", err)}
	}
	path := file.Name()
	_, err = file.WriteString(draft)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path) // cleanup after the failure already being reported
		return draftFileMsg{err: fmt.Errorf("write the draft: %w", err)}
	}
	return draftFileMsg{path: path}
}

// handleDraftFile runs the configured editor on the written draft, suspending the
// program (tea.ExecProcess needs the path when the command is built).
func (m Model) handleDraftFile(msg draftFileMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not open an editor", msg.err), nil
	}
	argv := m.conf.base.Composer.EditorCommand()
	// #nosec G204 -- argv comes from this user's own config or their $EDITOR, and is
	// passed as separate arguments with no shell, so the path is the only input.
	cmd := exec.CommandContext(m.ctx, argv[0], append(argv[1:], msg.path)...)
	return m, tea.ExecProcess(cmd, editorFinished(msg.path))
}

// editorFinished is the callback run once the editor exits: it reads the draft back
// and removes the file, off the Update loop.
func editorFinished(path string) func(error) tea.Msg {
	return func(err error) tea.Msg {
		msg := externalEditMsg{path: path, err: err}
		if err == nil {
			body, rerr := os.ReadFile(path) // #nosec G304 -- the path is one this process just created
			// Editors add trailing newlines; other whitespace is the author's.
			msg.text, msg.readErr = strings.TrimRight(string(body), "\n"), rerr
		}
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			msg.removeErr = rerr
		}
		return msg
	}
}

// handleExternalEdit takes the edited draft back into the composer.
func (m Model) handleExternalEdit(msg externalEditMsg) (Model, tea.Cmd) {
	// A draft copy left in the temp dir: worth knowing about, not the user's problem now.
	m.logErr(slog.LevelWarn, "remove the external-edit temp file", msg.removeErr, "path", msg.path)
	if msg.err != nil {
		m.logErr(slog.LevelWarn, "external editor failed", msg.err)
		return m.say("the editor exited with an error; your draft is unchanged"), nil
	}
	if msg.readErr != nil {
		m.logErr(slog.LevelWarn, "read the edited draft back failed", msg.readErr)
		return m.say("could not read the draft back; it is unchanged"), nil
	}
	text := msg.text

	// One undo step: ctrl+z brings back the draft as it was before the editor.
	m = m.store(fieldComposer, m.editorFor(fieldComposer).changed(text, len(text), editWhole))
	m.compose.insertMode = true
	m.focus = paneTimeline
	if text == "" {
		return m.say("the editor came back empty; nothing to send"), nil
	}
	lines := strings.Count(text, "\n") + 1
	return m.say(fmt.Sprintf("draft updated — %d line(s)", lines)), nil
}
