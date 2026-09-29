package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/vocab"
)

// vocab.Fold and spell.Words must see the words the full-text index sees, or recent
// completion offers words the index spells differently. Compared per message, over
// the words completion can offer (letters only, 3+ long). Numbers, code and URLs are
// skipped by spell.Words on purpose; those are not compared.
func TestRecentVocabTokensAgreeWithTheIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	bodies := []string{
		"The Café opened; naïve déjà-vu at Ångström's.",
		"Deploying the release today",
		"Привет, как дела? Всё хорошо, ёлка стоит.",
		"שלום, מה שלומך? אני בסדר גמור",
		"don't won't it's O'Brien well-known co-operate",
		"Straße GROSS Überraschung façade piñata",
		"emoji 🎉 then words: résumé, coöperate, jalapeño",
	}
	if err := cache.SaveRooms(ctx, []domain.Room{{ID: "!a:x"}}); err != nil {
		t.Fatal(err)
	}
	// The index's own terms, through a vocabulary view on this connection only: the
	// schema has none, since nothing but this test would read it.
	if _, err := cache.db.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.index_terms USING fts5vocab(main, messages_fts, 'instance')`); err != nil {
		t.Fatal(err)
	}
	for i, body := range bodies {
		if err := cache.SaveMessages(ctx, "!a:x", []domain.Message{{ID: domain.EventID(fmt.Sprintf("$%d", i)),
			RoomID: "!a:x", Body: body, Timestamp: time.Unix(int64(1_700_000_000+i), 0)}}); err != nil {
			t.Fatal(err)
		}
	}

	completable := func(w string) bool {
		return len([]rune(w)) >= 3 && !strings.ContainsFunc(w, func(r rune) bool { return !unicode.IsLetter(r) })
	}
	agree, total := 0, 0
	for i, body := range bodies {
		terms, err := collect(ctx, cache.db, "index terms",
			`SELECT v.term FROM temp.index_terms v JOIN messages m ON m.rowid = v.doc WHERE m.event_id = ?`,
			func(rows *sql.Rows) (string, error) {
				var term string
				err := rows.Scan(&term)
				return term, err
			}, fmt.Sprintf("$%d", i))
		if err != nil {
			t.Fatal(err)
		}
		index := slices.DeleteFunc(terms, func(term string) bool { return !completable(term) })
		ours := map[string]bool{}
		for _, w := range vocab.Words(body) {
			ours[w] = true
		}
		var missing []string
		for _, term := range index {
			total++
			if ours[term] {
				agree++
			} else {
				missing = append(missing, term)
			}
		}
		if len(missing) > 0 {
			t.Logf("%q: the index has %v, recent completion does not", body, missing)
		}
	}
	rate := float64(agree) / float64(total)
	t.Logf("agreement: %d of %d completable index terms (%.0f%%)", agree, total, 100*rate)
	if rate < 0.95 {
		t.Errorf("recent completion agrees with the index on only %.0f%% of words", 100*rate)
	}

	// The one deliberate difference: the index has the words of URLs and inline code,
	// and recent completion does not offer them.
	for _, word := range vocab.Words("see https://example.org/path and `make check` now") {
		if slices.Contains([]string{"https", "example", "org", "path", "make", "check"}, word) {
			t.Errorf("%q from a URL or code span is counted", word)
		}
	}
}

// BenchmarkRecentCompletion measures recent-window completion on the 200k fixture:
// building each window from the cache, and ranking a prefix over them.
func BenchmarkRecentCompletion(b *testing.B) {
	cache, rooms := seedBench(b)
	ctx := context.Background()
	room, space := rooms[17], rooms[:30]
	build := func(rooms domain.RoomSet, sender string, n int) *vocab.Window {
		w := vocab.NewWindow(n)
		bodies, err := cache.RecentBodies(ctx, rooms, sender, n)
		if err != nil {
			b.Fatal(err)
		}
		for _, body := range bodies {
			w.Add(body)
		}
		return w
	}
	var scope vocab.Scope
	b.Run("build/room", func(b *testing.B) {
		for b.Loop() {
			scope.Room = build(domain.TheseRooms([]domain.RoomID{room}), "", 1000)
		}
	})
	b.Run("build/space", func(b *testing.B) {
		for b.Loop() {
			scope.Space = build(domain.TheseRooms(space), "", 3000)
		}
	})
	b.Run("build/mine", func(b *testing.B) {
		for b.Loop() {
			scope.Mine = build(domain.EveryRoom(), benchMe, 2000)
		}
	})
	b.Run("build/global", func(b *testing.B) {
		for b.Loop() {
			scope.Global = build(domain.EveryRoom(), "", 5000)
		}
	})

	for _, prefix := range []string{"the", "pla", "zzz"} {
		b.Run("rank/"+prefix, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = vocab.Rank(prefix, scope, 8)
			}
		})
	}
}

// RecentBodies is newest first, bounded, and never a taken-back message; rooms and
// sender narrow it.
func TestRecentBodies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, []domain.Room{{ID: "!a:x"}, {ID: "!b:x"}}); err != nil {
		t.Fatal(err)
	}
	for i, m := range []struct {
		room   domain.RoomID
		sender string
		body   string
	}{
		{"!a:x", "@me:x", "first"}, {"!b:x", "@dana:x", "second"}, {"!a:x", "@dana:x", "third"},
		{"!a:x", "@me:x", "taken back"},
	} {
		if err := cache.SaveMessages(ctx, m.room, []domain.Message{{ID: domain.EventID(fmt.Sprintf("$%d", i)),
			RoomID: m.room, Sender: m.sender, Body: m.body, Timestamp: time.Unix(int64(1_700_000_000+i), 0)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cache.MarkRedacted(ctx, "!a:x", "$3", "@me:x", "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		rooms  domain.RoomSet
		sender string
		n      int
		want   []string
	}{
		{"everything, newest first", domain.EveryRoom(), "", 10, []string{"third", "second", "first"}},
		{"bounded", domain.EveryRoom(), "", 2, []string{"third", "second"}},
		{"one room", domain.TheseRooms([]domain.RoomID{"!a:x"}), "", 10, []string{"third", "first"}},
		{"one sender", domain.EveryRoom(), "@me:x", 10, []string{"first"}},
		{"none asked for", domain.EveryRoom(), "", 0, nil},
		{"no rooms named", domain.TheseRooms(nil), "", 10, nil},
	}
	for _, c := range cases {
		got, err := cache.RecentBodies(ctx, c.rooms, c.sender, c.n)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
