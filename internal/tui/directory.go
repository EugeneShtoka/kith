package tui

import (
	"cmp"
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// People are named by kithd (KnownAs on what it sends). The daemon's directory
// (domain.Directory) is read here for the rest: who a card or a hidden person is, and a
// room shown only as a number. Re-read with every room list, since a sync that brings
// names brings rooms.

// directoryState is the directory as last read, and a revision that moves when it changes.
type directoryState struct {
	dir domain.Directory
	rev uint64
}

type directoryMsg struct {
	dir domain.Directory
	err error
}

func (m Model) directoryCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.Directory, func(dir domain.Directory, err error) tea.Msg {
		return directoryMsg{dir: dir, err: err}
	})
}

// handleDirectory keeps a changed directory; on a read failure the last one stays.
func (m Model) handleDirectory(msg directoryMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "read the directory", msg.err)
	if msg.err != nil || msg.dir.Equal(m.dir.dir) {
		return m, nil
	}
	m.dir = directoryState{dir: msg.dir, rev: m.dir.rev + 1}
	return m.rebuiltRail(), nil
}

// memberName is a member's name as kithd names them, else the network's.
func (m Model) memberName(member domain.Member) string {
	return cmp.Or(member.KnownAs, member.Name())
}

// byNumber is label, or the directory's name for it when label is only a number.
func (m Model) byNumber(label string) string {
	if name, ok := m.dir.dir.Named(label); ok {
		return name
	}
	return label
}
