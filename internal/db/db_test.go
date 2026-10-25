package db

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

func openTemp(t *testing.T) *Cache {
	t.Helper()
	cache, err := Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func mustSave(t *testing.T, cache *Cache, room domain.RoomID, msg domain.Message) {
	t.Helper()
	if err := cache.SaveMessages(context.Background(), room, []domain.Message{msg}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
}

func TestRoomsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	// Cold cache is empty, not an error.
	if got, err := cache.Rooms(ctx); err != nil || len(got) != 0 {
		t.Fatalf("cold Rooms() = %v, %v; want empty", got, err)
	}

	rooms := []domain.Room{
		{ID: mx + "!b:x", Name: "Bravo", Members: []string{"Bianca Reyes", "Cyrus Vale"}},
		{ID: mx + "!a:x", Name: "Alpha", IsDirect: true},
	}
	if err := cache.SaveRooms(ctx, mxRooms, rooms); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}

	got, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms() error = %v", err)
	}
	if len(got) != 2 || got[0].Name != "Alpha" || !got[0].IsDirect {
		t.Fatalf("Rooms() = %+v, want sorted with Alpha (direct) first", got)
	}
	// Members round-trip through the JSON column; none stays nil.
	if len(got[0].Members) != 0 {
		t.Errorf("Alpha members = %+v, want none", got[0].Members)
	}
	if len(got[1].Members) != 2 || got[1].Members[0] != "Bianca Reyes" {
		t.Errorf("Bravo members = %+v, want [Bianca Reyes, Cyrus Vale]", got[1].Members)
	}

	// SaveRooms replaces, not appends.
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!c:x", Name: "Charlie"}}); err != nil {
		t.Fatalf("second SaveRooms() error = %v", err)
	}
	got, _ = cache.Rooms(ctx)
	if len(got) != 1 || got[0].Name != "Charlie" {
		t.Fatalf("after replace Rooms() = %+v, want [Charlie]", got)
	}
}

func TestSpacesRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	spaces := []domain.Space{
		{ID: mx + "!work:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x", mx + "!b:x"}},
		{ID: mx + "!fun:x", Name: "Friends"},
	}
	if err := cache.SaveSpaces(ctx, mxRooms, spaces); err != nil {
		t.Fatalf("SaveSpaces() error = %v", err)
	}

	got, err := cache.Spaces(ctx)
	if err != nil {
		t.Fatalf("Spaces() error = %v", err)
	}
	if len(got) != 2 || got[0].Name != "Friends" {
		t.Fatalf("Spaces() = %+v, want sorted with Friends first", got)
	}
	// Work's children come back in insertion order.
	var work domain.Space
	for _, s := range got {
		if s.Name == "Work" {
			work = s
		}
	}
	if len(work.Children) != 2 || work.Children[0] != mx+"!a:x" || work.Children[1] != mx+"!b:x" {
		t.Fatalf("Work children = %v, want [!a:x !b:x]", work.Children)
	}
}

func TestReopenPreservesDataAndSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if serr := first.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); serr != nil {
		t.Fatalf("SaveRooms() error = %v", serr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}

	// Reopening re-runs migrate (a no-op); the data survives.
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen Open() error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	got, err := second.Rooms(ctx)
	if err != nil || len(got) != 1 || got[0].Name != "Alpha" {
		t.Fatalf("after reopen Rooms() = %+v, %v; want [Alpha]", got, err)
	}
}

func TestMessagesRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }
	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@b:x", Body: "second", Timestamp: at(2)},
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "first", Timestamp: at(1)},
	}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}

	got, err := cache.Messages(ctx, mx+"!a:x", 10)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(got) != 2 || got[0].Body != "first" || got[1].Body != "second" {
		t.Fatalf("Messages() = %+v, want oldest-first [first second]", got)
	}
	if !got[0].Timestamp.Equal(at(1)) {
		t.Errorf("timestamp not preserved: %v", got[0].Timestamp)
	}

	// A later upsert accumulates and updates in place, scoped to the room.
	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
		{ID: mx + "$3", RoomID: mx + "!a:x", Body: "third", Timestamp: at(3)},
	}); err != nil {
		t.Fatalf("second SaveMessages() error = %v", err)
	}
	got, _ = cache.Messages(ctx, mx+"!a:x", 10)
	if len(got) != 3 || got[2].Body != "third" {
		t.Fatalf("after append Messages() = %+v, want 3 with third last", got)
	}
	if other, _ := cache.Messages(ctx, mx+"!other:x", 10); len(other) != 0 {
		t.Errorf("messages leaked across rooms: %+v", other)
	}
}

func TestMarkRedacted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "secret", Timestamp: at(1)},
	}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
	if err := cache.MarkRedacted(ctx, mx+"!a:x", mx+"$1", "matrix:@mod:x", "spam", time.Time{}, false); err != nil {
		t.Fatalf("MarkRedacted() error = %v", err)
	}

	got, _ := cache.Messages(ctx, mx+"!a:x", 10)
	// The row survives for the placeholder but the words do not:
	// [display.deleted] keep is off here.
	if len(got) != 1 || !got[0].Redacted {
		t.Fatalf("message not marked redacted: %+v", got)
	}
	if got[0].Sender != "matrix:@a:x" || !got[0].Timestamp.Equal(at(1)) {
		t.Errorf("MarkRedacted clobbered other columns: %+v", got[0])
	}
	if got[0].Body != "" {
		t.Errorf("the deleted words are still on disk: %q", got[0].Body)
	}
	if got[0].RedactedBy != "matrix:@mod:x" || got[0].RedactedReason != "spam" {
		t.Errorf("who deleted it and why was not recorded: %+v", got[0])
	}

	// A later re-save must not clear the redaction.
	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "secret", Timestamp: at(1)},
	}); err != nil {
		t.Fatalf("re-SaveMessages() error = %v", err)
	}
	if got, _ = cache.Messages(ctx, mx+"!a:x", 10); !got[0].Redacted {
		t.Error("re-save cleared the redaction flag; max() should preserve it")
	}
}

func TestEditsPersist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	orig := func(room domain.RoomID, id domain.EventID) domain.Message {
		return domain.Message{ID: id, RoomID: room, Sender: "matrix:@a:x", Body: "old", Timestamp: at(5)}
	}
	edit := func(room domain.RoomID, id domain.EventID) domain.Message {
		return domain.Message{ID: id, RoomID: room, Sender: "matrix:@a:x", Body: "new", Timestamp: at(9), Edited: true}
	}

	// Original then edit: replacement body, edited flag, and the original's
	// timestamp, so the row keeps its position.
	mustSave(t, cache, mx+"!a:x", orig(mx+"!a:x", mx+"$1"))
	mustSave(t, cache, mx+"!a:x", edit(mx+"!a:x", mx+"$1"))
	got, _ := cache.Messages(ctx, mx+"!a:x", 10)
	if len(got) != 1 || got[0].Body != "new" || !got[0].Edited || !got[0].Timestamp.Equal(at(5)) {
		t.Fatalf("edit not applied in place: %+v", got)
	}
	// A later re-save of the original must not revert the edit.
	mustSave(t, cache, mx+"!a:x", orig(mx+"!a:x", mx+"$1"))
	if got, _ = cache.Messages(ctx, mx+"!a:x", 10); got[0].Body != "new" || !got[0].Edited {
		t.Errorf("re-save reverted the edit: %+v", got[0])
	}

	// Edit cached before the original gives the same result.
	mustSave(t, cache, mx+"!b:x", edit(mx+"!b:x", mx+"$2"))
	mustSave(t, cache, mx+"!b:x", orig(mx+"!b:x", mx+"$2"))
	if got, _ = cache.Messages(ctx, mx+"!b:x", 10); got[0].Body != "new" || !got[0].Edited || !got[0].Timestamp.Equal(at(5)) {
		t.Errorf("reverse-order edit wrong: %+v", got[0])
	}
}

// Relation and flag columns persist and survive a later re-save that lacks them.
func TestStickyFieldsSurviveABareResave(t *testing.T) {
	t.Parallel()
	at := time.UnixMilli(2000)
	bare := domain.Message{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@b:x", Body: "re", Timestamp: at}
	for _, tc := range []struct {
		name string
		set  func(*domain.Message)
		has  func(domain.Message) bool
	}{
		{"reply_to", func(m *domain.Message) { m.ReplyTo = mx + "$1" }, func(m domain.Message) bool { return m.ReplyTo == mx+"$1" }},
		{"mentioned", func(m *domain.Message) { m.Mentioned = true }, func(m domain.Message) bool { return m.Mentioned }},
		{"emote", func(m *domain.Message) { m.Emote = true }, func(m domain.Message) bool { return m.Emote }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cache := openTemp(t)
			full := bare
			tc.set(&full)
			mustSave(t, cache, mx+"!a:x", full)
			got, _ := cache.Messages(context.Background(), mx+"!a:x", 10)
			if len(got) != 1 || !tc.has(got[0]) {
				t.Fatalf("not persisted: %+v", got)
			}
			mustSave(t, cache, mx+"!a:x", bare)
			if got, _ = cache.Messages(context.Background(), mx+"!a:x", 10); !tc.has(got[0]) {
				t.Errorf("bare re-save cleared it: %+v", got[0])
			}
		})
	}
}

func TestMediaPersists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	mustSave(t, cache, mx+"!a:x", domain.Message{
		ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "cat.jpg", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.jpg", Mime: "image/jpeg", Width: 800, Height: 600, Size: 123},
	})
	got, _ := cache.Messages(ctx, mx+"!a:x", 10)
	if len(got) != 1 || got[0].Media == nil {
		t.Fatalf("media metadata not persisted: %+v", got)
	}
	if got[0].Media.Type != domain.MediaImage || got[0].Media.Name != "cat.jpg" || got[0].Media.Width != 800 || got[0].Media.Size != 123 {
		t.Errorf("media = %+v", got[0].Media)
	}

	// A non-media message round-trips with a nil Media.
	mustSave(t, cache, mx+"!a:x", domain.Message{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "hi", Timestamp: at(2)})
	if got, _ = cache.Messages(ctx, mx+"!a:x", 10); got[1].Media != nil {
		t.Errorf("non-media message got a Media: %+v", got[1].Media)
	}
}

// Each scope counts only its own uses; scopes are not weights.
func TestEmojiScopesAreStrictNotWeighted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	rec := func(room domain.RoomID, e string, n int, ts int64) {
		for range n {
			if err := cache.RecordEmoji(ctx, domain.EmojiComposed, room, e, ts); err != nil {
				t.Fatalf("RecordEmoji() error = %v", err)
			}
		}
	}
	const here = domain.RoomID(mx + "!a:x")
	sibling := domain.RoomID(mx + "!b:x") // same space
	far := domain.RoomID(mx + "!c:x")     // no relation
	space := []domain.RoomID{here, sibling}

	rec(here, "🔥", 1, 10)     // once, but right here
	rec(sibling, "❤️", 2, 20) // twice, next door
	rec(far, "👍", 9, 30)      // nine times, far away

	scores := func(scope string) map[string]int {
		got, err := cache.EmojiScores(ctx, domain.EmojiComposed, here, space, nil, scope)
		if err != nil {
			t.Fatalf("EmojiScores(%q) error = %v", scope, err)
		}
		return got
	}

	// "room": here, then nearby, then everywhere — 🔥 wins on one use.
	room := scores("room")
	for emoji, want := range map[string]int{
		"🔥":  roomWeight + spaceWeight + 1, // all three terms
		"❤️": 2 * (spaceWeight + 1),        // space and global
		"👍":  9,                            // global only
	} {
		if room[emoji] != want {
			t.Errorf("room scope: %s scored %d, want %d", emoji, room[emoji], want)
		}
	}
	if room["🔥"] <= room["❤️"] {
		t.Errorf("room scope should lead with this room's 🔥: %v", room)
	}

	// "space": no room term, so ❤️ (twice in the space) overtakes 🔥 (once).
	sp := scores("space")
	if sp["🔥"] != spaceWeight+1 {
		t.Errorf("space scope: 🔥 scored %d, want %d — the room term should be gone", sp["🔥"], spaceWeight+1)
	}
	if sp["❤️"] <= sp["🔥"] {
		t.Errorf("space scope should lead with ❤️, used twice in the space: %v", sp)
	}

	// "global": a plain count; the nine far-away uses win.
	g := scores("global")
	for emoji, want := range map[string]int{"🔥": 1, "❤️": 2, "👍": 9} {
		if g[emoji] != want {
			t.Errorf("global scope: %s scored %d, want %d — a plain count", emoji, g[emoji], want)
		}
	}
	if g["👍"] <= g["❤️"] || g["❤️"] <= g["🔥"] {
		t.Errorf("global ranking = %v, want 👍 > ❤️ > 🔥", g)
	}

	// An unrecognized scope reads as "room", so a config typo does not flatten the ranking.
	if typo := scores("evrywhere"); typo["🔥"] != room["🔥"] {
		t.Errorf("an unknown scope scored 🔥 %d, want the room scope's %d", typo["🔥"], room["🔥"])
	}
}

func TestMediaSourceRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	// The source hangs off the cached message and cascades with it, so the message
	// is saved first.
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{{ID: mx + "$1", Body: "look"}}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}

	if err := cache.SaveMediaSource(ctx, mx+"$1", mx+"!a:x", "mxc://x/abc", `{"key":"v"}`); err != nil {
		t.Fatalf("SaveMediaSource() error = %v", err)
	}
	mxc, fileJSON, ok, err := cache.MediaSource(ctx, mx+"!a:x", mx+"$1")
	if err != nil || !ok {
		t.Fatalf("MediaSource() ok=%v err=%v", ok, err)
	}
	if mxc != "mxc://x/abc" || fileJSON != `{"key":"v"}` {
		t.Errorf("MediaSource() = %q, %q", mxc, fileJSON)
	}

	// Upsert replaces; unknown event is a clean not-found.
	if err := cache.SaveMediaSource(ctx, mx+"$1", mx+"!a:x", "mxc://x/def", ""); err != nil {
		t.Fatalf("re-SaveMediaSource() error = %v", err)
	}
	if mxc, _, _, _ := cache.MediaSource(ctx, mx+"!a:x", mx+"$1"); mxc != "mxc://x/def" {
		t.Errorf("upsert did not replace mxc: %q", mxc)
	}
	if _, _, ok, _ := cache.MediaSource(ctx, mx+"!a:x", mx+"$none"); ok {
		t.Error("unknown event should report not-found")
	}

	// A message the cache does not hold has nothing to attach a source to: no error,
	// nothing kept.
	if err := cache.SaveMediaSource(ctx, mx+"$trimmed", mx+"!a:x", "", `{"key":"w"}`); err != nil {
		t.Errorf("SaveMediaSource for an uncached message = %v, want nothing done", err)
	}
	if _, _, ok, _ := cache.MediaSource(ctx, mx+"!a:x", mx+"$trimmed"); ok {
		t.Error("a source was kept for a message the cache does not hold")
	}
}

func TestReactionsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveReactions(ctx, []domain.Reaction{
		{ID: mx + "$r1", RoomID: mx + "!a:x", Target: mx + "$m", Sender: "matrix:@a:x", Key: "👍"},
		{ID: mx + "$r2", RoomID: mx + "!a:x", Target: mx + "$m", Sender: "matrix:@b:x", Key: "❤️"},
		{ID: mx + "$r3", RoomID: mx + "!b:x", Target: mx + "$n", Sender: "matrix:@a:x", Key: "🎉"},
	}); err != nil {
		t.Fatalf("SaveReactions() error = %v", err)
	}

	got, err := cache.Reactions(ctx, mx+"!a:x")
	if err != nil {
		t.Fatalf("Reactions() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Reactions(!a:x) = %+v, want 2 (scoped to room)", got)
	}

	// DeleteReaction returns the removed row and the reaction disappears.
	del, ok, err := cache.DeleteReaction(ctx, mx+"$r1")
	if err != nil || !ok {
		t.Fatalf("DeleteReaction($r1) ok=%v err=%v", ok, err)
	}
	if del.Target != mx+"$m" || del.RoomID != mx+"!a:x" || del.Key != "👍" {
		t.Errorf("deleted row = %+v, want target $m room !a:x key 👍", del)
	}
	if got, _ = cache.Reactions(ctx, mx+"!a:x"); len(got) != 1 || got[0].ID != mx+"$r2" {
		t.Errorf("after delete = %+v, want only $r2", got)
	}

	// An unknown id is not-found: that tells a message redaction from an un-react.
	if _, ok, _ := cache.DeleteReaction(ctx, mx+"$nope"); ok {
		t.Error("DeleteReaction reported ok for an unknown id")
	}
}

func TestMessagesTrimToLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	cache.UseKeep(func(domain.RoomID) int { return testKeep })

	// Write more than the per-room cap; only the newest survive.
	msgs := make([]domain.Message, testKeep+20)
	for i := range msgs {
		msgs[i] = domain.Message{
			ID:        domain.EventID(fmt.Sprintf(mx+"$%04d", i)),
			RoomID:    mx + "!a:x",
			Body:      "m",
			Timestamp: time.UnixMilli(int64(i) * 1000),
		}
	}
	if err := cache.SaveMessages(ctx, mx+"!a:x", msgs); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
	got, _ := cache.Messages(ctx, mx+"!a:x", testKeep+100)
	if len(got) != testKeep {
		t.Fatalf("cached count = %d, want trimmed to %d", len(got), testKeep)
	}
	// The oldest kept message is the 20th.
	if got[0].ID != domain.EventID(fmt.Sprintf(mx+"$%04d", 20)) {
		t.Errorf("oldest kept = %q, want the 20th message", got[0].ID)
	}
}

func TestClearEmptiesEverything(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	if err := cache.SaveSpaces(ctx, mxRooms, []domain.Space{{ID: mx + "!s:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x"}}}); err != nil {
		t.Fatalf("SaveSpaces() error = %v", err)
	}
	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{{ID: mx + "$1", RoomID: mx + "!a:x", Body: "hi"}}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: mx + "!a:x", Notifications: 3}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	if err := cache.SaveMembers(ctx, mx+"!a:x", []domain.Member{{UserID: "matrix:@a:x", DisplayName: "A"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.RecordMention(ctx, mx+"!a:x", "matrix:@a:x", 1); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveInvites(ctx, mxRooms, []domain.Room{{ID: mx + "!i1:x", Name: "One"}}); err != nil {
		t.Fatal(err)
	}

	if err := cache.Clear(ctx); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	if rooms, _ := cache.Rooms(ctx); len(rooms) != 0 {
		t.Errorf("rooms after clear = %d, want 0", len(rooms))
	}
	if spaces, _ := cache.Spaces(ctx); len(spaces) != 0 {
		t.Errorf("spaces after clear = %d, want 0", len(spaces))
	}
	if msgs, _ := cache.Messages(ctx, mx+"!a:x", 10); len(msgs) != 0 {
		t.Errorf("messages after clear = %d, want 0", len(msgs))
	}
	if unread, _ := cache.Unread(ctx); len(unread) != 0 {
		t.Errorf("unread after clear = %d, want 0", len(unread))
	}
	if members, _ := cache.Members(ctx, mx+"!a:x", 0); len(members) != 0 {
		t.Errorf("members after clear = %d, want 0", len(members))
	}
	if mentions, _ := cache.FrequentMentions(ctx, mx+"!a:x", 10); len(mentions) != 0 {
		t.Errorf("mention records after clear = %d, want 0", len(mentions))
	}
	if invites, _ := cache.Invites(ctx); len(invites) != 0 {
		t.Errorf("invites after clear = %d, want 0", len(invites))
	}
}

func TestUnreadRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	// Cold cache is empty, not an error.
	if got, err := cache.Unread(ctx); err != nil || len(got) != 0 {
		t.Fatalf("cold Unread() = %v, %v; want empty", got, err)
	}

	want := domain.Unread{RoomID: mx + "!a:x", Notifications: 5, Highlights: 2, ReadEvent: mx + "$e1"}
	if err := cache.SaveUnread(ctx, want, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	got, err := cache.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread() error = %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("Unread() = %+v, want [%+v]", got, want)
	}

	// A second save for the same room upserts.
	next := domain.Unread{RoomID: mx + "!a:x", Notifications: 0, Highlights: 0, ReadEvent: mx + "$e2"}
	if err := cache.SaveUnread(ctx, next, 0); err != nil {
		t.Fatalf("second SaveUnread() error = %v", err)
	}
	got, _ = cache.Unread(ctx)
	if len(got) != 1 || !reflect.DeepEqual(got[0], next) {
		t.Fatalf("after upsert Unread() = %+v, want [%+v]", got, next)
	}
}

// A deleted message's words are erased from the file unless keep is set.
func TestRedactionKeepsTheWordsOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	for _, keep := range []bool{false, true} {
		ctx := t.Context()
		cache := openTemp(t)
		if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
			{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "secret", Format: richtext.FromMarkup("<strong>secret</strong>"),
				Timestamp: time.Unix(1, 0)},
		}); err != nil {
			t.Fatalf("SaveMessages: %v", err)
		}
		if err := cache.MarkRedacted(ctx, mx+"!a:x", mx+"$1", "matrix:@a:x", "", time.Time{}, keep); err != nil {
			t.Fatalf("MarkRedacted: %v", err)
		}
		// A page already in flight arriving afterwards must not restore the stripped words.
		if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
			{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "secret", Format: richtext.FromMarkup("<strong>secret</strong>"),
				Timestamp: time.Unix(1, 0)},
		}); err != nil {
			t.Fatalf("re-save: %v", err)
		}
		got, err := cache.Messages(ctx, mx+"!a:x", 10)
		if err != nil || len(got) != 1 {
			t.Fatalf("Messages = %v, %v", got, err)
		}
		switch {
		case keep && got[0].Body != "secret":
			t.Errorf("keep = true lost the words anyway: %q", got[0].Body)
		case !keep && got[0].Body != "":
			t.Errorf("keep = false left the words on disk: %q", got[0].Body)
		case !keep && got[0].Format.Markup() != "":
			t.Errorf("keep = false left the formatting on disk: %q", got[0].Format.Markup())
		}
	}
}

// Every version of a message is kept beside the drawn one.
func TestRevisionsKeepEveryVersionInOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	original := domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "seven", Timestamp: at(1)}
	firstEdit := domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "seven thirty",
		Format: richtext.FromMarkup("seven <b>thirty</b>"), RevisionID: mx + "$e1", Timestamp: at(2)}
	secondEdit := domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "seven thirty, usual place",
		RevisionID: mx + "$e2", Timestamp: at(3)}

	for _, m := range []domain.Message{original, firstEdit, secondEdit} {
		if err := cache.SaveMessagesWithRevisions(ctx, mx+"!a:x", []domain.Message{m}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := cache.Revisions(ctx, mx+"!a:x", mx+"$m")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d versions, want 3: %+v", len(got), got)
	}
	// Oldest first.
	for i, want := range []string{"seven", "seven thirty", "seven thirty, usual place"} {
		if got[i].Body != want {
			t.Errorf("version %d = %q, want %q", i+1, got[i].Body, want)
		}
	}
	// The first version is keyed by the message's ID, the rest by their edit's ID,
	// so a re-delivered edit is one row.
	if got[0].ID != mx+"$m" || got[1].ID != mx+"$e1" || got[2].ID != mx+"$e2" {
		t.Errorf("ids = %q, %q, %q", got[0].ID, got[1].ID, got[2].ID)
	}
	if got[1].Format.Markup() == "" {
		t.Error("a version's formatting was not kept with its words")
	}
	// A repeated edit (re-page, resync) is one version.
	if err := cache.SaveMessagesWithRevisions(ctx, mx+"!a:x", []domain.Message{firstEdit, firstEdit}); err != nil {
		t.Fatal(err)
	}
	if again, _ := cache.Revisions(ctx, mx+"!a:x", mx+"$m"); len(again) != 3 {
		t.Errorf("%d versions after re-saving one, want 3", len(again))
	}
}

// Erasing a deleted message's words also erases its versions.
func TestErasingADeletionTakesItsVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	msg := domain.Message{ID: mx + "$m", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "first", Timestamp: at(1)}
	if err := cache.SaveMessagesWithRevisions(ctx, mx+"!a:x", []domain.Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := cache.MarkRedacted(ctx, mx+"!a:x", mx+"$m", "matrix:@her:x", "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := cache.Revisions(ctx, mx+"!a:x", mx+"$m"); len(got) != 0 {
		t.Errorf("%d versions survived an erase: %+v", len(got), got)
	}

	// With keeping on, deletion leaves the versions.
	kept := domain.Message{ID: mx + "$k", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: "held", Timestamp: at(2)}
	if err := cache.SaveMessagesWithRevisions(ctx, mx+"!a:x", []domain.Message{kept}); err != nil {
		t.Fatal(err)
	}
	if err := cache.MarkRedacted(ctx, mx+"!a:x", mx+"$k", "matrix:@her:x", "", time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := cache.Revisions(ctx, mx+"!a:x", mx+"$k"); len(got) != 1 {
		t.Errorf("%d versions after a kept deletion, want 1", len(got))
	}
}

// A trim removes others' reactions to trimmed messages. It keeps reactions from
// any of our IDs (they rank reaction emoji), to kept messages, and to targets never
// cached (they may be paged in later).
func TestTrimTakesOthersReactionsToTrimmedMessages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	cache.UseKeep(func(domain.RoomID) int { return testKeep })
	native := domain.NativePerson(domain.ProtocolWhatsApp, "447700900001@s.whatsapp.net")
	cache.UseSelves(func() []string { return []string{"", "matrix:@me:x", "matrix:@whatsapp_447700900001:x", native} })

	// At the cap nothing is trimmed until one more arrives.
	msgs := make([]domain.Message, testKeep)
	for i := range msgs {
		msgs[i] = domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$%04d", i)), RoomID: mx + "!a:x", Body: "m",
			Timestamp: time.UnixMilli(int64(i) * 1000)}
	}
	oldest, newest := msgs[0].ID, msgs[len(msgs)-1].ID
	if err := cache.SaveMessages(ctx, mx+"!a:x", msgs); err != nil {
		t.Fatal(err)
	}
	reactions := []domain.Reaction{
		{ID: mx + "$theirs-old", RoomID: mx + "!a:x", Target: oldest, Sender: "matrix:@bob:x", Key: "👍"},
		{ID: mx + "$mine-old", RoomID: mx + "!a:x", Target: oldest, Sender: "matrix:@me:x", Key: "🎉"},
		{ID: mx + "$bridged-old", RoomID: mx + "!a:x", Target: oldest, Sender: "matrix:@whatsapp_447700900001:x", Key: "🎉"},
		{ID: mx + "$native-old", RoomID: mx + "!a:x", Target: oldest, Sender: native, Key: "🎉"},
		{ID: mx + "$theirs-new", RoomID: mx + "!a:x", Target: newest, Sender: "matrix:@bob:x", Key: "👍"},
		{ID: mx + "$theirs-uncached", RoomID: mx + "!a:x", Target: mx + "$never-seen", Sender: "matrix:@bob:x", Key: "👍"},
	}
	if err := cache.SaveReactions(ctx, reactions); err != nil {
		t.Fatal(err)
	}
	// One more pushes the oldest out.
	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{{ID: mx + "$next", RoomID: mx + "!a:x", Body: "m",
		Timestamp: time.UnixMilli(int64(len(msgs)) * 1000)}}); err != nil {
		t.Fatal(err)
	}

	got, err := cache.Reactions(ctx, mx+"!a:x")
	if err != nil {
		t.Fatal(err)
	}
	kept := map[domain.EventID]bool{}
	for _, r := range got {
		kept[r.ID] = true
	}
	for id, want := range map[domain.EventID]bool{
		mx + "$theirs-old": false, mx + "$mine-old": true, mx + "$bridged-old": true, mx + "$native-old": true,
		mx + "$theirs-new": true, mx + "$theirs-uncached": true,
	} {
		if kept[id] != want {
			t.Errorf("%s kept = %v, want %v", id, kept[id], want)
		}
	}
}

// A trim with no known selves keeps every reaction rather than take ours.
func TestTrimWithoutSelvesKeepsEveryReaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	cache.UseKeep(func(domain.RoomID) int { return testKeep })
	cache.UseSelves(func() []string { return []string{""} })
	msgs := make([]domain.Message, testKeep+1)
	for i := range msgs {
		msgs[i] = domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$%04d", i)), RoomID: mx + "!a:x", Body: "m",
			Timestamp: time.UnixMilli(int64(i) * 1000)}
	}
	if err := cache.SaveMessages(ctx, mx+"!a:x", msgs[:testKeep]); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveReactions(ctx, []domain.Reaction{
		{ID: mx + "$r", RoomID: mx + "!a:x", Target: msgs[0].ID, Sender: "matrix:@me:x", Key: "🎉"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMessages(ctx, mx+"!a:x", msgs[testKeep:]); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.Reactions(ctx, mx+"!a:x"); err != nil || len(got) != 1 {
		t.Errorf("reactions = (%v, %v), want the one kept", got, err)
	}
}

// A read that fails is an error, not "not cached".
func TestAFailedLookupIsNotNotCached(t *testing.T) {
	t.Parallel()
	cache := openTemp(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cache.MessagesAround(canceled, mx+"!a:x", mx+"$m", 2, 2); err == nil {
		t.Error("MessagesAround on a canceled context reported the message as not cached")
	}
}

// An edit's formatting is order-independent like its words: the edit replaces
// it (plain removes it), and a re-arriving original never restores it.
func TestEditsCarryTheirFormatting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }
	orig := domain.Message{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "old", Format: richtext.FromMarkup("<b>old</b>"), Timestamp: at(5)}
	formatted := domain.Message{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "new", Format: richtext.FromMarkup("<i>new</i>"), Timestamp: at(9), Edited: true}
	plain := domain.Message{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "plain now", Timestamp: at(12), Edited: true}

	steps := []struct {
		name string
		save domain.Message
		body string
		html string
	}{
		{"the original", orig, "old", "<b>old</b>"},
		{"a formatted edit", formatted, "new", "<i>new</i>"},
		{"the original again", orig, "new", "<i>new</i>"},
		{"a plain edit", plain, "plain now", ""},
		{"the original once more", orig, "plain now", ""},
	}
	for _, s := range steps {
		mustSave(t, cache, mx+"!a:x", s.save)
		got, err := cache.Messages(ctx, mx+"!a:x", 10)
		if err != nil || len(got) != 1 {
			t.Fatalf("after %s: %+v, %v", s.name, got, err)
		}
		if got[0].Body != s.body || got[0].Format.Markup() != s.html {
			t.Fatalf("after %s: body %q html %q, want %q %q", s.name, got[0].Body, got[0].Format.Markup(), s.body, s.html)
		}
	}
}

// Formatting without its markup is refused, not saved as "no formatting", which
// would wipe the stored formatting.
func TestFormattingWithoutMarkupIsRefused(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cache := openTemp(t)
	stored := domain.Message{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "hi",
		Format: richtext.FromMarkup("<b>hi</b>"), Timestamp: time.Unix(1, 0)}
	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{stored}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	drawn := stored
	drawn.Format = richtext.Drawn(stored.Format.Text(), stored.Format.Spans())
	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{drawn}); !errors.Is(err, errDrawnOnly) {
		t.Errorf("SaveMessages(drawn only) = %v, want errDrawnOnly", err)
	}
	if err := cache.SaveRevisionsFor(ctx, mx+"!a:x", mx+"$1", []domain.Revision{
		{ID: mx + "$e1", Body: "hi", Format: drawn.Format, At: time.Unix(2, 0)},
	}); !errors.Is(err, errDrawnOnly) {
		t.Errorf("SaveRevisionsFor(drawn only) = %v, want errDrawnOnly", err)
	}
	got, _, err := cache.Message(ctx, mx+"!a:x", mx+"$1")
	if err != nil || got.Format.Markup() != "<b>hi</b>" {
		t.Errorf("after the refusals the stored formatting is %q, %v", got.Format.Markup(), err)
	}
}

// testKeep is the per-room cap the trimming tests install.
const testKeep = 2000

// With no cap every message is kept; a room's own cap trims it alone; a negative
// cap keeps all.
func TestEachRoomKeepsWhatItsCapSays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	save := func(room domain.RoomID, n int) {
		msgs := make([]domain.Message, n)
		for i := range msgs {
			msgs[i] = domain.Message{ID: domain.EventID(fmt.Sprintf("%s/%04d", room, i)), RoomID: room, Body: "m", Timestamp: time.UnixMilli(int64(i) * 1000)}
		}
		if err := cache.SaveMessages(ctx, room, msgs); err != nil {
			t.Fatal(err)
		}
	}
	count := func(room domain.RoomID) int {
		got, err := cache.Messages(ctx, room, 10000)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	save(mx+"!all:x", 2500)
	if n := count(mx + "!all:x"); n != 2500 {
		t.Errorf("with no cap, kept %d of 2500", n)
	}
	cache.UseKeep(func(room domain.RoomID) int {
		if room == mx+"!small:x" {
			return 10
		}
		return -1
	})
	save(mx+"!small:x", 30)
	save(mx+"!all:x", 1)
	if small, all := count(mx+"!small:x"), count(mx+"!all:x"); small != 10 || all != 2500 {
		t.Errorf("kept %d in the capped room (want 10) and %d in the other (want 2500)", small, all)
	}
}

// Forgetting a space removes its row and children; other spaces stay.
func TestForgettingASpace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveSpaces(ctx, mxRooms, []domain.Space{
		{ID: mx + "!work:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x", mx + "!b:x"}},
		{ID: mx + "!fun:x", Name: "Friends", Children: []domain.RoomID{mx + "!a:x"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.ForgetRooms(ctx, []domain.RoomID{mx + "!work:x"}); err != nil {
		t.Fatal(err)
	}
	got, err := cache.Spaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != mx+"!fun:x" || len(got[0].Children) != 1 {
		t.Errorf("Spaces after forgetting Work = %+v, want Friends alone with its room", got)
	}
}
