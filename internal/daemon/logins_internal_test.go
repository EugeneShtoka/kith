package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
)

// asker is a network whose login sometimes shows a code and asks until told "ok";
// running counts its logins under way.
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

// Under any interleaving of begin, answer and cancel from several callers: no call
// waits past its deadline, every answer is a step or "ended", and no login outlives
// the daemon.
func TestLoginsNeverHangOrLeak(t *testing.T) {
	t.Parallel()
	for seed := range uint64(40) {
		running := &atomic.Int32{}
		run, stop := context.WithCancel(context.Background())
		l := NewLogins(run, func() []api.LoginLeader { return []api.LoginLeader{asker{running}} })
		l.LogTo(slog.New(slog.DiscardHandler)) // logging on under every interleaving, for -race
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
// refused as ended.
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

// A login's steps and end are logged, so a stuck one leaves a trace, and nothing
// typed or shown is: not an answer, not a code.
func TestLoginsLogStepsButNotWhatIsTyped(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	var mu sync.Mutex
	l := NewLogins(context.Background(), func() []api.LoginLeader { return []api.LoginLeader{asker{&atomic.Int32{}}} })
	l.LogTo(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil)))
	ctx := context.Background()
	step, err := l.BeginLogin(ctx, "fake", "home")
	if err != nil {
		t.Fatal(err)
	}
	// The asker shows CODE on its second round, then asks again.
	for _, answer := range []string{"hunter2-secret", "ok"} {
		if step.Code != "" {
			if step, err = l.AnswerLogin(ctx, step.Login, nil); err != nil {
				t.Fatal(err)
			}
		}
		if step, err = l.AnswerLogin(ctx, step.Login, map[string]string{"x": answer}); err != nil {
			t.Fatal(err)
		}
	}
	for step.Done == "" {
		if step.Code != "" {
			step, err = l.AnswerLogin(ctx, step.Login, nil)
		} else {
			step, err = l.AnswerLogin(ctx, step.Login, map[string]string{"x": "ok"})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	for _, want := range []string{"login begun", "login asks", "fields=[x]", "login shows a code", "login done"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
	for _, leaked := range []string{"hunter2-secret", "CODE"} {
		if strings.Contains(out, leaked) {
			t.Errorf("%q reached the log:\n%s", leaked, out)
		}
	}
}

// lockedWriter serializes writes to w.
type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
