package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// openFile hands the selected message's m.file attachment (PDF, zip, …) to the
// desktop's handler, like `U` on a link.
func (m Model) openFile(msg domain.Message) (Model, tea.Cmd) {
	opener, err := m.fileOpenerCommand()
	if err != nil {
		return m.say(err.Error()), nil
	}
	name := msg.Media.Name
	if name == "" {
		name = "the file"
	}
	job := m.jobFor(msg)
	return m.doing("opening " + isolate(name) + "…"), m.openFileCmd(opener, job, name)
}

// fileOpenerCommand is the desktop's file handler, or `[display.media] opener`.
func (m Model) fileOpenerCommand() ([]string, error) {
	return resolveCommand(m.prefs.display.Media.Opener, fileOpenerCandidates, "file opener")
}

var fileOpenerCandidates = [][]string{
	{"xdg-open"},
	{"gio", "open"},
	{"open"},
}

// openFileCmd puts the attachment on disk and hands it over, off the event loop. A
// no-cache room still opens files; the copy is just left for Trim to reclaim.
func (m Model) openFileCmd(opener []string, job mediaJob, name string) tea.Cmd {
	cache, backend, ctx := m.pics.cache, m.backend, m.ctx
	return func() tea.Msg {
		path := ensureOnDisk(ctx, cache, backend.LoadImage, job)
		if path == "" {
			return fileOpenedMsg{name: name, err: errNoFile}
		}
		if err := launch(ctx, opener, path); err != nil {
			return fileOpenedMsg{name: name, err: err}
		}
		return fileOpenedMsg{name: name}
	}
}

// fileOpenedMsg reports what came of handing a file to the desktop.
type fileOpenedMsg struct {
	name string
	err  error
}

// handleFileOpened reports the handoff's outcome.
func (m Model) handleFileOpened(msg fileOpenedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not open "+msg.name, msg.err), nil
	}
	return m.say("opened " + isolate(msg.name)), nil
}

// errNoFile reports an attachment that could not be fetched.
var errNoFile = errors.New("it could not be fetched")
