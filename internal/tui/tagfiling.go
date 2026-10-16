package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
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
// room, in [display] priority then rail order (unshown tags last). A [display]
// filing_spaces list keeps its spaces in written order before the tags; false when it
// names no space.
func (m Model) filingRows(room domain.Room) ([]pickerItem, map[string]bool, bool) {
	spaces, checked := m.fileableSpaces(room.ID)
	tags := m.tagRows(checked, room)
	if len(m.prefs.display.FilingSpaces) > 0 {
		if len(spaces) == 0 {
			return nil, nil, false
		}
		return append(spaces, m.byPriority(tags)...), checked, true
	}
	rows := slices.Concat(spaces, tags)
	position := func(item pickerItem) int {
		key := m.filingKey(item)
		if at := slices.IndexFunc(m.rail.groups, func(g group) bool { return g.key == key }); at >= 0 {
			return at
		}
		return len(m.rail.groups)
	}
	slices.SortStableFunc(rows, func(a, b pickerItem) int { return position(a) - position(b) })
	return m.byPriority(rows), checked, true
}

// filingKey names a filing row as [display] priority does: a space by name, a tag as tag:<name>.
func (m Model) filingKey(item pickerItem) string {
	if isTagGroup(item.value) {
		return item.value
	}
	if space, ok := m.spaceByID(domain.SpaceID(item.value)); ok {
		return space.DisplayName()
	}
	return item.value
}

// byPriority puts rows named in [display] priority first, keeping the rest in order.
func (m Model) byPriority(rows []pickerItem) []pickerItem {
	keys := make([]string, len(rows))
	for i := range rows {
		keys[i] = m.filingKey(rows[i])
	}
	out := make([]pickerItem, 0, len(rows))
	used := make([]bool, len(rows))
	for _, key := range domain.NewHomeOrder(m.prefs.display.Priority, nil, domain.TagSet{}, nil, m.prefs.display.SpaceLabels()).ByPriority(keys) {
		for i := range rows {
			if !used[i] && keys[i] == key {
				used[i] = true
				out = append(out, rows[i])
				break
			}
		}
	}
	return out
}

// tagRows is a row per tag filed by hand (not rule-filled, see TagSet.Automatic): the
// rail's tags in its order and names, then unshown ones, ticked where the tag holds the room.
func (m Model) tagRows(checked map[string]bool, room domain.Room) []pickerItem {
	view := m.unreadView()
	var items []pickerItem
	if view.tags.Len() == 0 {
		return items
	}
	held := view.tagsOf(room).in
	listed := make(map[int]bool, view.tags.Len())
	add := func(i int, label string) {
		listed[i] = true
		if view.tags.Automatic(i) {
			return // filled by its rule (Unread, DMs, All…): not a place to file a room
		}
		key := tagGroupKey(view.tags.At(i).Name)
		if held[i] {
			checked[key] = true
		}
		items = append(items, pickerItem{label: isolate(label), detail: "tag", value: key, match: label + " " + key})
	}
	for _, g := range m.rail.groups {
		if name, ok := domain.TagOf(g.key); ok {
			if i, ok := view.tags.Index(name); ok && !listed[i] {
				add(i, g.label)
			}
		}
	}
	for i := range view.tags.Len() {
		if !listed[i] {
			add(i, view.tags.At(i).Name)
		}
	}
	return items
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
	held := view.tagsOf(room).in
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
	return m.fileTags(room, []tagFiling{{tag: i, label: label, in: !view.tagsOf(room).in[i]}})
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
	cfg := m.conf.base.Clone()
	var into, outOf, leaving []string
	for _, f := range filings {
		tag := view.tags.At(f.tag)
		for j := range cfg.Tags {
			if strings.EqualFold(strings.TrimSpace(cfg.Tags[j].Name), tag.Name) {
				cfg.Tags[j].Picked, cfg.Tags[j].Excluded = view.tags.Filed(f.tag, facts, view.tagState(f.tag, room), f.in, others)
			}
		}
		if f.in {
			into = append(into, f.label)
		} else {
			outOf = append(outOf, f.label)
			leaving = append(leaving, tag.Name)
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
	view := m.unreadView()
	for _, f := range filings {
		if !strings.EqualFold(view.tags.At(f.tag).Name, mirrored) {
			continue
		}
		if strings.EqualFold(m.rail.archives[network], mirrored) && room.Archived != f.in {
			room.Archived = f.in
			m = m.withArchived(room.ID, f.in)
		}
		return m, room, m.setArchivedCmd(room.ID, f.in, m.roomName(room))
	}
	return m, room, nil
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
	return m.refreshPlaces()
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
