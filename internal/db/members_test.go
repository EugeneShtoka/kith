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

	// A cold cache has no members, which is not an error — the dropdown shows what
	// it has and the refresh fills it in.
	got, err := cache.Members(ctx, "!a:x", 0)
	if err != nil {
		t.Fatalf("Members on a cold cache: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cold cache returned %d members", len(got))
	}

	want := []domain.Member{
		{UserID: "@alice:x", DisplayName: "Alice"},
		{UserID: "@bob:x", DisplayName: "Bob"},
		{UserID: "@nameless:x"},
	}
	if serr := cache.SaveMembers(ctx, "!a:x", want); serr != nil {
		t.Fatalf("SaveMembers: %v", serr)
	}
	got, err = cache.Members(ctx, "!a:x", 0)
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
	if byID["@alice:x"].DisplayName != "Alice" {
		t.Errorf("alice = %+v", byID["@alice:x"])
	}
	if byID["@nameless:x"].DisplayName != "" {
		t.Errorf("a member with no display name should read back blank, got %+v", byID["@nameless:x"])
	}
}

// The list is a per-room snapshot: someone missing from /joined_members has left,
// and an upsert would keep offering them in the dropdown forever. Other rooms must
// be untouched.
func TestSaveMembersIsAPerRoomSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveMembers(ctx, "!a:x", []domain.Member{
		{UserID: "@alice:x", DisplayName: "Alice"},
		{UserID: "@leaver:x", DisplayName: "Leaver"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	if err := cache.SaveMembers(ctx, "!b:x", []domain.Member{
		{UserID: "@bob:x", DisplayName: "Bob"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}

	// Leaver has gone from room A.
	if err := cache.SaveMembers(ctx, "!a:x", []domain.Member{
		{UserID: "@alice:x", DisplayName: "Alice"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	roomA, err := cache.Members(ctx, "!a:x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(roomA) != 1 || roomA[0].UserID != "@alice:x" {
		t.Errorf("room A = %+v, want just alice", roomA)
	}
	// Room B is untouched.
	roomB, err := cache.Members(ctx, "!b:x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(roomB) != 1 || roomB[0].UserID != "@bob:x" {
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
			UserID:      fmt.Sprintf("@u%03d:x", i),
			DisplayName: fmt.Sprintf("Person %03d", i),
		})
	}
	if err := cache.SaveMembers(ctx, "!big:x", members); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	limited, err := cache.Members(ctx, "!big:x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 10 {
		t.Fatalf("limit 10 returned %d", len(limited))
	}
	// Ordered by display name, so the limit takes a predictable slice.
	if limited[0].DisplayName != "Person 000" {
		t.Errorf("first = %q, want the alphabetically first", limited[0].DisplayName)
	}
	all, err := cache.Members(ctx, "!big:x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 500 {
		t.Errorf("limit 0 returned %d, want every member", len(all))
	}
}

// Who you address in this room is a better guess than who you address in general,
// but the general answer beats nothing in a room you are new to.
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
	record("!a:x", "@rare:x", 1, at(10))
	record("!a:x", "@often:x", 5, at(20))
	record("!b:x", "@elsewhere:x", 50, at(30))

	// In room A, the room's own history leads; the global rung then contributes
	// whoever isn't already placed.
	got, err := cache.FrequentMentions(ctx, "!a:x", 10)
	if err != nil {
		t.Fatalf("FrequentMentions: %v", err)
	}
	if len(got) < 3 || got[0] != "@often:x" || got[1] != "@rare:x" {
		t.Fatalf("room-scoped ranking = %v, want often, rare, then the rest", got)
	}
	if got[2] != "@elsewhere:x" {
		t.Errorf("the global rung should follow, got %v", got)
	}

	// In an unknown room there is no local history, so the global ranking is all
	// there is — and it is much better than nothing.
	cold, err := cache.FrequentMentions(ctx, "!new:x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cold) == 0 || cold[0] != "@elsewhere:x" {
		t.Errorf("cold-room ranking = %v, want the global favorite first", cold)
	}

	// Recency breaks a tie on count.
	record("!c:x", "@older:x", 3, at(10))
	record("!c:x", "@newer:x", 3, at(99))
	tie, err := cache.FrequentMentions(ctx, "!c:x", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tie) < 2 || tie[0] != "@newer:x" {
		t.Errorf("tie broken as %v, want the more recent first", tie)
	}
}

// The dropdown's best free guess: whoever just spoke.
func TestRecentSpeakers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@old:x", Body: "first", Timestamp: at(10)},
		{ID: "$2", RoomID: "!a:x", Sender: "@recent:x", Body: "second", Timestamp: at(30)},
		{ID: "$3", RoomID: "!a:x", Sender: "@old:x", Body: "third", Timestamp: at(20)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	// A different room, saved separately — SaveMessages files every message under
	// the room it is given, not the one on the message.
	if err := cache.SaveMessages(ctx, "!other:x", []domain.Message{
		{ID: "$4", RoomID: "!other:x", Sender: "@elsewhere:x", Body: "other room", Timestamp: at(99)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	got, err := cache.RecentSpeakers(ctx, "!a:x", 10)
	if err != nil {
		t.Fatalf("RecentSpeakers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want two distinct speakers", got)
	}
	// Ranked by each speaker's most recent message, not by message order.
	if got[0] != "@recent:x" || got[1] != "@old:x" {
		t.Errorf("speakers = %v, want recent then old", got)
	}
	// Scoped to the room.
	for _, sender := range got {
		if sender == "@elsewhere:x" {
			t.Error("another room's speaker leaked in")
		}
	}
}

// Who a search can be narrowed to: the people who actually posted, across whatever
// rooms the scope covers, ranked by how much they said.
func TestSearchSenders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@quiet:x", Body: "one", Timestamp: at(90)},
		{ID: "$2", RoomID: "!a:x", Sender: "@loud:x", Body: "two", Timestamp: at(10)},
		{ID: "$3", RoomID: "!a:x", Sender: "@loud:x", Body: "three", Timestamp: at(20)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	if err := cache.SaveMessages(ctx, "!b:x", []domain.Message{
		{ID: "$4", RoomID: "!b:x", Sender: "@elsewhere:x", Body: "another room", Timestamp: at(99)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	// A name comes from the membership, and only for those who have one.
	if err := cache.SaveMembers(ctx, "!a:x", []domain.Member{
		{UserID: "@loud:x", DisplayName: "Loud Larry"},
		{UserID: "@quiet:x"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}

	// Scoped to one room: whoever said most first.
	got, err := cache.SearchSenders(ctx, domain.TheseRooms([]domain.RoomID{"!a:x"}), 0)
	if err != nil {
		t.Fatalf("SearchSenders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want the two people who posted in !a:x", got)
	}
	if got[0].UserID != "@loud:x" || got[0].DisplayName != "Loud Larry" {
		t.Errorf("first = %+v, want the most prolific sender, named", got[0])
	}
	// No membership row with a name: offered under the MXID rather than dropped,
	// because that is what the filter matches on anyway.
	if got[1].UserID != "@quiet:x" || got[1].DisplayName != "" {
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

	// The limit caps the ranking rather than truncating it arbitrarily.
	top, err := cache.SearchSenders(ctx, domain.EveryRoom(), 1)
	if err != nil {
		t.Fatalf("SearchSenders(limit): %v", err)
	}
	if len(top) != 1 || top[0].UserID != "@loud:x" {
		t.Errorf("limit 1 returned %+v, want only the most prolific", top)
	}
}

// SaveMember is the sync stream's writer: one person at a time, and it must not
// disturb anyone else in the room. SaveMembers replaces the roster; this one edits
// a row in it.
func TestSaveMemberUpsertsWithoutClearingTheRoom(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveMembers(ctx, "!a:x", []domain.Member{
		{UserID: "@alice:x", DisplayName: "Alice"},
		{UserID: "@bob:x", DisplayName: "Bob"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}

	// A newcomer joins: added, and the two already there are untouched.
	if err := cache.SaveMember(ctx, "!a:x", domain.Member{UserID: "@carol:x", DisplayName: "Carol"}); err != nil {
		t.Fatalf("SaveMember: %v", err)
	}
	if got := memberNames(t, cache, "!a:x"); len(got) != 3 || got["@alice:x"] != "Alice" || got["@carol:x"] != "Carol" {
		t.Fatalf("after a join: %v", got)
	}

	// A rename is the same event type, so it must overwrite rather than fail on the
	// primary key or add a second row for the same person.
	if err := cache.SaveMember(ctx, "!a:x", domain.Member{UserID: "@bob:x", DisplayName: "Robert"}); err != nil {
		t.Fatalf("SaveMember rename: %v", err)
	}
	got := memberNames(t, cache, "!a:x")
	if len(got) != 3 {
		t.Fatalf("rename changed the roster size: %v", got)
	}
	if got["@bob:x"] != "Robert" {
		t.Errorf("display_name = %q after rename, want %q", got["@bob:x"], "Robert")
	}
}

// A leave has to remove the row, or the mention dropdown keeps offering someone who
// is gone and Space.Keeper keeps counting the room toward their reach.
func TestRemoveMemberDropsOnlyThatPerson(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveMembers(ctx, "!a:x", []domain.Member{
		{UserID: "@alice:x", DisplayName: "Alice"},
		{UserID: "@bob:x", DisplayName: "Bob"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	if err := cache.RemoveMember(ctx, "!a:x", "@alice:x"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	got := memberNames(t, cache, "!a:x")
	if _, still := got["@alice:x"]; still {
		t.Error("@alice:x survived the leave")
	}
	if got["@bob:x"] != "Bob" {
		t.Errorf("@bob:x was collateral: %v", got)
	}

	// Leaves arrive for people whose join this cache never saw — a cold start that
	// begins mid-conversation. That is a no-op, not an error.
	if err := cache.RemoveMember(ctx, "!a:x", "@ghost:x"); err != nil {
		t.Errorf("RemoveMember on an unknown member: %v", err)
	}
	if err := cache.RemoveMember(ctx, "!never:x", "@ghost:x"); err != nil {
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

// Naming one person twice (an MXID and a name that resolves to it) narrows to the
// rooms they are in, not to none: the count the query matches against is of
// distinct people.
func TestRoomsWithCountsEachPersonOnce(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!r:x")
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: room, Name: "R"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMembers(ctx, room, []domain.Member{{UserID: "@d:x", DisplayName: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{"@d:x"}, {"@d:x", "@d:x"}, {"@d:x", " @d:x "}} {
		got, err := cache.RoomsWith(ctx, ids, domain.EveryRoom(), 10)
		if err != nil || len(got) != 1 {
			t.Errorf("RoomsWith(%q) = %d rooms, %v; want the one room", ids, len(got), err)
		}
	}
}

// Rooms equally recent (no messages cached, or bridged ones stamped alike) come back
// in one order every time, not whichever the query planner met first.
func TestRoomsWithOrdersTiesByRoom(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	rooms := []domain.Room{{ID: "!c:x", Name: "C"}, {ID: "!a:x", Name: "A"}, {ID: "!b:x", Name: "B"}}
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, rooms); err != nil {
		t.Fatal(err)
	}
	for _, r := range rooms {
		if err := cache.SaveMembers(ctx, r.ID, []domain.Member{{UserID: "@d:x"}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := cache.RoomsWith(ctx, []string{"@d:x"}, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]domain.RoomID, len(got))
	for i := range got {
		ids[i] = got[i].ID
	}
	if want := []domain.RoomID{"!a:x", "!b:x", "!c:x"}; !slices.Equal(ids, want) {
		t.Errorf("rooms with no activity came back %v, want by room ID %v", ids, want)
	}
}
