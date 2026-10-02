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

// mustSave persists one message and fails the test on error.
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
		{ID: "!b:x", Name: "Bravo", Members: []string{"Bianca Reyes", "Cyrus Vale"}},
		{ID: "!a:x", Name: "Alpha", IsDirect: true},
	}
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, rooms); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}

	got, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms() error = %v", err)
	}
	if len(got) != 2 || got[0].Name != "Alpha" || !got[0].IsDirect {
		t.Fatalf("Rooms() = %+v, want sorted with Alpha (direct) first", got)
	}
	// Members round-trip through the JSON column; a room without any stays nil.
	if len(got[0].Members) != 0 {
		t.Errorf("Alpha members = %+v, want none", got[0].Members)
	}
	if len(got[1].Members) != 2 || got[1].Members[0] != "Bianca Reyes" {
		t.Errorf("Bravo members = %+v, want [Bianca Reyes, Cyrus Vale]", got[1].Members)
	}

	// SaveRooms replaces, not appends.
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!c:x", Name: "Charlie"}}); err != nil {
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
		{ID: "!work:x", Name: "Work", Children: []domain.RoomID{"!a:x", "!b:x"}},
		{ID: "!fun:x", Name: "Friends"},
	}
	if err := cache.SaveSpaces(ctx, domain.MatrixRooms, spaces); err != nil {
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
	if len(work.Children) != 2 || work.Children[0] != "!a:x" || work.Children[1] != "!b:x" {
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
	if serr := first.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); serr != nil {
		t.Fatalf("SaveRooms() error = %v", serr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}

	// Reopening runs migrate again (a no-op) and the data survives.
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
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$2", RoomID: "!a:x", Sender: "@b:x", Body: "second", Timestamp: at(2)},
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "first", Timestamp: at(1)},
	}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}

	got, err := cache.Messages(ctx, "!a:x", 10)
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
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$3", RoomID: "!a:x", Body: "third", Timestamp: at(3)},
	}); err != nil {
		t.Fatalf("second SaveMessages() error = %v", err)
	}
	got, _ = cache.Messages(ctx, "!a:x", 10)
	if len(got) != 3 || got[2].Body != "third" {
		t.Fatalf("after append Messages() = %+v, want 3 with third last", got)
	}
	if other, _ := cache.Messages(ctx, "!other:x", 10); len(other) != 0 {
		t.Errorf("messages leaked across rooms: %+v", other)
	}
}

func TestMarkRedacted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "secret", Timestamp: at(1)},
	}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
	if err := cache.MarkRedacted(ctx, "!a:x", "$1", "@mod:x", "spam", time.Time{}, false); err != nil {
		t.Fatalf("MarkRedacted() error = %v", err)
	}

	got, _ := cache.Messages(ctx, "!a:x", 10)
	// The row survives for the placeholder — sender and timestamp — and the words do
	// not: keeping them is [display.deleted] keep, and it was not asked for here.
	if len(got) != 1 || !got[0].Redacted {
		t.Fatalf("message not marked redacted: %+v", got)
	}
	if got[0].Sender != "@a:x" || !got[0].Timestamp.Equal(at(1)) {
		t.Errorf("MarkRedacted clobbered other columns: %+v", got[0])
	}
	if got[0].Body != "" {
		t.Errorf("the deleted words are still on disk: %q", got[0].Body)
	}
	if got[0].RedactedBy != "@mod:x" || got[0].RedactedReason != "spam" {
		t.Errorf("who deleted it and why was not recorded: %+v", got[0])
	}

	// A later re-save of the same event must not clear the redaction (max()).
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "secret", Timestamp: at(1)},
	}); err != nil {
		t.Fatalf("re-SaveMessages() error = %v", err)
	}
	if got, _ = cache.Messages(ctx, "!a:x", 10); !got[0].Redacted {
		t.Error("re-save cleared the redaction flag; max() should preserve it")
	}
}

func TestEditsPersist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	orig := func(room domain.RoomID, id domain.EventID) domain.Message {
		return domain.Message{ID: id, RoomID: room, Sender: "@a:x", Body: "old", Timestamp: at(5)}
	}
	edit := func(room domain.RoomID, id domain.EventID) domain.Message {
		return domain.Message{ID: id, RoomID: room, Sender: "@a:x", Body: "new", Timestamp: at(9), Edited: true}
	}

	// Original then edit: replacement body, edited flag, and the original's earlier
	// timestamp (so the row keeps its position rather than jumping to the edit time).
	mustSave(t, cache, "!a:x", orig("!a:x", "$1"))
	mustSave(t, cache, "!a:x", edit("!a:x", "$1"))
	got, _ := cache.Messages(ctx, "!a:x", 10)
	if len(got) != 1 || got[0].Body != "new" || !got[0].Edited || !got[0].Timestamp.Equal(at(5)) {
		t.Fatalf("edit not applied in place: %+v", got)
	}
	// A later re-save of the original must not revert the edit.
	mustSave(t, cache, "!a:x", orig("!a:x", "$1"))
	if got, _ = cache.Messages(ctx, "!a:x", 10); got[0].Body != "new" || !got[0].Edited {
		t.Errorf("re-save reverted the edit: %+v", got[0])
	}

	// Reverse order (edit cached before the original) is equally order-independent.
	mustSave(t, cache, "!b:x", edit("!b:x", "$2"))
	mustSave(t, cache, "!b:x", orig("!b:x", "$2"))
	if got, _ = cache.Messages(ctx, "!b:x", 10); got[0].Body != "new" || !got[0].Edited || !got[0].Timestamp.Equal(at(5)) {
		t.Errorf("reverse-order edit wrong: %+v", got[0])
	}
}

// Relation and flag columns persist and survive a later re-save that lacks them.
func TestStickyFieldsSurviveABareResave(t *testing.T) {
	t.Parallel()
	at := time.UnixMilli(2000)
	bare := domain.Message{ID: "$2", RoomID: "!a:x", Sender: "@b:x", Body: "re", Timestamp: at}
	for _, tc := range []struct {
		name string
		set  func(*domain.Message)
		has  func(domain.Message) bool
	}{
		{"reply_to", func(m *domain.Message) { m.ReplyTo = "$1" }, func(m domain.Message) bool { return m.ReplyTo == "$1" }},
		{"mentioned", func(m *domain.Message) { m.Mentioned = true }, func(m domain.Message) bool { return m.Mentioned }},
		{"emote", func(m *domain.Message) { m.Emote = true }, func(m domain.Message) bool { return m.Emote }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cache := openTemp(t)
			full := bare
			tc.set(&full)
			mustSave(t, cache, "!a:x", full)
			got, _ := cache.Messages(context.Background(), "!a:x", 10)
			if len(got) != 1 || !tc.has(got[0]) {
				t.Fatalf("not persisted: %+v", got)
			}
			mustSave(t, cache, "!a:x", bare)
			if got, _ = cache.Messages(context.Background(), "!a:x", 10); !tc.has(got[0]) {
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

	mustSave(t, cache, "!a:x", domain.Message{
		ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "cat.jpg", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.jpg", Mime: "image/jpeg", Width: 800, Height: 600, Size: 123},
	})
	got, _ := cache.Messages(ctx, "!a:x", 10)
	if len(got) != 1 || got[0].Media == nil {
		t.Fatalf("media metadata not persisted: %+v", got)
	}
	if got[0].Media.Type != domain.MediaImage || got[0].Media.Name != "cat.jpg" || got[0].Media.Width != 800 || got[0].Media.Size != 123 {
		t.Errorf("media = %+v", got[0].Media)
	}

	// A non-media message round-trips with a nil Media.
	mustSave(t, cache, "!a:x", domain.Message{ID: "$2", RoomID: "!a:x", Sender: "@a:x", Body: "hi", Timestamp: at(2)})
	if got, _ = cache.Messages(ctx, "!a:x", 10); got[1].Media != nil {
		t.Errorf("non-media message got a Media: %+v", got[1].Media)
	}
}

// The scope decides which uses count at all, which is the difference between a setting
// that is honored and one that is approximated.
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
	const here = domain.RoomID("!a:x")
	sibling := domain.RoomID("!b:x") // same space
	far := domain.RoomID("!c:x")     // no relation
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

	// "space": the room term is gone, so the space's own habit ranks without this
	// conversation tilting it — ❤️ (twice, in the space) overtakes 🔥 (once).
	sp := scores("space")
	if sp["🔥"] != spaceWeight+1 {
		t.Errorf("space scope: 🔥 scored %d, want %d — the room term should be gone", sp["🔥"], spaceWeight+1)
	}
	if sp["❤️"] <= sp["🔥"] {
		t.Errorf("space scope should lead with ❤️, used twice in the space: %v", sp)
	}

	// "global": a plain count, and the nine uses far away finally win.
	g := scores("global")
	for emoji, want := range map[string]int{"🔥": 1, "❤️": 2, "👍": 9} {
		if g[emoji] != want {
			t.Errorf("global scope: %s scored %d, want %d — a plain count", emoji, g[emoji], want)
		}
	}
	if g["👍"] <= g["❤️"] || g["❤️"] <= g["🔥"] {
		t.Errorf("global ranking = %v, want 👍 > ❤️ > 🔥", g)
	}

	// An unrecognized scope is read as "room" rather than as nothing: a typo in a
	// config should not silently flatten the ranking.
	if typo := scores("evrywhere"); typo["🔥"] != room["🔥"] {
		t.Errorf("an unknown scope scored 🔥 %d, want the room scope's %d", typo["🔥"], room["🔥"])
	}
}

func TestMediaSourceRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	// The source hangs off the cached message and cascades with it, so the message
	// comes first — which is the order every real caller writes them in.
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{{ID: "$1", Body: "look"}}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}

	if err := cache.SaveMediaSource(ctx, "$1", "!a:x", "mxc://x/abc", `{"key":"v"}`); err != nil {
		t.Fatalf("SaveMediaSource() error = %v", err)
	}
	mxc, fileJSON, ok, err := cache.MediaSource(ctx, "!a:x", "$1")
	if err != nil || !ok {
		t.Fatalf("MediaSource() ok=%v err=%v", ok, err)
	}
	if mxc != "mxc://x/abc" || fileJSON != `{"key":"v"}` {
		t.Errorf("MediaSource() = %q, %q", mxc, fileJSON)
	}

	// Upsert replaces; unknown event is a clean not-found.
	if err := cache.SaveMediaSource(ctx, "$1", "!a:x", "mxc://x/def", ""); err != nil {
		t.Fatalf("re-SaveMediaSource() error = %v", err)
	}
	if mxc, _, _, _ := cache.MediaSource(ctx, "!a:x", "$1"); mxc != "mxc://x/def" {
		t.Errorf("upsert did not replace mxc: %q", mxc)
	}
	if _, _, ok, _ := cache.MediaSource(ctx, "!a:x", "$none"); ok {
		t.Error("unknown event should report not-found")
	}
}

func TestReactionsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveReactions(ctx, []domain.Reaction{
		{ID: "$r1", RoomID: "!a:x", Target: "$m", Sender: "@a:x", Key: "👍"},
		{ID: "$r2", RoomID: "!a:x", Target: "$m", Sender: "@b:x", Key: "❤️"},
		{ID: "$r3", RoomID: "!b:x", Target: "$n", Sender: "@a:x", Key: "🎉"},
	}); err != nil {
		t.Fatalf("SaveReactions() error = %v", err)
	}

	got, err := cache.Reactions(ctx, "!a:x")
	if err != nil {
		t.Fatalf("Reactions() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Reactions(!a:x) = %+v, want 2 (scoped to room)", got)
	}

	// Un-react: DeleteReaction returns the removed row (so the caller knows the
	// room/target) and the reaction disappears.
	del, ok, err := cache.DeleteReaction(ctx, "$r1")
	if err != nil || !ok {
		t.Fatalf("DeleteReaction($r1) ok=%v err=%v", ok, err)
	}
	if del.Target != "$m" || del.RoomID != "!a:x" || del.Key != "👍" {
		t.Errorf("deleted row = %+v, want target $m room !a:x key 👍", del)
	}
	if got, _ = cache.Reactions(ctx, "!a:x"); len(got) != 1 || got[0].ID != "$r2" {
		t.Errorf("after delete = %+v, want only $r2", got)
	}

	// An unknown id reports not-found, which is how a message redaction is told
	// apart from an un-react.
	if _, ok, _ := cache.DeleteReaction(ctx, "$nope"); ok {
		t.Error("DeleteReaction reported ok for an unknown id")
	}
}

func TestMessagesTrimToLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	// Write more than the per-room cap; only the newest survive.
	msgs := make([]domain.Message, messagesPerRoom+20)
	for i := range msgs {
		msgs[i] = domain.Message{
			ID:        domain.EventID(fmt.Sprintf("$%04d", i)),
			RoomID:    "!a:x",
			Body:      "m",
			Timestamp: time.UnixMilli(int64(i) * 1000),
		}
	}
	if err := cache.SaveMessages(ctx, "!a:x", msgs); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
	got, _ := cache.Messages(ctx, "!a:x", messagesPerRoom+100)
	if len(got) != messagesPerRoom {
		t.Fatalf("cached count = %d, want trimmed to %d", len(got), messagesPerRoom)
	}
	// The oldest kept message is the (20th) — everything older was trimmed.
	if got[0].ID != domain.EventID(fmt.Sprintf("$%04d", 20)) {
		t.Errorf("oldest kept = %q, want the 20th message", got[0].ID)
	}
}

func TestClearEmptiesEverything(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	if err := cache.SaveSpaces(ctx, domain.MatrixRooms, []domain.Space{{ID: "!s:x", Name: "Work", Children: []domain.RoomID{"!a:x"}}}); err != nil {
		t.Fatalf("SaveSpaces() error = %v", err)
	}
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{{ID: "$1", RoomID: "!a:x", Body: "hi"}}); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", Notifications: 3}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	if err := cache.SaveMembers(ctx, "!a:x", []domain.Member{{UserID: "@a:x", DisplayName: "A"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.RecordMention(ctx, "!a:x", "@a:x", 1); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveInvites(ctx, []domain.Room{{ID: "!i1:x", Name: "One"}}); err != nil {
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
	if msgs, _ := cache.Messages(ctx, "!a:x", 10); len(msgs) != 0 {
		t.Errorf("messages after clear = %d, want 0", len(msgs))
	}
	if unread, _ := cache.Unread(ctx); len(unread) != 0 {
		t.Errorf("unread after clear = %d, want 0", len(unread))
	}
	if members, _ := cache.Members(ctx, "!a:x", 0); len(members) != 0 {
		t.Errorf("members after clear = %d, want 0", len(members))
	}
	if mentions, _ := cache.FrequentMentions(ctx, "!a:x", 10); len(mentions) != 0 {
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

	want := domain.Unread{RoomID: "!a:x", Notifications: 5, Highlights: 2, ReadEvent: "$e1"}
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

	// A second save for the same room upserts (replaces) rather than duplicating.
	next := domain.Unread{RoomID: "!a:x", Notifications: 0, Highlights: 0, ReadEvent: "$e2"}
	if err := cache.SaveUnread(ctx, next, 0); err != nil {
		t.Fatalf("second SaveUnread() error = %v", err)
	}
	got, _ = cache.Unread(ctx)
	if len(got) != 1 || !reflect.DeepEqual(got[0], next) {
		t.Fatalf("after upsert Unread() = %+v, want [%+v]", got, next)
	}
}

// Keeping a deleted message is a setting, and the two halves of it are what the words
// are for: erased, they are gone from the file; kept, they are still readable.
func TestRedactionKeepsTheWordsOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	for _, keep := range []bool{false, true} {
		ctx := t.Context()
		cache := openTemp(t)
		if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
			{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "secret", Format: richtext.FromMarkup("<strong>secret</strong>"),
				Timestamp: time.Unix(1, 0)},
		}); err != nil {
			t.Fatalf("SaveMessages: %v", err)
		}
		if err := cache.MarkRedacted(ctx, "!a:x", "$1", "@a:x", "", time.Time{}, keep); err != nil {
			t.Fatalf("MarkRedacted: %v", err)
		}
		// And a page that was already in flight arrives afterwards, carrying the words
		// the server has since stripped. It must not put them back.
		if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
			{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "secret", Format: richtext.FromMarkup("<strong>secret</strong>"),
				Timestamp: time.Unix(1, 0)},
		}); err != nil {
			t.Fatalf("re-save: %v", err)
		}
		got, err := cache.Messages(ctx, "!a:x", 10)
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

// Every version a message had, kept beside the one the timeline draws.
func TestRevisionsKeepEveryVersionInOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	original := domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "seven", Timestamp: at(1)}
	firstEdit := domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "seven thirty",
		Format: richtext.FromMarkup("seven <b>thirty</b>"), RevisionID: "$e1", Timestamp: at(2)}
	secondEdit := domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "seven thirty, usual place",
		RevisionID: "$e2", Timestamp: at(3)}

	for _, m := range []domain.Message{original, firstEdit, secondEdit} {
		if err := cache.SaveMessagesWithRevisions(ctx, "!a:x", []domain.Message{m}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := cache.Revisions(ctx, "!a:x", "$m")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d versions, want 3: %+v", len(got), got)
	}
	// Oldest first, because that is the order they happened in and the order the
	// history is read in.
	for i, want := range []string{"seven", "seven thirty", "seven thirty, usual place"} {
		if got[i].Body != want {
			t.Errorf("version %d = %q, want %q", i+1, got[i].Body, want)
		}
	}
	// The first version is keyed by the message's own ID; the rest by the edit that
	// carried them, which is what makes a re-delivered edit one row and not two.
	if got[0].ID != "$m" || got[1].ID != "$e1" || got[2].ID != "$e2" {
		t.Errorf("ids = %q, %q, %q", got[0].ID, got[1].ID, got[2].ID)
	}
	if got[1].Format.Markup() == "" {
		t.Error("a version's formatting was not kept with its words")
	}
	// Idempotent: the same edit arriving twice — a re-page, a resync — is one version.
	if err := cache.SaveMessagesWithRevisions(ctx, "!a:x", []domain.Message{firstEdit, firstEdit}); err != nil {
		t.Fatal(err)
	}
	if again, _ := cache.Revisions(ctx, "!a:x", "$m"); len(again) != 3 {
		t.Errorf("%d versions after re-saving one, want 3", len(again))
	}
}

// Erasing what a deleted message said takes its drafts with it. Keeping every
// version of a message whose words were just erased would be the erase undone by the
// feature beside it.
func TestErasingADeletionTakesItsVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	msg := domain.Message{ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "first", Timestamp: at(1)}
	if err := cache.SaveMessagesWithRevisions(ctx, "!a:x", []domain.Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := cache.MarkRedacted(ctx, "!a:x", "$m", "@her:x", "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := cache.Revisions(ctx, "!a:x", "$m"); len(got) != 0 {
		t.Errorf("%d versions survived an erase: %+v", len(got), got)
	}

	// With keeping on, the deletion leaves them exactly where they are — that is the
	// whole feature.
	kept := domain.Message{ID: "$k", RoomID: "!a:x", Sender: "@her:x", Body: "held", Timestamp: at(2)}
	if err := cache.SaveMessagesWithRevisions(ctx, "!a:x", []domain.Message{kept}); err != nil {
		t.Fatal(err)
	}
	if err := cache.MarkRedacted(ctx, "!a:x", "$k", "@her:x", "", time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := cache.Revisions(ctx, "!a:x", "$k"); len(got) != 1 {
		t.Errorf("%d versions after a kept deletion, want 1", len(got))
	}
}

// A trim takes others' reactions to what it trims with it. It keeps our own (reaction
// emoji are ranked from them), reactions to kept messages, and reactions whose target
// was never cached (it may be paged in later).
func TestTrimTakesOthersReactionsToTrimmedMessages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	cache.UseAccount("@me:x")

	// Exactly at the cap: nothing is trimmed until one more arrives.
	msgs := make([]domain.Message, messagesPerRoom)
	for i := range msgs {
		msgs[i] = domain.Message{ID: domain.EventID(fmt.Sprintf("$%04d", i)), RoomID: "!a:x", Body: "m",
			Timestamp: time.UnixMilli(int64(i) * 1000)}
	}
	oldest, newest := msgs[0].ID, msgs[len(msgs)-1].ID
	if err := cache.SaveMessages(ctx, "!a:x", msgs); err != nil {
		t.Fatal(err)
	}
	reactions := []domain.Reaction{
		{ID: "$theirs-old", RoomID: "!a:x", Target: oldest, Sender: "@bob:x", Key: "👍"},
		{ID: "$mine-old", RoomID: "!a:x", Target: oldest, Sender: "@me:x", Key: "🎉"},
		{ID: "$theirs-new", RoomID: "!a:x", Target: newest, Sender: "@bob:x", Key: "👍"},
		{ID: "$theirs-uncached", RoomID: "!a:x", Target: "$never-seen", Sender: "@bob:x", Key: "👍"},
	}
	if err := cache.SaveReactions(ctx, reactions); err != nil {
		t.Fatal(err)
	}
	// One more pushes the oldest out.
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{{ID: "$next", RoomID: "!a:x", Body: "m",
		Timestamp: time.UnixMilli(int64(len(msgs)) * 1000)}}); err != nil {
		t.Fatal(err)
	}

	got, err := cache.Reactions(ctx, "!a:x")
	if err != nil {
		t.Fatal(err)
	}
	kept := map[domain.EventID]bool{}
	for _, r := range got {
		kept[r.ID] = true
	}
	for id, want := range map[domain.EventID]bool{
		"$theirs-old": false, "$mine-old": true, "$theirs-new": true, "$theirs-uncached": true,
	} {
		if kept[id] != want {
			t.Errorf("%s kept = %v, want %v", id, kept[id], want)
		}
	}
}

// A read that fails is an error, not "not cached".
func TestAFailedLookupIsNotNotCached(t *testing.T) {
	t.Parallel()
	cache := openTemp(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cache.MessagesAround(canceled, "!a:x", "$m", 2, 2); err == nil {
		t.Error("MessagesAround on a canceled context reported the message as not cached")
	}
}

// An edit's formatting follows the same order-independent rule as its words: the edit
// replaces it (plain removes it), and the original arriving again never restores it.
func TestEditsCarryTheirFormatting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }
	orig := domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "old", Format: richtext.FromMarkup("<b>old</b>"), Timestamp: at(5)}
	formatted := domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "new", Format: richtext.FromMarkup("<i>new</i>"), Timestamp: at(9), Edited: true}
	plain := domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "plain now", Timestamp: at(12), Edited: true}

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
		mustSave(t, cache, "!a:x", s.save)
		got, err := cache.Messages(ctx, "!a:x", 10)
		if err != nil || len(got) != 1 {
			t.Fatalf("after %s: %+v, %v", s.name, got, err)
		}
		if got[0].Body != s.body || got[0].Format.Markup() != s.html {
			t.Fatalf("after %s: body %q html %q, want %q %q", s.name, got[0].Body, got[0].Format.Markup(), s.body, s.html)
		}
	}
}

// Formatting that arrived without its markup (a client's drawn copy) is refused, not
// saved as "no formatting": that would wipe the stored formatting of the message.
func TestFormattingWithoutMarkupIsRefused(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cache := openTemp(t)
	stored := domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "hi",
		Format: richtext.FromMarkup("<b>hi</b>"), Timestamp: time.Unix(1, 0)}
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{stored}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	drawn := stored
	drawn.Format = richtext.Drawn(stored.Format.Text(), stored.Format.Spans())
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{drawn}); !errors.Is(err, errDrawnOnly) {
		t.Errorf("SaveMessages(drawn only) = %v, want errDrawnOnly", err)
	}
	if err := cache.SaveRevisionsFor(ctx, "!a:x", "$1", []domain.Revision{
		{ID: "$e1", Body: "hi", Format: drawn.Format, At: time.Unix(2, 0)},
	}); !errors.Is(err, errDrawnOnly) {
		t.Errorf("SaveRevisionsFor(drawn only) = %v, want errDrawnOnly", err)
	}
	got, _, err := cache.Message(ctx, "!a:x", "$1")
	if err != nil || got.Format.Markup() != "<b>hi</b>" {
		t.Errorf("after the refusals the stored formatting is %q, %v", got.Format.Markup(), err)
	}
}
