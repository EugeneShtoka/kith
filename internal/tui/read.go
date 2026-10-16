package tui

import (
	"fmt"
	"log/slog"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Marking read without reading. The daemon resolves the receipt's target event
// (api.MarkRoomsRead), since an unread room's timeline is usually not loaded.

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

// askMarkGroupRead captures the rooms with the question so a membership change cannot
// redirect it.
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
	// Isolated once: the prompt, status and outcome all use it.
	m.confirm = confirmState{action: pendingMarkGroupRead, group: isolate(g.label), rooms: rooms}
	return m, nil
}

// unreadView is the single answer to "how much is unread here"; every badge, sum, filter and
// gesture uses it so they cannot disagree. Built per call (see group.admits), so never stale.
type unreadView struct {
	counts map[domain.RoomID]domain.Unread
	local  bool
	// parents maps a room to its canonical parent space, as resolved by the daemon.
	parents map[domain.RoomID]domain.SpaceID
	// facts is the slow way to a room's facts (roomFacts holds them precomputed).
	facts func(domain.Room) domain.RoomFacts
	// tags and the facts they judge by (precomputed), and the drafts the `draft` state word reads;
	// tagMemo caches the per-room result (tagged.go), keyed by tagsRev.
	tags      domain.TagSet
	tagsRev   uint64
	tagMemo   *tagMemo
	roomFacts map[domain.RoomID]domain.RoomFacts
	drafts    map[domain.RoomID]draft
}

// count is one room's own numbers, archived or not.
func (v unreadView) count(room domain.Room) (count, highlights int) {
	return v.counts[room.ID].Count(v.local)
}

// tallies reports whether a room counts toward group badges. Invitations and rooms a
// silent tag holds do not.
func (v unreadView) tallies(room domain.Room) bool {
	if room.IsInvite() || v.silenced(room) {
		return false
	}
	// HasUnread, not a count: a marked room is unread with nothing to count.
	return v.counts[room.ID].HasUnread(v.local)
}

// marked reports a hand-set unread flag; a silent tag wins over it.
func (v unreadView) marked(room domain.Room) bool {
	return v.counts[room.ID].Marked && !v.silenced(room)
}

// refreshPlaces recomputes the rooms' facts.
func (m Model) refreshPlaces() Model { return m.refreshFacts() }

// refreshFacts recomputes every room's facts, as a new map (see refreshArchived).
func (m Model) refreshFacts() Model {
	m.rail.homes = domain.NewHomeOrder(m.prefs.display.Priority, m.prefs.display.Rail.Order, m.rail.tags, m.rooms.spaces, m.prefs.display.SpaceLabels())
	facts := make(map[domain.RoomID]domain.RoomFacts, len(m.rooms.all))
	held := map[string]int{}
	for i := range m.rooms.all {
		f := m.factsFor(m.rooms.all[i])
		facts[m.rooms.all[i].ID] = f
		for _, tag := range f.Tags {
			held[strings.ToLower(domain.TagEntry(tag))]++
		}
	}
	spanning := map[string]bool{}
	for home, n := range held {
		if n == len(m.rooms.all) {
			spanning[home] = true
		}
	}
	m.rail.roomFacts, m.rail.spanning = facts, spanning
	return m
}

func (m Model) unreadView() unreadView {
	return unreadView{
		counts:    m.unread,
		local:     m.prefs.unreadLocal,
		parents:   m.parents,
		facts:     m.factsFor,
		tags:      m.rail.tags,
		tagsRev:   m.rail.tagsRev,
		tagMemo:   m.rail.tagMemo,
		roomFacts: m.rail.roomFacts,
		drafts:    m.drafts,
	}
}

// unreadIn lists a rail group's unread rooms for marking read, including those a silent tag
// holds (Archived): they leave the group total but still show unread and are read with it.
func (m Model) unreadIn(g group) []domain.RoomID {
	view := m.unreadView()
	var ids []domain.RoomID
	for i := range m.rooms.all {
		room := m.rooms.all[i]
		if !g.admits(view, room) || room.IsInvite() || !view.counts[room.ID].HasUnread(view.local) {
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

// A room that leaves its spaces but not its origin, or the other way round, needs its
// canonical parent space (m.space.parent with canonical: true), resolved lazily for
// those rooms only.

// parentsMsg carries canonical parents resolved for the rooms that needed them.
type parentsMsg struct {
	parents map[domain.RoomID]domain.SpaceID
}

// resolveParentsCmd asks the daemon for canonical parents of not-yet-known rooms whose
// spaces differ by origin; nil when there is nothing to ask.
func (m Model) resolveParentsCmd() tea.Cmd {
	view := m.unreadView()
	var want []domain.RoomID
	for i := range m.rooms.all {
		room := m.rooms.all[i]
		if leaves := view.tagsOf(room).leaves; leaves.Spaces == leaves.Origin {
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

// handleParents records the answers; rail groups read them through their view.
func (m Model) handleParents(msg parentsMsg) (Model, tea.Cmd) {
	parents := make(map[domain.RoomID]domain.SpaceID, len(m.parents)+len(msg.parents))
	maps.Copy(parents, m.parents)
	maps.Copy(parents, msg.parents)
	m.parents = parents
	return m, nil
}
