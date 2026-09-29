package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestDraftsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}

	written := time.Unix(1_700_000_000, 0)
	draft := domain.StoredDraft{
		RoomID: "!a:x", Body: "half a thought about Dana Levi", Caret: 7,
		Mentions: []domain.Mention{{UserID: "@dana:x", Name: "Dana Levi"}},
		ReplyTo:  "$m:x", Author: domain.DraftAgent, Updated: written,
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
	// The mentions survive, which is the half worth pinning: a pill lost in a restart
	// notifies nobody and looks exactly like one that works.
	if len(got[0].Mentions) != 1 || got[0].Mentions[0].UserID != "@dana:x" {
		t.Errorf("mentions = %+v, want Dana's", got[0].Mentions)
	}
	if got[0].Body != draft.Body || got[0].Caret != 7 || got[0].ReplyTo != "$m:x" {
		t.Errorf("Drafts() = %+v, want what was stored", got[0])
	}
	// And the provenance, which is what lets a composer say whose words these are rather
	// than presenting them as a live thought.
	if got[0].Author != domain.DraftAgent || !got[0].Updated.Equal(written) {
		t.Errorf("author/updated = %q/%v, want the agent and %v", got[0].Author, got[0].Updated, written)
	}
	if got[0].Author == "" {
		t.Error("an agent's draft reported itself as hand-written")
	}

	// Replaced wholesale rather than appended: a draft is the current state of a
	// composer, not a history of one.
	draft.Body, draft.Author = "second thoughts", ""
	if err := putDraft(ctx, cache, draft); err != nil {
		t.Fatalf("putDraft(again): %v", err)
	}
	got, _ = cache.Drafts(ctx)
	if len(got) != 1 || got[0].Body != "second thoughts" || got[0].Author != "" {
		t.Fatalf("Drafts() = %+v, want one row, rewritten and now hand-written", got)
	}

	// An empty draft is a delete, because an empty composer is a legitimate answer to
	// "what does this room hold".
	if err := putDraft(ctx, cache, domain.StoredDraft{RoomID: "!a:x"}); err != nil {
		t.Fatalf("putDraft(empty): %v", err)
	}
	if got, _ := cache.Drafts(ctx); len(got) != 0 {
		t.Fatalf("Drafts() = %+v, want none after an empty save", got)
	}
}

// A draft belongs to its room and goes with it: leaving a room in another client should
// not leave words behind pointing at a room this account is no longer in.
func TestDraftsGoWithTheirRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	if err := putDraft(ctx, cache, domain.StoredDraft{RoomID: "!a:x", Body: "words"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := cache.db.ExecContext(ctx, `DELETE FROM rooms WHERE id = '!a:x'`); err != nil {
		t.Fatalf("delete room: %v", err)
	}
	if got, _ := cache.Drafts(ctx); len(got) != 0 {
		t.Fatalf("Drafts() = %+v, want the draft to have gone with its room", got)
	}
}

// The two reads the agent integration turns on: the room a set of people share, and the
// conversation around a search hit.
func TestRoomsWithNarrowsAsPeopleAreAdded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	rooms := []domain.Room{{ID: "!both:x", Name: "Both"}, {ID: "!one:x", Name: "One"}, {ID: "!none:x", Name: "None"}}
	if err := cache.SaveRooms(ctx, rooms); err != nil {
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
	member("!both:x", "@noa:x", "@evgeny:x", "@dana:x")
	member("!one:x", "@noa:x", "@dana:x")
	member("!none:x", "@dana:x")

	// One name is a wide question.
	got, err := cache.RoomsWith(ctx, []string{"@noa:x"}, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatalf("RoomsWith: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("RoomsWith(noa) = %+v, want both rooms they are in", got)
	}
	// A second name can only narrow it, which is the property that makes this useful:
	// "the room with Noa and Evgeny" is one room, not a list to disambiguate.
	got, err = cache.RoomsWith(ctx, []string{"@noa:x", "@evgeny:x"}, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatalf("RoomsWith: %v", err)
	}
	if len(got) != 1 || got[0].ID != "!both:x" {
		t.Fatalf("RoomsWith(noa, evgeny) = %+v, want only the room with both", got)
	}
	// Nobody is not everybody.
	if blank, _ := cache.RoomsWith(ctx, []string{"", "  "}, domain.EveryRoom(), 10); len(blank) != 0 {
		t.Fatalf("RoomsWith(blank) = %+v, want nothing", blank)
	}
	// A room set restricts the candidates *before* the limit, so a scoped caller asking
	// for one room gets the one it may show rather than the one ranked first overall.
	got, err = cache.RoomsWith(ctx, []string{"@noa:x"}, domain.TheseRooms([]domain.RoomID{"!one:x", "!none:x"}), 1)
	if err != nil {
		t.Fatalf("RoomsWith(scoped): %v", err)
	}
	if len(got) != 1 || got[0].ID != "!one:x" {
		t.Fatalf("RoomsWith(noa, within one/none) = %+v, want only !one:x", got)
	}
}

func TestMessagesAroundGivesAHitItsConversation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	var msgs []domain.Message
	for i := range 9 {
		msgs = append(msgs, domain.Message{
			ID: domain.EventID("$" + string(rune('a'+i))), RoomID: "!a:x", Sender: "@dana:x",
			Body: string(rune('a' + i)), Timestamp: time.Unix(int64(1_700_000_000+i*60), 0),
		})
	}
	if err := cache.SaveMessages(ctx, "!a:x", msgs); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}

	got, err := cache.MessagesAround(ctx, "!a:x", "$e", 2, 2)
	if err != nil {
		t.Fatalf("MessagesAround: %v", err)
	}
	// Two before, the hit, two after — in the order it was said, which is the whole
	// point: a snippet says what was mentioned and this says what was asked.
	want := []string{"c", "d", "e", "f", "g"}
	if len(got) != len(want) {
		t.Fatalf("MessagesAround() = %d messages, want %d", len(got), len(want))
	}
	for i, body := range want {
		if got[i].Body != body {
			t.Fatalf("MessagesAround() = %v, want %v in order", bodies(got), want)
		}
	}
	// At the edges it gives what there is rather than failing.
	if got, _ := cache.MessagesAround(ctx, "!a:x", "$a", 5, 1); len(got) != 2 {
		t.Errorf("MessagesAround(first) = %v, want the message and its one neighbor", bodies(got))
	}
	// A hit whose message is no longer cached has no context, which is an answer.
	if got, _ := cache.MessagesAround(ctx, "!a:x", "$missing", 2, 2); len(got) != 0 {
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
	const room = domain.RoomID("!r:x")
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

// putDraft stores d over whatever the room holds, as a writer that just read it does.
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

// A write over a draft read before it was deleted (sent, or cleared) is refused, even
// when what was read had no words: a reply-only draft read by kith-mcp, then sent from
// the TUI, must not come back with its reply target when kith-mcp's append lands.
func TestReplaceDraftRefusesAWriteOverADeletedDraft(t *testing.T) {
	t.Parallel()

	for _, read := range []domain.StoredDraft{
		{RoomID: "!r:x", ReplyTo: "$q", Updated: time.UnixMilli(5000)},
		{RoomID: "!r:x", Body: "hi", Updated: time.UnixMilli(5000)},
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
		if saved, err := cache.ReplaceDraft(ctx, domain.StoredDraft{RoomID: "!r:x"}, was); err != nil || !saved {
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

// Two writers can stamp the same millisecond with the same words (a correction both
// hold): the rest of the composition tells the versions apart, so a write over one
// is refused over the other.
func TestReplaceDraftTellsVersionsApartByTheirComposition(t *testing.T) {
	t.Parallel()
	at := time.UnixMilli(5000)
	read := domain.StoredDraft{RoomID: "!r:x", Body: "fixed", Editing: "$e", EditSaved: "hi", Updated: at}
	for name, change := range map[string]func(*domain.StoredDraft){
		"the draft the edit returns to": func(d *domain.StoredDraft) { d.EditSaved = "hi\n\np5" },
		"the reply target":              func(d *domain.StoredDraft) { d.ReplyTo = "$q" },
		"the edit target":               func(d *domain.StoredDraft) { d.Editing = "$other" },
	} {
		cache, ctx := openTemp(t), context.Background()
		stored := read
		change(&stored)
		if saved, err := cache.ReplaceDraft(ctx, stored, domain.StoredDraft{}); err != nil || !saved {
			t.Fatalf("%s: storing = %v, %v", name, saved, err)
		}
		mine := domain.StoredDraft{RoomID: "!r:x", Body: "hi", Updated: time.UnixMilli(6000)}
		if saved, err := cache.ReplaceDraft(ctx, mine, read); err != nil || saved {
			t.Errorf("%s differs: a write over the version this writer read = %v, %v; want refused", name, saved, err)
		}
	}
}
