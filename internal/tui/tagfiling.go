package tui

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Filing a room into or out of a tag, from the filing picker (spaces.go) or /tag. A tag
// holds what its rule says plus its picked rooms, less its excluded ones, so filing
// rewrites those lists in the config (domain.TagSet.Filed).

type tagFiling struct {
	tag   int // index in the tag set
	label string
	in    bool
}

// filingRows is the filing picker's rows, spaces and tags, ticked where they hold the
// room, in the order every client offers them (domain.FilingPlaces); false when
// [display] filing_spaces names no space.
func (m Model) filingRows(room domain.Room) ([]pickerItem, map[string]bool, bool) {
	view := m.unreadView()
	rows := make([]domain.RailRow, len(m.rail.groups))
	for i, g := range m.rail.groups {
		rows[i] = domain.RailRow{Key: g.key, Label: g.label}
	}
	var held []bool
	if view.tags.Len() > 0 {
		held = view.tagsOf(room).In
	}
	places, ok := domain.FilingPlaces(room.ID, domain.FilingRail{
		Rows: rows, Spaces: m.rooms.spaces, Tags: view.tags, Held: held, Homes: m.rail.homes,
		Priority: m.prefs.display.Priority, SpaceLabels: m.prefs.display.SpaceLabels(), Named: m.prefs.device.FilingSpaces,
	})
	if !ok {
		return nil, nil, false
	}
	items := make([]pickerItem, 0, len(places))
	checked := make(map[string]bool, len(places))
	for _, p := range places {
		item := pickerItem{label: isolate(p.Label), value: tagGroupKey(p.Tag), detail: "tag"}
		item.match = p.Label + " " + item.value
		if p.Space != "" {
			name := string(p.Space)
			if space, ok := m.spaceByID(p.Space); ok {
				name = space.DisplayName()
			}
			item = pickerItem{label: isolate(p.Label), value: string(p.Space), match: p.Label + " " + name + " " + string(p.Space)}
		}
		if p.In {
			checked[item.value] = true
		}
		items = append(items, item)
	}
	return items, checked, true
}

// fileInTags applies the tag rows whose tick differs from the current state, reporting
// whether anything changed.
func (m Model) fileInTags(roomID domain.RoomID, rows []pickerItem, want map[domain.SpaceID]bool) (Model, bool, tea.Cmd) {
	room, ok := m.roomByID(roomID)
	if !ok {
		return m, false, nil
	}
	view := m.unreadView()
	if view.tags.Len() == 0 {
		return m, false, nil
	}
	held := view.tagsOf(room).In
	var filings []tagFiling
	for _, item := range rows {
		name, ok := domain.TagOf(item.value)
		if !ok {
			continue
		}
		i, ok := view.tags.Index(name)
		if ticked := want[domain.SpaceID(item.value)]; ok && held[i] != ticked {
			filings = append(filings, tagFiling{tag: i, label: stripIsolates(item.label), in: ticked})
		}
	}
	if len(filings) == 0 {
		return m, false, nil
	}
	next, cmd := m.fileTags(room, filings)
	return next, true, cmd
}

// toggleTag is /tag: the named tag takes the room or lets it go; an unknown name makes
// that tag with the room in it. With no name, the filing picker opens.
func (m Model) toggleTag(name string, room domain.Room) (Model, tea.Cmd) {
	m.compose.input, m.compose.drafted = "", nil
	if strings.TrimSpace(name) == "" {
		return m.openSpacePicker()
	}
	if room.ID == "" || room.IsInvite() {
		return m.say("open a room to tag it"), nil
	}
	view := m.unreadView()
	i, ok := view.tags.Index(name)
	if !ok {
		return m.fileInNewTag(strings.TrimSpace(name), room)
	}
	label := view.tags.At(i).Name
	return m.fileTags(room, []tagFiling{{tag: i, label: label, in: !view.tagsOf(room).In[i]}})
}

// fileTags writes the filings into the config's tags and applies it, keeping the
// cursor near the room.
//
// Filing into or out of a network's mirrored archive tag also archives or unarchives
// on the network. Where kith follows that archive, the network decides: the lists only
// lose what contradicts it (fileArchive).
func (m Model) fileTags(room domain.Room, filings []tagFiling) (Model, tea.Cmd) {
	m, room, archive := m.fileArchive(room, filings)
	view := m.unreadView()
	facts := view.factsOf(room)
	others := make([]domain.RoomFacts, 0, len(m.rooms.all))
	for i := range m.rooms.all {
		others = append(others, view.factsOf(m.rooms.all[i]))
	}
	cfg := setup.FileTags(m.conf.base, view.tags, facts, func(tag int) domain.RoomState { return view.tagState(tag, room) },
		plainFilings(filings), others)
	var into, outOf, leaving []string
	for _, f := range filings {
		if f.in {
			into = append(into, f.label)
		} else {
			outOf = append(outOf, f.label)
			leaving = append(leaving, view.tags.At(f.tag).Name)
		}
	}
	var said []string
	if len(into) > 0 {
		said = append(said, "in "+strings.Join(into, ", "))
	}
	if len(outOf) > 0 {
		said = append(said, "out of "+strings.Join(outOf, ", "))
	}
	was := m.roomCursor()
	release := m.releaseCmds(room.ID, leaving)
	next, cmd := m.applyConfig(cfg, m.roomName(room)+" is "+strings.Join(said, ", and "))
	moved, move := next.keepCursorNearby(was)
	return moved, tea.Batch(cmd, move, archive, release)
}

// fileArchive returns the call that archives or unarchives room on its network when
// the filings cross its mirrored archive tag and, where the archive is followed, the
// room as the network will hold it (the room list corrects it if the call fails).
func (m Model) fileArchive(room domain.Room, filings []tagFiling) (Model, domain.Room, tea.Cmd) {
	network := domain.NetworkOf(string(room.ID))
	mirrored := m.rail.mirrored[network]
	if mirrored == "" {
		return m, room, nil
	}
	if current, ok := m.roomByID(room.ID); ok {
		room = current // as the list holds it now: an earlier filing may have moved it
	}
	f, followed, ok := domain.ArchiveFiling(m.unreadView().tags, m.rail.mirrored, m.rail.archives, network, plainFilings(filings))
	if !ok {
		return m, room, nil
	}
	if followed && room.Archived != f.In {
		room.Archived = f.In
		m = m.withArchived(room.ID, f.In)
	}
	return m, room, m.setArchivedCmd(room.ID, f.In, m.roomName(room))
}

// plainFilings is filings without their labels.
func plainFilings(filings []tagFiling) []domain.TagFiling {
	out := make([]domain.TagFiling, len(filings))
	for i, f := range filings {
		out[i] = domain.TagFiling{Tag: f.tag, In: f.in}
	}
	return out
}

// withArchived marks the joined room id as archived by its network, or not.
func (m Model) withArchived(id domain.RoomID, archived bool) Model {
	joined := slices.Clone(m.rooms.joined)
	for i := range joined {
		if joined[i].ID == id {
			joined[i].Archived = archived
		}
	}
	m.rooms = m.rooms.withJoined(joined)
	m = m.refreshPlaces()
	// The rail served says what the network said before this change: until it says this,
	// the room's facts are worked out here.
	if room, ok := m.rooms.byID(id); ok {
		facts := maps.Clone(m.rail.roomFacts)
		facts[id] = m.factsFor(room)
		m.rail.roomFacts = facts
	}
	return m
}

type archivedMsg struct {
	label    string
	archived bool
	err      error
}

func (m Model) setArchivedCmd(roomID domain.RoomID, archived bool, label string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return archivedMsg{label: label, archived: archived, err: backend.SetRoomArchived(ctx, roomID, archived)}
	}
}

// handleArchived reports a failed archive call and reloads the room list so the room
// shows where its network holds it. Success needs nothing: the network's change brings
// the list.
func (m Model) handleArchived(msg archivedMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m, nil
	}
	verb := "unarchive "
	if msg.archived {
		verb = "archive "
	}
	return m.sayErr("could not "+verb+msg.label+" on its network", msg.err), m.loadRoomsCmd()
}
