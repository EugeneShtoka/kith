package tui

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Archived rooms are those an archive tag holds ([*.archive] tag, "Archived" by
// default) or that their network archived. show_archived ([display], or a place's
// space_rule) decides whether a list shows them; the archived key flips it for the
// place in view until restart. An archive's own list always shows them.

func (m Model) isArchived(view unreadView, room domain.Room) bool {
	facts := view.factsOf(room)
	if facts.ArchivedIn != "" {
		return true
	}
	return slices.ContainsFunc(facts.Tags, m.isArchiveTag)
}

func (m Model) isArchiveTag(name string) bool {
	return slices.ContainsFunc(m.rail.archiveTags, func(a string) bool { return strings.EqualFold(a, name) })
}

// archivedShownIn reports whether place's list shows archived rooms: always in an
// archive, else as the archived key left it, else as configured.
func (m Model) archivedShownIn(place string) bool {
	if name, isTag := domain.TagOf(place); isTag && m.isArchiveTag(name) {
		return true
	}
	if shown, flipped := m.rail.archivedShown[place]; flipped {
		return shown
	}
	return m.prefs.display.ShowsArchivedIn(place)
}

// toggleArchived shows or hides archived rooms in the place in view until restart.
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
