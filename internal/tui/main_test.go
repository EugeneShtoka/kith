package tui

import (
	tea "charm.land/bubbletea/v2"
)

// asModel converts Update's result into the concrete Model and checks the row-cache
// and rail-memo invariants (see staleRow, staleRail) after each call, panicking since it
// has no *testing.T.
func asModel(mdl tea.Model, cmd tea.Cmd) (Model, tea.Cmd) {
	m := mdl.(Model)
	if err := staleRow(m); err != nil {
		panic(err)
	}
	if err := staleRail(m); err != nil {
		panic(err)
	}
	if m.hasFrameRows {
		panic("a Model left Update carrying View's room list (frameRows)")
	}
	return m, cmd
}
