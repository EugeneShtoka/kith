package tui

import (
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// searchLimit caps the hits of one search; newest first, so the oldest are trimmed.
const searchLimit = 200

// searchSenderLimit caps the people `from:` completes against, ranked by activity.
const searchSenderLimit = 300

// searchScope is how wide a search reaches, a visible mode cycled with a key.
type searchScope int

const (
	searchInRoom  searchScope = iota // the open room
	searchInGroup                    // every room of the selected rail group
	searchEverywhere
)

func (s searchScope) next() searchScope { return (s + 1) % 3 }

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
	// kind is a word search or a list (mentions, files, …); typing in a list narrows it.
	kind searchKind
	// people are who `from:` completes against, asked once per scope (peopleScope).
	people      []domain.Member
	peopleScope searchScope
}

// showing defaults to a word search, so the zero value left by closing the pane is safe.
func (s searchState) showing() searchKind {
	if s.kind == nil {
		return wordsList{}
	}
	return s.kind
}

func (s searchState) global() bool { return s.scope == searchEverywhere }

// awaiting reports whether a late answer belongs to the running search.
func (s searchState) awaiting(query string, scope searchScope) bool {
	return s.active && s.query == query && s.scope == scope
}

// awaitingScope is awaiting for a scope-only answer (the `from:` list).
func (s searchState) awaitingScope(scope searchScope) bool {
	return s.active && s.scope == scope
}

// answered replaces the hits, ordered by the kind, and resets the cursor.
func (s searchState) answered(hits []domain.SearchHit, err error) searchState {
	s.showing().order(hits)
	s.hits, s.err, s.cursor = hits, err, 0
	return s
}

func (s searchState) moved(delta int) searchState {
	s.cursor = moveCursor(s.cursor, len(s.hits), delta, false)
	return s
}

func (s searchState) selected() (domain.SearchHit, bool) {
	if s.cursor < 0 || s.cursor >= len(s.hits) {
		return domain.SearchHit{}, false
	}
	return s.hits[s.cursor], true
}

// openSearch starts a search in the open room or across all; the timeline pane shows results.
func (m Model) openSearch(global bool) (Model, tea.Cmd) {
	return m.openSearchFor(global, "")
}

// openSearchFor is openSearch with terms already typed (`/search foo`); they run at once.
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
	// Ask for `from:` candidates before it is typed, and the cards a field term meets.
	m, cards := m.loadCards(false, "")
	senders := tea.Batch(m.searchSendersCmd(m.searchRooms(reach), reach), cards)
	if terms == "" {
		return m, senders
	}
	m = m.store(fieldPrompt, m.editorFor(fieldPrompt).insert(terms))
	mdl, cmd := m.runSearch(terms)
	return mdl, tea.Batch(cmd, senders)
}

// openMentions lists messages that name you, newest first. It opens everywhere
// whatever the pane, since it is about you, not a room; `tab` narrows it.
func (m Model) openMentions() (Model, tea.Cmd) { return m.openMentionsIn(searchEverywhere) }

func (m Model) openMentionsIn(reach searchScope) (Model, tea.Cmd) {
	return m.openList(promptMentions, reach, mentionsList{})
}

func (m Model) openFiles(global bool) (Model, tea.Cmd) {
	scope := m.defaultSearchScope()
	if global {
		scope = searchEverywhere
	}
	return m.openList(promptFiles, scope, filesList{})
}

func (m Model) openStarred() (Model, tea.Cmd) {
	return m.openList(promptStarred, m.defaultSearchScope(), starredList{})
}

// openTracked lists messages with a tracked word; word narrows to one entry, empty
// groups by entry.
func (m Model) openTracked(word string) (Model, tea.Cmd) {
	return m.openList(promptTracked, m.defaultSearchScope(), trackedList{words: m.prefs.display.Tracked.Words, only: word})
}

// openList opens the results pane on a list kind. The kind's clause is the whole
// query, but the prompt stays open so typing narrows it.
func (m Model) openList(prompt promptKind, scope searchScope, kind searchKind) (Model, tea.Cmd) {
	m = m.openPrompt(prompt)
	m.search = searchState{active: true, scope: scope, kind: kind}
	m.focus = paneTimeline
	m.compose = m.compose.left()
	return m.runSearch("")
}

// defaultSearchScope follows the focused pane: rail → everywhere, room list → its
// group, timeline/composer → the open room (the group when none is open).
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

// searchRooms resolves a scope to its rooms: the open room (none if none), the rail
// group's, or every room.
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

func (m Model) scopeLabel(reach searchScope) string {
	switch reach {
	case searchInRoom:
		if room, ok := m.currentRoom(); ok {
			return m.roomName(room) + " ▸ room"
		}
		return "this room"
	case searchInGroup:
		// No noun: not every group is a space, and "group" means a non-DM room in the config.
		return isolate(m.rail.label())
	default:
		return "everywhere"
	}
}

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

// runSearch searches for the prompt text; an empty query clears the results.
func (m Model) runSearch(query string) (Model, tea.Cmd) {
	m.search.query = query
	m.search.err = nil
	// Filters are parsed here so only terms reach FTS5 (see domain.ParseSearch).
	filter := domain.ParseSearch(query, time.Now())
	m.search.showing().narrow(&filter)
	if filter.SenderField != "" {
		senders, why := m.fieldSenders(filter.SenderField)
		if why != "" {
			m.search.hits, m.search.cursor = nil, 0
			return m.say(why), nil
		}
		filter.Senders = senders
	}
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

// handleSearchSenders applies the `from:` candidates. A failure is ignored: `from:`
// falls back to the open room's members.
func (m Model) handleSearchSenders(msg searchSendersMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelDebug, "load search senders", msg.err)
	if msg.err != nil || !m.search.awaitingScope(msg.scope) {
		return m, nil
	}
	m.search.people, m.search.peopleScope = msg.senders, msg.scope
	return m.refreshCandidates(), nil
}

// commitSearch moves the keyboard from the prompt to the results list, which stays up even empty.
func (m Model) commitSearch() (Model, tea.Cmd) {
	return m.clearStatus(), nil
}

func (m Model) closeSearch() (Model, tea.Cmd) {
	m.search = searchState{}
	return m.clearStatus(), nil
}

func (m Model) moveSearchCursor(delta int) (Model, tea.Cmd) {
	m.search = m.search.moved(delta)
	return m, nil
}

// jumpToHit opens the hit's room and selects its message once loaded (see resolveJump).
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

// resolveJump selects a pending jump target once the timeline holds it. A message
// trimmed from the cache since the search is reported once the load settles.
func (m Model) resolveJump(settled bool) (Model, tea.Cmd) {
	if m.jump.to == "" {
		return m, nil
	}
	if indexOfMessage(m.timeline.messages, m.jump.to) >= 0 {
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

// searchSummary is the results header: the scope, in the kind's sentence.
func (m Model) searchSummary() string {
	where := "in this room"
	if m.search.global() {
		where = "across all rooms"
	}
	return m.search.showing().summary(m, where)
}

func (m Model) searchHint() string {
	return m.hintLine(
		keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "results"),
		keyed(m.keys.keyHint(scopeSearch, actJump), "go to message"),
		keyed(m.keys.keyHint(scopeSearch, actSearchScope), "scope"),
		keyed(m.keys.keyHint(scopeSearch, actClose), "close"),
	)
}

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
	// Results are newest first, so the top is the newest end: all runs negative.
	if delta, ok := navDelta(act, m.take(), m.msgAreaRows(), -len(m.search.hits)); ok {
		return m.moveSearchCursor(delta)
	}
	return m, nil
}

func indexOfMessage(msgs []domain.Message, id domain.EventID) int {
	for i := range msgs {
		if msgs[i].ID == id {
			return i
		}
	}
	return -1
}
