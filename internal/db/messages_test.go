package db

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Marking a room read without opening it needs an event to point the receipt at,
// and the newest cached message is the only one such a room has.
func TestLatestEventsPicksTheNewestPerRoom(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	// Saved oldest-last, so a query that returned insertion order rather than time
	// order would pick the wrong one.
	for _, msg := range []domain.Message{
		{ID: "$a-new", Timestamp: base.Add(2 * time.Hour)},
		{ID: "$a-mid", Timestamp: base.Add(time.Hour)},
		{ID: "$a-old", Timestamp: base},
	} {
		mustSave(t, cache, "!a:x", msg)
	}
	mustSave(t, cache, "!b:x", domain.Message{ID: "$b-only", Timestamp: base})

	// !c:x is a room nothing has been cached for — never opened, nothing of its own
	// seen — and !d:x was never heard of at all.
	got, err := cache.LatestEvents(ctx, []domain.RoomID{"!a:x", "!b:x", "!c:x"})
	if err != nil {
		t.Fatalf("LatestEvents() error = %v", err)
	}
	want := map[domain.RoomID]domain.EventID{"!a:x": "$a-new", "!b:x": "$b-only"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LatestEvents() = %v, want %v", got, want)
	}
	if _, present := got["!c:x"]; present {
		t.Error("a room with nothing cached must be absent, not empty")
	}

	// No rooms asked about is not a query, and not an error either.
	if empty, eerr := cache.LatestEvents(ctx, nil); eerr != nil || len(empty) != 0 {
		t.Errorf("LatestEvents(nil) = %v, %v; want an empty map", empty, eerr)
	}
}

// A live message arriving later moves the answer, which is what makes the feature
// keep working: the receipt has to land on what arrived while nobody was looking.
func TestLatestEventsFollowsNewMessages(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	mustSave(t, cache, "!a:x", domain.Message{ID: "$first", Timestamp: base})

	if got, err := cache.LatestEvents(ctx, []domain.RoomID{"!a:x"}); err != nil || got["!a:x"] != "$first" {
		t.Fatalf("LatestEvents() = %v, %v; want $first", got, err)
	}
	mustSave(t, cache, "!a:x", domain.Message{ID: "$later", Timestamp: base.Add(time.Minute)})
	if got, err := cache.LatestEvents(ctx, []domain.RoomID{"!a:x"}); err != nil || got["!a:x"] != "$later" {
		t.Errorf("LatestEvents() = %v, %v; want $later", got, err)
	}
}

// When each room last had a message, for the room list's activity order. A room the
// cache holds nothing for is absent rather than zero: absence means unknown, and a
// room from 1970 would sort as if it had been silent since.
func TestLastMessages(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@s:x", Body: "one", Timestamp: at(10)},
		{ID: "$2", RoomID: "!a:x", Sender: "@s:x", Body: "two", Timestamp: at(30)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	if err := cache.SaveMessages(ctx, "!b:x", []domain.Message{
		{ID: "$3", RoomID: "!b:x", Sender: "@s:x", Body: "elsewhere", Timestamp: at(20)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}

	latest, err := cache.LastMessages(ctx)
	if err != nil {
		t.Fatalf("LastMessages: %v", err)
	}
	if len(latest) != 2 {
		t.Fatalf("got %v, want one entry per room with messages", latest)
	}
	if !latest["!a:x"].Equal(at(30)) {
		t.Errorf("!a:x = %v, want its newest message at %v", latest["!a:x"], at(30))
	}
	if !latest["!b:x"].Equal(at(20)) {
		t.Errorf("!b:x = %v, want %v", latest["!b:x"], at(20))
	}
	// A room with nothing cached is absent, not zero.
	if _, present := latest["!never:x"]; present {
		t.Error("a room with no cached messages should be absent")
	}
}

// A re-save overwrites a non-edited row's body.
func TestSaveMessagesOverwritesBodyOnResave(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	mustSave(t, cache, "!r:x", domain.Message{ID: "$e:x", Body: "the real message", Timestamp: at})
	mustSave(t, cache, "!r:x", domain.Message{ID: "$e:x", Body: "replaced", Timestamp: at})

	got, err := cache.Messages(ctx, "!r:x", 10)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(got), got)
	}
	if got[0].Body != "replaced" {
		t.Errorf("body = %q, want %q — the upsert no longer overwrites, so the "+
			"placeholder guard in internal/matrix may no longer be needed", got[0].Body, "replaced")
	}

	// An edited row is the one case that resists replacement, which is why the
	// guard cannot live here on its own: edited=1 is the only body the upsert protects.
	mustSave(t, cache, "!r:x", domain.Message{ID: "$ed:x", Body: "edited text", Edited: true, Timestamp: at})
	mustSave(t, cache, "!r:x", domain.Message{ID: "$ed:x", Body: "plain resave", Timestamp: at})
	got, err = cache.Messages(ctx, "!r:x", 10)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	for _, m := range got {
		if m.ID == "$ed:x" && m.Body != "edited text" {
			t.Errorf("edited body = %q, want it preserved", m.Body)
		}
	}
}

// NewestTS is the latest time among a room's messages and edits; 0 for none.
func TestNewestTSCoversMessagesAndEdits(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	if ts, err := cache.NewestTS(ctx, "!a:x"); err != nil || ts != 0 {
		t.Fatalf("NewestTS(empty) = %d, %v; want 0", ts, err)
	}
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$a", Timestamp: base},
		{ID: "$b", Timestamp: base.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	if ts, err := cache.NewestTS(ctx, "!a:x"); err != nil || ts != base.Add(time.Minute).UnixMilli() {
		t.Errorf("NewestTS = %d, %v; want $b's", ts, err)
	}
}

// A message found by its ending is one of the owner's, whole tail matched: not
// another account's, not one whose number only ends the same.
func TestMessagesAreFoundByTheirEnding(t *testing.T) {
	t.Parallel()
	c := openTemp(t)
	ctx := t.Context()
	for room, ids := range map[domain.RoomID][]string{
		"telegram:42/7":  {"5", "15"},
		"telegram:42/-3": {"6"},
		"telegram:43/7":  {"5"},
	} {
		var msgs []domain.Message
		for _, id := range ids {
			msgs = append(msgs, domain.Message{ID: domain.EventID(string(room) + "/" + id), RoomID: room, Body: "m", Timestamp: time.Now()})
		}
		if err := c.SaveMessages(ctx, room, msgs); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.MessagesEndingIn(ctx, domain.AccountRooms(domain.ProtocolTelegram, "42"), []string{"5", "6"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, string(m.RoomID)+" "+string(m.ID))
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"telegram:42/-3 telegram:42/-3/6", "telegram:42/7 telegram:42/7/5"}) {
		t.Errorf("found %v", ids)
	}
}
