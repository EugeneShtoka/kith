package daemon

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
)

// asker is a network whose login shows a code now and then and asks until it is told
// "ok"; running counts its logins under way.
type asker struct{ running *atomic.Int32 }

func (a asker) LoginNetwork() api.LoginNetwork { return api.LoginNetwork{Network: "fake"} }

func (a asker) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	a.running.Add(1)
	defer a.running.Add(-1)
	if account == "" {
		talk.Named(fmt.Sprintf("new%d", rand.IntN(3)))
	}
	for i := 0; ; i++ {
		if i%3 == 1 {
			if err := talk.Show(ctx, "CODE", ""); err != nil {
				return api.LoginEnd{}, err
			}
		}
		got, err := talk.Ask(ctx, "", api.LoginField{Key: "x"})
		if err != nil {
			return api.LoginEnd{}, err
		}
		if got["x"] == "ok" {
			return api.LoginEnd{Done: "done"}, nil
		}
	}
}

// Logins under any interleaving of beginnings, answers and cancels, from several
// callers at once: no call waits past its own deadline for a step that will not come,
// every answer is a step or says the login has ended, and when the daemon stops, no
// network's login is left running.
func TestLoginsNeverHangOrLeak(t *testing.T) {
	t.Parallel()
	for seed := range uint64(40) {
		running := &atomic.Int32{}
		run, stop := context.WithCancel(context.Background())
		l := NewLogins(run, func() []api.LoginLeader { return []api.LoginLeader{asker{running}} })
		var mu sync.Mutex
		var ids []string
		pick := func(rng *rand.Rand) string {
			mu.Lock()
			defer mu.Unlock()
			if len(ids) == 0 {
				return "none"
			}
			return ids[rng.IntN(len(ids))]
		}
		var hung atomic.Int32
		var wg sync.WaitGroup
		for w := range 4 {
			wg.Go(func() {
				rng := rand.New(rand.NewPCG(seed, uint64(w)))
				for range 25 {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					var err error
					switch rng.IntN(4) {
					case 0:
						var step api.LoginStep
						step, err = l.BeginLogin(ctx, "fake", []string{"", "home", "work"}[rng.IntN(3)])
						if err == nil {
							mu.Lock()
							ids = append(ids, step.Login)
							mu.Unlock()
						}
					case 1, 2:
						_, err = l.AnswerLogin(ctx, pick(rng), map[string]string{"x": []string{"no", "ok"}[rng.IntN(2)]})
					case 3:
						err = l.CancelLogin(ctx, pick(rng))
					}
					if errors.Is(err, context.DeadlineExceeded) {
						hung.Add(1)
					} else if err != nil && !errors.Is(err, errNoSuchLogin) {
						t.Errorf("seed %d: unexpected %v", seed, err)
					}
					cancel()
				}
			})
		}
		wg.Wait()
		if n := hung.Load(); n > 0 {
			t.Fatalf("seed %d: %d calls waited out their deadline", seed, n)
		}
		stop()
		for deadline := time.Now().Add(2 * time.Second); running.Load() > 0; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("seed %d: %d logins still running after the daemon stopped", seed, running.Load())
			}
		}
	}
}

// Beginning a login of an account ends the one under way: answering the older is
// refused as ended, and the newer goes on.
func TestANewLoginOfAnAccountEndsTheOlder(t *testing.T) {
	t.Parallel()
	running := &atomic.Int32{}
	l := NewLogins(t.Context(), func() []api.LoginLeader { return []api.LoginLeader{asker{running}} })
	ctx := t.Context()
	older, err := l.BeginLogin(ctx, "fake", "home")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := l.BeginLogin(ctx, "fake", "home")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AnswerLogin(ctx, older.Login, map[string]string{"x": "ok"}); !errors.Is(err, errNoSuchLogin) {
		t.Errorf("answering the older login = %v, want it ended", err)
	}
	if step, err := l.AnswerLogin(ctx, newer.Login, map[string]string{"x": "ok"}); err != nil || step.Done != "done" {
		t.Errorf("answering the newer login = (%+v, %v), want done", step, err)
	}
}
