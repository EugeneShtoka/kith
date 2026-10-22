package tui

import (
	tea "charm.land/bubbletea/v2"
)

// ignoredLoadedMsg carries the server-stored list of MXIDs this account ignores on
// Matrix, as the backend returns it (alphabetical, from the cache mirror).
type ignoredLoadedMsg struct {
	mxids []string
	err   error
}

// ignoredChangedMsg is the outcome of ignoreCmd / unignoreCmd. On success the
// chooser reopens with the new set so the row that moved does not get the cursor
// of the row that stayed.
type ignoredChangedMsg struct {
	err error
	// unignored is the MXID the picker's remove took out; the chooser reopens so
	// the row that moved does not get the cursor of the row that stayed.
	unignored string
}

// openIgnored is `/ignored`'s run: load the list and open the chooser over it.
func (m Model) openIgnored() (Model, tea.Cmd) {
	return m, m.openIgnoredCmd()
}

// openIgnoredCmd reads the ignored list and sends it to Update as ignoredLoadedMsg.
func (m Model) openIgnoredCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.Ignored, func(ids []string, err error) tea.Msg {
		return ignoredLoadedMsg{mxids: ids, err: err}
	})
}

// ignoreCmd adds mxid to the ignored list; a success emits ignoredChangedMsg so
// the open chooser can reopen with the new set.
func (m Model) ignoreCmd(mxid string) tea.Cmd {
	backend, ctx := m.backend, m.ctx
	return func() tea.Msg {
		return ignoredChangedMsg{err: backend.Ignore(ctx, mxid)}
	}
}

// unignoreCmd drops mxid from the ignored list.
func (m Model) unignoreCmd(mxid string) tea.Cmd {
	backend, ctx := m.backend, m.ctx
	return func() tea.Msg {
		return ignoredChangedMsg{err: backend.Unignore(ctx, mxid), unignored: mxid}
	}
}

// handleIgnoredLoaded opens the chooser over the ignored list, or says nothing is.
func (m Model) handleIgnoredLoaded(msg ignoredLoadedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("read the ignored list", msg.err), nil
	}
	if len(msg.mxids) == 0 {
		return m.say("no one is ignored on Matrix"), nil
	}
	items := make([]pickerItem, 0, len(msg.mxids))
	for _, id := range msg.mxids {
		items = append(items, pickerItem{label: id, value: id, match: id})
	}
	m.picker = newPicker(pickerIgnored, items)
	return m, nil
}

// handleIgnoredChanged responds to a server round-trip: a failure says so, a
// success reopens the chooser so the list reflects the change.
func (m Model) handleIgnoredChanged(msg ignoredChangedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("change the ignored list", msg.err), nil
	}
	// If the picker is open and the change came from inside it (remove), reopen
	// with the current server list to drop the row that just left.
	if m.picker.kind == pickerIgnored {
		return m, m.openIgnoredCmd()
	}
	if msg.unignored == "" {
		return m.say("ignored"), nil
	}
	return m.say("unignored " + msg.unignored), nil
}

// removeIgnoredRow is x on an /ignored row: unignores that MXID. The chooser
// stays open; handleIgnoredChanged reopens it with the new set when the server
// agrees.
func (m Model) removeIgnoredRow(item pickerItem) (Model, tea.Cmd, bool) {
	return m, m.unignoreCmd(item.value), true
}
