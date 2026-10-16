package readstate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// In any order of reads owed, answers settling them (late, out of order, for an older
// read) and an account's marks taken on reconnect, the queue owes each room its
// newest unanswered read: an answer settles only what it covered.
func TestOwedKeepsTheNewestUnansweredRead(t *testing.T) {
	t.Parallel()
	rooms := []domain.RoomID{"slack:T1/C1", "slack:T1/C2", "slack:T2/C1"}
	for seed := range uint64(200) {
		r := rand.New(rand.NewPCG(seed, 5))
		o := NewOwed(func(a, b int) bool { return a > b })
		want := map[domain.RoomID]int{}
		var sent []struct {
			room domain.RoomID
			at   int
		}
		var steps []string
		for range 20 {
			room := rooms[r.IntN(len(rooms))]
			switch r.IntN(4) {
			case 0, 1: // a read, at a position that may be older than one owed
				at := r.IntN(10)
				steps = append(steps, fmt.Sprint("read ", room, "@", at))
				o.Add(room, at)
				if old, ok := want[room]; !ok || at >= old {
					want[room] = at
				}
				sent = append(sent, struct {
					room domain.RoomID
					at   int
				}{room, at})
			case 2: // the network answers some earlier read
				if len(sent) == 0 {
					continue
				}
				s := sent[r.IntN(len(sent))]
				steps = append(steps, fmt.Sprint("answer ", s.room, "@", s.at))
				o.Settle(s.room, s.at)
				if old, ok := want[s.room]; ok && old <= s.at {
					delete(want, s.room)
				}
			default: // reconnecting: an account's marks taken and owed again
				account := domain.ParseID(string(room)).Account
				steps = append(steps, "reconnect "+account)
				for room, at := range o.Take(account) {
					if domain.ParseID(string(room)).Account != account {
						t.Fatalf("seed %d %v: took %s for account %s", seed, steps, room, account)
					}
					o.Add(room, at)
				}
			}
			got := map[domain.RoomID]int{}
			o.mu.Lock()
			maps.Copy(got, o.marks)
			o.mu.Unlock()
			if !maps.Equal(got, want) {
				t.Fatalf("seed %d %v: owed %v, want %v", seed, steps, got, want)
			}
			for _, room := range rooms {
				if _, ok := want[room]; o.Has(room) != ok {
					t.Fatalf("seed %d %v: Has(%s) = %v", seed, steps, room, !ok)
				}
			}
		}
	}
}

// cache is a fake cache whose reads can fail.
type cache struct {
	msgs   map[domain.EventID]domain.Message
	latest map[domain.RoomID]domain.EventID
	fail   error
}

func (c cache) MessageByID(_ context.Context, _ domain.RoomID, e domain.EventID) (domain.Message, bool, error) {
	if c.fail != nil {
		return domain.Message{}, false, c.fail
	}
	m, ok := c.msgs[e]
	return m, ok, nil
}

func (c cache) LatestEvents(context.Context, []domain.RoomID) (map[domain.RoomID]domain.EventID, error) {
	return c.latest, c.fail
}

// A read's position is its message's time; the fallback only for one not cached, and
// a failed lookup is an error, never the fallback.
func TestTimeOfNeverTurnsAFailureIntoTheFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sent, now := time.Unix(1_700_000_000, 0), time.Unix(1_800_000_000, 0)
	c := cache{msgs: map[domain.EventID]domain.Message{"$a": {Timestamp: sent}, "$untimed": {}}}
	for _, tc := range []struct {
		name  string
		c     cache
		event domain.EventID
		want  time.Time
		err   bool
	}{
		{"cached", c, "$a", sent, false},
		{"not cached", c, "$b", now, false},
		{"cached with no time", c, "$untimed", now, false},
		{"no event", c, "", now, false},
		{"the lookup fails", cache{fail: errors.New("SQLITE_BUSY")}, "$a", time.Time{}, true},
	} {
		got, err := TimeOf(ctx, tc.c, "!r", tc.event, now)
		if (err != nil) != tc.err || !got.Equal(tc.want) {
			t.Errorf("%s: (%v, %v), want %v (error %v)", tc.name, got, err, tc.want, tc.err)
		}
	}
}

// Rooms with no message are skipped, refused ones failed with the first refusal kept,
// the rest marked; the newest messages not read at all is an error.
func TestMarkRoomsReadCountsWhatWasRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := cache{latest: map[domain.RoomID]domain.EventID{"!a": "$1", "!b": "$2", "!c": "$3"}}
	var read []domain.RoomID
	res, err := MarkRoomsRead(ctx, c, []domain.RoomID{"!a", "!b", "!c", "!empty"}, func(room domain.RoomID, event domain.EventID) error {
		if room != "!a" {
			return fmt.Errorf("could not keep %s", room)
		}
		read = append(read, room)
		return nil
	})
	if err != nil || res.Marked != 1 || res.Failed != 2 || res.Skipped != 1 || res.FirstError != "could not keep !b" || len(read) != 1 {
		t.Errorf("result %+v (%v), read %v", res, err, read)
	}
	if _, err := MarkRoomsRead(ctx, cache{fail: errors.New("gone")}, []domain.RoomID{"!a"}, func(domain.RoomID, domain.EventID) error {
		t.Error("read a room whose newest message is not known")
		return nil
	}); err == nil {
		t.Error("the newest messages failing is not an error")
	}
}
