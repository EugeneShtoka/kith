package db

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestDraftsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}

	written := time.Unix(1_700_000_000, 0)
	draft := domain.StoredDraft{
		RoomID: mx + "!a:x", Body: "half a thought about Dana Levi", Caret: 7,
		Mentions: []domain.Mention{{UserID: "matrix:@dana:x", Name: "Dana Levi"}},
		ReplyTo:  mx + "$m:x", Author: domain.DraftAgent, Updated: written,
	}
	if err := putDraft(ctx, cache, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	got, err := cache.Drafts(ctx)
	if err != nil {
		t.Fatalf("Drafts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Drafts() = %+v, want one", got)
	}
	// Mentions survive: a lost pill notifies nobody and looks like one that works.
	if len(got[0].Mentions) != 1 || got[0].Mentions[0].UserID != "matrix:@dana:x" {
		t.Errorf("mentions = %+v, want Dana's", got[0].Mentions)
	}
	if got[0].Body != draft.Body || got[0].Caret != 7 || got[0].ReplyTo != mx+"$m:x" {
		t.Errorf("Drafts() = %+v, want what was stored", got[0])
	}
	// So does the provenance.
	if got[0].Author != domain.DraftAgent || !got[0].Updated.Equal(written) {
		t.Errorf("author/updated = %q/%v, want the agent and %v", got[0].Author, got[0].Updated, written)
	}
	if got[0].Author == "" {
		t.Error("an agent's draft reported itself as hand-written")
	}

	// Replaced wholesale, not appended.
	draft.Body, draft.Author = "second thoughts", ""
	if err := putDraft(ctx, cache, draft); err != nil {
		t.Fatalf("putDraft(again): %v", err)
	}
	got, _ = cache.Drafts(ctx)
	if len(got) != 1 || got[0].Body != "second thoughts" || got[0].Author != "" {
		t.Fatalf("Drafts() = %+v, want one row, rewritten and now hand-written", got)
	}

	// An empty draft is a delete.
	if err := putDraft(ctx, cache, domain.StoredDraft{RoomID: mx + "!a:x"}); err != nil {
		t.Fatalf("putDraft(empty): %v", err)
	}
	if got, _ := cache.Drafts(ctx); len(got) != 0 {
		t.Fatalf("Drafts() = %+v, want none after an empty save", got)
	}
}

// A draft goes with its room.
func TestDraftsGoWithTheirRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	if err := putDraft(ctx, cache, domain.StoredDraft{RoomID: mx + "!a:x", Body: "words"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := cache.db.ExecContext(ctx, `DELETE FROM rooms WHERE id = 'matrix:0123456789abcdef/!a:x'`); err != nil {
		t.Fatalf("delete room: %v", err)
	}
	if got, _ := cache.Drafts(ctx); len(got) != 0 {
		t.Fatalf("Drafts() = %+v, want the draft to have gone with its room", got)
	}
}

func TestRoomsWithNarrowsAsPeopleAreAdded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	rooms := []domain.Room{{ID: mx + "!both:x", Name: "Both"}, {ID: mx + "!one:x", Name: "One"}, {ID: mx + "!none:x", Name: "None"}}
	if err := cache.SaveRooms(ctx, mxRooms, rooms); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	member := func(room domain.RoomID, ids ...string) {
		t.Helper()
		members := make([]domain.Member, 0, len(ids))
		for _, id := range ids {
			members = append(members, domain.Member{UserID: id, DisplayName: id})
		}
		if err := cache.SaveMembers(ctx, room, members); err != nil {
			t.Fatalf("SaveMembers: %v", err)
		}
	}
	member(mx+"!both:x", "matrix:@noa:x", "matrix:@evgeny:x", "matrix:@dana:x")
	member(mx+"!one:x", "matrix:@noa:x", "matrix:@dana:x")
	member(mx+"!none:x", "matrix:@dana:x")

	got, err := cache.RoomsWith(ctx, []string{"matrix:@noa:x"}, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatalf("RoomsWith: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("RoomsWith(noa) = %+v, want both rooms they are in", got)
	}
	// A second name can only narrow it.
	got, err = cache.RoomsWith(ctx, []string{"matrix:@noa:x", "matrix:@evgeny:x"}, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatalf("RoomsWith: %v", err)
	}
	if len(got) != 1 || got[0].ID != mx+"!both:x" {
		t.Fatalf("RoomsWith(noa, evgeny) = %+v, want only the room with both", got)
	}
	// Nobody is not everybody.
	if blank, _ := cache.RoomsWith(ctx, []string{"", "  "}, domain.EveryRoom(), 10); len(blank) != 0 {
		t.Fatalf("RoomsWith(blank) = %+v, want nothing", blank)
	}
	// A room set restricts the candidates before the limit.
	got, err = cache.RoomsWith(ctx, []string{"matrix:@noa:x"}, domain.TheseRooms([]domain.RoomID{mx + "!one:x", mx + "!none:x"}), 1)
	if err != nil {
		t.Fatalf("RoomsWith(scoped): %v", err)
	}
	if len(got) != 1 || got[0].ID != mx+"!one:x" {
		t.Fatalf("RoomsWith(noa, within one/none) = %+v, want only !one:x", got)
	}
}

func TestMessagesAroundGivesAHitItsConversation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	var msgs []domain.Message
	for i := range 9 {
		msgs = append(msgs, domain.Message{
			ID: domain.EventID(mx + "$" + string(rune('a'+i))), RoomID: mx + "!a:x", Sender: "matrix:@dana:x",
			Body: string(rune('a' + i)), Timestamp: time.Unix(int64(1_700_000_000+i*60), 0),
		})
	}
	if err := cache.SaveMessages(ctx, mx+"!a:x", msgs); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}

	got, err := cache.MessagesAround(ctx, mx+"!a:x", mx+"$e", 2, 2)
	if err != nil {
		t.Fatalf("MessagesAround: %v", err)
	}
	// Two before, the hit, two after, in chronological order.
	want := []string{"c", "d", "e", "f", "g"}
	if len(got) != len(want) {
		t.Fatalf("MessagesAround() = %d messages, want %d", len(got), len(want))
	}
	for i, body := range want {
		if got[i].Body != body {
			t.Fatalf("MessagesAround() = %v, want %v in order", bodies(got), want)
		}
	}
	// At the edges it gives what there is.
	if got, _ := cache.MessagesAround(ctx, mx+"!a:x", mx+"$a", 5, 1); len(got) != 2 {
		t.Errorf("MessagesAround(first) = %v, want the message and its one neighbor", bodies(got))
	}
	// An uncached hit has no context, not an error.
	if got, _ := cache.MessagesAround(ctx, mx+"!a:x", mx+"$missing", 2, 2); len(got) != 0 {
		t.Errorf("MessagesAround(missing) = %v, want nothing", bodies(got))
	}
}

func bodies(msgs []domain.Message) []string {
	out := make([]string, 0, len(msgs))
	for i := range msgs {
		out = append(out, msgs[i].Body)
	}
	return out
}

// ReplaceDraft writes only over the draft its writer read, and says when it did not.
func TestReplaceDraftWritesOnlyOverWhatWasRead(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID(mx + "!r:x")
	first := domain.StoredDraft{RoomID: room, Body: "hi", Updated: time.UnixMilli(1000)}

	if saved, err := cache.ReplaceDraft(ctx, first, domain.StoredDraft{}); err != nil || !saved {
		t.Fatalf("ReplaceDraft over none = %v, %v; want saved", saved, err)
	}
	// Someone else writes; a replace built on the first read is refused.
	theirs := domain.StoredDraft{RoomID: room, Body: "hi there", Updated: time.UnixMilli(2000)}
	if err := putDraft(ctx, cache, theirs); err != nil {
		t.Fatal(err)
	}
	mine := domain.StoredDraft{RoomID: room, Body: "hi\n\nmore", Updated: time.UnixMilli(3000)}
	if saved, err := cache.ReplaceDraft(ctx, mine, first); err != nil || saved {
		t.Fatalf("ReplaceDraft over a stale read = %v, %v; want refused", saved, err)
	}
	if saved, _ := cache.ReplaceDraft(ctx, mine, domain.StoredDraft{}); saved {
		t.Fatal("ReplaceDraft over none saved over an existing draft")
	}
	drafts, err := cache.Drafts(ctx)
	if err != nil || len(drafts) != 1 || drafts[0].Body != "hi there" {
		t.Fatalf("Drafts() = %+v, %v; want theirs untouched", drafts, err)
	}
	// Over what is really there, it writes.
	if saved, err := cache.ReplaceDraft(ctx, mine, drafts[0]); err != nil || !saved {
		t.Fatalf("ReplaceDraft over the current draft = %v, %v; want saved", saved, err)
	}
}

// putDraft stores d over whatever the room holds.
func putDraft(ctx context.Context, c *Cache, d domain.StoredDraft) error {
	drafts, err := c.Drafts(ctx)
	if err != nil {
		return err
	}
	var over domain.StoredDraft
	for i := range drafts {
		if drafts[i].RoomID == d.RoomID {
			over = drafts[i]
		}
	}
	_, err = c.ReplaceDraft(ctx, d, over)
	return err
}

// A write over a draft read before it was deleted is refused, even a wordless one: a
// sent reply-only draft must not come back with its reply target.
func TestReplaceDraftRefusesAWriteOverADeletedDraft(t *testing.T) {
	t.Parallel()

	for _, read := range []domain.StoredDraft{
		{RoomID: mx + "!r:x", ReplyTo: mx + "$q", Updated: time.UnixMilli(5000)},
		{RoomID: mx + "!r:x", Body: "hi", Updated: time.UnixMilli(5000)},
	} {
		cache, ctx := openTemp(t), context.Background()
		if saved, err := cache.ReplaceDraft(ctx, read, domain.StoredDraft{}); err != nil || !saved {
			t.Fatalf("storing %+v = %v, %v", read, saved, err)
		}
		drafts, err := cache.Drafts(ctx)
		if err != nil || len(drafts) != 1 {
			t.Fatalf("Drafts() = %+v, %v", drafts, err)
		}
		was := drafts[0]
		if saved, err := cache.ReplaceDraft(ctx, domain.StoredDraft{RoomID: mx + "!r:x"}, was); err != nil || !saved {
			t.Fatalf("clearing = %v, %v; want saved", saved, err)
		}
		stale := was
		stale.Body, stale.Author, stale.Updated = "agent words", "agent", time.UnixMilli(6000)
		if saved, err := cache.ReplaceDraft(ctx, stale, was); err != nil || saved {
			t.Errorf("a write over %+v after it was deleted = %v, %v; want refused", was, saved, err)
		}
		if after, _ := cache.Drafts(ctx); len(after) != 0 {
			t.Errorf("drafts after the refused write = %+v, want none", after)
		}
	}
}

// Versions with the same millisecond and words are told apart by the rest of the
// composition, so a write over one is refused over the other.
func TestReplaceDraftTellsVersionsApartByTheirComposition(t *testing.T) {
	t.Parallel()
	at := time.UnixMilli(5000)
	read := domain.StoredDraft{RoomID: mx + "!r:x", Body: "fixed", Editing: mx + "$e", EditSaved: "hi", Updated: at}
	for name, change := range map[string]func(*domain.StoredDraft){
		"the draft the edit returns to": func(d *domain.StoredDraft) { d.EditSaved = "hi\n\np5" },
		"the reply target":              func(d *domain.StoredDraft) { d.ReplyTo = mx + "$q" },
		"the edit target":               func(d *domain.StoredDraft) { d.Editing = mx + "$other" },
		"the thread":                    func(d *domain.StoredDraft) { d.ThreadRoot = mx + "$t" },
	} {
		cache, ctx := openTemp(t), context.Background()
		stored := read
		change(&stored)
		if saved, err := cache.ReplaceDraft(ctx, stored, domain.StoredDraft{}); err != nil || !saved {
			t.Fatalf("%s: storing = %v, %v", name, saved, err)
		}
		mine := domain.StoredDraft{RoomID: mx + "!r:x", Body: "hi", Updated: time.UnixMilli(6000)}
		if saved, err := cache.ReplaceDraft(ctx, mine, read); err != nil || saved {
			t.Errorf("%s differs: a write over the version this writer read = %v, %v; want refused", name, saved, err)
		}
	}
}

// A draft's thread round-trips, is cleared when rewritten without one, and is deleted with it.
func TestADraftKeepsItsThread(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	inThread := domain.StoredDraft{RoomID: mx + "!r:x", Body: "on it", ThreadRoot: mx + "$root", Updated: time.UnixMilli(5000)}
	if saved, err := cache.ReplaceDraft(ctx, inThread, domain.StoredDraft{}); err != nil || !saved {
		t.Fatalf("storing = %v, %v", saved, err)
	}
	if got := onlyDraft(t, cache); got.ThreadRoot != mx+"$root" {
		t.Fatalf("read back thread %q, want $root", got.ThreadRoot)
	}
	moved := domain.StoredDraft{RoomID: mx + "!r:x", Body: "on it, in the room", Updated: time.UnixMilli(6000)}
	if saved, err := cache.ReplaceDraft(ctx, moved, inThread); err != nil || !saved {
		t.Fatalf("moving to the main timeline = %v, %v", saved, err)
	}
	if got := onlyDraft(t, cache); got.ThreadRoot != "" {
		t.Errorf("read back thread %q after the move, want none", got.ThreadRoot)
	}
	again := domain.StoredDraft{RoomID: mx + "!r:x", Body: "back", ThreadRoot: mx + "$root", Updated: time.UnixMilli(7000)}
	if saved, err := cache.ReplaceDraft(ctx, again, moved); err != nil || !saved {
		t.Fatal(saved, err)
	}
	if saved, err := cache.ReplaceDraft(ctx, domain.StoredDraft{RoomID: mx + "!r:x"}, again); err != nil || !saved {
		t.Fatalf("clearing = %v, %v", saved, err)
	}
	var left int
	if err := cache.db.QueryRowContext(ctx, "SELECT count(*) FROM draft_threads").Scan(&left); err != nil || left != 0 {
		t.Errorf("draft_threads holds %d rows after the draft was cleared (%v), want none", left, err)
	}
}

// onlyDraft is the one stored draft.
func onlyDraft(t *testing.T, cache *Cache) domain.StoredDraft {
	t.Helper()
	got, err := cache.Drafts(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("Drafts = %+v, %v; want one", got, err)
	}
	return got[0]
}

// RebaseDraft writes a window's draft over the version it read and keeps what another
// writer appended since, after the window's words, saying which words those were.
func TestRebaseDraftKeepsWhatWasAddedSinceTheRead(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID(mx + "!r:x")
	first := domain.StoredDraft{RoomID: room, Body: "hi", ReplyTo: mx + "$q", Updated: time.UnixMilli(1000)}
	if stored, added, err := cache.RebaseDraft(ctx, first, domain.StoredDraft{}, nil); err != nil || added != "" || stored.Body != "hi" {
		t.Fatalf("RebaseDraft over none = %+v, %q, %v; want it stored as written", stored, added, err)
	}
	// The assistant appends to what it read.
	theirs := first
	theirs.Body, theirs.Author, theirs.Updated = "hi\n\nthe report", "agent", time.UnixMilli(2000)
	if saved, err := cache.ReplaceDraft(ctx, theirs, first); err != nil || !saved {
		t.Fatalf("the assistant's append = %v, %v", saved, err)
	}
	mine := first
	mine.Body, mine.Updated = "hi there", time.UnixMilli(3000)
	stored, added, err := cache.RebaseDraft(ctx, mine, first, nil)
	if err != nil || added != "the report" || stored.Body != "hi there\n\nthe report" || stored.ReplyTo != mx+"$q" {
		t.Fatalf("RebaseDraft over a stale read = %+v, %q, %v; want the addition after the window's words", stored, added, err)
	}
	if got := onlyDraft(t, cache); got.Body != stored.Body || !got.Updated.Equal(stored.Updated) {
		t.Fatalf("stored %+v; want what RebaseDraft answered, %+v", got, stored)
	}
	// A write that landed with its answer lost, then more typing: its words are the
	// window's own, written once.
	lost := stored
	lost.Body, lost.Updated = "hi there you\n\nthe report", time.UnixMilli(4000)
	if _, _, err := cache.RebaseDraft(ctx, lost, stored, nil); err != nil {
		t.Fatal(err)
	}
	again := lost
	again.Body, again.Updated = "hi there you all\n\nthe report", time.UnixMilli(5000)
	if retried, added, err := cache.RebaseDraft(ctx, again, stored, []domain.StoredDraft{lost}); err != nil || added != "" ||
		retried.Body != again.Body {
		t.Fatalf("RebaseDraft after a lost answer = %+v, %q, %v; want %q once", retried, added, err, again.Body)
	}
	// Emptied (sent), the draft goes.
	if emptied, _, err := cache.RebaseDraft(ctx, domain.StoredDraft{RoomID: room}, again, nil); err != nil || !emptied.Empty() {
		t.Fatalf("an emptied draft = %+v, %v; want none stored", emptied, err)
	}
	if drafts, _ := cache.Drafts(ctx); len(drafts) != 0 {
		t.Fatalf("Drafts() after emptying = %+v, want none", drafts)
	}
}

// A window rebasing its typing and the assistant appending (compare-and-set, as
// kith-mcp does) at once, on goroutines: every word is stored exactly once.
func TestRebaseAndAppendsRacingKeepEveryWord(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID(mx + "!r:x")
	const rounds = 40
	var wg sync.WaitGroup
	wg.Go(func() {
		local, base := "", domain.StoredDraft{}
		for i := range rounds {
			local = strings.TrimSpace(local + fmt.Sprintf(" w%d", i))
			stored, added, err := cache.RebaseDraft(ctx,
				domain.StoredDraft{RoomID: room, Body: local, Updated: time.UnixMilli(int64(1000 + i))}, base, nil)
			if err != nil {
				t.Error(err)
				return
			}
			local, base = domain.JoinDraft(local, added), stored
		}
	})
	wg.Go(func() {
		for i := range rounds {
			if err := appendDraft(ctx, cache, room, fmt.Sprintf("a%d", i)); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
	count := map[string]int{}
	body := onlyDraft(t, cache).Body
	for f := range strings.FieldsSeq(body) {
		count[f]++
	}
	for i := range rounds {
		for _, word := range []string{fmt.Sprintf("w%d", i), fmt.Sprintf("a%d", i)} {
			if count[word] != 1 {
				t.Errorf("%q is stored %d times in %q", word, count[word], body)
			}
		}
	}
}

// appendDraft is kith-mcp's append: over the draft it read, read again when it moved.
func appendDraft(ctx context.Context, c *Cache, room domain.RoomID, word string) error {
	for {
		drafts, err := c.Drafts(ctx)
		if err != nil {
			return err
		}
		var over domain.StoredDraft
		if len(drafts) > 0 {
			over = drafts[0]
		}
		next := over
		next.RoomID, next.Body, next.Author = room, domain.JoinDraft(over.Body, word), "agent"
		next.Updated = time.UnixMilli(over.Updated.UnixMilli() + 1)
		if saved, err := c.ReplaceDraft(ctx, next, over); err != nil || saved {
			return err
		}
	}
}
