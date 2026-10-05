package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// homeserver is a fake Matrix homeserver: logins hand out new tokens, whoami
// accepts only those it handed out, and everything else is unknown.
type homeserver struct {
	*httptest.Server
	mu     sync.Mutex
	issued map[string]bool
	logins atomic.Int64
	// whoamiDelay is how long checking a session takes, so a login can land while
	// one is being checked.
	whoamiDelay atomic.Int64
}

func newHomeserver(t *testing.T) *homeserver {
	t.Helper()
	h := &homeserver{issued: map[string]bool{}}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)
	return h
}

func (h *homeserver) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/login") && r.Method == http.MethodPost:
		n := h.logins.Add(1)
		token := fmt.Sprintf("token-%d", n)
		h.mu.Lock()
		h.issued[token] = true
		h.mu.Unlock()
		body, _ := json.Marshal(map[string]string{ //nolint:errchkjson // strings only: cannot fail
			"user_id": "@me:x", "access_token": token, "device_id": fmt.Sprintf("DEVICE%d", n),
		})
		_, _ = w.Write(body)
	case strings.HasSuffix(r.URL.Path, "/account/whoami"):
		time.Sleep(time.Duration(h.whoamiDelay.Load()))
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		h.mu.Lock()
		ok := h.issued[token]
		h.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN_TOKEN","error":"unknown token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"user_id":"@me:x"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errcode":"M_UNRECOGNIZED","error":"not here"}`))
	}
}

// phases records what the adapter reported, in order.
type phases struct {
	mu  sync.Mutex
	got []domain.AccountPhase
}

func (p *phases) add(s domain.AccountStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, s.Phase)
}

func (p *phases) last() domain.AccountPhase {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.got) == 0 {
		return 0
	}
	return p.got[len(p.got)-1]
}

// testAdapter is a Matrix adapter for the fake homeserver, its secrets in the mock
// keyring under a service of its own, starting from saved.
func testAdapter(t *testing.T, hs *homeserver, saved domain.Session) (*Adapter, *phases, session.Store) {
	t.Helper()
	dir := t.TempDir()
	keys := session.Store{Service: "kith-test-" + filepath.Base(dir), Account: "@me:x", File: filepath.Join(dir, "session.toml")}
	cache, err := db.Open(context.Background(), filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	m := NewAdapter(cache, slog.New(slog.DiscardHandler), Account{
		Homeserver: hs.URL, User: "@me:x", AllowTokenFile: true,
		Crypto: CryptoPlace{Path: filepath.Join(dir, "crypto.db"), Keys: keys},
	}, saved)
	p := &phases{}
	m.OnStatus(p.add)
	return m, p, keys
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// noKeyring has the keyring refuse everything, so sessions go to the file fallback
// (allow_token_file): go-keyring's mock store is not safe for concurrent use, and these
// tests log in concurrently. It is process-wide: the tests that use it are not parallel.
func noKeyring() { keyring.MockInitWithError(errors.New("no secret service in tests")) }

// A daemon with no session, or one the homeserver rejects, waits logged out —
// nothing reaches Matrix — until a login hands it a session, which it then runs on.
func TestMatrixWaitsForALoginAndStartsOnIt(t *testing.T) {
	noKeyring()
	hs := newHomeserver(t)
	for name, saved := range map[string]domain.Session{
		"no session":       {},
		"rejected session": {Homeserver: hs.URL, UserID: "@me:x", DeviceID: "OLD", AccessToken: "revoked"},
	} {
		t.Run(name, func(t *testing.T) {
			m, p, keys := testAdapter(t, hs, saved)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- m.Start(ctx) }()
			defer func() {
				cancel()
				<-done
				m.Stop()
			}()

			waitUntil(t, "logged out", func() bool { return p.last() == domain.AccountLoggedOut })
			if m.LoggedIn() {
				t.Fatal("LoggedIn before any usable session")
			}
			in, err := m.passwordLogin(ctx, "secret")
			if err != nil || !in.Started || in.UserID != "@me:x" {
				t.Fatalf("passwordLogin = (%+v, %v), want started as @me:x", in, err)
			}
			waitUntil(t, "logged in", m.LoggedIn)
			if stored, found, err := session.Load(keys, true); err != nil || !found || stored.DeviceID != in.DeviceID {
				t.Errorf("saved session = (%+v, %t, %v), want the new device %s", stored, found, err, in.DeviceID)
			}
		})
	}
}

// A login while Matrix runs saves the session for the next start and leaves the
// running one alone: the socket never replaces the session the sync loop uses.
func TestALoginWhileRunningIsKeptForTheNextStart(t *testing.T) {
	noKeyring()
	hs := newHomeserver(t)
	m, _, keys := testAdapter(t, hs, domain.Session{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	defer func() {
		cancel()
		<-done
		m.Stop()
	}()

	first, err := m.passwordLogin(ctx, "secret")
	if err != nil || !first.Started {
		t.Fatalf("first passwordLogin = (%+v, %v), want started", first, err)
	}
	waitUntil(t, "logged in", m.LoggedIn)
	second, err := m.passwordLogin(ctx, "secret")
	if err != nil || second.Started {
		t.Fatalf("second passwordLogin = (%+v, %v), want saved, not started", second, err)
	}
	if got := m.Account(); got != "@me:x" {
		t.Errorf("Account = %q after a second login, want the running one's", got)
	}
	if stored, _, _ := session.Load(keys, true); stored.DeviceID != second.DeviceID {
		t.Errorf("saved device = %s, want the newest login's %s for the next start", stored.DeviceID, second.DeviceID)
	}
}

// Logins at any moment — before Start, while waiting, while resuming, while running,
// several at once — start Matrix exactly once and never block: one is handed over
// (or the saved session takes), the rest are saved.
func TestLoginsAtAnyMomentStartMatrixOnce(t *testing.T) {
	noKeyring()
	hs := newHomeserver(t)
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 0)) // #nosec G404 -- a test's reproducible interleaving
		saved := domain.Session{}
		if rng.IntN(3) == 0 {
			saved = domain.Session{Homeserver: hs.URL, UserID: "@me:x", DeviceID: "OLD", AccessToken: "revoked"}
		}
		m, _, _ := testAdapter(t, hs, saved)
		hs.whoamiDelay.Store(int64(time.Duration(rng.IntN(15)) * time.Millisecond))
		ctx, cancel := context.WithCancel(context.Background())
		logins := 1 + rng.IntN(4)
		before := rng.IntN(logins + 1) // logins sent before Start runs
		var started atomic.Int64
		var wg sync.WaitGroup
		login := func() {
			defer wg.Done()
			in, err := m.passwordLogin(ctx, "secret")
			if err != nil {
				t.Errorf("seed %d: passwordLogin = %v", seed, err)
				return
			}
			if in.Started {
				started.Add(1)
			}
		}
		wg.Add(logins)
		for range before {
			go login()
		}
		time.Sleep(time.Duration(rng.IntN(3)) * time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- m.Start(ctx) }()
		for range logins - before {
			time.Sleep(time.Duration(rng.IntN(3)) * time.Millisecond)
			go login()
		}
		wg.Wait()
		waitUntil(t, fmt.Sprintf("seed %d: logged in", seed), m.LoggedIn)
		if n := started.Load(); n != 1 {
			t.Errorf("seed %d: %d of %d logins (%d before Start) said they started Matrix, want exactly 1",
				seed, n, logins, before)
		}
		cancel()
		<-done
		m.Stop()
	}
}

// Shut down while logged out, the adapter returns cleanly.
func TestMatrixLoggedOutStopsCleanly(t *testing.T) {
	noKeyring()
	m, p, _ := testAdapter(t, newHomeserver(t), domain.Session{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	waitUntil(t, "logged out", func() bool { return p.last() == domain.AccountLoggedOut })
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Start = %v after shutdown while logged out, want nil", err)
	}
	m.Stop()
}
