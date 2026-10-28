package db

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// changeKey is one message the generator writes.
type changeKey struct {
	room  domain.RoomID
	event domain.EventID
}

var (
	changeRooms  = []domain.RoomID{mx + "!a:x", mx + "!b:x"}
	changeEvents = []domain.EventID{mx + "$1", mx + "$2", mx + "$3", mx + "$4"}
)

// readAll is every generated message as the cache returns it now.
func readAll(t *testing.T, cache *Cache) map[changeKey]domain.Message {
	t.Helper()
	out := map[changeKey]domain.Message{}
	for _, room := range changeRooms {
		for _, event := range changeEvents {
			msg, ok, err := cache.MessageByID(context.Background(), room, event)
			if err != nil {
				t.Fatalf("MessageByID: %v", err)
			}
			if ok {
				out[changeKey{room, event}] = msg
			}
		}
	}
	return out
}

// changedAfter is every message numbered after from, in number order, as the cache
// holds it.
func changedAfter(t *testing.T, cache *Cache, from int64) []domain.MessageChange {
	t.Helper()
	_, got, err := cache.ChangesAfter(context.Background(), from, 1000)
	if err != nil {
		t.Fatalf("ChangesAfter: %v", err)
	}
	return got
}

// epochOf names the cache's numbering.
func epochOf(t *testing.T, cache *Cache) string {
	t.Helper()
	epoch, _, err := cache.LastChange(context.Background())
	if err != nil {
		t.Fatalf("LastChange: %v", err)
	}
	return epoch
}

// lastChange is the last number the cache gave.
func lastChange(t *testing.T, cache *Cache) int64 {
	t.Helper()
	_, last, err := cache.LastChange(context.Background())
	if err != nil {
		t.Fatalf("LastChange: %v", err)
	}
	return last
}

// changeOp is one write the generator makes, named for the failure message. undone
// marks one that may change a message and change it back in one transaction, which is
// numbered though it reads the same after.
type changeOp struct {
	name   string
	run    func(context.Context, *Cache) error
	undone bool
}

// randomChangeOp draws a write over a small space of rooms, messages and contents, so
// echoes, edits of old messages, deletions before arrival and side rows alone all come up.
func randomChangeOp(r *rand.Rand, keep *int) changeOp {
	room := changeRooms[r.IntN(len(changeRooms))]
	event := changeEvents[r.IntN(len(changeEvents))]
	at := time.UnixMilli(int64(1000 * (1 + r.IntN(4))))
	bodies := []string{"hello", "hi", "bye"}
	body := bodies[r.IntN(len(bodies))]
	copyOf := func() domain.Message {
		m := domain.Message{ID: event, RoomID: room, Sender: "matrix:@a:x", Body: body, Timestamp: at}
		if r.IntN(3) == 0 {
			m.Format = richtext.FromMarkup("<b>" + body + "</b>")
		}
		if r.IntN(3) == 0 {
			m.Media = &domain.Media{Type: "m.image", Name: body + ".png", Width: 1 + r.IntN(2)}
		}
		if r.IntN(3) == 0 {
			m.Mentions = []domain.Mention{{UserID: "matrix:@b:x", Name: body}}
		}
		if r.IntN(4) == 0 {
			m.Seq = int64(1 + r.IntN(2))
		}
		return m
	}
	switch r.IntN(10) {
	case 0, 1:
		m := copyOf()
		return changeOp{name: fmt.Sprintf("save %s/%s %q", room, event, body), run: func(ctx context.Context, c *Cache) error {
			return c.SaveMessages(ctx, room, []domain.Message{m})
		}}
	case 2:
		// Two copies in one transaction: the later one is what the cache holds.
		a, b := copyOf(), copyOf()
		return changeOp{name: fmt.Sprintf("save %s/%s twice", room, event), run: func(ctx context.Context, c *Cache) error {
			return c.SaveMessages(ctx, room, []domain.Message{a, b})
		}, undone: true}
	case 3:
		m := copyOf()
		m.Edited, m.EditedAt = true, at.Add(time.Duration(r.IntN(3))*time.Second)
		m.RevisionID = domain.EventID(fmt.Sprintf("%s$e%d", mx, r.IntN(3)))
		return changeOp{name: fmt.Sprintf("edit %s/%s to %q", room, event, body), run: func(ctx context.Context, c *Cache) error {
			return c.SaveMessages(ctx, room, []domain.Message{m})
		}}
	case 4:
		// Who deleted it may come with the deletion, or later with a stripped copy.
		keepWords := r.IntN(2) == 0
		by, reason := "", ""
		if r.IntN(2) == 0 {
			by, reason = "matrix:@a:x", body
		}
		if r.IntN(3) == 0 {
			m := domain.Message{ID: event, RoomID: room, Sender: "matrix:@a:x", Timestamp: at,
				Redacted: true, RedactedBy: by, RedactedReason: reason}
			return changeOp{name: fmt.Sprintf("save a deleted copy of %s/%s by %q", room, event, by), run: func(ctx context.Context, c *Cache) error {
				return c.SaveMessages(ctx, room, []domain.Message{m})
			}}
		}
		return changeOp{name: fmt.Sprintf("delete %s/%s by %q (keep %v)", room, event, by, keepWords), run: func(ctx context.Context, c *Cache) error {
			return c.MarkRedacted(ctx, room, event, by, reason, at, keepWords)
		}}
	case 5:
		poll := domain.Poll{ID: "p", Question: body, Voters: r.IntN(2)}
		return changeOp{name: fmt.Sprintf("poll %s/%s", room, event), run: func(ctx context.Context, c *Cache) error {
			return c.SetPoll(ctx, room, event, poll)
		}}
	case 6:
		mentions := []domain.Mention{{UserID: "matrix:@b:x", Name: body}}
		return changeOp{name: fmt.Sprintf("mentions %s/%s", room, event), run: func(ctx context.Context, c *Cache) error {
			return c.AddMentions(ctx, room, event, mentions)
		}}
	case 7:
		v := domain.Revision{ID: event, Body: body, At: at}
		return changeOp{name: fmt.Sprintf("show a version of %s/%s", room, event), run: func(ctx context.Context, c *Cache) error {
			return c.ShowVersion(ctx, room, event, v)
		}}
	case 8:
		// Where the file comes from: nothing a read returns.
		return changeOp{name: fmt.Sprintf("file source of %s/%s", room, event), run: func(ctx context.Context, c *Cache) error {
			return c.SaveMediaSource(ctx, event, room, "mxc://x/"+body, "")
		}}
	default:
		// A trim: the room keeps fewer, then everything again.
		m := copyOf()
		return changeOp{name: fmt.Sprintf("save %s/%s keeping 2", room, event), run: func(ctx context.Context, c *Cache) error {
			*keep = 2
			defer func() { *keep = -1 }()
			return c.SaveMessages(ctx, room, []domain.Message{m})
		}}
	}
}

// The numbering tells exactly what changed: after any write, the messages numbered
// after the counter it started from are those whose read changed, each once, in order,
// as the cache now returns them, and OnChanged has heard; a write that changes nothing
// (an echo, a deletion of a message not cached) numbers nothing.
func TestEveryChangeToAMessageIsNumberedAndNothingElse(t *testing.T) {
	t.Parallel()
	for seed := range uint64(6) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			cache := openTemp(t)
			keep := -1
			cache.UseKeep(func(domain.RoomID) int { return keep })
			told := 0
			cache.OnChanged(func() { told++ })
			r := rand.New(rand.NewPCG(seed, 29))
			for step := range 150 {
				op := randomChangeOp(r, &keep)
				before, from, toldBefore := readAll(t, cache), lastChange(t, cache), told
				if err := op.run(ctx, cache); err != nil {
					t.Fatalf("step %d, %s: %v", step, op.name, err)
				}
				after := readAll(t, cache)
				var orphans int
				if err := cache.db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check('message_changes')").Scan(&orphans); err != nil || orphans > 0 {
					t.Fatalf("step %d, %s: %d numbers of messages gone (%v)", step, op.name, orphans, err)
				}
				got := changedAfter(t, cache, from)
				if len(got) > 0 && told == toldBefore {
					t.Fatalf("step %d, %s: numbered %d changes and told nobody", step, op.name, len(got))
				}
				numbered := map[changeKey]bool{}
				last := from
				for _, c := range got {
					key := changeKey{c.Message.RoomID, c.Message.ID}
					if c.Number <= last {
						t.Fatalf("step %d, %s: number %d after %d", step, op.name, c.Number, last)
					}
					last = c.Number
					if numbered[key] {
						t.Fatalf("step %d, %s: %v numbered twice", step, op.name, key)
					}
					numbered[key] = true
					if !reflect.DeepEqual(c.Message, after[key]) {
						t.Fatalf("step %d, %s: %v came as\n  %+v\nthe cache holds\n  %+v", step, op.name, key, c.Message, after[key])
					}
				}
				for key, now := range after {
					was, had := before[key]
					changed := !had || !reflect.DeepEqual(was, now)
					if changed && !numbered[key] {
						t.Fatalf("step %d, %s: %v changed and was not numbered:\n  %+v\n  %+v", step, op.name, key, was, now)
					}
					if !changed && numbered[key] && !op.undone {
						t.Fatalf("step %d, %s: %v was numbered though it reads the same:\n  %+v", step, op.name, key, now)
					}
				}
			}
		})
	}
}

// Writers at once (each network's sync) leave every message numbered, each with its
// own number, none past the counter: a reader from 0 gets the cache as it is.
func TestWritersAtOnceLeaveEveryMessageNumbered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	var writers sync.WaitGroup
	for w := range uint64(4) {
		writers.Go(func() {
			keep := -1 // not the cache's (no UseKeep): writers share no state but it
			r := rand.New(rand.NewPCG(w, 31))
			for range 60 {
				op := randomChangeOp(r, &keep)
				if err := op.run(ctx, cache); err != nil {
					t.Errorf("%s: %v", op.name, err)
				}
			}
		})
	}
	writers.Wait()
	seen := map[changeKey]domain.Message{}
	numbers := map[int64]bool{}
	for _, c := range changedAfter(t, cache, 0) {
		if numbers[c.Number] {
			t.Fatalf("number %d given twice", c.Number)
		}
		numbers[c.Number] = true
		seen[changeKey{c.Message.RoomID, c.Message.ID}] = c.Message
	}
	if last := lastChange(t, cache); len(numbers) > 0 && slices.Max(slices.Collect(maps.Keys(numbers))) > last {
		t.Fatalf("a number past the counter's %d", last)
	}
	if held := readAll(t, cache); !reflect.DeepEqual(seen, held) {
		t.Fatalf("the numbered messages\n  %v\nthe cache\n  %v", seen, held)
	}
	var left int
	if err := cache.db.QueryRowContext(ctx, "SELECT count(*) FROM message_touched").Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d touched messages left unnumbered (%v)", left, err)
	}
}

// Clearing the cache starts a new epoch: what a reader holds was emptied with the
// rest. Another cache numbers under its own.
func TestClearStartsANewEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	told := 0
	cache.OnChanged(func() { told++ })
	if err := cache.SaveMessages(ctx, changeRooms[0], []domain.Message{{ID: changeEvents[0], RoomID: changeRooms[0], Body: "hi", Timestamp: time.UnixMilli(1000)}}); err != nil {
		t.Fatal(err)
	}
	first := epochOf(t, cache)
	if first == "" {
		t.Fatal("no epoch")
	}
	if other := epochOf(t, openTemp(t)); other == first {
		t.Fatalf("a second cache's epoch is the first's, %q", first)
	}
	told = 0
	if err := cache.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if now := epochOf(t, cache); now == first {
		t.Fatalf("after Clear the epoch is %q, as before", now)
	}
	if told != 1 {
		t.Fatalf("Clear told %d times, want once", told)
	}
}

// A reader that reads on from its last number whenever it is told gets every message
// as the cache ends up holding it, however the writers interleave.
func TestAReaderToldOfEachChangeEndsWithWhatTheCacheHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	told := make(chan struct{}, 1)
	cache.OnChanged(func() {
		select {
		case told <- struct{}{}:
		default:
		}
	})

	seen := map[changeKey]domain.Message{}
	var after int64
	read := func() {
		for {
			_, got, err := cache.ChangesAfter(ctx, after, 7)
			if err != nil {
				t.Errorf("ChangesAfter: %v", err)
				return
			}
			for _, c := range got {
				seen[changeKey{c.Message.RoomID, c.Message.ID}] = c.Message
				after = c.Number
			}
			if len(got) == 0 {
				return
			}
		}
	}

	var writers sync.WaitGroup
	for w := range uint64(4) {
		writers.Go(func() {
			keep := -1 // not the cache's (no UseKeep): writers share no state but it
			r := rand.New(rand.NewPCG(w, 37))
			for range 60 {
				op := randomChangeOp(r, &keep)
				if err := op.run(ctx, cache); err != nil {
					t.Errorf("%s: %v", op.name, err)
				}
			}
		})
	}
	done := make(chan struct{})
	go func() { writers.Wait(); close(done) }()
	for open := true; open; {
		select {
		case <-told:
			read()
		case <-done:
			open = false
		}
	}
	select { // a last tell may still be queued; a change after the last read has one
	case <-told:
		read()
	default:
	}
	if held := readAll(t, cache); !reflect.DeepEqual(seen, held) {
		t.Fatalf("the reader holds\n  %v\nthe cache\n  %v", seen, held)
	}
}

// A deleted message keeps when it was deleted, whichever way the deletion came: a
// deletion of a cached message, one that came before the message (a tombstone), or a
// deleted copy. The first time heard stays; an unknown one is not kept.
func TestADeletedMessageKeepsWhenItWasDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	room := changeRooms[0]
	sent, deleted, later := time.UnixMilli(1000), time.UnixMilli(5000), time.UnixMilli(9000)
	copyOf := func(id domain.EventID) domain.Message {
		return domain.Message{ID: id, RoomID: room, Sender: "matrix:@a:x", Body: "hi", Timestamp: sent}
	}
	for name, deleteIt := range map[string]func(*Cache, domain.EventID) error{
		"deleted when cached": func(c *Cache, id domain.EventID) error {
			if err := c.SaveMessages(ctx, room, []domain.Message{copyOf(id)}); err != nil {
				return err
			}
			return c.MarkRedacted(ctx, room, id, "", "", deleted, false)
		},
		"deleted before it came": func(c *Cache, id domain.EventID) error {
			if err := c.MarkRedacted(ctx, room, id, "", "", deleted, false); err != nil {
				return err
			}
			return c.SaveMessages(ctx, room, []domain.Message{copyOf(id)})
		},
		"a deleted copy": func(c *Cache, id domain.EventID) error {
			m := copyOf(id)
			m.Body, m.Redacted, m.RedactedAt = "", true, deleted
			return c.SaveMessages(ctx, room, []domain.Message{m})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cache := openTemp(t)
			id := changeEvents[0]
			if err := deleteIt(cache, id); err != nil {
				t.Fatal(err)
			}
			// Heard again later, and with no time: the first stays.
			if err := cache.MarkRedacted(ctx, room, id, "", "", later, false); err != nil {
				t.Fatal(err)
			}
			if err := cache.MarkRedacted(ctx, room, id, "", "", time.Time{}, false); err != nil {
				t.Fatal(err)
			}
			got, ok, err := cache.MessageByID(ctx, room, id)
			if err != nil || !ok || !got.Redacted || !got.RedactedAt.Equal(deleted) {
				t.Fatalf("read back %+v (%v, %v), want deleted at %v", got, ok, err, deleted)
			}
		})
	}
	cache := openTemp(t)
	if err := cache.SaveMessages(ctx, room, []domain.Message{copyOf(changeEvents[1])}); err != nil {
		t.Fatal(err)
	}
	if err := cache.MarkRedacted(ctx, room, changeEvents[1], "", "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := cache.MessageByID(ctx, room, changeEvents[1]); !got.RedactedAt.IsZero() {
		t.Fatalf("an unknown time read back as %v", got.RedactedAt)
	}
	// Unknown is not a first time: the time heard after it is kept.
	if err := cache.MarkRedacted(ctx, room, changeEvents[1], "", "", deleted, false); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := cache.MessageByID(ctx, room, changeEvents[1]); !got.RedactedAt.Equal(deleted) {
		t.Fatalf("read back deleted at %v after an unknown time, want %v", got.RedactedAt, deleted)
	}
}
