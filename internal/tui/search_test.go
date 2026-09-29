package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// searchBackend records the queries issued and answers with canned hits.
type searchBackend struct {
	apitest.Nop
	queries  []string
	rooms    []domain.RoomID
	requests []domain.SearchRequest
	hits     []domain.SearchHit
	err      error
	// senders answers `from:`; senderScopes records the room sets asked about.
	senders      []domain.Member
	senderScopes []domain.RoomSet
}

func (b *searchBackend) SearchSenders(_ context.Context, rooms domain.RoomSet, _ int) ([]domain.Member, error) {
	b.senderScopes = append(b.senderScopes, rooms)
	return b.senders, b.err
}

func (b *searchBackend) SearchMessages(_ context.Context, req domain.SearchRequest) ([]domain.SearchHit, error) {
	b.queries = append(b.queries, req.Filter.Terms)
	b.requests = append(b.requests, req)
	room := domain.RoomID("")
	if len(req.Rooms.IDs) == 1 {
		room = req.Rooms.IDs[0]
	}
	b.rooms = append(b.rooms, room)
	return b.hits, b.err
}

// hit builds one search result.
func hit(room domain.RoomID, event domain.EventID, name, snippet string, agoHours int) domain.SearchHit {
	return domain.SearchHit{
		RoomID: room, EventID: event, Sender: "@" + name + ":x", SenderName: name,
		Timestamp: time.Now().Add(-time.Duration(agoHours) * time.Hour),
		Snippet:   snippet,
	}
}

// searching returns a model with the search prompt open over two rooms, and its backend.
func searching(t *testing.T, global bool, hits ...domain.SearchHit) (Model, *searchBackend) {
	t.Helper()
	b := &searchBackend{hits: hits}
	m := update(t, New(context.Background(), b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo", IsDirect: true},
	}})
	m = sized(t, m)
	// Open a room so a room-scoped search has something to scope to.
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = m.clearStatus()
	// From the timeline, so the default scope is this room.
	m.focus = paneTimeline

	key := keyText("/")
	if global {
		key = tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}
	}
	m, _ = press(t, m, key)
	return m, b
}

// typeQuery feeds text into the prompt a keystroke at a time, running each command.
func typeQuery(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		next, cmd := press(t, m, keyText(string(r)))
		m = next
		if cmd != nil {
			m = update(t, m, cmd())
		}
	}
	return m
}

func TestSearchOpensScopedToTheOpenRoom(t *testing.T) {
	t.Parallel()

	m, b := searching(t, false)
	if m.prompt.kind != promptSearchRoom {
		t.Fatalf("prompt kind = %v, want the room-search prompt", m.prompt.kind)
	}
	if !m.search.active {
		t.Error("the results pane should be up as soon as the prompt opens")
	}
	if m.search.scope != searchInRoom || m.search.global() {
		t.Errorf("scope = %v (global %v), want the open room", m.search.scope, m.search.global())
	}
	if m.focus != paneTimeline {
		t.Error("search should focus the pane its results appear in")
	}
	m = typeQuery(t, m, "deploy")
	if len(b.rooms) == 0 || b.rooms[len(b.rooms)-1] != "!a:x" {
		t.Errorf("searched rooms = %v, want the open room", b.rooms)
	}
}

func TestSearchAllIsGlobal(t *testing.T) {
	t.Parallel()

	m, b := searching(t, true)
	if m.prompt.kind != promptSearchAll {
		t.Fatalf("prompt kind = %v, want the all-rooms prompt", m.prompt.kind)
	}
	if !m.search.global() {
		t.Error("search all should not be scoped to a room")
	}
	m = typeQuery(t, m, "x")
	if len(b.rooms) == 0 || b.rooms[len(b.rooms)-1] != "" {
		t.Errorf("searched rooms = %v, want the empty (global) scope", b.rooms)
	}
}

// With no room open the search opens on the group rather than refusing.
func TestSearchRoomWithNoRoomOpen(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel()) // no rooms, so no open room
	m.focus = paneTimeline
	m, _ = press(t, m, keyText("/"))
	if !m.search.active {
		t.Fatal("search should still open")
	}
	if m.search.scope != searchInGroup {
		t.Errorf("scope = %v, want the group", m.search.scope)
	}
}

// Where the key was pressed decides how wide the search starts.
func TestSearchScopeDefaultsToThePane(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pane pane
		want searchScope
	}{
		{paneRail, searchEverywhere},
		{paneRooms, searchInGroup},
		{paneTimeline, searchInRoom},
	} {
		m, _ := searching(t, false)
		closed, _ := m.closeSearch()
		m = closed
		m.focus = tc.pane
		next, _ := m.openSearch(false)
		opened := next
		if opened.search.scope != tc.want {
			t.Errorf("from pane %v: scope = %v, want %v", tc.pane, opened.search.scope, tc.want)
		}
	}
}

// The scope key widens without retyping, and wraps.
func TestSearchScopeCycles(t *testing.T) {
	t.Parallel()

	m, b := searching(t, false)
	m = typeQuery(t, m, "deploy")
	if m.search.scope != searchInRoom {
		t.Fatalf("setup: scope = %v", m.search.scope)
	}
	for _, want := range []searchScope{searchInGroup, searchEverywhere, searchInRoom} {
		next, cmd := m.cycleSearchScope()
		m = next
		if cmd != nil {
			m = update(t, m, cmd())
		}
		if m.search.scope != want {
			t.Fatalf("cycled to %v, want %v", m.search.scope, want)
		}
	}
	// Each cycle re-ran the query rather than making the user retype it.
	if len(b.queries) < 4 || b.queries[len(b.queries)-1] != "deploy" {
		t.Errorf("queries = %v, want the same query re-run on each scope change", b.queries)
	}
}

// The scope is in the title, because a mode you cannot see is one you lose track of.
func TestSearchTitleNamesTheScope(t *testing.T) {
	t.Parallel()

	m, _ := searching(t, false)
	if got := m.searchTitle(); !strings.Contains(got, "room") {
		t.Errorf("title = %q, want it to name the room scope", got)
	}
	// The group rung names the rail group, with no category noun.
	next, _ := m.cycleSearchScope()
	m = next
	if got := m.searchTitle(); !strings.Contains(got, displayName(m.rail.label())) {
		t.Errorf("title = %q, want it to name the group %q", got, m.rail.label())
	}
	if strings.Contains(m.searchTitle(), "space") {
		t.Errorf("title = %q still says \"space\" for a rung that is a rail group", m.searchTitle())
	}
	next, _ = m.cycleSearchScope()
	m = next
	if got := m.searchTitle(); !strings.Contains(got, "everywhere") {
		t.Errorf("title = %q, want it to say everywhere", got)
	}
}

// One search per character typed.
func TestSearchRunsAsYouType(t *testing.T) {
	t.Parallel()

	m, b := searching(t, false, hit("!a:x", "$1", "Alice", "the deploy", 1))
	m = typeQuery(t, m, "dep")

	if got := strings.Join(b.queries, ","); got != "d,de,dep" {
		t.Errorf("queries = %q, want one per keystroke", got)
	}
	if len(m.search.hits) != 1 {
		t.Errorf("hits = %d, want 1", len(m.search.hits))
	}
	// Backspacing to nothing clears the results rather than searching for nothing.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	before := len(b.queries)
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if cmd != nil {
		t.Error("an empty query should not be searched")
	}
	if len(b.queries) != before {
		t.Error("an empty query should not reach the backend")
	}
	if len(m.search.hits) != 0 {
		t.Error("clearing the query should clear the results")
	}
}

// A slow search for an earlier keystroke must not overwrite a later one.
func TestStaleSearchResultsAreDropped(t *testing.T) {
	t.Parallel()

	m, _ := searching(t, false)
	m = typeQuery(t, m, "deploy")

	fresh := []domain.SearchHit{hit("!a:x", "$1", "Alice", "current", 1)}
	m = update(t, m, searchResultsMsg{query: "deploy", scope: searchInRoom, hits: fresh})
	if len(m.search.hits) != 1 {
		t.Fatalf("hits = %d, want the current results", len(m.search.hits))
	}

	// A result for "dep" arriving late is for a query the user has typed past.
	stale := []domain.SearchHit{
		hit("!a:x", "$9", "Bob", "stale", 2), hit("!a:x", "$8", "Bob", "stale", 3),
	}
	m = update(t, m, searchResultsMsg{query: "dep", scope: searchInRoom, hits: stale})
	if len(m.search.hits) != 1 || m.search.hits[0].EventID != "$1" {
		t.Errorf("stale results overwrote current ones: %+v", m.search.hits)
	}
	// So is one for a different scope.
	m = update(t, m, searchResultsMsg{query: "deploy", scope: searchEverywhere, hits: stale})
	if len(m.search.hits) != 1 {
		t.Error("results for a different scope should be dropped")
	}
}

// The arrows walk the results, clamping at both ends.
func TestSearchResultsAreWalkable(t *testing.T) {
	t.Parallel()

	m, _ := searching(t, true,
		hit("!a:x", "$1", "Alice", "one", 1),
		hit("!b:x", "$2", "Bob", "two", 2),
		hit("!a:x", "$3", "Dana", "three", 3))
	m = typeQuery(t, m, "x")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.prompt.active() {
		t.Fatal("submitting should close the prompt")
	}
	if !m.search.active {
		t.Fatal("submitting should keep the results")
	}
	if m.search.cursor != 0 {
		t.Errorf("cursor = %d, want the first result", m.search.cursor)
	}
	// Up at the top stays put.
	m, _ = press(t, m, keyText("k"))
	if m.search.cursor != 0 {
		t.Errorf("cursor = %d after up at the top, want 0", m.search.cursor)
	}
	m, _ = press(t, m, keyText("j"))
	m, _ = press(t, m, keyText("j"))
	if m.search.cursor != 2 {
		t.Errorf("cursor = %d after two downs, want 2", m.search.cursor)
	}
	// Down past the end stays on the last.
	m, _ = press(t, m, keyText("j"))
	if m.search.cursor != 2 {
		t.Errorf("cursor = %d past the end, want the last result", m.search.cursor)
	}
	// The ends jump.
	m, _ = press(t, m, keyText("g"))
	if m.search.cursor != 2 {
		t.Errorf("g = %d, want the oldest result", m.search.cursor)
	}
	m, _ = press(t, m, keyCode('G'))
	if m.search.cursor != 0 {
		t.Errorf("G = %d, want the newest result", m.search.cursor)
	}
}

// esc from the prompt and esc from the results both put the conversation back.
func TestSearchCloses(t *testing.T) {
	t.Parallel()

	// From the prompt.
	m, _ := searching(t, false, hit("!a:x", "$1", "Alice", "one", 1))
	m = typeQuery(t, m, "x")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.search.active || m.prompt.active() {
		t.Error("esc from the prompt should dismiss the search entirely")
	}

	// From the results.
	m, _ = searching(t, false, hit("!a:x", "$1", "Alice", "one", 1))
	m = typeQuery(t, m, "x")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.search.active {
		t.Error("esc from the results should dismiss them")
	}
}

// The point of the whole feature: a hit becomes a selected message in its room.
func TestJumpToResultSelectsTheMessage(t *testing.T) {
	t.Parallel()

	m, _ := searching(t, true, hit("!b:x", "$target", "Bob", "found it", 5))
	m = typeQuery(t, m, "found")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // commit
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // jump

	if m.search.active {
		t.Error("jumping should dismiss the results")
	}
	if m.openRoom != "!b:x" {
		t.Errorf("open room = %q, want the hit's room", m.openRoom)
	}
	if m.jump.to != "$target" {
		t.Errorf("jump.to = %q, want the hit's event", m.jump.to)
	}
	if m.timeline.selected == "$target" {
		t.Error("the message cannot be selected before the timeline holds it")
	}

	m = update(t, m, cachedTimelineMsg{roomID: "!b:x", messages: []domain.Message{
		{ID: "$older", RoomID: "!b:x", Sender: "@bob:x", Body: "before", Timestamp: time.Now().Add(-6 * time.Hour)},
		{ID: "$target", RoomID: "!b:x", Sender: "@bob:x", Body: "found it", Timestamp: time.Now().Add(-5 * time.Hour)},
		{ID: "$newer", RoomID: "!b:x", Sender: "@bob:x", Body: "after", Timestamp: time.Now().Add(-4 * time.Hour)},
	}})
	if m.timeline.selected != "$target" {
		t.Errorf("selected = %q, want the jump target", m.timeline.selected)
	}
	if m.jump.to != "" {
		t.Error("a resolved jump should be cleared")
	}
	if !strings.Contains(m.status(), "jumped") {
		t.Errorf("status = %q, should say the jump happened", m.status())
	}
}

// A hit trimmed from the cache since the search is reported.
func TestJumpToUncachedMessageIsReported(t *testing.T) {
	t.Parallel()

	m, _ := searching(t, true, hit("!b:x", "$gone", "Bob", "was here", 5))
	m = typeQuery(t, m, "was")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// The cached page arrives without it, and the jump stays pending…
	m = update(t, m, cachedTimelineMsg{roomID: "!b:x", messages: []domain.Message{
		{ID: "$other", RoomID: "!b:x", Sender: "@bob:x", Body: "something else"},
	}})
	if m.jump.to != "$gone" {
		t.Error("the jump should still be pending while history is arriving")
	}
	// …until the live page settles it.
	m = update(t, m, timelineMsg{roomID: "!b:x", page: domain.TimelinePage{
		Messages: []domain.Message{{ID: "$other", RoomID: "!b:x", Sender: "@bob:x", Body: "something else"}},
	}})
	if m.jump.to != "" {
		t.Error("an unresolvable jump should be abandoned once history has settled")
	}
	if !strings.Contains(m.status(), "no longer cached") {
		t.Errorf("status = %q, should explain the jump did not happen", m.status())
	}
}

// A hit in a room that has since been left cannot be jumped to, and says so.
func TestJumpToLeftRoom(t *testing.T) {
	t.Parallel()

	m, _ := searching(t, true, hit("!gone:x", "$1", "Bob", "x", 1))
	m = typeQuery(t, m, "x")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.status(), "no longer in your list") {
		t.Errorf("status = %q, should explain the room is gone", m.status())
	}
}

// The summary is the answer to the question, so it has to be right at every count.
func TestSearchSummary(t *testing.T) {
	t.Parallel()

	m, _ := searching(t, false)
	if got := m.searchSummary(); !strings.Contains(got, "type to search") {
		t.Errorf("empty query summary = %q", got)
	}
	m.search.query = "x"
	if got := m.searchSummary(); !strings.Contains(got, "no matches in this room") {
		t.Errorf("no-match summary = %q", got)
	}
	m.search.hits = []domain.SearchHit{hit("!a:x", "$1", "A", "s", 1)}
	if got := m.searchSummary(); !strings.Contains(got, "1 match") {
		t.Errorf("one-match summary = %q", got)
	}
	m.search.hits = append(m.search.hits, hit("!a:x", "$2", "B", "s", 2))
	if got := m.searchSummary(); !strings.Contains(got, "2 matches") {
		t.Errorf("two-match summary = %q", got)
	}
	// A capped result set says so, because the answer is "narrow it", not "scroll".
	m.search.hits = make([]domain.SearchHit, searchLimit)
	if got := m.searchSummary(); !strings.Contains(got, "narrow") {
		t.Errorf("capped summary = %q, should suggest narrowing", got)
	}
	// A failure is reported rather than shown as "no matches".
	m.search.err = errTest
	if got := m.searchSummary(); !strings.Contains(got, "failed") {
		t.Errorf("error summary = %q", got)
	}
	// And global scope says so.
	m.search.scope, m.search.err, m.search.hits = searchEverywhere, nil, nil
	if got := m.searchSummary(); !strings.Contains(got, "all rooms") {
		t.Errorf("global summary = %q", got)
	}
}

// The room is shown only when searching across rooms.
func TestSearchPaneContents(t *testing.T) {
	t.Parallel()

	H, E := domain.HighlightStart, domain.HighlightEnd
	m, _ := searching(t, true,
		hit("!a:x", "$1", "Alice", "the "+H+"deploy"+E+" went out", 1),
		hit("!b:x", "$2", "Bob", H+"deploy"+E+" blocked", 2))
	m = typeQuery(t, m, "deploy")
	body := stripStyles(m.View().Content)

	for _, want := range []string{"2 matches across all rooms", "Alice", "Bob", "deploy", "[Alpha]", "[Bravo]"} {
		if !strings.Contains(body, want) {
			t.Errorf("the results pane should show %q", want)
		}
	}
	// The highlight markers are styling, never text.
	if strings.ContainsAny(body, domain.HighlightStart+domain.HighlightEnd) {
		t.Error("highlight markers leaked into the rendered output")
	}

	// Room-scoped results drop the room column: it would be the same on every row.
	m2, _ := searching(t, false, hit("!a:x", "$1", "Alice", "x", 1))
	m2 = typeQuery(t, m2, "x")
	if strings.Contains(stripStyles(m2.View().Content), "[Alpha]") {
		t.Error("a room-scoped search should not repeat the room on every row")
	}
}

// A multi-line message body must not break the one-row-per-result layout.
func TestSearchExcerptIsOneLine(t *testing.T) {
	t.Parallel()

	m, _ := searching(t, false, hit("!a:x", "$1", "Alice", "first line\n\nsecond line", 1))
	m = typeQuery(t, m, "line")
	rows := 0
	for line := range strings.SplitSeq(stripStyles(m.View().Content), "\n") {
		if strings.Contains(line, "first line") || strings.Contains(line, "second line") {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("a multi-line body rendered across %d rows, want 1", rows)
	}
}

// A search prompt types freely: bindings must not swallow characters a query needs.
func TestSearchPromptTypesEverything(t *testing.T) {
	t.Parallel()

	m, b := searching(t, false)
	m = typeQuery(t, m, `j/k "q?" -x`)
	if m.prompt.input != `j/k "q?" -x` {
		t.Errorf("prompt input = %q, want every character typed", m.prompt.input)
	}
	if len(b.queries) == 0 || b.queries[len(b.queries)-1] != `j/k "q?" -x` {
		t.Errorf("queries = %v, want the full text searched", b.queries)
	}
}

// A backend failure is surfaced, not swallowed into an empty result set.
func TestSearchErrorSurfaces(t *testing.T) {
	t.Parallel()

	b := &searchBackend{err: errTest}
	m := sized(t, update(t, New(context.Background(), b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	m = typeQuery(t, m, "x")

	if m.search.err == nil {
		t.Fatal("a backend error should be recorded")
	}
	if !strings.Contains(stripStyles(m.View().Content), "search failed") {
		t.Error("a failed search should say so in the pane")
	}
}
