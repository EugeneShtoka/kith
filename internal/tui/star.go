package tui

import (
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Starring: a private bookmark on a message, kept in room account data (see
// internal/matrix/starred.go) and mirrored by the cache.

// toggleStar stars the message under the cursor, or unstars it. The mark is set
// optimistically; handleStarred undoes it on failure.
func (m Model) toggleStar() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	on := !m.timeline.starred[msg.ID]
	m.timeline.starred = withEntry(m.timeline.starred, msg.ID, on)
	if on {
		m = m.say("starred")
	} else {
		m = m.say("unstarred")
	}
	return m, m.starCmd(msg.RoomID, msg.ID, on)
}

// unstarSelectedHit clears the star on the hit under the cursor in the starred
// list and re-runs the search; the cursor keeps its index.
func (m Model) unstarSelectedHit() (Model, tea.Cmd) {
	hit, ok := m.search.selected()
	if !ok {
		return m, nil
	}
	if m.timeline.starred != nil {
		m.timeline.starred = withEntry(m.timeline.starred, hit.EventID, false)
	}
	m = m.say("unstarred")
	return m, tea.Batch(m.starCmd(hit.RoomID, hit.EventID, false), m.rerunSearchCmd())
}

// isStarred reports whether a message carries a star.
func (m Model) isStarred(id domain.EventID) bool { return m.timeline.starred[id] }

// handleStarred reports a bookmark the server refused and puts the row back.
func (m Model) handleStarred(msg starredMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m, nil
	}
	if m.timeline.starred != nil {
		m.timeline.starred = withEntry(m.timeline.starred, msg.eventID, !msg.on)
	}
	what := "star"
	if !msg.on {
		what = "unstar"
	}
	return m.sayErr("could not "+what, msg.err), nil
}

// starredInMsg carries a room's bookmark set back from the daemon.
type starredInMsg struct {
	roomID domain.RoomID
	ids    []domain.EventID
	err    error
}

// starredInCmd asks which of a room's messages are starred.
func (m Model) starredInCmd(roomID domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		ids, err := backend.StarredIn(ctx, roomID)
		return starredInMsg{roomID: roomID, ids: ids, err: err}
	}
}

// handleStarredIn adopts the set unless it is for a room no longer open. Failures
// are silent: the marks are decoration.
func (m Model) handleStarredIn(msg starredInMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelDebug, "load starred messages", msg.err, "room", msg.roomID)
	if msg.err != nil || msg.roomID != m.openRoom {
		return m, nil
	}
	set := make(map[domain.EventID]bool, len(msg.ids))
	for _, id := range msg.ids {
		set[id] = true
	}
	m.timeline.starred = set
	return m, nil
}

// handleStarMsg dispatches the starring messages and the scoped thread list's.
func (m Model) handleStarMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case starredMsg:
		mdl, cmd := m.handleStarred(msg)
		return mdl, cmd, true
	case starredInMsg:
		mdl, cmd := m.handleStarredIn(msg)
		return mdl, cmd, true
	case scopeThreadsMsg:
		mdl, cmd := m.handleScopeThreads(msg)
		return mdl, cmd, true
	case rerunSearchMsg:
		mdl, cmd := m.runSearch(m.search.query)
		return mdl, cmd, true
	}
	return m, nil, false
}
