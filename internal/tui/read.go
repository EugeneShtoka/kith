package tui

import (
	"fmt"
	"log/slog"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Marking read without reading. The receipt's target event is resolved by the daemon
// (api.MarkRoomsRead), since an unread room's timeline is usually not loaded here.

// markRoomRead marks the selected room read without opening it, unconditionally.
func (m Model) markRoomRead() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	name := m.roomName(room)
	m = m.say("marking " + name + " read…")
	return m, m.markRoomsReadCmd([]domain.RoomID{room.ID}, name)
}

// toggleRoomUnread sets or clears MSC2867's m.marked_unread on the selected room.
// The badge updates when the flag comes back on the Unread stream.
func (m Model) toggleRoomUnread() (Model, tea.Cmd) {
	if row, ok := m.selectedRow(); ok && row.isThread() {
		// m.marked_unread is room account data; there is no per-thread form.
		m = m.say("a conversation cannot be marked unread — the room can")
		return m, nil
	}
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	want := !m.unread[room.ID].Marked
	return m, m.markRoomUnreadCmd(room.ID, want, m.roomName(room))
}

// handleMarkedUnread reports the outcome; the badge follows from the server.
func (m Model) handleMarkedUnread(msg markedUnreadMsg) (Model, tea.Cmd) {
	switch {
	case msg.err != nil:
		m = m.sayErr("could not mark "+msg.label, msg.err)
	case msg.unread:
		m = m.say("marked " + msg.label + " unread")
	default:
		m = m.say(msg.label + " is no longer marked unread")
	}
	return m, nil
}

// askMarkGroupRead confirms marking a rail group read, capturing the rooms with the
// question so a membership change cannot redirect it.
func (m Model) askMarkGroupRead() (Model, tea.Cmd) {
	g, ok := m.rail.at()
	if !ok {
		return m, nil
	}
	rooms := m.unreadIn(g)
	if len(rooms) == 0 {
		m = m.say("nothing unread in " + isolate(g.label))
		return m, nil
	}
	// Isolated once here: the prompt, the status and the outcome all say it.
	m.confirm = confirmState{action: pendingMarkGroupRead, group: isolate(g.label), rooms: rooms}
	return m, nil
}

// unreadView is the single answer to "how much is unread here"; every badge, sum,
// filter and gesture goes through it so they cannot disagree. It is built from the
// Model each time it is asked for (see group.admits), so it is never stale.
type unreadView struct {
	counts   map[domain.RoomID]domain.Unread
	local    bool
	archived domain.Archive
	// pinned rooms get their own rail group as well as their spaces.
	pinned domain.Pinned
	// base names spaces that keep showing their archived rooms ([display] base_spaces).
	base []string
	// parents maps a room to its canonical parent space, as resolved by the daemon.
	parents map[domain.RoomID]domain.SpaceID
	// facts builds archivedRooms; it does not answer isArchived (too slow per call).
	facts func(domain.Room) domain.RoomFacts
	// spamRooms is the union of the hand-written lists and what the filters caught.
	spam      domain.Spam
	caught    map[domain.RoomID]domain.SpamVerdict
	spamRooms map[domain.RoomID]bool
	// archivedRooms is the archive precomputed: asked thousands of times per frame,
	// resolving display names per call made the room list visibly slow.
	archivedRooms map[domain.RoomID]bool
}

// keepsArchived reports whether space keeps showing an archived room: the room names
// it as canonical parent; or, naming no parent, the space is a bridge's (mautrix sets
// no m.space.parent on portals); or it is listed in base_spaces. A hand-made space
// you filed the room into stops showing it.
func (v unreadView) keepsArchived(room domain.Room, space domain.SpaceID, name string, bridge domain.Protocol) bool {
	if parent, known := v.parents[room.ID]; known && parent != "" {
		return parent == space
	}
	if bridge != "" && bridge != domain.ProtocolMatrix {
		return true
	}
	for _, entry := range v.base {
		if strings.EqualFold(strings.TrimSpace(entry), name) {
			return true
		}
	}
	return false
}

// isArchived reports whether this room's unread has been told to stop counting.
func (v unreadView) isArchived(room domain.Room) bool {
	if !v.archived.Has() {
		return false
	}
	return v.archivedRooms[room.ID]
}

// resolveArchived is the slow answer, computed once per room when the inputs change.
func (v unreadView) resolveArchived(room domain.Room) bool {
	if !v.archived.Has() || v.facts == nil {
		return false
	}
	return v.archived.Archived(v.facts(room))
}

// count is one room's own numbers, archived or not.
func (v unreadView) count(room domain.Room) (count, highlights int) {
	return v.counts[room.ID].Count(v.local)
}

// tallies reports whether a room counts toward group badges, the Unread group and
// group mark-read. Invitations, archived and spam rooms do not.
func (v unreadView) tallies(room domain.Room) bool {
	if room.IsInvite() || v.isArchived(room) || v.isSpam(room) {
		return false
	}
	// HasUnread, not a count: a marked room is unread with nothing to count.
	return v.counts[room.ID].HasUnread(v.local)
}

// marked reports a hand-set unread flag; archiving wins over it.
func (v unreadView) marked(room domain.Room) bool {
	return v.counts[room.ID].Marked && !v.isArchived(room)
}

// refreshPlaces recomputes the archived and spam sets, which share their inputs.
func (m Model) refreshPlaces() Model { return m.refreshArchived().refreshSpam() }

// refreshArchived recomputes which rooms the archive covers, as a new set: the rail's
// groups read it through the view they are handed (group.admits), so none holds an
// old one.
func (m Model) refreshArchived() Model {
	archived := map[domain.RoomID]bool{}
	if m.rail.archive.Has() {
		view := m.unreadView()
		for i := range m.rooms.all {
			if view.resolveArchived(m.rooms.all[i]) {
				archived[m.rooms.all[i].ID] = true
			}
		}
	}
	m.rail.archivedRooms = archived
	return m
}

// unreadView builds the view from the running configuration.
func (m Model) unreadView() unreadView {
	return unreadView{
		counts:        m.unread,
		local:         m.prefs.unreadLocal,
		archived:      m.rail.archive,
		pinned:        m.rail.pinned,
		base:          m.rail.baseSpaces,
		parents:       m.parents,
		facts:         m.factsFor,
		archivedRooms: m.rail.archivedRooms,
		spam:          m.rail.spam,
		caught:        m.rail.caught,
		spamRooms:     m.rail.spamRooms,
	}
}

// unreadIn lists the rooms of a rail group that have something unread.
func (m Model) unreadIn(g group) []domain.RoomID {
	view := m.unreadView()
	var ids []domain.RoomID
	for i := range m.rooms.all {
		room := m.rooms.all[i]
		if !g.admits(view, room) || !view.tallies(room) {
			continue
		}
		ids = append(ids, room.ID)
	}
	return ids
}

// groupUnread sums a rail group's unread over the same rooms as unreadIn.
func (m Model) groupUnread(g group) (notifications, highlights int) {
	view := m.unreadView()
	for i := range m.rooms.all {
		room := m.rooms.all[i]
		if !g.admits(view, room) || !view.tallies(room) {
			continue
		}
		n, h := view.count(room)
		notifications += n
		highlights += h
	}
	return notifications, highlights
}

// handleMarkedRead reports the outcome; the badges clear via the Unread stream.
func (m Model) handleMarkedRead(msg markedReadMsg) (Model, tea.Cmd) {
	switch {
	case msg.err != nil:
		m.logErr(slog.LevelWarn, "mark read failed", msg.err, "label", msg.label, "rooms", msg.rooms)
	case msg.result.Failed > 0:
		m.log.Warn("mark read refused", "label", msg.label, "rooms", msg.rooms,
			"failed", msg.result.Failed, "err", msg.result.FirstError)
	}
	m = m.say(readStatus(msg))
	return m, nil
}

// readStatus names one room or counts a group, reporting skipped and refused rooms.
func readStatus(msg markedReadMsg) string {
	if msg.err != nil {
		return "could not mark read: " + msg.err.Error()
	}
	if msg.rooms == 1 {
		switch {
		case msg.result.Marked == 1:
			return "marked " + msg.label + " read"
		case msg.result.Skipped == 1:
			return "nothing of " + msg.label + " is cached yet — open it to mark it read"
		case msg.result.FirstError != "":
			return "could not mark " + msg.label + " read: " + msg.result.FirstError
		default:
			return "could not mark " + msg.label + " read"
		}
	}
	parts := []string{fmt.Sprintf("marked %s in %s read", roomsPhrase(msg.result.Marked), msg.label)}
	if msg.result.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%s had nothing cached", roomsPhrase(msg.result.Skipped)))
	}
	if msg.result.Failed > 0 {
		refused := fmt.Sprintf("%s refused", roomsPhrase(msg.result.Failed))
		if msg.result.FirstError != "" {
			refused += ": " + msg.result.FirstError
		}
		parts = append(parts, refused)
	}
	return strings.Join(parts, " · ")
}

// roomsPhrase pluralizes a room count for the status line and the confirmation.
func roomsPhrase(n int) string {
	if n == 1 {
		return "1 room"
	}
	return fmt.Sprintf("%d rooms", n)
}

// toggleArchive archives or un-archives the selected room by ID and writes the config.
// A room archived by an entry naming its space cannot be undone here.
func (m Model) toggleArchive() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	name := m.roomName(room)
	id := string(room.ID)
	if m.unreadView().isArchived(room) && !m.rail.archive.Lists(id) {
		m = m.say(name + " is archived by an entry naming its space — edit [display] archived")
		return m, nil
	}
	archiving := !m.rail.archive.Lists(id)
	display := m.prefs.display
	display.Archived = m.rail.archive.With(id, archiving).Entries
	done := "un-archived " + name + " — it counts again"
	if archiving {
		done = "archived " + name + " — still readable, no longer counted"
	}
	return m.applyDisplay(display, done)
}

// An archived room stays visible in its canonical parent space (m.space.parent with
// canonical: true), resolved lazily and only for archived rooms.

// parentsMsg carries canonical parents resolved for the rooms that needed them.
type parentsMsg struct {
	parents map[domain.RoomID]domain.SpaceID
}

// resolveParentsCmd asks the daemon where not-yet-known archived rooms live; nil
// when there is nothing to ask.
func (m Model) resolveParentsCmd() tea.Cmd {
	view := m.unreadView()
	var want []domain.RoomID
	for i := range m.rooms.all {
		room := m.rooms.all[i]
		if !view.isArchived(room) {
			continue
		}
		if _, known := m.parents[room.ID]; !known {
			want = append(want, room.ID)
		}
	}
	if len(want) == 0 {
		return nil
	}
	backend, ctx := m.backend, m.ctx
	return func() tea.Msg {
		out := make(map[domain.RoomID]domain.SpaceID, len(want))
		for _, roomID := range want {
			parent, err := backend.CanonicalParent(ctx, roomID)
			if err != nil {
				// Left unrecorded so the next attempt retries.
				continue
			}
			out[roomID] = parent
		}
		return parentsMsg{parents: out}
	}
}

// handleParents records the answers; the rail's groups read them through the view
// they are handed.
func (m Model) handleParents(msg parentsMsg) (Model, tea.Cmd) {
	parents := make(map[domain.RoomID]domain.SpaceID, len(m.parents)+len(msg.parents))
	maps.Copy(parents, m.parents)
	maps.Copy(parents, msg.parents)
	m.parents = parents
	return m, nil
}

// togglePin follows or unfollows the selected room by ID and writes the config.
// A room pinned by an entry naming its space cannot be undone here.
func (m Model) togglePin() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	name, id := m.roomName(room), string(room.ID)
	if m.rail.pinned.Pins(m.factsFor(room)) && !m.rail.pinned.Lists(id) {
		m = m.say(name + " is pinned by an entry naming its space — edit [display] pinned")
		return m, nil
	}
	pinning := !m.rail.pinned.Lists(id)
	display := m.prefs.display
	display.Pinned = m.rail.pinned.With(id, pinning).Entries
	done := "unpinned " + name
	if pinning {
		done = "pinned " + name + " — it is in Pinned as well as its own space"
	}
	return m.applyDisplay(display, done)
}
