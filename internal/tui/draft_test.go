package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Words typed in one room do not follow you to another, and come back with the room.
func TestADraftDoesNotFollowYouToAnotherRoom(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = openedFromList(t, m, "!a:x") // lands in the composer
	m = typeInto(t, m, "for Alpha only")
	if m.compose.input != "for Alpha only" {
		t.Fatalf("composer holds %q", m.compose.input)
	}

	moved := openedFromList(t, m, "!ops:x")
	if moved.compose.input != "" {
		t.Errorf("the draft followed into the next room: %q", moved.compose.input)
	}
	back := openedFromList(t, moved, "!a:x")
	if back.compose.input != "for Alpha only" {
		t.Errorf("coming back gave %q, want the draft returned", back.compose.input)
	}
	if back.compose.caret.at != len("for Alpha only") {
		t.Errorf("caret at %d, want the end of the restored draft", back.compose.caret.at)
	}
}

// The reply target and the undo history travel with the draft.
func TestADraftCarriesItsReplyAndItsUndo(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = openedFromList(t, m, "!a:x")
	m.compose.replyTo = "$somewhere-in-alpha"
	m = typeInto(t, m, "one two")

	moved := openedFromList(t, m, "!ops:x")
	if moved.compose.replyTo != "" {
		t.Errorf("the reply target followed to another room: %s", moved.compose.replyTo)
	}
	back := openedFromList(t, moved, "!a:x")
	if back.compose.replyTo != "$somewhere-in-alpha" {
		t.Errorf("reply target = %q, want it restored with the draft", back.compose.replyTo)
	}
	undone, _ := press(t, back, tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if undone.compose.input != "one " {
		t.Errorf("ctrl+z after coming back gave %q, want %q", undone.compose.input, "one ")
	}
}

// The reply target is the draft's, so selecting the open room again (enter on its
// row, a jump into it, a re-sort) keeps it, as it keeps the words.
func TestReselectingTheOpenRoomKeepsTheReplyTarget(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = openedFromList(t, m, "!a:x")
	m.compose.replyTo = "$somewhere-in-alpha"
	m = typeInto(t, m, "one two")

	again := openedFromList(t, m, "!a:x")
	if again.compose.replyTo != "$somewhere-in-alpha" {
		t.Errorf("reply target = %q after re-selecting the room, want it kept", again.compose.replyTo)
	}
	if again.compose.input != "one two" {
		t.Errorf("words = %q after re-selecting the room, want them kept", again.compose.input)
	}
}

// The room list marks a room holding a draft, right after its name, without
// shifting the name's column.
func TestTheRoomListMarksARoomHoldingADraft(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = openedFromList(t, m, "!a:x")
	m = typeInto(t, m, "unsent")
	m = openedFromList(t, m, "!ops:x")

	if !m.hasDraft("!a:x") {
		t.Fatal("the room holding the draft does not report one")
	}
	if m.hasDraft("!dana:x") {
		t.Error("a room with no draft reports one")
	}
	view := stripStyles(m.View().Content)
	row, found := rowContaining(view, "Alpha")
	if !found {
		t.Fatalf("no Alpha row in the room list:\n%s", view)
	}
	if !strings.Contains(row, "Alpha "+draftMark) {
		t.Errorf("Alpha's row does not carry the mark right after the name: %q", row)
	}
	marked, ok := roomCellContaining(view, "Alpha")
	if !ok {
		t.Fatal("no Alpha cell in the room list pane")
	}
	plain, ok := roomCellContaining(view, "Dana")
	if !ok {
		t.Fatal("no unmarked room row to compare alignment against")
	}
	if indent(marked) != indent(plain) {
		t.Errorf("the marked row shifted its name:\n%q\n%q", marked, plain)
	}
}

// An emptied draft is forgotten rather than remembered as empty.
func TestAnEmptiedDraftIsForgotten(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = openedFromList(t, m, "!a:x")
	m = typeInto(t, m, "never mind")
	for range len("never mind") {
		next, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
		m = next
	}
	m = openedFromList(t, m, "!ops:x")
	if m.hasDraft("!a:x") {
		t.Error("an emptied composer was stashed as a draft")
	}
	if _, held := m.drafts[domain.RoomID("!a:x")]; held {
		t.Error("the room is pinned in the draft map with nothing in it")
	}
}

// rowContaining is the rendered line holding text.
func rowContaining(view, text string) (string, bool) {
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, text) {
			return line, true
		}
	}
	return "", false
}

// The Drafts group lists rooms holding a draft (not the open one), and exists only
// while there is one.
func TestTheDraftsGroupCollectsRoomsHoldingWords(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	if _, ok := findGroup(m.rail.groups, draftsGroupKey); ok {
		t.Fatal("a Drafts group exists with nothing half-written")
	}

	m = openedFromList(t, m, "!a:x")
	m = typeInto(t, m, "unsent words")
	m = openedFromList(t, m, "!dana:x")

	g, ok := findGroup(m.rail.groups, draftsGroupKey)
	if !ok {
		t.Fatal("no Drafts group after leaving a room with a draft")
	}
	if !g.admits(m.unreadView(), domain.Room{ID: "!a:x"}) {
		t.Error("the Drafts group does not hold the room the draft was written in")
	}
	if g.admits(m.unreadView(), domain.Room{ID: "!ops:x"}) {
		t.Error("the Drafts group holds a room with no draft")
	}
	if g.admits(m.unreadView(), domain.Room{ID: "!dana:x"}) {
		t.Error("the open room is in the Drafts group while you are typing in it")
	}

	back := openedFromList(t, m, "!a:x")
	cleared, _ := press(t, back, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	cleared = openedFromList(t, cleared, "!ops:x")
	if _, still := findGroup(cleared.rail.groups, draftsGroupKey); still {
		t.Error("the Drafts group outlived the last draft")
	}
}

// hide_when_empty drops a group holding nothing, matched by key or label.
func TestHideWhenEmptyDropsAGroupHoldingNothing(t *testing.T) {
	t.Parallel()

	rooms := []domain.Room{{ID: "!a:x", Name: "Alpha"}}
	view := starterView(t, unreadView{counts: map[domain.RoomID]domain.Unread{}})

	groups := railGroups(nil, config.Rail{HideWhenEmpty: []string{unreadGroupKey}}, nil, view, rooms)
	if _, ok := findGroup(groups, unreadGroupKey); ok {
		t.Error("an empty Unread group survived hide_when_empty")
	}
	view.counts = map[domain.RoomID]domain.Unread{"!a:x": {Notifications: 2, Messages: 2, Counted: true}}
	groups = railGroups(nil, config.Rail{HideWhenEmpty: []string{unreadGroupKey}}, nil, view, rooms)
	if _, ok := findGroup(groups, unreadGroupKey); !ok {
		t.Error("Unread stayed hidden with something unread in it")
	}
	groups = railGroups(nil, config.Rail{HideWhenEmpty: []string{"DMs"}}, nil, view, rooms)
	if _, ok := findGroup(groups, dmsGroupKey); ok {
		t.Error("hide_when_empty did not match a group by its displayed label")
	}
	groups = railGroups(nil, config.Rail{HideWhenEmpty: []string{"nonsense"}}, nil, view, rooms)
	if len(groups) < 3 {
		t.Errorf("groups = %v, want the rail untouched by an unmatched name", groupKeys(groups))
	}
}

// A narrow pane truncates the name, never the draft mark.
func TestALongNameLosesLettersRatherThanItsMark(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	long := "A room with a very long name indeed that will not fit"
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: long}, {ID: "!ops:x", Name: "Ops"}}})
	m = openedFromList(t, m, "!a:x")
	m = typeInto(t, m, "held")
	m = openedFromList(t, m, "!ops:x")

	view := stripStyles(m.View().Content)
	row, found := rowContaining(view, "A room with")
	if !found {
		t.Fatalf("no long-named row in the list:\n%s", view)
	}
	if !strings.Contains(row, draftMark) {
		t.Errorf("the truncation ate the mark instead of the name: %q", row)
	}
	if !strings.Contains(row, "…") {
		t.Errorf("the name was not shortened at all: %q", row)
	}
}

// roomCellContaining is the room-list pane's cell on the row holding text.
func roomCellContaining(view, text string) (string, bool) {
	for line := range strings.SplitSeq(view, "\n") {
		cells := strings.Split(line, "│")
		// The rail is the first cell; the room list is the next with content.
		for i := 2; i < len(cells); i++ {
			if strings.Contains(cells[i], text) {
				return cells[i], true
			}
		}
	}
	return "", false
}

// indent is how many columns of space a cell starts with.
func indent(cell string) int { return len(cell) - len(strings.TrimLeft(cell, " ")) }

// Walking the rail (which drops the open-room pin) stashes the draft rather than
// throwing it away.
func TestADraftSurvivesWalkingTheRail(t *testing.T) {
	t.Parallel()

	m := jumping(t) // Work holds Alpha; Infra holds Ops
	m = openedFromList(t, m, "!a:x")
	m = typeInto(t, m, "for Alpha")

	m.focus, m.compose.insertMode = paneRail, false
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	moved, _ := press(t, m, keyText("j"))
	for moved.rail.key() == "Work" {
		next, _ := press(t, moved, keyText("j"))
		if next.rail.key() == moved.rail.key() {
			t.Fatal("the rail cursor stopped moving before it left Work")
		}
		moved = next
	}
	if moved.compose.input != "" {
		t.Errorf("the draft followed the rail into %q: %q", moved.rail.key(), moved.compose.input)
	}
	if !moved.hasDraft("!a:x") {
		t.Fatal("walking the rail threw the draft away")
	}
	g, ok := findGroup(moved.rail.groups, draftsGroupKey)
	if !ok {
		t.Fatal("no Drafts group after walking away from a room holding a draft")
	}
	if !g.admits(m.unreadView(), domain.Room{ID: "!a:x"}) {
		t.Error("the Drafts group does not hold the room the draft is in")
	}

	back := openedFromList(t, moved, "!a:x")
	if back.compose.input != "for Alpha" {
		t.Errorf("returning gave %q, want the draft back", back.compose.input)
	}
}

// Clearing the room keeps the draft under the room that was open.
func TestADraftSurvivesTheRoomListEmptying(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = openedFromList(t, m, "!a:x")
	m = typeInto(t, m, "kept")

	cleared, _ := m.clearRoom()
	next := cleared
	if next.compose.input != "" {
		t.Errorf("the composer kept %q with no room open", next.compose.input)
	}
	if !next.hasDraft("!a:x") {
		t.Error("clearing the room threw the draft away")
	}
}

// draftBackend records what was stored and answers with what it holds.
type draftBackend struct {
	apitest.Nop
	stored []domain.StoredDraft
	load   []domain.StoredDraft
}

func (b *draftBackend) ReplaceDraft(_ context.Context, draft, _ domain.StoredDraft) (bool, error) {
	b.stored = append(b.stored, draft)
	return true, nil
}

func (b *draftBackend) Drafts(context.Context) ([]domain.StoredDraft, error) { return b.load, nil }

func drafting(t *testing.T, held ...domain.StoredDraft) (Model, *draftBackend) {
	t.Helper()
	b := &draftBackend{load: held}
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus = paneTimeline
	m.compose.insertMode = true
	return m.clearStatus(), b
}

// Typing writes the draft on a pause, not per keystroke.
func TestDraftIsWrittenOnAPause(t *testing.T) {
	t.Parallel()

	m, b := drafting(t)
	m = typeInto(t, m, "half a thought")
	if len(b.stored) != 0 {
		t.Fatalf("stored %+v before the pause, want nothing", b.stored)
	}
	next, cmd := asModel(m.Update(draftTickMsg{gen: m.draftSync.gen}))
	m = next
	if cmd == nil {
		t.Fatal("the pause wrote nothing")
	}
	_ = cmd()
	if len(b.stored) != 1 || b.stored[0].Body != "half a thought" || b.stored[0].RoomID != "!a:x" {
		t.Fatalf("stored %+v, want the draft under the open room", b.stored)
	}
	if b.stored[0].Author != "" {
		t.Errorf("author = %q, want it to be the person at the keyboard", b.stored[0].Author)
	}
}

// Leaving a room writes what it held.
func TestLeavingARoomWritesItsDraft(t *testing.T) {
	t.Parallel()

	m, b := drafting(t)
	m = typeInto(t, m, "for Alpha")
	next, _ := m.selectRoom(domain.Room{ID: "!b:x", Name: "Bravo"})
	m = next
	if m.compose.input != "" {
		t.Fatalf("composer = %q after the switch, want Bravo's empty one", m.compose.input)
	}
	// armDraftSave is called directly: the Update helpers discard its commands.
	m, arm := m.armDraftSave()
	m = deliver(t, m, arm)
	var wrote bool
	for _, stored := range b.stored {
		if stored.RoomID == "!a:x" && stored.Body == "for Alpha" {
			wrote = true
		}
	}
	if !wrote {
		t.Fatalf("stored %+v, want Alpha's draft written when it was left", b.stored)
	}
}

// A switch between two empty rooms writes nothing and returns no command.
func TestAnEmptyRoomSwitchWritesNothing(t *testing.T) {
	t.Parallel()

	m, b := drafting(t)
	next, _ := m.selectRoom(domain.Room{ID: "!b:x", Name: "Bravo"})
	m = next
	_, cmd := m.armDraftSave()
	if cmd != nil {
		t.Error("an empty room switch asked for work")
	}
	if len(b.stored) != 0 {
		t.Fatalf("stored %+v, want nothing", b.stored)
	}
}

// What was drafted while the client was closed comes back, and says whose it is.
func TestStoredDraftsComeBackWithTheirProvenance(t *testing.T) {
	t.Parallel()

	written := time.Now().Add(-3 * time.Hour)
	m, _ := drafting(t, domain.StoredDraft{
		RoomID: "!a:x", Body: "drafted for you", Author: domain.DraftAgent, Updated: written,
	})
	m = deliver(t, m, m.loadDraftsCmd())
	if m.compose.input != "drafted for you" {
		t.Fatalf("composer = %q, want the stored draft", m.compose.input)
	}
	note := m.draftNote()
	if !strings.Contains(note, domain.DraftAgent) || !strings.Contains(note, "3h ago") {
		t.Fatalf("draftNote() = %q, want the author and the age", note)
	}
	// Editing it drops the note and the author.
	m = typeInto(t, m, "!")
	if note := m.draftNote(); note != "" {
		t.Fatalf("draftNote() = %q after editing, want nothing", note)
	}
	if m.composerDraft().stored("!a:x").Author != "" {
		t.Error("an edited draft still claimed to be the agent's")
	}
}

// A slow load must not land on top of words typed while it was in flight.
func TestLoadedDraftsDoNotOverwriteLiveTyping(t *testing.T) {
	t.Parallel()

	m, _ := drafting(t, domain.StoredDraft{RoomID: "!a:x", Body: "from disk"})
	m = typeInto(t, m, "typed first")
	m = deliver(t, m, m.loadDraftsCmd())
	if m.compose.input != "typed first" {
		t.Fatalf("composer = %q, want what was being typed", m.compose.input)
	}
}

// A draft written elsewhere while this client runs is picked up on the poll, and
// shows in the Drafts group.
func TestThePollPicksUpADraftWrittenElsewhere(t *testing.T) {
	t.Parallel()

	m, b := drafting(t)
	m = openedFromList(t, m, "!b:x")
	b.load = []domain.StoredDraft{{
		RoomID: "!a:x", Body: "drafted for you", Author: "claude-code", Updated: time.Now(),
	}}

	next, cmd := asModel(m.Update(pollTickMsg{}))
	m = next
	if cmd == nil {
		t.Fatal("the poll asked for nothing")
	}
	for _, msg := range pollAnswers(t, cmd) {
		m = update(t, m, msg)
	}

	if !m.hasDraft("!a:x") {
		t.Fatal("the poll did not pick up a draft written elsewhere")
	}
	if indexOfGroup(m.rail.groups, draftsGroupKey) < 0 {
		t.Error("the Drafts group is missing after a draft arrived")
	}
}

// What this client holds always wins over a polled draft.
func TestThePollDoesNotDisturbADraftThisClientHolds(t *testing.T) {
	t.Parallel()

	m, b := drafting(t)
	m = typeInto(t, m, "mine, half typed")
	b.load = []domain.StoredDraft{{
		RoomID: "!a:x", Body: "mine, half typed\n\nand the assistant's",
		Author: "claude-code", Updated: time.Now(),
	}}

	next, cmd := asModel(m.Update(pollTickMsg{}))
	m = next
	for _, msg := range pollAnswers(t, cmd) {
		m = update(t, m, msg)
	}

	if m.compose.input != "mine, half typed" {
		t.Errorf("composer = %q, want the words being typed here left alone", m.compose.input)
	}
}

// pollAnswers is what a poll produced, skipping commands (the next tick's timer) that
// do not answer promptly.
func pollAnswers(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("the poll returned %T, want a batch of commands", cmd())
	}
	var out []tea.Msg
	for _, one := range batch {
		answered := make(chan tea.Msg, 1)
		go func(c tea.Cmd) { answered <- c() }(one)
		select {
		case msg := <-answered:
			if msg != nil {
				out = append(out, msg)
			}
		case <-time.After(500 * time.Millisecond):
		}
	}
	return out
}

// A sent or cleared draft leaves the Drafts group, is written at once, and is not
// refilled by the poll from a stale map entry.
func TestAClearedDraftStaysCleared(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m, _ = m.selectRoom(m.filteredRooms()[0])
	room := m.openRoom

	m.compose.insertMode = true
	m.compose.input = "half a sentence"
	m, _ = m.armDraftSave()
	if _, held := m.drafts[room]; !held {
		t.Fatal("typing did not put the room in the drafts map, so nothing below is tested")
	}
	// The pause writes it, so there is a stored draft for the clear to remove.
	next, save := asModel(m.Update(draftTickMsg{gen: m.draftSync.gen}))
	m = deliver(t, next, save)
	if m.draftSync.bases[room] == (draftStamp{}) {
		t.Fatal("the pause stored nothing, so nothing below is tested")
	}
	if !m.hasDraft(room) {
		t.Error("hasDraft is false for a room being typed in")
	}

	var cmd tea.Cmd
	m.compose.input = ""
	m, cmd = m.armDraftSave()
	if _, held := m.drafts[room]; held {
		t.Error("the room is still in the drafts map after the composer emptied")
	}

	if cmd == nil {
		t.Fatal("emptying the composer produced no write, so the stored draft would linger")
	}

	loaded, _ := m.handleDraftsLoaded(draftsLoadedMsg{})
	if loaded.compose.input != "" {
		t.Errorf("the poll refilled the composer with %q — nobody typed that",
			loaded.compose.input)
	}
	if loaded.hasDraft(room) {
		t.Error("the room is back in the Drafts group after a poll")
	}
}

// draftFailer refuses every draft write.
type draftFailer struct{ apitest.Nop }

func (draftFailer) ReplaceDraft(context.Context, domain.StoredDraft, domain.StoredDraft) (bool, error) {
	return false, errors.New("disk full")
}

// A failing draft store is said once, not on every pause that retries it, and its
// recovery is said too.
func TestDraftSaveFailuresAreSaidOnceAndRecoverySaid(t *testing.T) {
	t.Parallel()
	m := sized(t, starterNew(draftFailer{}, config.Display{}))

	saved, ok := m.saveDraftCmd("!a:x", draft{input: "half a thought"}.stored("!a:x"))().(draftSavedMsg)
	if !ok || saved.err == nil {
		t.Fatalf("saveDraftCmd = %#v, want the failure reported back", saved)
	}
	m, _ = m.handleDraftSaved(saved)
	if got := m.renderStatus(); !strings.Contains(got, "draft not saved") {
		t.Fatalf("status = %q, want the failure said", got)
	}
	m = m.clearStatus()
	m, _ = m.handleDraftSaved(saved)
	if got := m.renderStatus(); strings.Contains(got, "draft not saved") {
		t.Errorf("status = %q, want a repeat failure left unsaid", got)
	}
	m, _ = m.handleDraftSaved(draftSavedMsg{})
	if got := m.renderStatus(); !strings.Contains(got, "saved again") {
		t.Errorf("status = %q, want the recovery said", got)
	}
}

// A reply target chosen alone, with no words typed, arms the debounce, whose tick
// writes it: the saver watches the whole composition, not only the words.
func TestAReplyChosenAloneIsSaved(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	gen := m.draftSync.gen
	m.compose.replyTo = "$target"
	m, armed := m.armDraftSave()
	if armed == nil || m.draftSync.gen == gen {
		t.Fatal("a reply chosen alone armed no save")
	}
	m, save := m.handleDraftTick(draftTickMsg{gen: m.draftSync.gen})
	_ = settle(t, m, save)
	if got := store.drafts["!a:x"]; got.ReplyTo != "$target" {
		t.Fatalf("stored %+v, want the reply target written", got)
	}
}
