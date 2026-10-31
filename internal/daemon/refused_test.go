package daemon

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/schedule"
)

// scriptedSender answers each message's sends from its script, in turn: "ok", "temp"
// (a failure that may pass), "lost" (it landed, its answer lost), "refused" or
// "no-power" (for good). Past the end, ok. It deduplicates by transaction ID as a
// homeserver does: a message that landed once under one is not counted again. A
// message's during runs as its first send is on the wire: another client acting on it.
type scriptedSender struct {
	mu        sync.Mutex
	scripts   map[string][]string
	sent      map[string]int
	delivered map[string]bool
	during    map[string]func()
}

func (s *scriptedSender) Send(_ context.Context, _ domain.RoomID, draft domain.Draft) error {
	s.mu.Lock()
	during := s.during[draft.Body]
	delete(s.during, draft.Body)
	s.mu.Unlock()
	if during != nil {
		during()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	script := s.scripts[draft.Body]
	answer := "ok"
	if len(script) > 0 {
		answer, s.scripts[draft.Body] = script[0], script[1:]
	}
	switch answer {
	case "temp":
		return errors.New("homeserver unreachable")
	case "refused":
		return fmt.Errorf("matrix: send message: %w: M_TOO_LARGE", api.ErrRefused)
	case "no-power":
		return fmt.Errorf("%w: only admins post here", api.ErrNoPower)
	}
	if draft.TxnID == "" || !s.delivered[draft.TxnID] {
		s.sent[draft.Body]++
	}
	if draft.TxnID != "" {
		s.delivered[draft.TxnID] = true
	}
	if answer == "lost" {
		return errors.New("context deadline exceeded")
	}
	return nil
}

// A send the network refused for good is not retried: it stays queued, marked with
// why, across restarts, until retried (then sent at once) or canceled; one that may
// pass is retried. Generated over adds, retry passes, retries, cancels and restarts;
// over sends that landed with their answer lost; over a client asking again for a
// message whose first answer it lost (the window's Schedule again), and messages with
// no transaction ID (the TUI's /at); and over another client canceling, retrying or
// asking again while the message's send is on the wire. Each message lands once, is
// queued once, and a cancel says whether it was still waiting.
func TestARefusedSendFailsAtOnceAndWaitsForItsAuthor(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(118, 2026))
	for n := range 80 {
		seed := r.Uint64()
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()
			checkRefusals(t, rand.New(rand.NewPCG(seed, uint64(n))))
		})
	}
}

func checkRefusals(t *testing.T, r *rand.Rand) {
	t.Helper()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store := schedule.New(filepath.Join(t.TempDir(), "scheduled.toml"))
	sender := &scriptedSender{
		scripts: map[string][]string{}, sent: map[string]int{},
		delivered: map[string]bool{}, during: map[string]func(){},
	}
	start := func() *Scheduler {
		s := NewScheduler(store, sender, func() time.Duration { return 24 * time.Hour }, nil)
		s.now = func() time.Time { return now }
		t.Cleanup(s.Stop)
		s.base = context.Background()
		s.load(context.Background())
		return s
	}
	s := start()

	// The model: each message's expected state.
	const (
		pending  = "pending"
		refused  = "refused"
		sent     = "sent"
		canceled = "canceled"
	)
	state := map[string]string{}
	ids := map[string]string{} // body → queue ID
	msgs := map[string]domain.ScheduledMessage{}
	landed := map[string]bool{} // a send landed, its answer lost
	var log []string
	// sendTo follows what one try of a message does in the model.
	left := map[string][]string{} // the model's own copy of each script
	try := func(body string) {
		answer := "ok"
		if script := left[body]; len(script) > 0 {
			answer, left[body] = script[0], script[1:]
		}
		switch answer {
		case "ok":
			state[body] = sent
		case "lost":
			landed[body] = true
		case "refused", "no-power":
			state[body] = refused
		}
	}
	// meanwhile is another client acting on body while its send is on the wire: out of
	// the queue, so a cancel finds it no longer waiting, a retry has nothing to do, and
	// asking again is answered with it.
	meanwhile := func(body, what string) func() {
		return func() {
			switch what {
			case "cancel":
				if waiting, err := s.Cancel(ids[body]); err != nil || waiting {
					t.Errorf("%v\n%s: canceled during its send: waiting %v, %v; want no longer waiting", log, body, waiting, err)
				}
			case "retry":
				if err := s.Retry(ids[body]); err != nil {
					t.Error(err)
				}
			case "again":
				again := msgs[body]
				again.ID = body + " again"
				if id, err := s.Add(again); err != nil || id != ids[body] {
					t.Errorf("%v\n%s: asked again during its send: %q, %v; want %q", log, body, id, err, ids[body])
				}
			}
		}
	}
	// pass is what the model expects of one pass: every pending message tried.
	pass := func() {
		for body, st := range state {
			if st == pending {
				try(body)
			}
		}
	}

	for step := range 12 {
		bodies := make([]string, 0, len(state))
		for body := range state {
			bodies = append(bodies, body)
		}
		switch op := r.IntN(8); {
		case op == 0 || len(bodies) == 0:
			body := "message " + strconv.Itoa(step)
			script := make([]string, r.IntN(3))
			for i := range script {
				script[i] = []string{"ok", "temp", "lost", "refused", "no-power"}[r.IntN(5)]
			}
			sender.scripts[body] = script
			left[body] = append([]string(nil), script...)
			// Half come with a transaction ID (the window's), half with none (the TUI's /at).
			msg := domain.ScheduledMessage{RoomID: mx + "!a:x", Body: body, At: now.Add(-time.Second)}
			if r.IntN(2) == 0 {
				msg.TxnID = body
			}
			msg.ID = body
			ids[body], msgs[body] = msg.ID, msg
			what := []string{"", "", "cancel", "retry", "again"}[r.IntN(5)]
			if what == "again" && msg.TxnID == "" {
				what = ""
			}
			if what != "" {
				sender.during[body] = meanwhile(body, what)
			}
			log = append(log, fmt.Sprintf("add %q %v txn %q, %s meanwhile", body, script, msg.TxnID, what))
			// Adding sends what is due, the new one with the rest.
			state[body] = pending
			pass()
			if _, err := s.Add(msg); err != nil {
				t.Fatal(err)
			}
		case op >= 6:
			// The window asks again for a message it scheduled, the first answer lost: one
			// still queued is the same message; one gone is queued again, and lands once.
			body := bodies[r.IntN(len(bodies))]
			if msgs[body].TxnID == "" {
				break // the TUI asks only once
			}
			log = append(log, fmt.Sprintf("again %q", body))
			again := msgs[body]
			again.ID = fmt.Sprintf("%s again %d", body, step)
			id, err := s.Add(again)
			if err != nil {
				t.Fatal(err)
			}
			if st := state[body]; st == pending || st == refused {
				if id != ids[body] {
					t.Fatalf("%v\n%s: asked again while queued, answered %q; want %q", log, body, id, ids[body])
				}
				break
			}
			ids[body] = id
			landed[body] = landed[body] || state[body] == sent
			state[body] = pending
			pass()
		case op == 1:
			log = append(log, "retry pass")
			pass()
			s.pass(context.Background())
		case op == 2:
			body := bodies[r.IntN(len(bodies))]
			log = append(log, fmt.Sprintf("retry %q", body))
			if st := state[body]; st == refused || st == pending {
				state[body] = pending
				pass()
			}
			if err := s.Retry(ids[body]); err != nil {
				t.Fatal(err)
			}
		case op == 3:
			body := bodies[r.IntN(len(bodies))]
			log = append(log, fmt.Sprintf("cancel %q", body))
			wasWaiting := state[body] == pending || state[body] == refused
			if state[body] != sent {
				state[body] = canceled
			}
			waiting, err := s.Cancel(ids[body])
			if err != nil {
				t.Fatal(err)
			}
			if waiting != wasWaiting {
				t.Fatalf("%v\n%s: canceling answered waiting %v; want %v", log, body, waiting, wasWaiting)
			}
		default:
			log = append(log, "restart")
			s.Stop()
			s = start()
		}

		queued := map[string]domain.ScheduledMessage{}
		listed := s.List()
		for i := range listed {
			queued[listed[i].Body] = listed[i]
		}
		if len(queued) != len(listed) {
			t.Fatalf("%v\none message queued twice: %+v", log, listed)
		}
		for body, want := range state {
			m, in := queued[body]
			got := sender.sent[body]
			// What landed: a message sent, or one whose send landed unheard.
			arrived := 0
			if want == sent || landed[body] {
				arrived = 1
			}
			switch want {
			case sent:
				if got != 1 || in {
					t.Fatalf("%v\n%s: sent %d times, queued %v; want sent once and gone", log, body, got, in)
				}
			case refused:
				if got != arrived || !in || m.Refused == "" {
					t.Fatalf("%v\n%s: sent %d, queued %v, refused %q; want kept, marked, landed %d", log, body, got, in, m.Refused, arrived)
				}
			case pending:
				if got != arrived || !in || m.Refused != "" {
					t.Fatalf("%v\n%s: sent %d, queued %v, refused %q; want kept for a retry, landed %d", log, body, got, in, m.Refused, arrived)
				}
			case canceled:
				if in || got != arrived {
					t.Fatalf("%v\n%s: queued %v, sent %d after it was canceled; want landed %d", log, body, in, got, arrived)
				}
			}
		}
	}
}
