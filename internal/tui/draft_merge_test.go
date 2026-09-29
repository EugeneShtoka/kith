package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// sharedDrafts is the daemon's draft store as two writers see it: every write is
// conditional on the version its writer read, as db.ReplaceDraft is.
type sharedDrafts struct {
	apitest.Nop
	mu     sync.Mutex
	drafts map[domain.RoomID]domain.StoredDraft
}

func (s *sharedDrafts) Drafts(context.Context) ([]domain.StoredDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.StoredDraft, 0, len(s.drafts))
	for room := range s.drafts {
		out = append(out, s.drafts[room])
	}
	return out, nil
}

func (s *sharedDrafts) ReplaceDraft(_ context.Context, draft, over domain.StoredDraft) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, held := s.drafts[draft.RoomID]
	switch {
	case !held && (over.Body != "" || !over.Updated.IsZero()):
		return false, nil
	case held && (!sameStored(current, over) || current.Updated.UnixMilli() != over.Updated.UnixMilli()):
		return false, nil
	}
	if draft.Empty() {
		delete(s.drafts, draft.RoomID)
	} else {
		draft.Updated = time.UnixMilli(draft.Updated.UnixMilli())
		s.drafts[draft.RoomID] = draft
	}
	return true, nil
}

// assistantWrites is kith-mcp appending to a room's stored draft.
func (s *sharedDrafts) assistantWrites(room domain.RoomID, body string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drafts[room] = domain.StoredDraft{RoomID: room, Body: body, Author: "claude-code", Updated: at}
}

func (s *sharedDrafts) body(room domain.RoomID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drafts[room].Body
}

// sharing is a client in !a:x with "hi" already stored there, loaded as it starts.
func sharing(t *testing.T) (Model, *sharedDrafts) {
	t.Helper()
	store := &sharedDrafts{drafts: map[domain.RoomID]domain.StoredDraft{
		"!a:x": {RoomID: "!a:x", Body: "hi", Updated: time.UnixMilli(1000)},
	}}
	m := update(t, New(context.Background(), store, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus, m.compose.insertMode = paneTimeline, true
	m = deliver(t, m, m.loadDraftsCmd())
	if m.compose.input != "hi" {
		t.Fatalf("composer = %q after loading, want the stored draft", m.compose.input)
	}
	return m.clearStatus(), store
}

// polled is m after one draft poll.
func polled(t *testing.T, m Model) Model {
	t.Helper()
	return deliver(t, m, m.loadDraftsCmd())
}

// The assistant adds to a draft this client already holds. With nothing typed here
// since, the poll takes the new version: in the open composer and in a stashed room.
func TestAnIdleDraftTakesTheAssistantsAddition(t *testing.T) {
	t.Parallel()

	t.Run("open", func(t *testing.T) {
		t.Parallel()
		m, store := sharing(t)
		store.assistantWrites("!a:x", "hi\n\nthe report is attached", time.UnixMilli(2000))
		m = polled(t, m)
		if m.compose.input != "hi\n\nthe report is attached" {
			t.Errorf("composer = %q, want the assistant's addition taken", m.compose.input)
		}
	})
	t.Run("stashed", func(t *testing.T) {
		t.Parallel()
		m, store := sharing(t)
		m = openedFromList(t, m, "!b:x")
		store.assistantWrites("!a:x", "hi\n\nthe report is attached", time.UnixMilli(2000))
		m = polled(t, m)
		if got := m.drafts["!a:x"].input; got != "hi\n\nthe report is attached" {
			t.Errorf("stashed draft = %q, want the assistant's addition taken", got)
		}
	})
}

// Typing here and the assistant's addition cross: the poll leaves the typing alone,
// and the save keeps both, in the store and in the composer.
func TestTypingAndAnAdditionThatCrossKeepBoth(t *testing.T) {
	t.Parallel()

	m, store := sharing(t)
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	store.assistantWrites("!a:x", "hi\n\nthe report is attached", time.UnixMilli(2000))
	m = polled(t, m)
	if m.compose.input != "hi there" {
		t.Fatalf("composer = %q, want the typing left alone by the poll", m.compose.input)
	}

	next, cmd := asModel(m.Update(draftTickMsg{gen: m.draftSync.gen}))
	m = deliver(t, next, cmd)
	const want = "hi there\n\nthe report is attached"
	if got := store.body("!a:x"); got != want {
		t.Errorf("stored = %q, want the typing and then the addition: %q", got, want)
	}
	if m.compose.input != want {
		t.Errorf("composer = %q, want the addition folded in: %q", m.compose.input, want)
	}
	// The next pause writes nothing that would undo either.
	next, cmd = asModel(m.Update(draftTickMsg{gen: m.draftSync.gen}))
	_ = deliver(t, next, cmd)
	if got := store.body("!a:x"); got != want {
		t.Errorf("after another pause, stored = %q, want %q", got, want)
	}
}

// Sending clears the composer; an addition that arrived meanwhile survives as the
// room's draft instead of being deleted with it.
func TestAnAdditionOutlivesTheDraftItWasAddedTo(t *testing.T) {
	t.Parallel()

	m, store := sharing(t)
	store.assistantWrites("!a:x", "hi\n\none more thing", time.UnixMilli(2000))
	m.compose.input = "" // sent
	m, cmd := m.armDraftSave()
	m = deliver(t, m, cmd)
	if got := store.body("!a:x"); got != "one more thing" {
		t.Errorf("stored = %q, want the assistant's words kept", got)
	}
	if m.compose.input != "one more thing" {
		t.Errorf("composer = %q, want the assistant's words back", m.compose.input)
	}
}

// The failures below were each found by the audit of 2026-09-26b, and each is a
// schedule TestDraftsSurviveEveryInterleaving can reach; named here so the order that
// broke it is on record.

// leaveFor moves to another room and runs the writes leaving asks for (openedFromList
// goes through press, which drops them).
func leaveFor(t *testing.T, m Model, id domain.RoomID) Model {
	t.Helper()
	room, _ := m.rooms.byID(id)
	m, _ = m.selectRoom(room)
	m, cmd := m.armDraftSave()
	return settle(t, m, cmd)
}

// tickCmd is the debounce firing, and the save it asks for (not yet run).
func tickCmd(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	return asModel(m.Update(draftTickMsg{gen: m.draftSync.gen}))
}

// A send that crosses this client's own save still on the wire must not bring the
// sent words back (credited to the assistant).
func TestASendCrossingItsOwnSaveStaysSent(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"save answers first", "save answers after the send"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			m, store := sharing(t)
			m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
			m = typeInto(t, m, " there")
			m, save := tickCmd(t, m)
			saved := msgsOf(t, save) // on the wire: stored, the reply not yet handled
			m.compose.input = ""     // sent
			m, clear := m.armDraftSave()
			if order == "save answers first" {
				for _, msg := range saved {
					var next tea.Cmd
					m, next = asModel(m.Update(msg))
					clear = tea.Batch(clear, next)
				}
				m = settle(t, m, clear)
			} else {
				m = settle(t, m, clear)
				for _, msg := range saved {
					var next tea.Cmd
					m, next = asModel(m.Update(msg))
					m = settle(t, m, next)
				}
			}
			if got := store.body("!a:x"); got != "" || m.compose.input != "" {
				t.Errorf("after the send: stored %q, composer %q; want both empty", got, m.compose.input)
			}
		})
	}
}

// Two saves of this client's own typing, the first slower than the debounce, store
// the typing once.
func TestOverlappingOwnSavesDoNotDoubleTheText(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	m, first := tickCmd(t, m)
	m = typeInto(t, m, " you")
	m, second := tickCmd(t, m)
	for _, cmd := range []tea.Cmd{first, second} {
		for _, msg := range msgsOf(t, cmd) {
			var next tea.Cmd
			m, next = asModel(m.Update(msg))
			m = settle(t, m, next)
		}
	}
	if got := store.body("!a:x"); got != "hi there you" || m.compose.input != "hi there you" {
		t.Errorf("stored %q, composer %q; want %q in both", got, m.compose.input, "hi there you")
	}
}

// A poll read before this client's save, and delivered after it, is not taken.
func TestAPollOlderThanASaveIsNotTaken(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	stale := m.loadDraftsCmd()() // reads "hi"
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	if store.body("!a:x") != "hi there" {
		t.Fatalf("stored %q, want the save through", store.body("!a:x"))
	}
	m = update(t, m, stale)
	if m.compose.input != "hi there" {
		t.Errorf("composer %q after a stale poll, want the typing kept", m.compose.input)
	}
}

// A draft deleted under this client (its room left elsewhere, which deletes it) goes
// here too, and is not written back by leaving the room.
func TestADraftDeletedElsewhereStaysDeleted(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	store.mu.Lock()
	delete(store.drafts, "!a:x")
	store.mu.Unlock()
	m = polled(t, m)
	if m.compose.input != "" {
		t.Errorf("composer %q after the poll, want the deletion taken", m.compose.input)
	}
	m = leaveFor(t, m, "!b:x")
	if got := store.body("!a:x"); got != "" {
		t.Errorf("stored %q: a draft deleted elsewhere came back by switching rooms", got)
	}
}

// Deleted under this client while it typed more: no other window sent those words
// (there is one, api.Seat), so all of what this client holds is written back.
func TestTypingOverADraftDeletedElsewhereKeepsItAll(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	store.mu.Lock()
	delete(store.drafts, "!a:x")
	store.mu.Unlock()
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	if got := store.body("!a:x"); got != "hi there" || m.compose.input != "hi there" {
		t.Errorf("stored %q, composer %q; want all of it: %q", got, m.compose.input, "hi there")
	}
}

// The assistant's addition crossing the start of an edit belongs to the draft the
// composer returns to, and survives canceling the edit.
func TestAnAdditionCrossingAnEditSurvivesItsCancel(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	store.assistantWrites("!a:x", "hi\n\nthe report is attached", time.UnixMilli(2000))
	m.compose.editSaved = m.editorFor(fieldComposer).text
	m.compose.editing = "$mine"
	m = m.store(fieldComposer, newEditor("old words").end())
	m, _ = m.armDraftSave() // arms the debounce, which fires next
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	if m.compose.input != "old words" {
		t.Errorf("edit composer %q: the addition went into the correction", m.compose.input)
	}
	m = m.cancelEdit()
	m, _ = m.armDraftSave()
	m, save = tickCmd(t, m)
	m = settle(t, m, save)
	const want = "hi\n\nthe report is attached"
	if got := store.body("!a:x"); got != want || m.compose.input != want {
		t.Errorf("after cancel: stored %q, composer %q; want %q", got, m.compose.input, want)
	}
}

// Where this client had no draft and the assistant wrote one, both are kept whole:
// the assistant's words are not cut where they happen to start like the typing.
func TestADraftStartedOnBothSidesKeepsBothWhole(t *testing.T) {
	t.Parallel()
	store := &sharedDrafts{drafts: map[domain.RoomID]domain.StoredDraft{}}
	m := update(t, New(context.Background(), store, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = deliver(t, m, m.loadDraftsCmd())
	m.focus, m.compose.insertMode = paneTimeline, true
	m = typeInto(t, m, "Hi")
	store.assistantWrites("!a:x", "Hi Bob, the report is attached", time.UnixMilli(3000))
	m, save := tickCmd(t, m)
	_ = settle(t, m, save)
	if got := store.body("!a:x"); got != "Hi\n\nHi Bob, the report is attached" {
		t.Errorf("stored %q, want both texts whole", got)
	}
}

// Visiting a room holds its draft, it does not write it: who drafted it stays.
func TestVisitingADraftKeepsWhoWroteIt(t *testing.T) {
	t.Parallel()
	store := &sharedDrafts{drafts: map[domain.RoomID]domain.StoredDraft{
		"!a:x": {RoomID: "!a:x", Body: "from claude", Author: "claude-code", Updated: time.UnixMilli(1000)},
	}}
	m := update(t, New(context.Background(), store, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	m = sized(t, m)
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = deliver(t, m, m.loadDraftsCmd())
	_ = leaveFor(t, m, "!b:x")
	store.mu.Lock()
	got := store.drafts["!a:x"]
	store.mu.Unlock()
	if got.Author != "claude-code" || !got.Updated.Equal(time.UnixMilli(1000)) {
		t.Errorf("stored after a visit: author %q at %v; want it untouched", got.Author, got.Updated)
	}
}

// Quitting inside the debounce writes the typing first, and quits once it is stored.
func TestQuitWritesTheTypingFirst(t *testing.T) {
	t.Parallel()
	w := newDraftWorld(t, 1)
	w.typeWord()
	w.leaving = true
	w.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	w.update(keyText("q"))
	w.rest()
	if w.exits == 0 {
		t.Error("quit never came")
	}
	if got, want := w.stored("!a:x").Body, "hi w1"; got != want {
		t.Errorf("stored after quit %q, want the typing kept: %q", got, want)
	}
}
