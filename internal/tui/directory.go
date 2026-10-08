package tui

import (
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// People are named from the daemon's directory (domain.Directory): the best name any
// account or bridge gives any of a person's numbers and network IDs. Your own names
// come first: a room's [[display.name]], an identity's alias. The directory is read
// again with every room list, since a sync that brings names brings rooms.

// directoryState is the directory as last read, and a revision that moves when it
// changes.
type directoryState struct {
	dir domain.Directory
	rev uint64
}

// directoryMsg is the daemon's directory.
type directoryMsg struct {
	dir domain.Directory
	err error
}

// directoryCmd reads the directory.
func (m Model) directoryCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.Directory, func(dir domain.Directory, err error) tea.Msg {
		return directoryMsg{dir: dir, err: err}
	})
}

// handleDirectory keeps a directory that changed; one that could not be read keeps
// the last.
func (m Model) handleDirectory(msg directoryMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "read the directory", msg.err)
	if msg.err != nil || msg.dir.Equal(m.dir.dir) {
		return m, nil
	}
	m.dir = directoryState{dir: msg.dir, rev: m.dir.rev + 1}
	return m.rebuiltRail(), nil
}

// memberName is a member's name, the phone book's when they show only as a number.
func (m Model) memberName(member domain.Member) string { return m.byNumber(member.Name()) }

// byNumber is label, or the directory's name for it when label is only a number.
func (m Model) byNumber(label string) string {
	if name, ok := m.dir.dir.Named(label); ok {
		return name
	}
	return label
}
