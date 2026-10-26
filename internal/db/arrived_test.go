package db

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// OnArrived names each room a write gave messages it did not have, with the oldest of
// them, and nothing else: over any sequence of saves (re-saves, edits and deletions of
// cached and uncached messages, several rooms, out-of-order times), what it reports is
// what a model of "which IDs were cached" predicts. A failed write reports nothing.
func TestOnArrivedNamesOnlyMessagesTheCacheLacked(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	rooms := []domain.RoomID{mx + "!a:x", mx + "!b:x"}
	for seed := range uint64(40) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, seed))
			cache, ctx := openTemp(t), context.Background()
			type report struct {
				room  domain.RoomID
				since time.Time
			}
			var got []report
			cache.OnArrived(func(room domain.RoomID, since time.Time) { got = append(got, report{room, since}) })
			cached := map[domain.RoomID]map[domain.EventID]bool{}
			for step := range 30 {
				room := rooms[rng.IntN(len(rooms))]
				var batch []domain.Message
				fresh := map[domain.EventID]bool{}
				for range 1 + rng.IntN(4) {
					id := domain.EventID(fmt.Sprintf("%s$%d", mx, rng.IntN(12)))
					m := domain.Message{ID: id, RoomID: room, Sender: "@s:x", Body: "hi",
						Timestamp: base.Add(time.Duration(rng.IntN(600)) * time.Minute)}
					switch rng.IntN(5) {
					case 0:
						m.Edited, m.RevisionID = true, id+"-edit"
					case 1:
						m.Redacted = true
					}
					batch = append(batch, m)
					if !m.IsUpdate() && !cached[room][id] {
						fresh[id] = true
					}
				}
				got = nil
				failing := rng.IntN(8) == 0
				saveCtx := ctx
				if failing {
					canceled, cancel := context.WithCancel(ctx)
					cancel()
					saveCtx = canceled
				}
				err := cache.SaveMessages(saveCtx, room, batch)
				if failing {
					if err == nil {
						t.Fatalf("step %d: a canceled save succeeded", step)
					}
					if len(got) != 0 {
						t.Fatalf("step %d: a failed save reported %+v", step, got)
					}
					continue
				}
				if err != nil {
					t.Fatalf("step %d: SaveMessages: %v", step, err)
				}
				// The cache, not the batch, is the model's truth: a message that came
				// first as an edit or a deletion is cached all the same, and a batch's
				// copies of one message keep the earliest time.
				now, err := cache.Messages(ctx, room, 1000)
				if err != nil {
					t.Fatal(err)
				}
				if cached[room] == nil {
					cached[room] = map[domain.EventID]bool{}
				}
				var want time.Time
				for _, m := range now {
					if fresh[m.ID] && (want.IsZero() || m.Timestamp.Before(want)) {
						want = m.Timestamp
					}
					cached[room][m.ID] = true
				}
				switch {
				case want.IsZero() && len(got) != 0:
					t.Fatalf("step %d: %+v reported %+v, want nothing", step, batch, got)
				case !want.IsZero() && (len(got) != 1 || got[0].room != room || !got[0].since.Equal(want)):
					t.Fatalf("step %d: %+v reported %+v, want %s from %s", step, batch, got, room, want)
				}
			}
		})
	}
}
