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
// (a failure that may pass), "refused" or "no-power" (for good). Past the end, ok.
type scriptedSender struct {
	mu      sync.Mutex
	scripts map[string][]string
	sent    map[string]int
}

func (s *scriptedSender) Send(_ context.Context, _ domain.RoomID, draft domain.Draft) error {
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
	s.sent[draft.Body]++
	return nil
}

// A send the network refused for good is not retried: it stays queued, marked with
// why, across restarts, until retried (then sent at once) or canceled; one that may
// pass is retried. Generated over adds, retry passes, retries, cancels and restarts.
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
	sender := &scriptedSender{scripts: map[string][]string{}, sent: map[string]int{}}
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
		case "refused", "no-power":
			state[body] = refused
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
		switch op := r.IntN(6); {
		case op == 0 || len(bodies) == 0:
			body := "message " + strconv.Itoa(step)
			script := make([]string, r.IntN(3))
			for i := range script {
				script[i] = []string{"ok", "temp", "refused", "no-power"}[r.IntN(4)]
			}
			sender.scripts[body] = script
			left[body] = append([]string(nil), script...)
			log = append(log, fmt.Sprintf("add %q %v", body, script))
			// Adding sends what is due, the new one with the rest.
			state[body] = pending
			pass()
			msg := domain.ScheduledMessage{RoomID: mx + "!a:x", Body: body, At: now.Add(-time.Second), TxnID: body}
			msg.ID = body
			ids[body] = msg.ID
			if err := s.Add(msg); err != nil {
				t.Fatal(err)
			}
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
			if state[body] != sent {
				state[body] = canceled
			}
			if err := s.Cancel(ids[body]); err != nil {
				t.Fatal(err)
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
		for body, want := range state {
			m, in := queued[body]
			got := sender.sent[body]
			switch want {
			case sent:
				if got != 1 || in {
					t.Fatalf("%v\n%s: sent %d times, queued %v; want sent once and gone", log, body, got, in)
				}
			case refused:
				if got != 0 || !in || m.Refused == "" {
					t.Fatalf("%v\n%s: sent %d, queued %v, refused %q; want kept, marked, unsent", log, body, got, in, m.Refused)
				}
			case pending:
				if got != 0 || !in || m.Refused != "" {
					t.Fatalf("%v\n%s: sent %d, queued %v, refused %q; want kept for a retry", log, body, got, in, m.Refused)
				}
			case canceled:
				if in || got != 0 {
					t.Fatalf("%v\n%s: queued %v, sent %d after it was canceled", log, body, in, got)
				}
			}
		}
	}
}
