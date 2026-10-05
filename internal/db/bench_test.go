package db

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Benchmarks for the cache's hot queries at a realistic size: 400 rooms holding 500
// messages each (200k in all, about 2.7M words), words Zipf-distributed so common prefixes hit many
// rows, as real text does. Run:
//
//	go test ./internal/db -run xxx -bench Queries -benchmem
//
// Seeding takes a while, so every query is a sub-benchmark of one fixture.

const (
	benchRooms   = 400
	benchPerRoom = 500
	benchMe      = "@me:example.org"
)

// benchWords is a vocabulary: common English words first (the Zipf head), then
// generated ones sharing their prefixes.
func benchWords() []string {
	head := strings.Fields(`the to and of a in is it you that for on this we be with
		have are not can will just so but what about do if all at meeting plan planning
		please thanks think then there they time today tomorrow project problem review
		release ready really right send sent should standup status sure team test
		testing that's thing things through update working would yes`)
	words := append([]string(nil), head...)
	for i := range 3000 {
		words = append(words, fmt.Sprintf("%s%c%c", head[i%len(head)], 'a'+rune(i%26), 'a'+rune(i/26%26)))
	}
	return words
}

// seedBench fills a cache with the fixture and returns it with the rooms it made.
func seedBench(b *testing.B) (*Cache, []domain.RoomID) {
	b.Helper()
	ctx := context.Background()
	cache, err := Open(ctx, filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cache.Close() })
	cache.UseSelves(func() []string { return []string{benchMe} })

	words := benchWords()
	rng := rand.New(rand.NewPCG(7, 11))
	zipf := rand.NewZipf(rng, 1.1, 1, uint64(len(words)-1))
	senders := []string{benchMe, "@dana:example.org", "@sam:example.org", "@alex:example.org", "@noa:example.org"}
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	rooms := make([]domain.RoomID, benchRooms)
	joined := make([]domain.Room, benchRooms)
	for r := range rooms {
		rooms[r] = domain.RoomID(fmt.Sprintf("!room%03d:example.org", r))
		joined[r] = domain.Room{ID: rooms[r], Name: fmt.Sprintf("Room %d", r)}
	}
	// All at once: SaveRooms is the whole joined set, and sweeps any room it omits.
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, joined); err != nil {
		b.Fatal(err)
	}
	for r := range rooms {
		msgs := make([]domain.Message, benchPerRoom)
		var reactions []domain.Reaction
		for i := range msgs {
			n := 4 + rng.IntN(20) // a chat message: a few words to a couple of sentences
			body := make([]string, n)
			for w := range body {
				body[w] = words[zipf.Uint64()]
			}
			msgs[i] = domain.Message{
				ID:        domain.EventID(fmt.Sprintf("$r%dm%d", r, i)),
				RoomID:    rooms[r],
				Sender:    senders[(r+i)%len(senders)],
				Body:      strings.Join(body, " "),
				Timestamp: start.Add(time.Duration(r*benchPerRoom+i) * time.Minute),
				Mentioned: i%97 == 0,
			}
			if i%4 == 0 {
				reactions = append(reactions, domain.Reaction{
					ID: domain.EventID(fmt.Sprintf("$r%dx%d", r, i)), RoomID: rooms[r], Target: msgs[i].ID,
					Sender: senders[(r+i+1)%len(senders)], Key: []string{"👍", "🎉", "❤️", "😂"}[i%4],
				})
			}
		}
		if err := cache.SaveMessages(ctx, rooms[r], msgs); err != nil {
			b.Fatal(err)
		}
		if err := cache.SaveReactions(ctx, reactions); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := cache.db.ExecContext(ctx, "ANALYZE"); err != nil {
		b.Fatal(err)
	}
	var n int
	if err := cache.db.QueryRowContext(ctx, "SELECT count(*) FROM messages").Scan(&n); err != nil {
		b.Fatal(err)
	}
	if n != benchRooms*benchPerRoom {
		b.Fatalf("fixture holds %d messages, want %d", n, benchRooms*benchPerRoom)
	}
	return cache, rooms
}

func BenchmarkQueries(b *testing.B) {
	cache, rooms := seedBench(b)
	ctx := context.Background()
	room := rooms[17]
	space := rooms[:30]

	run := func(name string, query func() error) {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := query(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	run("Messages/window=50", func() error { _, err := cache.Messages(ctx, room, 50); return err })
	run("Reactions/room", func() error { _, err := cache.Reactions(ctx, room); return err })
	run("Search/global", func() error {
		_, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "planning review"}, Limit: 50, Rooms: domain.EveryRoom()})
		return err
	})
	run("Search/room", func() error {
		_, err := cache.SearchMessages(ctx, domain.SearchRequest{
			Filter: domain.SearchFilter{Terms: "planning review"}, Rooms: domain.TheseRooms([]domain.RoomID{room}), Limit: 50,
		})
		return err
	})
	run("Search/common-prefix", func() error {
		_, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "th*"}, Limit: 50, Rooms: domain.EveryRoom()})
		return err
	})
	run("CountUnreadAll", func() error { _, err := cache.CountUnreadAll(ctx, []string{benchMe}); return err })
	run("EmojiScores/reaction", func() error {
		_, err := cache.EmojiScores(ctx, domain.EmojiReaction, room, space, []string{benchMe}, "room")
		return err
	})
	run("LastMessages", func() error { _, err := cache.LastMessages(ctx); return err })
	run("SaveMessages/one", func() error {
		return cache.SaveMessages(ctx, room, []domain.Message{{
			ID: domain.EventID(fmt.Sprintf("$live%d", time.Now().UnixNano())), RoomID: room, Sender: "@dana:example.org",
			Body: "the planning review is ready", Timestamp: time.Now(),
		}})
	})
}
