package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Filing a room into a tag and back out, from the filing picker (spaces.go) or /tag.
// A tag holds what its rule says plus its picked rooms, less its excluded ones, so
// putting a room in or out rewrites those two lists (domain.TagSet.Filed) in the
// config, and the rail follows the applied config.

// tagFiling is one tag a room goes into (in) or out of.
type tagFiling struct {
	tag   int // index in the tag set
	label string
	in    bool
}

// filingRows is the filing picker's rows, spaces and tags, ticked where they hold the
// room: in [display] priority, then as the rail orders them (tags it does not show
// last). A [display] filing_spaces list keeps its spaces in its written order, before
// the tags; false when it names no space at all.
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

// filingKey is a filing row as [display] priority and the rail name it: a space by
// its name, a tag as tag:<name>.
func (m Model) filingKey(item pickerItem) string {
	if isTagGroup(item.value) {
		return item.value
	}
	if space, ok := m.spaceByID(domain.SpaceID(item.value)); ok {
		return space.DisplayName()
	}
	return item.value
}

// byPriority reorders rows by [display] priority, keeping the order of the rest.
func (m Model) byPriority(rows []pickerItem) []pickerItem {
	keys := make([]string, len(rows))
	for i := range rows {
		keys[i] = m.filingKey(rows[i])
	}
	out := make([]pickerItem, 0, len(rows))
	used := make([]bool, len(rows))
	for _, key := range domain.OrderSpaces(keys, m.prefs.display.Priority) {
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

// tagRows is a filing row per tag: the rail's tags in the rail's order under its
// names, then those it does not show, each ticked when the tag holds the room now.
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

// fileInTags applies the picker's tag rows: each whose tick differs from whether the
// tag holds the room now. It reports whether anything changed.
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

// toggleTag is /tag: the named tag takes the room if it does not hold it, and lets it
// go if it does; with no name, the filing picker opens.
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
		return m.say("no tag is named " + strings.TrimSpace(name) + " — a [[tag]] in the config makes one"), nil
	}
	label := view.tags.At(i).Name
	return m.fileTags(room, []tagFiling{{tag: i, label: label, in: !view.tagsOf(room).in[i]}})
}

// fileTags writes the filings into the config's tags and applies it, keeping the
// cursor near the room, which may have left the row it was in.
func (m Model) fileTags(room domain.Room, filings []tagFiling) (Model, tea.Cmd) {
	view := m.unreadView()
	facts := view.factsOf(room)
	others := make([]domain.RoomFacts, 0, len(m.rooms.all))
	for i := range m.rooms.all {
		others = append(others, view.factsOf(m.rooms.all[i]))
	}
	cfg := m.conf.base.Clone()
	var into, outOf []string
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
	next, cmd := m.applyConfig(cfg, "", m.roomName(room)+" is "+strings.Join(said, ", and "))
	moved, move := next.keepCursorNearby(was)
	return moved, tea.Batch(cmd, move)
}
