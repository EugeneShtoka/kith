package tui

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Archived rooms are those an archive tag holds ([*.archive] tag, "Archived" by
// default), or that their network archived. A room list shows them or not as [display]
// show_archived says, a place's [[display.space_rule]] show_archived over it; the
// archived key flips it for the place in view until kith restarts. The archive's own
// list always shows them: it is where they are.

// isArchived reports whether room is archived.
func (m Model) isArchived(view unreadView, room domain.Room) bool {
	facts := view.factsOf(room)
	if facts.ArchivedIn != "" {
		return true
	}
	return slices.ContainsFunc(facts.Tags, m.isArchiveTag)
}

// isArchiveTag reports whether a tag, by name, is an archive.
func (m Model) isArchiveTag(name string) bool {
	return slices.ContainsFunc(m.rail.archiveTags, func(a string) bool { return strings.EqualFold(a, name) })
}

// archivedShownIn reports whether place's room list shows archived rooms: always in an
// archive's own, else as the archived key last left it, else as the config says.
func (m Model) archivedShownIn(place string) bool {
	if name, isTag := domain.TagOf(place); isTag && m.isArchiveTag(name) {
		return true
	}
	if shown, flipped := m.rail.archivedShown[place]; flipped {
		return shown
	}
	return m.prefs.display.ShowsArchivedIn(place)
}

// toggleArchived shows the place in view's archived rooms, or hides them, until kith
// restarts; an archive's own list always shows them.
func (m Model) toggleArchived() (Model, tea.Cmd) {
	g, ok := m.currentGroup()
	if !ok {
		return m, nil
	}
	if name, isTag := domain.TagOf(g.key); isTag && m.isArchiveTag(name) {
		return m.say(isolate(g.label) + " is the archive: it always lists its rooms"), nil
	}
	shown := !m.archivedShownIn(g.key)
	flips := make(map[string]bool, len(m.rail.archivedShown)+1)
	maps.Copy(flips, m.rail.archivedShown)
	flips[g.key] = shown
	m.rail.archivedShown = flips
	was := m.roomCursor()
	word := "hidden"
	if shown {
		word = "shown"
	}
	m = m.say("archived rooms " + word + " in " + isolate(g.label))
	return m.keepCursorNearby(was)
}
