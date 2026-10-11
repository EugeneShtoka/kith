package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// openFile hands the selected message's file attachment to the desktop's handler.
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

// openFileCmd fetches the attachment and hands it over, off the event loop. A no-cache room
// still opens files; Trim reclaims the copy.
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

type fileOpenedMsg struct {
	name string
	err  error
}

func (m Model) handleFileOpened(msg fileOpenedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not open "+msg.name, msg.err), nil
	}
	return m.say("opened " + isolate(msg.name)), nil
}

// errNoFile reports an attachment that could not be fetched.
var errNoFile = errors.New("it could not be fetched")
