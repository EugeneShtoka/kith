package tui

import (
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// searchLimit caps how many hits one search returns (newest first, so the oldest are
// trimmed).
const searchLimit = 200

// searchSenderLimit caps how many people `from:` completes against, ranked by activity.
const searchSenderLimit = 300

// searchScope is how wide a search reaches — a visible mode cycled with a key, not a
// typed token.
type searchScope int

const (
	searchInRoom  searchScope = iota // the open room
	searchInGroup                    // every room of the selected rail group
	searchEverywhere
)

// next cycles room → group → everywhere → room.
func (s searchScope) next() searchScope { return (s + 1) % 3 }

// searchState is a search and its results.
type searchState struct {
	// active gates the results pane; it outlives the prompt.
	active bool
	// query and scope are what the current hits answer; rooms is what scope resolved
	// to. A late result for anything else is dropped.
	query  string
	scope  searchScope
	rooms  domain.RoomSet
	hits   []domain.SearchHit
	cursor int
	err    error
	// kind is a word search or a list (mentions, files, …); see searchKind. Typing in
	// a list narrows it.
	kind searchKind
	// people are who `from:` completes against, asked once per scope; peopleScope is
	// the scope they answer for.
	people      []domain.Member
	peopleScope searchScope
}

// showing is the kind the pane has open, defaulting to a word search so the zero
// value left by closing the pane is safe.
func (s searchState) showing() searchKind {
	if s.kind == nil {
		return wordsList{}
	}
	return s.kind
}

// global reports whether the search covers every room.
func (s searchState) global() bool { return s.scope == searchEverywhere }

// awaiting reports whether a late answer belongs to the search that is running.
func (s searchState) awaiting(query string, scope searchScope) bool {
	return s.active && s.query == query && s.scope == scope
}

// awaitingScope is awaiting for an answer about the scope only (the `from:` list).
func (s searchState) awaitingScope(scope searchScope) bool {
	return s.active && s.scope == scope
}

// answered replaces the hits, ordered by the kind, and resets the cursor.
func (s searchState) answered(hits []domain.SearchHit, err error) searchState {
	s.showing().order(hits)
	s.hits, s.err, s.cursor = hits, err, 0
	return s
}

// moved walks the cursor through the hits, without wrapping.
func (s searchState) moved(delta int) searchState {
	s.cursor = moveCursor(s.cursor, len(s.hits), delta, false)
	return s
}

// selected is the hit under the cursor, and whether there is one.
func (s searchState) selected() (domain.SearchHit, bool) {
	if s.cursor < 0 || s.cursor >= len(s.hits) {
		return domain.SearchHit{}, false
	}
	return s.hits[s.cursor], true
}

// openSearch starts a search, in the open room or across all of them; the timeline
// pane becomes the results list.
func (m Model) openSearch(global bool) (Model, tea.Cmd) {
	return m.openSearchFor(global, "")
}

// openSearchFor is openSearch with terms already typed (`/search foo`); they run at
// once, and none just opens the box.
func (m Model) openSearchFor(global bool, terms string) (Model, tea.Cmd) {
	reach := searchEverywhere
	if !global {
		reach = m.defaultSearchScope()
	}
	kind := promptSearchRoom
	if reach == searchEverywhere {
		kind = promptSearchAll
	}
	m = m.openPrompt(kind)
	m.search = searchState{active: true, scope: reach, kind: wordsList{}}
	m.focus = paneTimeline
	m.compose = m.compose.left()
	// Ask for `from:` candidates now so they are there before it is typed.
	senders := m.searchSendersCmd(m.searchRooms(reach), reach)
	if terms == "" {
		return m, senders
	}
	m = m.store(fieldPrompt, m.editorFor(fieldPrompt).insert(terms))
	mdl, cmd := m.runSearch(terms)
	return mdl, tea.Batch(cmd, senders)
}

// openMentions lists every message that names you, newest first. Unlike every other
// list it opens everywhere whatever pane it was asked from — who named you is a
// question about you, not about a room; `tab` narrows it.
func (m Model) openMentions() (Model, tea.Cmd) { return m.openMentionsIn(searchEverywhere) }

// openMentionsIn is the mentions list at a chosen scope (`/mentions` asks about here).
func (m Model) openMentionsIn(reach searchScope) (Model, tea.Cmd) {
	return m.openList(promptMentions, reach, mentionsList{})
}

// openFiles lists every message in scope carrying an attachment.
func (m Model) openFiles(global bool) (Model, tea.Cmd) {
	scope := m.defaultSearchScope()
	if global {
		scope = searchEverywhere
	}
	return m.openList(promptFiles, scope, filesList{})
}

// openStarred lists every message you have bookmarked, newest star first.
func (m Model) openStarred() (Model, tea.Cmd) {
	return m.openList(promptStarred, m.defaultSearchScope(), starredList{})
}

// openTracked lists every message carrying one of your tracked words; word narrows to
// one entry, empty opens the whole list grouped.
func (m Model) openTracked(word string) (Model, tea.Cmd) {
	return m.openList(promptTracked, m.defaultSearchScope(), trackedList{words: m.prefs.display.Tracked.Words, only: word})
}

// openList opens the results pane on a list kind. No terms are needed — the kind's
// clause is the whole query — but the prompt stays open so typing narrows it.
func (m Model) openList(prompt promptKind, scope searchScope, kind searchKind) (Model, tea.Cmd) {
	m = m.openPrompt(prompt)
	m.search = searchState{active: true, scope: scope, kind: kind}
	m.focus = paneTimeline
	m.compose = m.compose.left()
	return m.runSearch("")
}

// defaultSearchScope is the scope for a search asked from the focused pane, one ladder
// left to right: rail → everywhere, room list → its group, timeline/composer → the open
// room (the group when none is open).
func (m Model) defaultSearchScope() searchScope {
	switch m.focus {
	case paneRail:
		return searchEverywhere
	case paneRooms:
		return searchInGroup
	case paneTimeline, paneCount:
	}
	if _, ok := m.currentRoom(); !ok {
		return searchInGroup
	}
	return searchInRoom
}

// searchRooms resolves a scope into the rooms it covers: the open room (none when no
// room is open), the rail group's, or every room.
func (m Model) searchRooms(reach searchScope) domain.RoomSet {
	switch reach {
	case searchInRoom:
		if _, ok := m.currentRoom(); ok {
			return domain.TheseRooms([]domain.RoomID{m.openRoom})
		}
		return domain.TheseRooms(nil)
	case searchInGroup:
		filtered := m.filteredRooms()
		rooms := make([]domain.RoomID, 0, len(filtered))
		for i := range filtered {
			rooms = append(rooms, filtered[i].ID)
		}
		return domain.TheseRooms(rooms)
	default:
		return domain.EveryRoom()
	}
}

// scopeLabel names a scope for the results title, with the thing it covers.
func (m Model) scopeLabel(reach searchScope) string {
	switch reach {
	case searchInRoom:
		if room, ok := m.currentRoom(); ok {
			return m.roomName(room) + " ▸ room"
		}
		return "this room"
	case searchInGroup:
		// No noun: not every group is a space, and "group" means a non-DM room
		// elsewhere in the config vocabulary.
		return isolate(m.rail.label())
	default:
		return "everywhere"
	}
}

// cycleSearchScope widens or wraps the scope and re-runs the query.
func (m Model) cycleSearchScope() (Model, tea.Cmd) {
	if !m.search.active {
		return m, nil
	}
	m.search.scope = m.search.scope.next()
	// With no room open the room scope means nothing; skip it.
	if _, ok := m.currentRoom(); !ok && m.search.scope == searchInRoom {
		m.search.scope = m.search.scope.next()
	}
	reach := m.search.scope
	senders := m.searchSendersCmd(m.searchRooms(reach), reach)
	mdl, cmd := m.runSearch(m.editorFor(fieldPrompt).text)
	return mdl, tea.Batch(cmd, senders)
}

// runSearch issues a search for the current prompt text; an empty query clears the
// results.
func (m Model) runSearch(query string) (Model, tea.Cmd) {
	m.search.query = query
	m.search.err = nil
	// Filters are parsed here so only terms ever reach FTS5 (see domain.ParseSearch).
	filter := domain.ParseSearch(query, time.Now())
	m.search.showing().narrow(&filter)
	if filter.Empty() {
		m.search.hits, m.search.cursor = nil, 0
		return m, nil
	}
	m.search.rooms = m.searchRooms(m.search.scope)
	return m, m.searchCmd(query, domain.SearchRequest{
		Filter: filter, Rooms: m.search.rooms, Limit: searchLimit,
	})
}

// handleSearchResults applies hits, dropping answers to an earlier keystroke or scope.
func (m Model) handleSearchResults(msg searchResultsMsg) (Model, tea.Cmd) {
	if !m.search.awaiting(msg.query, msg.scope) {
		return m, nil
	}
	m.search = m.search.answered(msg.hits, msg.err)
	return m, nil
}

// handleSearchSenders applies the people a search can be narrowed to. A failure is
// ignored: `from:` falls back to the open room's members.
func (m Model) handleSearchSenders(msg searchSendersMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelDebug, "load search senders", msg.err)
	if msg.err != nil || !m.search.awaitingScope(msg.scope) {
		return m, nil
	}
	m.search.people, m.search.peopleScope = msg.senders, msg.scope
	return m.refreshCandidates(), nil
}

// commitSearch hands the keyboard from the prompt to the results list, which (even
// empty) stays up.
func (m Model) commitSearch() (Model, tea.Cmd) {
	return m.clearStatus(), nil
}

// closeSearch dismisses the results and returns the timeline to the conversation.
func (m Model) closeSearch() (Model, tea.Cmd) {
	m.search = searchState{}
	return m.clearStatus(), nil
}

// moveSearchCursor walks the results list, clamping.
func (m Model) moveSearchCursor(delta int) (Model, tea.Cmd) {
	m.search = m.search.moved(delta)
	return m, nil
}

// jumpToHit opens the hit's room and asks for its message to be selected once the
// timeline has it (see resolveJump).
func (m Model) jumpToHit() (Model, tea.Cmd) {
	hit, ok := m.search.selected()
	if !ok {
		return m, nil
	}
	room, found := m.roomByID(hit.RoomID)
	if !found {
		return m.say("that room is no longer in your list"), nil
	}
	m = m.closePrompt()
	m.search = searchState{}
	m.jump.to, m.jump.why = hit.EventID, "search result"
	next, cmd := m.selectRoom(room)
	next.focus = paneTimeline
	return next, cmd
}

// resolveJump selects a pending jump target once the timeline holds it. It can miss
// when the message was trimmed from the cache since the search; that is reported once
// the load has settled.
func (m Model) resolveJump(settled bool) (Model, tea.Cmd) {
	if m.jump.to == "" {
		return m, nil
	}
	if indexOfMessage(m.timeline.messages, m.jump.to) >= 0 {
		// revealMessage opens the thread a hit lives in, fetching its root if needed.
		next, cmd := m.revealMessage(m.jump.to)
		m = next
		said := m.jump.why
		if said == "" {
			said = "message"
		}
		m.jump.to, m.jump.why = "", ""
		m = m.say("jumped to the " + said)
		return m, cmd
	}
	if settled {
		m.jump.to, m.jump.why = "", ""
		m = m.say("that message is no longer cached")
	}
	return m, nil
}

// searchSummary is the results pane's header: the scope, in the kind's sentence.
func (m Model) searchSummary() string {
	where := "in this room"
	if m.search.global() {
		where = "across all rooms"
	}
	return m.search.showing().summary(m, where)
}

// searchHint is the results list's own key legend.
func (m Model) searchHint() string {
	return m.hintLine(
		keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "results"),
		keyed(m.keys.keyHint(scopeSearch, actJump), "go to message"),
		keyed(m.keys.keyHint(scopeSearch, actSearchScope), "scope"),
		keyed(m.keys.keyHint(scopeSearch, actClose), "close"),
	)
}

// handleSearchKey drives the results list; nav keys fall through from scopeNav.
func (m Model) handleSearchKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	act := m.keys.lookup(key.String(), scopeSearch, scopeNav, scopeCommand)
	switch act {
	case actJump:
		return m.jumpToHit()
	case actStar:
		// The star key unstars, in the starred list only.
		if m.search.showing() == (starredList{}) {
			return m.unstarSelectedHit()
		}
		return m, nil
	case actSearchScope:
		return m.cycleSearchScope()
	case actClose, actBack:
		return m.closeSearch()
	case actHelp:
		m.reader = m.reader.opening(readerHelp)
		return m, nil
	}
	// Results are newest first, so the newest end is the top: all runs negative.
	if delta, ok := navDelta(act, m.take(), m.msgAreaRows(), -len(m.search.hits)); ok {
		return m.moveSearchCursor(delta)
	}
	return m, nil
}

// indexOfMessage returns the position of the message with id, or -1.
func indexOfMessage(msgs []domain.Message, id domain.EventID) int {
	for i := range msgs {
		if msgs[i].ID == id {
			return i
		}
	}
	return -1
}
