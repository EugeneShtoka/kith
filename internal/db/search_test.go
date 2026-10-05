package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Shorthands, so the seed tables below read as data rather than as type names.
type domainMessage = domain.Message

func domainRoomID(s string) domain.RoomID   { return domain.RoomID(s) }
func domainEventID(s string) domain.EventID { return domain.EventID(s) }

const (
	domain_HighlightStart = domain.HighlightStart
	domain_HighlightEnd   = domain.HighlightEnd
)

func TestFTSQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		// A single word narrows as you type.
		{"deploy", `"deploy"*`},
		// Several words are ANDed; only the last is still being typed.
		{"code review", `"code" "review"*`},
		{"  code   review  ", `"code" "review"*`},
		// A closed phrase stays whole and is not prefix-matched.
		{`"code review"`, `"code review"`},
		{`"code review" later`, `"code review" "later"*`},
		{`deploy "on friday"`, `"deploy" "on friday"`},
		// An unterminated phrase is someone mid-typing, so it still searches.
		{`"code rev`, `"code rev"*`},
		// Punctuation is quoted, never interpreted.
		{"what?", `"what?"*`},
		{"-x", `"-x"*`},
		{"foo(", `"foo("*`},
		{"a*", `"a*"*`},
		{"NEAR", `"NEAR"*`},
		{"AND OR NOT", `"AND" "OR" "NOT"*`},
		// An embedded quote is doubled, which is FTS5's own escape.
		{`say "hi`, `"say" "hi"*`},
		{`a"b`, `"a" "b"*`}, // a quote delimits a phrase wherever it appears
		// Nothing searchable.
		{"", ""},
		{"   ", ""},
		{`""`, ""},
		{`" "`, `" "`},
	}
	for _, tc := range tests {
		if got := ftsQuery(tc.in); got != tc.want {
			t.Errorf("ftsQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Whatever the user types, the query must run. FTS5's parser is unforgiving and a
// chat search box receives every character on the keyboard.
func TestSearchNeverErrorsOnUserInput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	seedSearchable(t, cache)

	hostile := []string{
		`?`, `*`, `(`, `)`, `"`, `""`, `"""`, `-`, `-x`, `^`, `:`, `.`, `,`,
		`AND`, `OR`, `NOT`, `NEAR`, `NEAR(a b)`, `a AND`, `AND a`,
		`col:val`, `body:x`, `{a}`, `[a]`, `a~b`, `%`, `_`, `\`, `'`, `''`,
		`deploy)`, `(deploy`, `"deploy`, `deploy"`, `**`, `a**b`,
		strings.Repeat("a", 500), "emoji 🎉", "שלום", "\t\n",
	}
	for _, q := range hostile {
		if _, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch(q, time.Now()), Rooms: domain.EveryRoom(), Limit: 20}); err != nil {
			t.Errorf("SearchMessages(%q) errored: %v", q, err)
		}
	}
}

func TestSearchMessages(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	seedSearchable(t, cache)

	t.Run("finds a word anywhere in the body", func(t *testing.T) {
		hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("deploy", time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
		if err != nil {
			t.Fatalf("SearchMessages: %v", err)
		}
		if len(hits) != 2 {
			t.Fatalf("got %d hits, want 2: %+v", len(hits), hits)
		}
		// Newest first: time is the axis you scan a conversation on.
		if !hits[0].Timestamp.After(hits[1].Timestamp) {
			t.Errorf("hits are not newest-first: %v then %v", hits[0].Timestamp, hits[1].Timestamp)
		}
	})

	t.Run("carries what the list needs", func(t *testing.T) {
		hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("blocked", time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
		if err != nil || len(hits) != 1 {
			t.Fatalf("hits = %+v (err %v)", hits, err)
		}
		h := hits[0]
		if h.RoomID != "!a:x" || h.EventID != "$3" {
			t.Errorf("hit points at %s/%s, want !a:x/$3", h.RoomID, h.EventID)
		}
		if h.Sender != "@bob:x" || h.SenderName != "Bob" {
			t.Errorf("sender = %q/%q", h.Sender, h.SenderName)
		}
		if !strings.Contains(h.Snippet, domain_HighlightStart+"blocked"+domain_HighlightEnd) {
			t.Errorf("snippet %q should highlight the match", h.Snippet)
		}
	})

	t.Run("scopes to one room", func(t *testing.T) {
		all, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("deploy", time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		scoped, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("deploy", time.Now()), Rooms: domain.TheseRooms([]domain.RoomID{"!b:x"}), Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 2 || len(scoped) != 1 {
			t.Fatalf("global %d hits, scoped %d — want 2 and 1", len(all), len(scoped))
		}
		if scoped[0].RoomID != "!b:x" {
			t.Errorf("scoped hit is in %s", scoped[0].RoomID)
		}
	})

	t.Run("all terms must match", func(t *testing.T) {
		both, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("deploy blocked", time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(both) != 0 {
			t.Errorf("got %d hits for two words in different messages, want 0", len(both))
		}
		same, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("review blocked", time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(same) != 1 {
			t.Errorf("got %d hits for two words in one message, want 1", len(same))
		}
	})

	t.Run("narrows as you type", func(t *testing.T) {
		for _, prefix := range []string{"d", "de", "dep", "deplo", "deploy"} {
			hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch(prefix, time.Now()), Rooms: domain.TheseRooms([]domain.RoomID{"!b:x"}), Limit: 20})
			if err != nil {
				t.Fatalf("%q: %v", prefix, err)
			}
			if len(hits) == 0 {
				t.Errorf("%q found nothing — a prefix should match the word being typed", prefix)
			}
		}
	})

	t.Run("a closed phrase is exact", func(t *testing.T) {
		phrase, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch(`"review blocked"`, time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(phrase) != 1 {
			t.Errorf(`"review blocked" as a phrase found %d, want 1`, len(phrase))
		}
		reversed, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch(`"blocked review"`, time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(reversed) != 0 {
			t.Errorf("the words in the wrong order should not match a phrase, got %d", len(reversed))
		}
	})

	t.Run("non-Latin scripts", func(t *testing.T) {
		hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("תודה", time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 {
			t.Errorf("Hebrew search found %d, want 1", len(hits))
		}
	})

	t.Run("diacritics fold", func(t *testing.T) {
		// remove_diacritics folds ü to u, so the umlaut need not be typed.
		for _, q := range []string{"grusse", "grüsse", "GRUSSE"} {
			hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch(q, time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
			if err != nil {
				t.Fatalf("%q: %v", q, err)
			}
			if len(hits) != 1 {
				t.Errorf("Grüsse should be found by %q, got %d hits", q, len(hits))
			}
		}
	})

	t.Run("respects the limit", func(t *testing.T) {
		hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("the", time.Now()), Rooms: domain.EveryRoom(), Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) > 1 {
			t.Errorf("got %d hits with limit 1", len(hits))
		}
	})

	t.Run("nothing searchable finds nothing, quietly", func(t *testing.T) {
		for _, q := range []string{"", "   ", "?!", "-"} {
			hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch(q, time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
			if err != nil {
				t.Errorf("%q errored: %v", q, err)
			}
			if len(hits) != 0 {
				t.Errorf("%q found %d hits", q, len(hits))
			}
		}
	})
}

// The index is maintained by triggers, so it must track every way a message can
// change — and a redaction must take the text out of results even though the cache
// keeps it for the "(deleted)" placeholder.
func TestSearchIndexFollowsMessageChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }

	save := func(msgs ...domainMessage) {
		t.Helper()
		if err := cache.SaveMessages(ctx, "!r:x", msgs); err != nil {
			t.Fatalf("SaveMessages: %v", err)
		}
	}
	count := func(q string) int {
		t.Helper()
		hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch(q, time.Now()), Rooms: domain.EveryRoom(), Limit: 50})
		if err != nil {
			t.Fatalf("SearchMessages(%q): %v", q, err)
		}
		return len(hits)
	}

	// Insert.
	save(domainMessage{ID: "$1", RoomID: "!r:x", Sender: "@a:x", Body: "original pineapple", Timestamp: at(1)})
	if count("pineapple") != 1 {
		t.Fatal("a new message should be searchable")
	}

	// Edit — the upsert's ON CONFLICT path is an UPDATE, which is a different
	// trigger from the insert.
	save(domainMessage{ID: "$1", RoomID: "!r:x", Sender: "@a:x", Body: "edited mango", Timestamp: at(1), Edited: true})
	if count("pineapple") != 0 {
		t.Error("the replaced text should no longer be searchable")
	}
	if count("mango") != 1 {
		t.Error("the edited text should be searchable")
	}

	// Redaction — the row survives for the placeholder, but must leave results.
	if err := cache.MarkRedacted(ctx, "!r:x", "$1", "", "", time.Time{}, false); err != nil {
		t.Fatalf("MarkRedacted: %v", err)
	}
	if count("mango") != 0 {
		t.Error("a redacted message must not appear in results")
	}

	// Delete, via Clear.
	save(domainMessage{ID: "$2", RoomID: "!r:x", Sender: "@a:x", Body: "kumquat", Timestamp: at(2)})
	if count("kumquat") != 1 {
		t.Fatal("setup")
	}
	if err := cache.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if count("kumquat") != 0 {
		t.Error("Clear should empty the search index too")
	}
}

// The trim that caps a room at testKeep deletes rows, which must also
// leave the index — otherwise search would offer hits it can no longer jump to.
func TestSearchIndexFollowsTrim(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	cache.UseKeep(func(domain.RoomID) int { return testKeep })
	msgs := make([]domainMessage, 0, testKeep+5)
	for i := range testKeep + 5 {
		msgs = append(msgs, domainMessage{
			ID:        domainEventID(fmt.Sprintf("$%d", i)),
			RoomID:    "!r:x",
			Sender:    "@a:x",
			Body:      fmt.Sprintf("message tangerine %d", i),
			Timestamp: time.UnixMilli(int64(i) * 1000),
		})
	}
	if err := cache.SaveMessages(ctx, "!r:x", msgs); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	// The five oldest were trimmed, so their text must be gone from the index.
	hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("tangerine", time.Now()), Rooms: domain.EveryRoom(), Limit: testKeep + 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != testKeep {
		t.Errorf("index holds %d trimmed-table rows, want %d", len(hits), testKeep)
	}
	for _, h := range hits {
		if h.EventID == "$0" || h.EventID == "$4" {
			t.Errorf("%s was trimmed from the table but is still indexed", h.EventID)
		}
	}
}

// seedSearchable fills two rooms with messages the search tests look for.
func seedSearchable(t *testing.T, cache *Cache) {
	t.Helper()
	ctx := context.Background()
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }
	rooms := map[string][]domainMessage{
		"!a:x": {
			{ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "the deploy is out", Timestamp: at(10)},
			{ID: "$3", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "review blocked on the tests", Timestamp: at(30)},
			{ID: "$4", RoomID: "!a:x", Sender: "@dana:x", SenderName: "Dana", Body: "תודה רבה", Timestamp: at(40)},
			{ID: "$5", RoomID: "!a:x", Sender: "@eva:x", SenderName: "Eva", Body: "Grüsse aus Berlin", Timestamp: at(50)},
		},
		"!b:x": {
			{ID: "$2", RoomID: "!b:x", Sender: "@carol:x", SenderName: "Carol", Body: "can we deploy tomorrow", Timestamp: at(20)},
		},
	}
	for room, msgs := range rooms {
		if err := cache.SaveMessages(ctx, domainRoomID(room), msgs); err != nil {
			t.Fatalf("SaveMessages(%s): %v", room, err)
		}
	}
}
