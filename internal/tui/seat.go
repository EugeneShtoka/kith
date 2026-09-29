package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
)

// The daemon serves one window at a time (api.Seat): kith takes the seat before the
// program starts, and a window started with --force takes it from this one. This one
// then saves its drafts and quits, the way quit does.

// ErrOpenedElsewhere is Run ending because another window took the seat.
var ErrOpenedElsewhere = errors.New("kith was opened in another window")

// seatLostMsg is another window taking the seat.
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

// handleSeatLost saves the drafts and quits.
func (m Model) handleSeatLost() (Model, tea.Cmd) {
	m.link.seatLost = true
	return m.say("kith was opened in another window — saving drafts and closing").quitAfterDrafts(nil)
}
