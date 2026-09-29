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

// lostReply is a store whose nth ReplaceDraft commits and then reports an error, as
// when the daemon restarts after COMMIT or the socket drops before the answer.
type lostReply struct {
	*sharedDrafts
	loseOn, calls int
	// before runs before the next ReplaceDraft: another writer landing first.
	before func()
}

func (l *lostReply) ReplaceDraft(ctx context.Context, d, over domain.StoredDraft) (bool, error) {
	l.calls++
	if hook := l.before; hook != nil {
		l.before = nil
		hook()
	}
	ok, err := l.sharedDrafts.ReplaceDraft(ctx, d, over)
	if l.calls == l.loseOn && ok {
		return false, errors.New("unavailable: connection reset")
	}
	return ok, err
}

// A write that landed with its reply lost is this client's own: the retry after more
// typing does not take its words for another writer's and write them twice.
func TestALostReplyThenTypingWritesTheWordsOnce(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.backend = &lostReply{sharedDrafts: store, loseOn: 1}
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	if store.body("!a:x") != "hi there" || !m.draftSync.retry["!a:x"] {
		t.Fatalf("stored %q, retry %v: want the write landed and a retry owed", store.body("!a:x"), m.draftSync.retry)
	}
	m = typeInto(t, m, " more")
	_ = settle(t, m, m.loadDraftsCmd()) // the poll retries the failed save
	if got := store.body("!a:x"); got != "hi there more" {
		t.Fatalf("stored %q, want %q", got, "hi there more")
	}
}

// A write that folded the assistant's words in and whose reply was lost: the retry
// keeps those words, once, though the local copy never heard of them.
func TestALostReplyAfterAFoldKeepsTheAdditionOnce(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	l := &lostReply{sharedDrafts: store, loseOn: 2}
	l.before = func() {
		store.drafts["!a:x"] = domain.StoredDraft{RoomID: "!a:x", Body: "hi\n\na1", Author: "claude-code", Updated: time.UnixMilli(2000)}
	}
	m.backend = l
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	m = settle(t, m, m.loadDraftsCmd())
	if got, want := store.body("!a:x"), "hi there\n\na1"; got != want || m.compose.input != want {
		t.Fatalf("stored %q, composer %q; want both %q", got, m.compose.input, want)
	}
}

// The same lost fold, then more typing before the poll: the retry is based on that
// write and carries its addition, which the local copy still has not heard of, so no
// version stored after it is without the assistant's words (an exit then would lose
// them).
func TestALostReplyAfterAFoldThenTypingKeepsTheAdditionOnce(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	l := &lostReply{sharedDrafts: store, loseOn: 2}
	l.before = func() {
		store.drafts["!a:x"] = domain.StoredDraft{RoomID: "!a:x", Body: "hi\n\na1", Author: "claude-code", Updated: time.UnixMilli(2000)}
	}
	w := &writesSeen{Backend: l}
	m.backend = w
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	m = typeInto(t, m, " you")
	m = settle(t, m, m.loadDraftsCmd())
	if got, want := store.body("!a:x"), "hi there you\n\na1"; got != want || m.compose.input != want {
		t.Fatalf("stored %q, composer %q; want both %q", got, m.compose.input, want)
	}
	for _, body := range w.stored[1:] { // the first is the lost write itself
		if !strings.Contains(body, "a1") {
			t.Errorf("stored %q after the lost write, without the assistant's words (all: %q)", body, w.stored)
		}
	}
}

// writesSeen records the body of every write the store took.
type writesSeen struct {
	api.Backend
	stored []string
}

func (w *writesSeen) ReplaceDraft(ctx context.Context, d, over domain.StoredDraft) (bool, error) {
	ok, err := w.Backend.ReplaceDraft(ctx, d, over)
	if ok || err != nil { // a lost reply stored it too
		w.stored = append(w.stored, d.Body)
	}
	return ok, err
}

func TestDescentOf(t *testing.T) {
	t.Parallel()
	v := func(body string, ms int64) draftStamp { return draftStamp{body: body, ms: ms} }
	at := func(body string, ms int64) domain.StoredDraft {
		return domain.StoredDraft{Body: body, Updated: time.UnixMilli(ms)}
	}
	cases := []struct {
		name    string
		current domain.StoredDraft
		base    draftStamp
		own     []ownWrite
		want    draftStamp
	}{
		{"nothing of ours: the base", at("hi\n\na1", 9), v("hi", 1), nil, v("hi", 1)},
		{"our write, stored as it was", at("hi there", 5), v("hi", 1), []ownWrite{{v: v("hi there", 5)}}, v("hi there", 5)},
		{"our write, and the assistant after it", at("hi there\n\na1", 9), v("hi", 1),
			[]ownWrite{{v: v("hi there", 5)}}, v("hi there", 5)},
		{"the longest of ours that it extends", at("hi there you\n\na1", 9), v("hi", 1),
			[]ownWrite{{v: v("hi there", 5)}, {v: v("hi there you", 6)}}, v("hi there you", 6)},
		{"ours that it does not extend: the base", at("hi\n\na1", 9), v("hi", 1),
			[]ownWrite{{v: v("hi there", 5)}}, v("hi", 1)},
		{"a base it does not hold does not outrank ours", at("w1\n\na6", 9), v("hi", 1),
			[]ownWrite{{v: v("w1", 5)}}, v("w1", 5)},
		{"a reply chosen here, landed unheard, then the assistant: ours", domain.StoredDraft{
			Body: "hi\n\na1", ReplyTo: "$t", Updated: time.UnixMilli(9),
		}, v("hi", 1), []ownWrite{{v: draftStamp{body: "hi", reply: "$t", ms: 5}}}, draftStamp{body: "hi", reply: "$t", ms: 5}},
		{"a word boundary: a10 does not extend a1", at("a10", 9), v("", 0), []ownWrite{{v: v("a1", 5)}}, v("", 0)},
		{"words alike by chance, the edit state not: not ours", at("a2", 9), v("", 0),
			[]ownWrite{{v: draftStamp{body: "fixed", saved: "a2", editing: "$e", ms: 5}}}, v("", 0)},
		{"words alike by chance, the reply target not: not ours", at("hi\n\na1", 9), v("hi", 1),
			[]ownWrite{{v: draftStamp{body: "hi\n\na1", reply: "$t", ms: 5}}}, v("hi", 1)},
	}
	for _, c := range cases {
		if got, _ := descentOf(c.current, c.base, c.own); got != c.want {
			t.Errorf("%s: descentOf = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Two writes in one millisecond get different stamps, so one the store turned down is
// never mistaken for one that landed.
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

func (failingDrafts) ReplaceDraft(context.Context, domain.StoredDraft, domain.StoredDraft) (bool, error) {
	return false, errors.New("unavailable: daemon gone")
}

// The last write at exit reports the rooms the daemon did not take; nothing is kept
// for later (unsaved.go): what typing it held is gone.
func TestTheLastWriteReportsWhatItCouldNotSave(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " there")
	if lost := m.flushDrafts(context.Background()); len(lost) != 0 {
		t.Fatalf("lost %v with the daemon answering", lost)
	}
	if got := store.body("!a:x"); got != "hi there" {
		t.Fatalf("stored %q, want the typing written at exit", got)
	}
	m = typeInto(t, m, " again")
	m.backend = failingDrafts{store}
	if lost := m.flushDrafts(context.Background()); !slices.Equal(lost, []domain.RoomID{"!a:x"}) {
		t.Fatalf("lost %v, want the room the daemon did not take", lost)
	}
}

// Typing that goes on without a pause is still saved every draftSaveEvery: an exit that
// cannot reach the daemon loses at most that much.
func TestTypingWithoutAPauseIsSavedAllTheSame(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " a")
	if got := store.body("!a:x"); got != "hi" {
		t.Fatalf("stored %q while typing, before the debounce: want it unsaved yet", got)
	}
	m.draftSync.since = time.Now().Add(-draftSaveEvery) // typing has gone on this long
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m, cmd := asModel(m.Update(keyText("b")))
	_ = settle(t, m, cmd)
	if got := store.body("!a:x"); got != "hi ab" {
		t.Errorf("stored %q after %v of typing, want it saved without a pause", got, draftSaveEvery)
	}
}
