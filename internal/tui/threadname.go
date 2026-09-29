package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Model-generated thread names. Off unless `[assist] name_threads`; the answer is
// written to `[[display.name]]` under `thread:<root>`, the same list a typed name
// goes to, so a typed name wins.

// threadNamedMsg is the answer for one thread.
type threadNamedMsg struct {
	room domain.RoomID
	root domain.EventID
	name string
}

// nameOpenRoomThreads names the threads in the room on screen, from the timeline's
// own collapse (independent of any room-list display setting).
func (m Model) nameOpenRoomThreads() (Model, tea.Cmd) {
	if m.openRoom == "" {
		return m, nil
	}
	_, threads := domain.CollapseThreads(m.timeline.messages)
	return m.nameThreads(m.openRoom, threads)
}

// nameThreads asks the model to name every thread in a room that has no name yet, and
// notes each as asked, so it is asked once per session.
func (m Model) nameThreads(roomID domain.RoomID, threads []domain.Thread) (Model, tea.Cmd) {
	if !m.conf.base.Assist.NameThreads || !m.conf.base.Assist.AssistEnabled() {
		return m, nil
	}
	after := m.conf.base.Assist.NameThreadsAfterOrDefault()
	var cmds []tea.Cmd
	for i := range threads {
		root := threads[i].Root
		// Skip unnamed-able, already named (the alias is the memo across restarts),
		// already asked this session, and too short to be worth a name.
		if root == "" || m.prefs.threadAliases[root] != "" || m.namedThreads[root] || threads[i].Count < after {
			continue
		}
		m.namedThreads = withEntry(m.namedThreads, root, true)
		cmds = append(cmds, m.nameThreadCmd(roomID, root))
	}
	return m, tea.Batch(cmds...)
}

// nameThreadCmd asks about one thread. Failures and refusals are silent: nobody
// asked, and the thread keeps its snippet.
func (m Model) nameThreadCmd(roomID domain.RoomID, root domain.EventID) tea.Cmd {
	log := m.log
	backend, ctx := m.backend, m.ctx
	return func() tea.Msg {
		result, err := backend.ModelTask(ctx, domain.ModelRequest{
			Task:    domain.ModelThreadName,
			RoomID:  roomID,
			ReplyTo: root, // the thread root, as for every other task
		})
		if err != nil {
			log.Debug("name thread failed", "room", roomID, "root", root, "err", err)
			return nil
		}
		if result.Refusal != "" {
			return nil
		}
		name := domain.ThreadNameFrom(result.Text)
		if name == "" {
			return nil
		}
		return threadNamedMsg{room: roomID, root: root, name: name}
	}
}

// handleThreadNamed writes one generated name into config.toml (so it is stable
// across restarts), unless it was named by hand meanwhile. No status line: this is
// background work.
func (m Model) handleThreadNamed(msg threadNamedMsg) (Model, tea.Cmd) {
	if msg.name == "" || msg.root == "" || m.prefs.threadAliases[msg.root] != "" {
		return m, nil
	}
	display := m.prefs.display
	display.Names = config.SetName(display.Names, config.NameTargetThread+string(msg.root), msg.name)
	return m.applyDisplay(display, "")
}
