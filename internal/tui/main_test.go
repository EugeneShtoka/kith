package tui

import (
	tea "charm.land/bubbletea/v2"
)

// asModel converts Update's result into the concrete Model, serves it the rail kithd would
// (serveRail), and checks the row-cache and rail invariants (see staleRow, staleRail) after
// each call, panicking since it has no *testing.T.
func asModel(mdl tea.Model, cmd tea.Cmd) (Model, tea.Cmd) {
	m := serveRail(mdl.(Model))
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
