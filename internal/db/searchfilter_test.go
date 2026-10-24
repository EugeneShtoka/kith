package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// filteredCache holds three people talking across three days, so each filter is
// distinguishable.
func filteredCache(t *testing.T) *Cache {
	t.Helper()
	cache := openTemp(t)
	base := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	msgs := []struct {
		room, id, sender, name, body string
		day                          int
	}{
		{mx + "!a:x", mx + "$1", "matrix:@dana:x", "Dana", "the invoice is attached", 0},
		{mx + "!a:x", mx + "$2", "matrix:@bob:x", "Bob", "thanks for the invoice", 1},
		{mx + "!b:x", mx + "$3", "matrix:@dana:x", "Dana", "another invoice entirely", 2},
		{mx + "!c:x", mx + "$4", "matrix:@carol:x", "Carol", "invoice from the printer", 2},
	}
	for _, m := range msgs {
		mustSave(t, cache, domain.RoomID(m.room), domain.Message{
			ID: domain.EventID(m.id), RoomID: domain.RoomID(m.room),
			Sender: m.sender, SenderName: m.name, Body: m.body,
			Timestamp: base.AddDate(0, 0, m.day),
		})
	}
	return cache
}

func search(t *testing.T, cache *Cache, req domain.SearchRequest) []domain.SearchHit {
	t.Helper()
	if req.Limit == 0 {
		req.Limit = 50
	}
	hits, err := cache.SearchMessages(context.Background(), req)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	return hits
}

// The scope is a set of rooms: one room, a space's rooms or every room.
func TestSearchScopesToARoomSet(t *testing.T) {
	cache := filteredCache(t)
	all := search(t, cache, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "invoice"}, Rooms: domain.EveryRoom()})
	if len(all) != 4 {
		t.Fatalf("unscoped found %d, want 4", len(all))
	}
	space := search(t, cache, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: "invoice"},
		Rooms:  domain.TheseRooms([]domain.RoomID{mx + "!a:x", mx + "!b:x"}),
	})
	if len(space) != 3 {
		t.Errorf("two rooms found %d, want 3", len(space))
	}
	one := search(t, cache, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: "invoice"},
		Rooms:  domain.TheseRooms([]domain.RoomID{mx + "!c:x"}),
	})
	if len(one) != 1 || one[0].EventID != mx+"$4" {
		t.Errorf("one room = %+v, want just $4", one)
	}
}

// A sender filter matches the MXID and the display name.
func TestSearchFiltersBySender(t *testing.T) {
	cache := filteredCache(t)
	for _, who := range []string{"matrix:@dana:x", "dana", "Dana"} {
		hits := search(t, cache, domain.SearchRequest{
			Filter: domain.SearchFilter{Terms: "invoice", Sender: who},
			Rooms:  domain.EveryRoom(),
		})
		if len(hits) != 2 {
			t.Errorf("from:%s found %d, want Dana's 2", who, len(hits))
		}
	}
}

func TestSearchFiltersByDateSpan(t *testing.T) {
	cache := filteredCache(t)
	day1 := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	since := search(t, cache, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: "invoice", Since: day1},
		Rooms:  domain.EveryRoom(),
	})
	if len(since) != 3 {
		t.Errorf("since day 1 found %d, want 3", len(since))
	}
	until := search(t, cache, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: "invoice", Until: day1},
		Rooms:  domain.EveryRoom(),
	})
	if len(until) != 1 || until[0].EventID != mx+"$1" {
		t.Errorf("until day 1 = %+v, want just the first day", until)
	}
	span := search(t, cache, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: "invoice", Since: day1, Until: day1.AddDate(0, 0, 1)},
		Rooms:  domain.EveryRoom(),
	})
	if len(span) != 1 || span[0].EventID != mx+"$2" {
		t.Errorf("a one-day span = %+v, want just $2", span)
	}
}

// A search with filters but no terms drops the full-text join (FTS5 has no safe
// match-all).
func TestSearchWithNoTermsButFilters(t *testing.T) {
	cache := filteredCache(t)
	hits := search(t, cache, domain.SearchRequest{Filter: domain.SearchFilter{Sender: "matrix:@dana:x"}, Rooms: domain.EveryRoom()})
	if len(hits) != 2 {
		t.Errorf("sender-only search found %d, want Dana's 2", len(hits))
	}
	if got := search(t, cache, domain.SearchRequest{}); len(got) != 0 {
		t.Errorf("an empty request found %d, want none", len(got))
	}
}

// A name containing a LIKE wildcard must match itself, not everything.
func TestSearchSenderWildcardsAreLiteral(t *testing.T) {
	cache := openTemp(t)
	mustSave(t, cache, mx+"!a:x", domain.Message{
		ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@odd:x", SenderName: "100%", Body: "hello",
		Timestamp: time.Now(),
	})
	mustSave(t, cache, mx+"!a:x", domain.Message{
		ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob", Body: "hello",
		Timestamp: time.Now(),
	})
	hits := search(t, cache, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "hello", Sender: "%"}, Rooms: domain.EveryRoom()})
	if len(hits) != 1 || hits[0].EventID != mx+"$1" {
		t.Errorf("a %% in a name matched %d rows, want only the one called 100%%", len(hits))
	}
}

// The file list is a search with no terms: messages with an attachment, newest first.
func TestSearchNarrowsToMessagesWithAFile(t *testing.T) {
	t.Parallel()

	cache := openTemp(t)
	base := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	msgs := []struct {
		id, body string
		media    *domain.Media
		day      int
	}{
		{mx + "$1", "just talking", nil, 0},
		{mx + "$2", "photo.png", &domain.Media{Type: domain.MediaImage, Name: "photo.png", Mime: "image/png"}, 1},
		{mx + "$3", "talking again", nil, 2},
		{mx + "$4", "report.pdf", &domain.Media{Type: domain.MediaFile, Name: "report.pdf", Mime: "application/pdf"}, 3},
	}
	for _, m := range msgs {
		mustSave(t, cache, mx+"!a:x", domain.Message{
			ID: domain.EventID(m.id), RoomID: mx + "!a:x", Sender: "matrix:@dana:x", SenderName: "Dana",
			Body: m.body, Media: m.media, Timestamp: base.AddDate(0, 0, m.day),
		})
	}

	hits := search(t, cache, domain.SearchRequest{Filter: domain.SearchFilter{HasFile: true}, Rooms: domain.EveryRoom()})
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want the two messages carrying a file", len(hits))
	}
	if hits[0].EventID != mx+"$4" || hits[1].EventID != mx+"$2" {
		t.Errorf("hits = %q, %q; want $4 then $2", hits[0].EventID, hits[1].EventID)
	}
	if hits[0].FileName != "report.pdf" || hits[1].FileName != "photo.png" {
		t.Errorf("names = %q, %q; want the attachment names", hits[0].FileName, hits[1].FileName)
	}

	// Without the clause, file-less messages return too and files keep their name.
	all := search(t, cache, domain.SearchRequest{Filter: domain.SearchFilter{Sender: "dana"}, Rooms: domain.EveryRoom()})
	if len(all) != 4 {
		t.Fatalf("got %d hits unfiltered, want all four", len(all))
	}
	named := 0
	for _, hit := range all {
		if hit.FileName != "" {
			named++
		}
	}
	if named != 2 {
		t.Errorf("%d hits carried a file name, want 2 — the join is not filter-dependent", named)
	}
}

// The file clause and full-text terms compose.
func TestSearchCombinesFileClauseWithTerms(t *testing.T) {
	t.Parallel()

	cache := openTemp(t)
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	for _, m := range []struct {
		id, body string
		media    *domain.Media
	}{
		{mx + "$1", "the quarterly report", nil},
		{mx + "$2", "quarterly numbers", &domain.Media{Type: domain.MediaFile, Name: "q3.xlsx"}},
		{mx + "$3", "holiday snap", &domain.Media{Type: domain.MediaImage, Name: "beach.jpg"}},
	} {
		mustSave(t, cache, mx+"!a:x", domain.Message{
			ID: domain.EventID(m.id), RoomID: mx + "!a:x", Sender: "matrix:@dana:x",
			Body: m.body, Media: m.media, Timestamp: now,
		})
	}

	hits := search(t, cache, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: "quarterly", HasFile: true},
		Rooms:  domain.EveryRoom(),
	})
	if len(hits) != 1 || hits[0].EventID != mx+"$2" {
		t.Fatalf("got %d hits (%v), want only the quarterly message that has a file", len(hits), hits)
	}
}

// An empty room set reads no room; only EveryRoom reads everything.
func TestAnEmptyRoomSetReadsNoRoom(t *testing.T) {
	t.Parallel()
	cache := filteredCache(t)
	ctx := context.Background()
	for name, set := range map[string]domain.RoomSet{"nil": domain.TheseRooms(nil), "empty": domain.TheseRooms([]domain.RoomID{}), "zero": {}} {
		if hits := search(t, cache, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "invoice"}, Rooms: set}); len(hits) != 0 {
			t.Errorf("%s: SearchMessages found %d, want none", name, len(hits))
		}
		if people, err := cache.SearchSenders(ctx, set, 0); err != nil || len(people) != 0 {
			t.Errorf("%s: SearchSenders = %d, %v; want nobody", name, len(people), err)
		}
		if rooms, err := cache.RoomsWith(ctx, []string{"matrix:@dana:x"}, set, 10); err != nil || len(rooms) != 0 {
			t.Errorf("%s: RoomsWith = %d, %v; want none", name, len(rooms), err)
		}
	}
	if hits := search(t, cache, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "invoice"}, Rooms: domain.EveryRoom()}); len(hits) != 4 {
		t.Errorf("every room: SearchMessages found %d, want 4", len(hits))
	}
	if people, err := cache.SearchSenders(ctx, domain.EveryRoom(), 0); err != nil || len(people) != 3 {
		t.Errorf("every room: SearchSenders = %d, %v; want the 3 who posted", len(people), err)
	}
}
