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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/EugeneShtoka/kith/internal/session"
)

// homeserver is a fake homeserver: logins issue tokens for the user logging in, and
// whoami accepts only those.
type homeserver struct {
	*httptest.Server
	mu     sync.Mutex
	issued map[string]string // token to user
	// data is each user's account data, by type.
	data   map[string]map[string]json.RawMessage
	logins atomic.Int64
	// whoamiDelay lets a login land while a session is being checked.
	whoamiDelay atomic.Int64
}

func newHomeserver(t *testing.T) *homeserver {
	t.Helper()
	h := &homeserver{issued: map[string]string{}, data: map[string]map[string]json.RawMessage{}}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)
	return h
}

func (h *homeserver) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/login") && r.Method == http.MethodGet:
		// DiscoverFlow asks; this server speaks m.login.password.
		_, _ = w.Write([]byte(`{"flows":[{"type":"m.login.password"}]}`))
	case strings.HasSuffix(r.URL.Path, "/login") && r.Method == http.MethodPost:
		var req struct {
			Identifier struct {
				User string `json:"user"`
			} `json:"identifier"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := h.logins.Add(1)
		token := fmt.Sprintf("token-%d", n)
		h.mu.Lock()
		h.issued[token] = req.Identifier.User
		h.mu.Unlock()
		body, _ := json.Marshal(map[string]string{ //nolint:errchkjson // strings only: cannot fail
			"user_id": req.Identifier.User, "access_token": token, "device_id": fmt.Sprintf("DEVICE%d", n),
		})
		_, _ = w.Write(body)
	case strings.HasSuffix(r.URL.Path, "/logout") && r.Method == http.MethodPost:
		h.mu.Lock()
		delete(h.issued, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		h.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	case strings.Contains(r.URL.Path, "/account_data/"):
		h.accountData(w, r)
	case strings.HasSuffix(r.URL.Path, "/account/whoami"):
		time.Sleep(time.Duration(h.whoamiDelay.Load()))
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		h.mu.Lock()
		user, ok := h.issued[token]
		h.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN_TOKEN","error":"unknown token"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"user_id":%q}`, user)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errcode":"M_UNRECOGNIZED","error":"not here"}`))
	}
}

// accountData keeps and serves the account data of the token's user.
func (h *homeserver) accountData(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	h.mu.Lock()
	defer h.mu.Unlock()
	user := h.issued[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if r.Method == http.MethodPut {
		var body json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if h.data[user] == nil {
			h.data[user] = map[string]json.RawMessage{}
		}
		h.data[user][kind] = body
		_, _ = w.Write([]byte(`{}`))
		return
	}
	if body, ok := h.data[user][kind]; ok {
		_, _ = w.Write(body)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"no such account data"}`))
}

// phases records what the adapter reported, in order, by account.
type phases struct {
	mu  sync.Mutex
	got map[string][]domain.AccountPhase
}

func (p *phases) add(s domain.AccountStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.got == nil {
		p.got = map[string][]domain.AccountPhase{}
	}
	p.got[s.Account] = append(p.got[s.Account], s.Phase)
}

// last is account's latest phase.
func (p *phases) last(account string) domain.AccountPhase {
	p.mu.Lock()
	defer p.mu.Unlock()
	got := p.got[account]
	if len(got) == 0 {
		return 0
	}
	return got[len(got)-1]
}

// testPlace keeps each account's crypto store and session under dir, its keyring
// entries under service.
func testPlace(dir string) Place {
	return Place{
		CryptoPath: func(key string) string { return filepath.Join(dir, "crypto-"+key+".db") },
		Keys: func(key string) session.Store {
			return session.Store{Service: "kith-test-" + filepath.Base(dir), Account: key, File: filepath.Join(dir, "session-"+key+".toml")}
		},
	}
}

// testAdapter is an adapter for hs running @me:x (named "me"), starting from saved.
func testAdapter(t *testing.T, hs *homeserver, saved domain.Session) (*Adapter, *phases, session.Store) {
	t.Helper()
	m, p, dir := freshAdapter(t)
	keys := testPlace(dir).Keys(domain.AccountKey("@me:x"))
	if saved.AccessToken != "" {
		if err := session.Save(keys, saved, true); err != nil {
			t.Fatal(err)
		}
	}
	m.UseConfig(t.Context(), accountsConfig(hs, "@me:x"))
	return m, p, keys
}

// freshAdapter is a Matrix adapter with no account, over a fresh cache and directory.
func freshAdapter(t *testing.T) (*Adapter, *phases, string) {
	t.Helper()
	dir := t.TempDir()
	cache, err := db.Open(context.Background(), filepath.Join(dir, "cache.db"), make([]byte, sealeddb.KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	m := NewAdapter(cache, slog.New(slog.DiscardHandler), testPlace(dir))
	p := &phases{}
	m.OnStatus(p.add)
	return m, p, dir
}

// accountsConfig is a config naming users on hs, each called by its localpart, their
// sessions in the file fallback.
func accountsConfig(hs *homeserver, users ...string) config.Config {
	cfg := config.Config{AllowTokenFile: true}
	for _, user := range users {
		local, _, _ := strings.Cut(strings.TrimPrefix(user, "@"), ":")
		cfg.Matrix.Accounts = append(cfg.Matrix.Accounts, config.MatrixAccount{Name: local, Homeserver: hs.URL, User: user})
	}
	return cfg
}

// named is the runner of the account called name.
func named(t *testing.T, m *Adapter, name string) *runner {
	t.Helper()
	r, err := m.runnerNamed(name)
	if err != nil {
		t.Fatal(err)
	}
	return r
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

// noKeyring forces the token-file fallback: go-keyring's mock is not safe for the
// concurrent logins here. It is process-wide, so its tests are not parallel.
func noKeyring() { keyring.MockInitWithError(errors.New("no secret service in tests")) }

// started runs m until the test ends.
func started(t *testing.T, m *Adapter) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		m.Stop()
	})
	return ctx
}

// With no session or a rejected one, an account waits logged out until a login.
func TestMatrixWaitsForALoginAndStartsOnIt(t *testing.T) {
	noKeyring()
	hs := newHomeserver(t)
	for name, saved := range map[string]domain.Session{
		"no session":       {},
		"rejected session": {Homeserver: hs.URL, UserID: "@me:x", DeviceID: "OLD", AccessToken: "revoked"},
	} {
		t.Run(name, func(t *testing.T) {
			m, p, keys := testAdapter(t, hs, saved)
			ctx := started(t, m)
			waitUntil(t, "logged out", func() bool { return p.last("me") == domain.AccountLoggedOut })
			if m.LoggedIn() {
				t.Fatal("LoggedIn before any usable session")
			}
			in, err := named(t, m, "me").passwordLogin(ctx, "secret")
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

// A login while running is saved for the next start; the running session stays.
func TestALoginWhileRunningIsKeptForTheNextStart(t *testing.T) {
	noKeyring()
	hs := newHomeserver(t)
	m, _, keys := testAdapter(t, hs, domain.Session{})
	ctx := started(t, m)
	me := named(t, m, "me")
	first, err := me.passwordLogin(ctx, "secret")
	if err != nil || !first.Started {
		t.Fatalf("first passwordLogin = (%+v, %v), want started", first, err)
	}
	waitUntil(t, "logged in", m.LoggedIn)
	second, err := me.passwordLogin(ctx, "secret")
	if err != nil || second.Started {
		t.Fatalf("second passwordLogin = (%+v, %v), want saved, not started", second, err)
	}
	if got := me.Account(); got != "matrix:@me:x" {
		t.Errorf("Account = %q after a second login, want the running one's", got)
	}
	if stored, _, _ := session.Load(keys, true); stored.DeviceID != second.DeviceID {
		t.Errorf("saved device = %s, want the newest login's %s for the next start", stored.DeviceID, second.DeviceID)
	}
}

// Logins at any moment, concurrent or not, start an account exactly once and never block.
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
		me := named(t, m, "me")
		hs.whoamiDelay.Store(int64(time.Duration(rng.IntN(15)) * time.Millisecond))
		ctx, cancel := context.WithCancel(context.Background())
		logins := 1 + rng.IntN(4)
		before := rng.IntN(logins + 1) // logins sent before Start runs
		var started atomic.Int64
		var wg sync.WaitGroup
		login := func() {
			defer wg.Done()
			in, err := me.passwordLogin(ctx, "secret")
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

func TestMatrixLoggedOutStopsCleanly(t *testing.T) {
	noKeyring()
	m, p, _ := testAdapter(t, newHomeserver(t), domain.Session{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	waitUntil(t, "logged out", func() bool { return p.last("me") == domain.AccountLoggedOut })
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Start = %v after shutdown while logged out, want nil", err)
	}
	m.Stop()
}

// An account set up inside kith starts without a restart, and so does a second one,
// beside the first.
func TestMatrixAccountsSetUpInsideKithStartWithoutARestart(t *testing.T) {
	noKeyring()
	hs := newHomeserver(t)
	m, _, _ := freshAdapter(t)
	ctx := started(t, m)
	time.Sleep(20 * time.Millisecond)
	if len(m.LoginNetwork().Accounts) != 0 || m.LoggedIn() {
		t.Fatalf("with no account: accounts %v; want none", m.LoginNetwork().Accounts)
	}
	var users []string
	setUp := func(user string, answers map[string][]string) (*apitest.Talk, api.LoginEnd, error) {
		talk := &apitest.Talk{Answers: answers}
		talk.OnConfigure = func(r api.LoginRecord) error {
			if r.Table != "matrix.account" || r.Values["name"] == "" {
				t.Errorf("configured %+v, want a named [[matrix.account]]", r)
			}
			users = append(users, r.Values["user"])
			cfg := accountsConfig(hs, users...)
			cfg.Matrix.Accounts[len(users)-1].Name = r.Values["name"]
			m.UseConfig(ctx, cfg)
			return nil
		}
		end, err := m.Login(ctx, "", talk)
		return talk, end, err
	}
	talk, end, err := setUp("@me:x", map[string][]string{
		"homeserver": {"not a url", hs.URL}, "user": {"@:x", "@me:x"}, "name": {""}, "password": {"secret"},
	})
	if err != nil || end.Restart || !strings.Contains(end.Done, "@me:x") {
		t.Fatalf("login = (%+v, %v), want logged in, no restart", end, err)
	}
	if len(talk.Notes) != 2 || talk.Account != "me" {
		t.Errorf("notes %q, named %q; want both refusals, and the suggested name me", talk.Notes, talk.Account)
	}
	waitUntil(t, "logged in", m.LoggedIn)

	talk, end, err = setUp("@other:x", map[string][]string{
		"homeserver": {hs.URL}, "user": {"@me:x", "@other:x"}, "name": {"me", "work"}, "password": {"secret"},
	})
	if err != nil || end.Restart {
		t.Fatalf("second login = (%+v, %v), want logged in, no restart", end, err)
	}
	if len(talk.Notes) != 2 {
		t.Errorf("notes %q, want @me:x and the name me refused as taken", talk.Notes)
	}
	waitUntil(t, "both logged in", func() bool { return len(m.live()) == 2 })
	if got := m.LoginNetwork().Accounts; len(got) != 2 || got[0].Name != "me" || got[1].Name != "work" || got[1].Detail != "@other:x" {
		t.Errorf("accounts = %v, want me and work", got)
	}
}

// Under any interleaving of reloads that add, remove, rename or move accounts, logins
// for them, sessions resuming and shutdown, every configured account ends with exactly
// one runner, logged in once it was logged in since it was last added; a removed one
// is stopped; and every exit route returns, with the merged streams closed.
func TestAccountsComeAndGoWhateverRuns(t *testing.T) {
	noKeyring()
	hs, other := newHomeserver(t), newHomeserver(t)
	users := []string{"@a:x", "@b:x", "@c:x"}
	for seed := range uint64(30) {
		rng := rand.New(rand.NewPCG(seed, 11))
		m, _, _ := freshAdapter(t)
		hs.whoamiDelay.Store(int64(time.Duration(rng.IntN(6)) * time.Millisecond))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		startAt := rng.IntN(4)
		var trail []string
		var cfg config.Config
		// loggedIn is the accounts logged in since they were last added, by user.
		loggedIn := map[string]bool{}
		var logins sync.WaitGroup
		for step := range 10 {
			if step == startAt {
				go func() { done <- m.Start(ctx) }()
				trail = append(trail, "start")
			}
			switch rng.IntN(3) {
			case 0, 1:
				var picked []string
				for _, u := range users {
					if rng.IntN(2) == 0 {
						picked = append(picked, u)
					}
				}
				next := accountsConfig(hs, picked...)
				for i := range next.Matrix.Accounts {
					acc := &next.Matrix.Accounts[i]
					if rng.IntN(4) == 0 {
						acc.Name += "-renamed"
					}
					if rng.IntN(5) == 0 {
						acc.Homeserver = other.URL // moved: the old runner stops before the new opens its store
					}
					was := slices.IndexFunc(cfg.Matrix.Accounts, func(a config.MatrixAccount) bool { return a.User == acc.User })
					if was < 0 || cfg.Matrix.Accounts[was].Homeserver != acc.Homeserver {
						loggedIn[acc.User] = false
					}
				}
				for _, u := range users {
					if !slices.ContainsFunc(next.Matrix.Accounts, func(a config.MatrixAccount) bool { return a.User == u }) {
						delete(loggedIn, u)
					}
				}
				cfg = next
				before := m.runnersNow()
				m.UseConfig(ctx, cfg)
				trail = append(trail, fmt.Sprintf("config %v", next.Matrix.Accounts))
				for _, r := range before {
					if slices.Contains(m.runnersNow(), r) {
						continue
					}
					select {
					case <-r.done:
					default:
						t.Fatalf("seed %d: %s's runner was dropped but still runs, after %v", seed, r.account().Name, trail)
					}
				}
			default:
				if len(cfg.Matrix.Accounts) == 0 {
					continue
				}
				acc := cfg.Matrix.Accounts[rng.IntN(len(cfg.Matrix.Accounts))]
				r, err := m.runnerNamed(acc.Name)
				if err != nil {
					t.Fatalf("seed %d: %s configured but %v", seed, acc.Name, err)
				}
				logins.Go(func() { _, _ = r.passwordLogin(ctx, "secret") })
				if acc.Homeserver == hs.URL {
					loggedIn[acc.User] = true
				}
				trail = append(trail, "login "+acc.Name)
			}
		}
		loginsDone := make(chan struct{})
		go func() { logins.Wait(); close(loginsDone) }()
		select {
		case <-loginsDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("seed %d: a login never returned, after %v", seed, trail)
		}
		names := make([]string, 0, len(cfg.Matrix.Accounts))
		for _, acc := range cfg.Matrix.Accounts {
			names = append(names, acc.Name)
		}
		var listed []string
		for _, a := range m.LoginNetwork().Accounts {
			listed = append(listed, a.Name)
		}
		if !slices.Equal(listed, names) {
			t.Fatalf("seed %d: accounts %v, want %v after %v", seed, listed, names, trail)
		}
		for _, acc := range cfg.Matrix.Accounts {
			if got := named(t, m, acc.Name).account().Homeserver; got != acc.Homeserver {
				t.Fatalf("seed %d: %s runs on %s, want %s, after %v", seed, acc.Name, got, acc.Homeserver, trail)
			}
			if loggedIn[acc.User] {
				r := named(t, m, acc.Name)
				waitUntil(t, fmt.Sprintf("seed %d: %s logged in after %v", seed, acc.Name, trail), r.loggedIn.Load)
			}
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("seed %d: Start did not return after shutdown, after %v", seed, trail)
		}
		m.Stop()
		if _, open := <-m.Messages(); open {
			t.Fatalf("seed %d: the merged messages stream is open after Stop", seed)
		}
	}
}

// A message an account published before it stopped reaches the merged stream, though
// the account stopped before it was carried over: each one is cached first, so losing
// it on the way would lose its notification.
func TestNoMessageIsLostWhenItsAccountStops(t *testing.T) {
	t.Parallel()
	const sent = 20
	in := make(chan domain.Message, sent)
	for i := range sent {
		in <- domain.Message{ID: domain.EventID(fmt.Sprintf("%s$%d", mx, i))}
	}
	done := make(chan struct{})
	close(done) // stopped before the carrying starts
	var got []domain.Message
	var wg sync.WaitGroup
	carry(&wg, in, done, func(v domain.Message) { got = append(got, v) })
	wg.Wait()
	if len(got) != sent {
		t.Errorf("%d of %d messages were carried over", len(got), sent)
	}
}
