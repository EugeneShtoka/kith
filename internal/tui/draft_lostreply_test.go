package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// lostReply is a store whose nth RebaseDraft commits and then errors, as when the
// daemon restarts after COMMIT or the socket drops before the answer.
type lostReply struct {
	*sharedDrafts
	loseOn, calls int
	// before runs before the next RebaseDraft: another writer landing first.
	before func()
}

func (l *lostReply) RebaseDraft(ctx context.Context, d, base domain.StoredDraft, unanswered []domain.StoredDraft) (domain.StoredDraft, string, error) {
	l.calls++
	if hook := l.before; hook != nil {
		l.before = nil
		hook()
	}
	stored, added, err := l.sharedDrafts.RebaseDraft(ctx, d, base, unanswered)
	if l.calls == l.loseOn && err == nil {
		return domain.StoredDraft{}, "", errors.New("unavailable: connection reset")
	}
	return stored, added, err
}

// A write that landed with its reply lost is this client's own: the retry after more
// typing does not count its words as another writer's and write them twice.
func TestALostReplyThenTypingWritesTheWordsOnce(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.backend = &lostReply{sharedDrafts: store, loseOn: 1}
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	if store.body(mx+"!a:x") != "hi there" || !m.draftSync.retry[mx+"!a:x"] {
		t.Fatalf("stored %q, retry %v: want the write landed and a retry owed", store.body(mx+"!a:x"), m.draftSync.retry)
	}
	m = typeInto(t, m, " more")
	_ = settle(t, m, m.loadDraftsCmd()) // the poll retries the failed save
	if got := store.body(mx + "!a:x"); got != "hi there more" {
		t.Fatalf("stored %q, want %q", got, "hi there more")
	}
}

// A lost-reply write that folded in the assistant's words: the retry keeps them once,
// though the local copy never heard of them.
func TestALostReplyAfterAFoldKeepsTheAdditionOnce(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	l := &lostReply{sharedDrafts: store, loseOn: 1}
	l.before = func() {
		store.drafts[mx+"!a:x"] = domain.StoredDraft{RoomID: mx + "!a:x", Body: "hi\n\na1", Author: "claude-code", Updated: time.UnixMilli(2000)}
	}
	m.backend = l
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	m = settle(t, m, m.loadDraftsCmd())
	if got, want := store.body(mx+"!a:x"), "hi there\n\na1"; got != want || m.compose.input != want {
		t.Fatalf("stored %q, composer %q; want both %q", got, m.compose.input, want)
	}
}

// The same lost fold, then more typing before the poll: the retry builds on that write
// and carries its addition, so no later stored version lacks the assistant's words.
func TestALostReplyAfterAFoldThenTypingKeepsTheAdditionOnce(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	l := &lostReply{sharedDrafts: store, loseOn: 1}
	l.before = func() {
		store.drafts[mx+"!a:x"] = domain.StoredDraft{RoomID: mx + "!a:x", Body: "hi\n\na1", Author: "claude-code", Updated: time.UnixMilli(2000)}
	}
	w := &writesSeen{Backend: l, store: store}
	m.backend = w
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	m = typeInto(t, m, " you")
	m = settle(t, m, m.loadDraftsCmd())
	if got, want := store.body(mx+"!a:x"), "hi there you\n\na1"; got != want || m.compose.input != want {
		t.Fatalf("stored %q, composer %q; want both %q", got, m.compose.input, want)
	}
	for _, body := range w.stored[1:] { // the first is the lost write itself
		if !strings.Contains(body, "a1") {
			t.Errorf("stored %q after the lost write, without the assistant's words (all: %q)", body, w.stored)
		}
	}
}

// writesSeen records what the store holds after every write (a lost reply stored too).
type writesSeen struct {
	api.Backend
	store  *sharedDrafts
	stored []string
}

func (w *writesSeen) RebaseDraft(ctx context.Context, d, base domain.StoredDraft, unanswered []domain.StoredDraft) (domain.StoredDraft, string, error) {
	stored, added, err := w.Backend.RebaseDraft(ctx, d, base, unanswered)
	w.stored = append(w.stored, w.store.body(d.RoomID))
	return stored, added, err
}

// Two writes in one millisecond get different stamps, so a turned-down write is never
// mistaken for one that landed.
func TestWriteStampsOnlyGoUp(t *testing.T) {
	t.Parallel()
	var s draftSaver
	now := time.UnixMilli(5000)
	s, first := s.stamped(now)
	s, second := s.stamped(now)
	_, earlier := s.stamped(now.Add(-time.Second))
	if !first.Before(second) || !second.Before(earlier) {
		t.Fatalf("stamps %v, %v, %v: want each after the last", first, second, earlier)
	}
}

// failingDrafts stores nothing: the daemon is gone.
type failingDrafts struct{ *sharedDrafts }

func (failingDrafts) RebaseDraft(context.Context, domain.StoredDraft, domain.StoredDraft, []domain.StoredDraft) (domain.StoredDraft, string, error) {
	return domain.StoredDraft{}, "", errors.New("unavailable: daemon gone")
}

// The last write at exit reports the rooms the daemon did not take; nothing is kept
// for later (unsaved.go).
func TestTheLastWriteReportsWhatItCouldNotSave(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	if lost := m.flushDrafts(context.Background()); len(lost) != 0 {
		t.Fatalf("lost %v with the daemon answering", lost)
	}
	if got := store.body(mx + "!a:x"); got != "hi there" {
		t.Fatalf("stored %q, want the typing written at exit", got)
	}
	m = typeInto(t, m, " again")
	m.backend = failingDrafts{store}
	if lost := m.flushDrafts(context.Background()); !slices.Equal(lost, []domain.RoomID{mx + "!a:x"}) {
		t.Fatalf("lost %v, want the room the daemon did not take", lost)
	}
}

// Unbroken typing is still saved every draftSaveEvery: an exit that cannot reach the
// daemon loses at most that much.
func TestTypingWithoutAPauseIsSavedAllTheSame(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " a")
	if got := store.body(mx + "!a:x"); got != "hi" {
		t.Fatalf("stored %q while typing, before the debounce: want it unsaved yet", got)
	}
	m.draftSync.since = time.Now().Add(-draftSaveEvery) // typing has gone on this long
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m, cmd := asModel(m.Update(keyText("b")))
	_ = settle(t, m, cmd)
	if got := store.body(mx + "!a:x"); got != "hi ab" {
		t.Errorf("stored %q after %v of typing, want it saved without a pause", got, draftSaveEvery)
	}
}
