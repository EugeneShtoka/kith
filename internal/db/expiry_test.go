package db

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A disappearing message is deleted once its timer has run from when its countdown
// started: when it was sent, or, for one counted from reading, when the account read
// it. Over any order of what arrives (the message, its timer, the account's reading,
// the same message and timer again, as a resumed sync delivers them) and sweeps at any
// time, the cache deletes exactly what a model says is due: none early, none that waits
// for a reading that has not come, a countdown never restarted, a deleted message never
// brought back, its text kept only under keep.
func TestADisappearingMessageGoesWhenItsTimeHasRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(19, 2))
	const timer = time.Hour
	for run := range 30 {
		c := openTemp(t)
		owner := domain.AccountRooms(domain.ProtocolSignal, "me")
		room := domain.RoomID(fmt.Sprintf("signal:me/chat-%d", run))
		keep := rng.IntN(2) == 0
		now := time.Now()
		type msg struct {
			id       domain.EventID
			at       time.Time
			fromSent bool // its countdown runs from its sending; else from its reading
			// archived: its countdown started long ago, as the phone's history says, and
			// later copies say nothing of it.
			archived bool
		}
		var msgs []msg
		for i := range 6 {
			msgs = append(msgs, msg{
				id: domain.EventID(fmt.Sprintf("%s/m%d", room, i)), at: now.Add(-3*time.Hour + time.Duration(i)*time.Minute),
				fromSent: rng.IntN(2) == 0, archived: rng.IntN(3) == 0,
			})
		}
		save := func(m msg) {
			if err := c.SaveMessages(ctx, room, []domain.Message{{ID: m.id, RoomID: room, Sender: "signal:dana", Body: "said " + string(m.id), Timestamp: m.at}}); err != nil {
				t.Fatal(err)
			}
		}
		setExpiry := func(m msg, again bool) {
			var starts time.Time
			switch {
			case m.fromSent:
				starts = m.at
			case m.archived && !again:
				starts = m.at.Add(10 * time.Minute)
			}
			if err := c.SetExpiry(ctx, room, m.id, timer, starts); err != nil {
				t.Fatal(err)
			}
		}
		readThrough := -1
		read := func(i int) {
			if err := c.SaveUnread(ctx, domain.Unread{RoomID: room}, msgs[i].at.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			readThrough = max(readThrough, i)
		}
		// What arrives, in any order: each message then its timer, and a reading of some
		// of them before, between or after.
		readAt := rng.IntN(len(msgs) + 1) // a reading after this many messages arrived
		upTo := rng.IntN(len(msgs)+1) - 1 // read through this message; -1: none
		for i, m := range msgs {
			if i == readAt && upTo >= 0 {
				read(upTo) // a reading may reach past what has arrived
			}
			save(m)
			setExpiry(m, false)
		}
		if readAt == len(msgs) && upTo >= 0 {
			read(upTo)
		}
		long := func(i int) bool { return msgs[i].fromSent || msgs[i].archived } // started hours ago
		started := func(i int) bool { return long(i) || i <= readThrough }
		check := func(when string, sweepAt time.Time, due func(i int) bool) {
			t.Helper()
			_, err := c.ExpireDue(ctx, owner, sweepAt, keep)
			if err != nil {
				t.Fatal(err)
			}
			for i, m := range msgs {
				got, ok, err := c.MessageByID(ctx, room, m.id)
				if err != nil || !ok {
					t.Fatalf("run %d %s: %s lost (%v)", run, when, m.id, err)
				}
				if got.Redacted != due(i) {
					t.Errorf("run %d %s: %s (from sent %v, read through %d) deleted = %v, want %v",
						run, when, m.id, m.fromSent, readThrough, got.Redacted, due(i))
				}
				if got.Redacted && (got.Body != "") == !keep {
					t.Errorf("run %d %s: %s kept %q under keep=%v", run, when, m.id, got.Body, keep)
				}
			}
		}
		// The same messages and timers again, saying nothing of when they started (a live
		// copy after the history): no countdown restarts.
		for _, m := range msgs {
			save(m)
			setExpiry(m, true)
		}
		// Half an hour on: what started hours ago is gone; what ran from a reading just now
		// is not yet.
		check("in half an hour", now.Add(30*time.Minute), long)
		// The same again, after: nothing comes back.
		for _, m := range msgs {
			save(m)
			setExpiry(m, true)
		}
		check("redelivered", now.Add(30*time.Minute), long)
		// Two hours on: every countdown started has run; one waiting for a reading waits.
		check("in two hours", now.Add(2*time.Hour), started)
		// Read through everything now: the rest start, and run an hour from now.
		read(len(msgs) - 1)
		check("read late, an hour on", now.Add(2*time.Hour), func(int) bool { return true })
	}
}
