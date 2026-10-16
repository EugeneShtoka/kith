package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// openFile hands the selected message's file attachment to the desktop's handler.
func (m Model) openFile(msg domain.Message) (Model, tea.Cmd) {
	opener, err := m.fileOpenerCommand()
	return m.handOff(msg, opener, err, "the file", "opened", "open")
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

// handOff fetches msg's attachment onto disk (see ensureOnDisk) and launches program
// on it, off the event loop. A no-cache room still opens files; Trim reclaims the copy.
// unnamed stands in for a file with no name; done and fail word the outcome.
func (m Model) handOff(msg domain.Message, program []string, err error, unnamed, done, fail string) (Model, tea.Cmd) {
	if err != nil {
		return m.say(err.Error()), nil
	}
	name := msg.Media.Name
	if name == "" {
		name = unnamed
	}
	out := handedOffMsg{name: name, done: done, fail: fail}
	cache, backend, ctx, job := m.pics.cache, m.backend, m.ctx, m.jobFor(msg)
	return m.doing("opening " + isolate(name) + "…"), func() tea.Msg {
		if path := ensureOnDisk(ctx, cache, backend.LoadImage, job); path == "" {
			out.err = errNoFile
		} else {
			out.err = launch(ctx, program, path)
		}
		return out
	}
}

type handedOffMsg struct {
	name, done, fail string
	err              error
}

func (m Model) handleHandedOff(msg handedOffMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not "+msg.fail+" "+msg.name, msg.err), nil
	}
	return m.say(msg.done + " " + isolate(msg.name)), nil
}

// errNoFile reports an attachment that could not be fetched.
var errNoFile = errors.New("it could not be fetched")
