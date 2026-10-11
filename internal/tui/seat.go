package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
)

// The daemon serves one window at a time (api.Seat). kith takes the seat before the
// program starts; a window started with --force takes it, and this one saves its
// drafts and quits.

// ErrOpenedElsewhere ends Run when another window took the seat.
var ErrOpenedElsewhere = errors.New("kith was opened in another window")

type seatLostMsg struct{}

// listenSeatCmd waits for the seat to be taken; nil for a backend with no seat.
func (m Model) listenSeatCmd() tea.Cmd {
	seat, ok := m.backend.(api.Seat)
	if !ok {
		return nil
	}
	ctx, lost := m.ctx, seat.SeatLost()
	return func() tea.Msg {
		select {
		case <-lost:
			return seatLostMsg{}
		case <-ctx.Done():
			return nil
		}
	}
}

func (m Model) handleSeatLost() (Model, tea.Cmd) {
	m.link.seatLost = true
	return m.say("kith was opened in another window — saving drafts and closing").quitAfterDrafts(nil)
}
