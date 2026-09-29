package tui

import (
	tea "charm.land/bubbletea/v2"
)

// asModel converts Update's (tea.Model, tea.Cmd) into the concrete Model. Nearly
// every test's Update goes through it, so it also holds the row-cache invariant
// after each one (see staleRow): a panic, since it has no *testing.T.
func asModel(mdl tea.Model, cmd tea.Cmd) (Model, tea.Cmd) {
	m := mdl.(Model)
	if err := staleRow(m); err != nil {
		panic(err)
	}
	if m.hasFrameRows {
		panic("a Model left Update carrying View's room list (frameRows)")
	}
	return m, cmd
}
