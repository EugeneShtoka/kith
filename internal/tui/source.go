package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// sourceLoadedMsg carries a raw event's JSON from the homeserver, in response to
// the gs key on a message cursor.
type sourceLoadedMsg struct {
	roomID  domain.RoomID
	eventID domain.EventID
	source  []byte
	err     error
}

// openEventSource is `gs`: fetch the raw JSON for the message under the cursor
// and show it in the pager.
func (m Model) openEventSource() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m.say("no message is selected"), nil
	}
	if !domain.IsMatrixRoomID(string(msg.RoomID)) {
		return m.say("raw source is Matrix only"), nil
	}
	roomID, eventID := msg.RoomID, msg.ID
	backend, ctx := m.backend, m.ctx
	return m, func() tea.Msg {
		src, err := backend.FetchEventSource(ctx, roomID, eventID)
		return sourceLoadedMsg{roomID: roomID, eventID: eventID, source: src, err: err}
	}
}

// handleSourceLoaded fills the pager with the fetched JSON, or says why it did
// not arrive.
func (m Model) handleSourceLoaded(msg sourceLoadedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("read the event source", msg.err), nil
	}
	m.pager = pagerState{
		title: string(msg.eventID),
		lines: strings.Split(string(msg.source), "\n"),
	}
	m.reader = m.reader.opening(readerSource)
	return m.clearStatus(), nil
}
