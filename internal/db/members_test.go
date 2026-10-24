package db

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestMembersRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	// A cold cache has no members, which is not an error.
	got, err := cache.Members(ctx, mx+"!a:x", 0)
	if err != nil {
		t.Fatalf("Members on a cold cache: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cold cache returned %d members", len(got))
	}

	want := []domain.Member{
		{UserID: "matrix:@alice:x", DisplayName: "Alice"},
		{UserID: "matrix:@bob:x", DisplayName: "Bob"},
		{UserID: "matrix:@nameless:x"},
	}
	if serr := cache.SaveMembers(ctx, mx+"!a:x", want); serr != nil {
		t.Fatalf("SaveMembers: %v", serr)
	}
	got, err = cache.Members(ctx, mx+"!a:x", 0)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d members, want 3: %+v", len(got), got)
	}
	byID := map[string]domain.Member{}
	for _, member := range got {
		byID[member.UserID] = member
	}
	if byID["matrix:@alice:x"].DisplayName != "Alice" {
		t.Errorf("alice = %+v", byID["matrix:@alice:x"])
	}
	if byID["matrix:@nameless:x"].DisplayName != "" {
		t.Errorf("a member with no display name should read back blank, got %+v", byID["matrix:@nameless:x"])
	}
}

// SaveMembers replaces one room's snapshot: anyone missing has left; other rooms are untouched.
func TestSaveMembersIsAPerRoomSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveMembers(ctx, mx+"!a:x", []domain.Member{
		{UserID: "matrix:@alice:x", DisplayName: "Alice"},
		{UserID: "matrix:@leaver:x", DisplayName: "Leaver"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	if err := cache.SaveMembers(ctx, mx+"!b:x", []domain.Member{
		{UserID: "matrix:@bob:x", DisplayName: "Bob"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}

	if err := cache.SaveMembers(ctx, mx+"!a:x", []domain.Member{
		{UserID: "matrix:@alice:x", DisplayName: "Alice"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	roomA, err := cache.Members(ctx, mx+"!a:x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(roomA) != 1 || roomA[0].UserID != "matrix:@alice:x" {
		t.Errorf("room A = %+v, want just alice", roomA)
	}
	roomB, err := cache.Members(ctx, mx+"!b:x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(roomB) != 1 || roomB[0].UserID != "matrix:@bob:x" {
		t.Errorf("room B = %+v, want just bob — a snapshot of one room must not touch another", roomB)
	}
}

// A large room must not be loaded in full to offer a handful of candidates.
func TestMembersLimit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	members := make([]domain.Member, 0, 500)
	for i := range 500 {
		members = append(members, domain.Member{
			UserID:      fmt.Sprintf("matrix:@u%03d:x", i),
			DisplayName: fmt.Sprintf("Person %03d", i),
		})
	}
	if err := cache.SaveMembers(ctx, mx+"!big:x", members); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	limited, err := cache.Members(ctx, mx+"!big:x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 10 {
		t.Fatalf("limit 10 returned %d", len(limited))
	}
	// Ordered by display name, so the limit is predictable.
	if limited[0].DisplayName != "Person 000" {
		t.Errorf("first = %q, want the alphabetically first", limited[0].DisplayName)
	}
	all, err := cache.Members(ctx, mx+"!big:x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 500 {
		t.Errorf("limit 0 returned %d, want every member", len(all))
	}
}

// Addressees rank by this room's history first, then globally.
func TestFrequentMentions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) int64 { return int64(sec) * 1000 }

	record := func(room domain.RoomID, user string, times int, ts int64) {
		t.Helper()
		for range times {
			if err := cache.RecordMention(ctx, room, user, ts); err != nil {
				t.Fatalf("RecordMention: %v", err)
			}
		}
	}
	record(mx+"!a:x", "matrix:@rare:x", 1, at(10))
	record(mx+"!a:x", "matrix:@often:x", 5, at(20))
	record(mx+"!b:x", "matrix:@elsewhere:x", 50, at(30))

	// The room's history leads; the global ranking adds whoever is not placed yet.
	got, err := cache.FrequentMentions(ctx, mx+"!a:x", 10)
	if err != nil {
		t.Fatalf("FrequentMentions: %v", err)
	}
	if len(got) < 3 || got[0] != "matrix:@often:x" || got[1] != "matrix:@rare:x" {
		t.Fatalf("room-scoped ranking = %v, want often, rare, then the rest", got)
	}
	if got[2] != "matrix:@elsewhere:x" {
		t.Errorf("the global rung should follow, got %v", got)
	}

	// An unknown room falls back to the global ranking.
	cold, err := cache.FrequentMentions(ctx, mx+"!new:x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cold) == 0 || cold[0] != "matrix:@elsewhere:x" {
		t.Errorf("cold-room ranking = %v, want the global favorite first", cold)
	}

	// Recency breaks a tie on count.
	record(mx+"!c:x", "matrix:@older:x", 3, at(10))
	record(mx+"!c:x", "matrix:@newer:x", 3, at(99))
	tie, err := cache.FrequentMentions(ctx, mx+"!c:x", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tie) < 2 || tie[0] != "matrix:@newer:x" {
		t.Errorf("tie broken as %v, want the more recent first", tie)
	}
}

// Recent speakers rank by their latest message, per room.
func TestRecentSpeakers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@old:x", Body: "first", Timestamp: at(10)},
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@recent:x", Body: "second", Timestamp: at(30)},
		{ID: mx + "$3", RoomID: mx + "!a:x", Sender: "matrix:@old:x", Body: "third", Timestamp: at(20)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	// Saved separately: SaveMessages files messages under the room it is given.
	if err := cache.SaveMessages(ctx, mx+"!other:x", []domain.Message{
		{ID: mx + "$4", RoomID: mx + "!other:x", Sender: "matrix:@elsewhere:x", Body: "other room", Timestamp: at(99)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	got, err := cache.RecentSpeakers(ctx, mx+"!a:x", 10)
	if err != nil {
		t.Fatalf("RecentSpeakers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want two distinct speakers", got)
	}
	// Ranked by each speaker's most recent message, not by message order.
	if got[0] != "matrix:@recent:x" || got[1] != "matrix:@old:x" {
		t.Errorf("speakers = %v, want recent then old", got)
	}
	for _, sender := range got {
		if sender == "matrix:@elsewhere:x" {
			t.Error("another room's speaker leaked in")
		}
	}
}

// Search senders are those who posted in scope, ranked by message count.
func TestSearchSenders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	if err := cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@quiet:x", Body: "one", Timestamp: at(90)},
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@loud:x", Body: "two", Timestamp: at(10)},
		{ID: mx + "$3", RoomID: mx + "!a:x", Sender: "matrix:@loud:x", Body: "three", Timestamp: at(20)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	if err := cache.SaveMessages(ctx, mx+"!b:x", []domain.Message{
		{ID: mx + "$4", RoomID: mx + "!b:x", Sender: "matrix:@elsewhere:x", Body: "another room", Timestamp: at(99)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	// A name comes from the membership, when there is one.
	if err := cache.SaveMembers(ctx, mx+"!a:x", []domain.Member{
		{UserID: "matrix:@loud:x", DisplayName: "Loud Larry"},
		{UserID: "matrix:@quiet:x"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}

	// One room: whoever said most first.
	got, err := cache.SearchSenders(ctx, domain.TheseRooms([]domain.RoomID{mx + "!a:x"}), 0)
	if err != nil {
		t.Fatalf("SearchSenders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want the two people who posted in !a:x", got)
	}
	if got[0].UserID != "matrix:@loud:x" || got[0].DisplayName != "Loud Larry" {
		t.Errorf("first = %+v, want the most prolific sender, named", got[0])
	}
	// Without a named membership the sender is offered under the MXID, not dropped.
	if got[1].UserID != "matrix:@quiet:x" || got[1].DisplayName != "" {
		t.Errorf("second = %+v, want @quiet:x with no name", got[1])
	}

	// Every room.
	all, err := cache.SearchSenders(ctx, domain.EveryRoom(), 0)
	if err != nil {
		t.Fatalf("SearchSenders(all): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("global scope returned %+v, want everyone who has posted anywhere", all)
	}

	// The limit caps the ranking.
	top, err := cache.SearchSenders(ctx, domain.EveryRoom(), 1)
	if err != nil {
		t.Fatalf("SearchSenders(limit): %v", err)
	}
	if len(top) != 1 || top[0].UserID != "matrix:@loud:x" {
		t.Errorf("limit 1 returned %+v, want only the most prolific", top)
	}
}

// SaveMember edits one row of the roster without disturbing the rest.
func TestSaveMemberUpsertsWithoutClearingTheRoom(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveMembers(ctx, mx+"!a:x", []domain.Member{
		{UserID: "matrix:@alice:x", DisplayName: "Alice"},
		{UserID: "matrix:@bob:x", DisplayName: "Bob"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}

	// A newcomer is added; the others are untouched.
	if err := cache.SaveMember(ctx, mx+"!a:x", domain.Member{UserID: "matrix:@carol:x", DisplayName: "Carol"}); err != nil {
		t.Fatalf("SaveMember: %v", err)
	}
	if got := memberNames(t, cache, mx+"!a:x"); len(got) != 3 || got["matrix:@alice:x"] != "Alice" || got["matrix:@carol:x"] != "Carol" {
		t.Fatalf("after a join: %v", got)
	}

	// A rename overwrites the row rather than adding one or failing on the key.
	if err := cache.SaveMember(ctx, mx+"!a:x", domain.Member{UserID: "matrix:@bob:x", DisplayName: "Robert"}); err != nil {
		t.Fatalf("SaveMember rename: %v", err)
	}
	got := memberNames(t, cache, mx+"!a:x")
	if len(got) != 3 {
		t.Fatalf("rename changed the roster size: %v", got)
	}
	if got["matrix:@bob:x"] != "Robert" {
		t.Errorf("display_name = %q after rename, want %q", got["matrix:@bob:x"], "Robert")
	}
}

// A leave removes the row, so the person is no longer offered or counted.
func TestRemoveMemberDropsOnlyThatPerson(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveMembers(ctx, mx+"!a:x", []domain.Member{
		{UserID: "matrix:@alice:x", DisplayName: "Alice"},
		{UserID: "matrix:@bob:x", DisplayName: "Bob"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	if err := cache.RemoveMember(ctx, mx+"!a:x", "matrix:@alice:x"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	got := memberNames(t, cache, mx+"!a:x")
	if _, still := got["matrix:@alice:x"]; still {
		t.Error("@alice:x survived the leave")
	}
	if got["matrix:@bob:x"] != "Bob" {
		t.Errorf("@bob:x was collateral: %v", got)
	}

	// A leave for someone never seen joining (cold start) is a no-op, not an error.
	if err := cache.RemoveMember(ctx, mx+"!a:x", "matrix:@ghost:x"); err != nil {
		t.Errorf("RemoveMember on an unknown member: %v", err)
	}
	if err := cache.RemoveMember(ctx, mx+"!never:x", "matrix:@ghost:x"); err != nil {
		t.Errorf("RemoveMember on an unknown room: %v", err)
	}
}

// memberNames reads a room's cached membership as a user-ID→name map.
func memberNames(t *testing.T, cache *Cache, roomID domain.RoomID) map[string]string {
	t.Helper()
	members, err := cache.Members(context.Background(), roomID, 0)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	names := make(map[string]string, len(members))
	for _, m := range members {
		names[m.UserID] = m.DisplayName
	}
	return names
}

// Naming one person twice (MXID and name) narrows to their rooms, not to none: the
// query counts distinct people.
func TestRoomsWithCountsEachPersonOnce(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID(mx + "!r:x")
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: room, Name: "R"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMembers(ctx, room, []domain.Member{{UserID: "matrix:@d:x", DisplayName: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{"matrix:@d:x"}, {"matrix:@d:x", "matrix:@d:x"}, {"matrix:@d:x", " matrix:@d:x "}} {
		got, err := cache.RoomsWith(ctx, ids, domain.EveryRoom(), 10)
		if err != nil || len(got) != 1 {
			t.Errorf("RoomsWith(%q) = %d rooms, %v; want the one room", ids, len(got), err)
		}
	}
}

// Equally recent rooms come back in a stable order.
func TestRoomsWithOrdersTiesByRoom(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	rooms := []domain.Room{{ID: mx + "!c:x", Name: "C"}, {ID: mx + "!a:x", Name: "A"}, {ID: mx + "!b:x", Name: "B"}}
	if err := cache.SaveRooms(ctx, mxRooms, rooms); err != nil {
		t.Fatal(err)
	}
	for _, r := range rooms {
		if err := cache.SaveMembers(ctx, r.ID, []domain.Member{{UserID: "matrix:@d:x"}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := cache.RoomsWith(ctx, []string{"matrix:@d:x"}, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]domain.RoomID, len(got))
	for i := range got {
		ids[i] = got[i].ID
	}
	if want := []domain.RoomID{mx + "!a:x", mx + "!b:x", mx + "!c:x"}; !slices.Equal(ids, want) {
		t.Errorf("rooms with no activity came back %v, want by room ID %v", ids, want)
	}
}
