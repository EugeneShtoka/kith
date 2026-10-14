package linkedin

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memSecrets is an in-memory secret store; fail makes every read fail, and beforeStore
// runs before each write (outside the store's lock).
type memSecrets struct {
	mu          sync.Mutex
	values      map[string]string
	fail        error
	beforeStore func(ref, value string)
}

func newSecrets() *memSecrets { return &memSecrets{values: map[string]string{}} }

func (m *memSecrets) Secret(ref string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return "", false, m.fail
	}
	v, ok := m.values[ref]
	return v, ok, nil
}

func (m *memSecrets) StoreSecret(ref, value string) error {
	if m.beforeStore != nil {
		m.beforeStore(ref, value)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[ref] = value
	return nil
}

func (m *memSecrets) DeleteSecret(ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, ref)
	return nil
}

// aSession is a complete session whose li_at cookie is liAt.
func aSession(profile, liAt string) Session {
	jar, err := linkedingo.NewJarFromCookieHeader(`li_at=` + liAt + `; JSESSIONID="ajax:1"; lang=v=2&lang=en-us`)
	if err != nil {
		panic(err)
	}
	return Session{ProfileID: profile, Cookies: jar}
}

// setLiAt rotates s's li_at cookie in place, as a response's Set-Cookie does.
func setLiAt(s Session, value string) {
	s.Cookies.SetCookies(linkedingo.CookieBaseURL, []*http.Cookie{{Name: cookieSession, Value: value}})
}

func withAccounts(names ...string) config.Config {
	var cfg config.Config
	for _, name := range names {
		cfg.LinkedIn.Accounts = append(cfg.LinkedIn.Accounts, config.LinkedInAccount{Name: name})
	}
	return cfg
}

// A session goes into the store whole and comes back the same; one that is not whole
// is unusable, which is the account's problem, not the store's.
func TestSessionsAreKeptWhole(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	s := aSession("ACoAA1", "token-1")
	s.Track, s.PageInstance = `{"clientVersion":"1"}`, "urn:li:page:x"
	if err := saveSession(secrets, "li", s); err != nil {
		t.Fatal(err)
	}
	got, ok, err := loadSession(secrets, "li")
	if err != nil || !ok {
		t.Fatalf("load = %v, %v", ok, err)
	}
	if got.ProfileID != "ACoAA1" || got.Cookies.GetCookie(cookieSession) != "token-1" ||
		got.Cookies.GetCookie(cookieCSRF) != "ajax:1" || got.Track != s.Track || got.PageInstance != s.PageInstance {
		t.Errorf("loaded %+v", got)
	}
	if _, ok, err := loadSession(secrets, "other"); ok || err != nil {
		t.Errorf("an account never signed in = %v, %v", ok, err)
	}
	if err := saveSession(secrets, "li", Session{ProfileID: "ACoAA1"}); !errors.Is(err, errIncomplete) {
		t.Errorf("saving a session with no cookies = %v", err)
	}
	if err := saveSession(secrets, "li", aSession("", "t")); !errors.Is(err, errIncomplete) {
		t.Errorf("saving a session with no profile = %v", err)
	}
	for name, blob := range map[string]string{
		"not json":     "{",
		"no cookies":   `{"profile_id":"ACoAA1"}`,
		"half cookies": `{"profile_id":"ACoAA1","cookies":"li_at=x"}`,
	} {
		secrets.values[sessionRef("li")] = blob
		if _, _, err := loadSession(secrets, "li"); !errors.Is(err, errUnusable) {
			t.Errorf("%s: load = %v, want unusable", name, err)
		}
	}

	// Unusable sessions are skipped, so the daemon does not wait for them; an
	// unreadable store is an error.
	a := New(nil, secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("li", "work"))
	_ = saveSession(secrets, "work", aSession("ACoAA2", "w"))
	if signed, err := a.SavedSessions(context.Background()); err != nil || len(signed) != 1 || signed[0] != "work" {
		t.Errorf("SavedSessions = %v, %v", signed, err)
	}
	secrets.fail = errors.New("locked")
	if _, err := a.SavedSessions(context.Background()); err == nil {
		t.Error("an unreadable store was not an error")
	}
}

// A paste is the Cookie header's value, the header line, or a one-line "Copy as
// cURL"; anything without the session cookies is refused saying what to copy.
func TestPastesAreRead(t *testing.T) {
	t.Parallel()

	const cookies = `bcookie="v=2&abc"; li_at=AQEDAT; JSESSIONID="ajax:123"; lang=v=2&lang=en-us`
	tests := map[string]struct {
		paste       string
		track, page string
		err         error
	}{
		"the value":           {paste: cookies},
		"spaces around":       {paste: "  " + cookies + "\n"},
		"the header line":     {paste: "Cookie: " + cookies},
		"lower-case header":   {paste: "cookie:" + cookies},
		"quoted":              {paste: `'li_at=AQEDAT; JSESSIONID="ajax:123"'`},
		"double-quoted":       {paste: `"li_at=AQEDAT; JSESSIONID=ajax:123"`},
		"a quoted value last": {paste: `li_at=AQEDAT; JSESSIONID="ajax:123"`},
		"with Google's state": {paste: `g_state={"i_l":0,"i_p":1}; ` + cookies},
		"Chrome's cURL": {
			paste: `curl 'https://www.linkedin.com/voyager/api/me' -H 'accept: */*' -H 'cookie: ` + cookies + `' ` +
				`-H 'x-li-page-instance: urn:li:page:feed;abc' -H 'x-li-track: {"clientVersion":"1.13.1","mpName":"voyager-web"}'`,
			track: `{"clientVersion":"1.13.1","mpName":"voyager-web"}`, page: "urn:li:page:feed;abc",
		},
		"Firefox's cURL with -b": {paste: `curl 'https://www.linkedin.com/feed/' -H 'Accept: text/html' -b '` + cookies + `'`},
		"double-quoted cURL":     {paste: `curl "https://www.linkedin.com/" -H "Cookie: li_at=AQEDAT; JSESSIONID=ajax:123"`},
		"nothing":                {paste: "  ", err: errNoCookies},
		"cURL with no cookies":   {paste: `curl 'https://www.linkedin.com/' -H 'accept: */*'`, err: errNoCookies},
		"no li_at":               {paste: `JSESSIONID="ajax:123"; lang=en`, err: errNoSession},
		"no JSESSIONID":          {paste: `li_at=AQEDAT`, err: errNoSession},
		"a token alone":          {paste: `AQEDAT`, err: errNoCookies},
	}
	for name, tc := range tests {
		got, err := parsePaste(tc.paste)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Errorf("%s: err = %v, want %v", name, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.cookies.GetCookie(cookieSession) != "AQEDAT" || got.cookies.GetCookie(cookieCSRF) != "ajax:123" {
			t.Errorf("%s: li_at %q, JSESSIONID %q", name, got.cookies.GetCookie(cookieSession), got.cookies.GetCookie(cookieCSRF))
		}
		if got.track != tc.track || got.pageInstance != tc.page {
			t.Errorf("%s: track %q, page %q", name, got.track, got.pageInstance)
		}
	}
}

// What LinkedIn's answers mean for a session: the ones that end it say what to do,
// the rest are worth trying again.
func TestRefusal(t *testing.T) {
	t.Parallel()

	redirected := &url.Error{Op: "Get", URL: "https://www.linkedin.com/voyager/api/me",
		Err: fmt.Errorf("%w: li_at cookie was deleted", linkedingo.ErrTokenInvalidated)}
	tests := map[string]struct {
		err  error
		says string
	}{
		"li_at deleted":       {redirected, "ended this session"},
		"refused on realtime": {fmt.Errorf("%w (got 401 on connect)", errSessionEnded), "ended this session"},
		"401":                 {&linkedingo.ResponseError{StatusCode: 401}, "refused these cookies"},
		"302 to the login":    {fmt.Errorf("wrapped: %w", &linkedingo.ResponseError{StatusCode: 302}), "refused these cookies"},
		"403":                 {&linkedingo.ResponseError{StatusCode: 403}, "wants a check"},
		"999":                 {&linkedingo.ResponseError{StatusCode: 999}, "wants a check"},
		"429":                 {&linkedingo.ResponseError{StatusCode: 429}, ""},
		"500":                 {&linkedingo.ResponseError{StatusCode: 500}, ""},
		"no network":          {errors.New("dial tcp: connection refused"), ""},
		"timed out":           {context.DeadlineExceeded, ""},
	}
	for name, tc := range tests {
		got := refusal(tc.err)
		if (tc.says == "") != (got == "") || !strings.Contains(got, tc.says) {
			t.Errorf("%s: refusal = %q, want %q", name, got, tc.says)
		}
	}
}

func TestCheckConfig(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	if err := a.CheckConfig(context.Background(), withAccounts("li", "work")); err != nil {
		t.Errorf("two named accounts: %v", err)
	}
	for name, cfg := range map[string]config.Config{"no name": withAccounts(""), "a name twice": withAccounts("li", "li")} {
		if err := a.CheckConfig(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "linkedin.account") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type statuses struct {
	mu  sync.Mutex
	got []domain.AccountStatus
}

func (s *statuses) hear(st domain.AccountStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, st)
}

func (s *statuses) last() domain.AccountStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.got) == 0 {
		return domain.AccountStatus{}
	}
	return s.got[len(s.got)-1]
}

// An account with no session says how to sign in, one whose session will not read says
// so; the adapter stops cleanly.
func TestAnAccountWithNoSessionSaysHowToSignIn(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	secrets.values[sessionRef("broken")] = "{"
	a := New(nil, secrets, nil, nil)
	var heard statuses
	a.OnStatus(heard.hear)
	a.UseConfig(context.Background(), withAccounts("li", "broken"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- a.Start(ctx) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Start = %v, want canceled", err)
	}
	a.Stop()
	a.Stop() // twice is fine
	heard.mu.Lock()
	got := heard.got
	heard.mu.Unlock()
	if len(got) != 2 || got[0].Phase != SignedOut || got[0].Network != domain.ProtocolLinkedIn ||
		!strings.Contains(got[0].Detail, "kith login linkedin li") ||
		got[1].Phase != SignedOut || !strings.Contains(got[1].Detail, "could not be read") {
		t.Errorf("statuses = %+v", got)
	}
	if err := a.Start(context.Background()); !errors.Is(err, errStartedTwice) {
		t.Errorf("a second Start = %v", err)
	}
	for _, open := range []bool{chanOpen(a.Messages()), chanOpen(a.Activity()), chanOpen(a.Unread()), chanOpen(a.Reactions())} {
		if open {
			t.Error("a stream is open after Stop")
		}
	}
	if a.Network() != domain.ProtocolLinkedIn {
		t.Errorf("Network = %v", a.Network())
	}
}

func chanOpen[T any](ch <-chan T) bool {
	_, open := <-ch
	return open
}

// A connection belongs to its account's newest sign-in while the account is
// configured; a removed account's connection is let go.
func TestConnectionsFollowSignInsAndConfig(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	a.UseConfig(context.Background(), withAccounts("li", "work"))
	old := a.newConn(Account{Name: "li"}, aSession("ACoAA1", "a"), nil)
	if !a.adopt(old) {
		t.Fatal("the first connection was refused")
	}
	signIn := a.newSignIn("li")
	if !a.adopt(a.newConn(Account{Name: "li"}, aSession("ACoAA1", "b"), nil)) {
		t.Fatal("a connection on the newest sign-in was refused")
	}
	if !old.closed() {
		t.Error("the replaced connection is still open")
	}
	if stale := a.newConn(Account{Name: "li"}, aSession("ACoAA1", "c"), &[]int{signIn - 1}[0]); a.adopt(stale) {
		t.Error("a connection from an older sign-in was adopted")
	}
	if a.adopt(a.newConn(Account{Name: "elsewhere"}, aSession("ACoAA3", "d"), nil)) {
		t.Error("a connection for an unconfigured account was adopted")
	}
	work := a.newConn(Account{Name: "work"}, aSession("ACoAA2", "e"), nil)
	a.adopt(work)
	if me := a.Me(); len(me) != 2 || me[0] != "linkedin:ACoAA1" || me[1] != "linkedin:ACoAA2" {
		t.Errorf("Me = %v", me)
	}
	if other := a.signedInAs("ACoAA2", "li"); other != "work" {
		t.Errorf("signedInAs(ACoAA2) = %q, want work", other)
	}
	if other := a.signedInAs("ACoAA2", "work"); other != "" {
		t.Errorf("an account is signed in as itself elsewhere: %q", other)
	}
	a.UseConfig(context.Background(), withAccounts("li"))
	if !work.closed() {
		t.Error("a removed account's connection is still open")
	}
	a.Stop()
	if a.adopt(a.newConn(Account{Name: "li"}, aSession("ACoAA1", "f"), nil)) {
		t.Error("a connection was adopted after Stop")
	}
}

// writer is one sign-in's connection, as the generator drives it.
type writer struct {
	c      *conn
	signIn int
	n      int // how many times its cookies rotated
}

// rotate gives w's session a new li_at cookie, tagged with its sign-in.
func (w *writer) rotate() {
	w.n++
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	setLiAt(w.c.session, fmt.Sprintf("s%d-%d", w.signIn, w.n))
}

// login is a sign-in landing as signIn does: counted, saved under keeping, adopted.
func login(t *testing.T, a *Adapter, secrets *memSecrets) *writer {
	t.Helper()
	signIn := a.newSignIn("li")
	s := aSession("ACoAA1", fmt.Sprintf("s%d-0", signIn))
	a.keeping.Lock()
	err := saveSession(secrets, "li", s)
	a.keeping.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c := a.newConn(Account{Name: "li"}, s, &signIn)
	a.adopt(c)
	return &writer{c: c, signIn: signIn}
}

// Two writers keep one account's session: a login saves a new one, and every
// connection saves its cookies when LinkedIn rotates them. Whatever the interleaving,
// the stored session is always the newest sign-in's: an older connection's rotation
// never overwrites it.
func TestTheStoredSessionIsAlwaysTheNewestSignIns(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(7, 99))
	for run := range 300 {
		secrets := newSecrets()
		a := New(nil, secrets, nil, nil)
		a.UseConfig(context.Background(), withAccounts("li"))
		writers := []*writer{login(t, a, secrets)}
		var steps []string
		for range 12 {
			w := writers[rng.IntN(len(writers))]
			switch rng.IntN(3) {
			case 0:
				writers = append(writers, login(t, a, secrets))
				steps = append(steps, "login")
			case 1:
				w.rotate()
				steps = append(steps, fmt.Sprintf("rotate s%d", w.signIn))
			default:
				a.keepCookies(w.c)
				steps = append(steps, fmt.Sprintf("keep s%d", w.signIn))
			}
			newest := writers[len(writers)-1]
			stored, _, err := loadSession(secrets, "li")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(stored.Cookies.GetCookie(cookieSession), fmt.Sprintf("s%d-", newest.signIn)) {
				t.Fatalf("run %d: after %v the stored session is %s, not sign-in %d's", run, steps,
					stored.Cookies.GetCookie(cookieSession), newest.signIn)
			}
		}
		// The newest connection's last rotation, once kept, is what is stored.
		newest := writers[len(writers)-1]
		newest.rotate()
		a.keepCookies(newest.c)
		stored, _, _ := loadSession(secrets, "li")
		if want := fmt.Sprintf("s%d-%d", newest.signIn, newest.n); stored.Cookies.GetCookie(cookieSession) != want {
			t.Fatalf("run %d: stored %s, want the newest rotation %s", run, stored.Cookies.GetCookie(cookieSession), want)
		}
	}
}

// The same, with the writers racing: an old connection keeps saving while logins land.
func TestSessionWritersRacing(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	a := New(nil, secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("li"))
	old := login(t, a, secrets)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			old.rotate()
			a.keepCookies(old.c)
		}
	})
	var newest *writer
	for range 20 {
		newest = login(t, a, secrets)
	}
	wg.Wait()
	stored, _, err := loadSession(secrets, "li")
	if err != nil || !strings.HasPrefix(stored.Cookies.GetCookie(cookieSession), fmt.Sprintf("s%d-", newest.signIn)) {
		t.Errorf("the stored session is %v (%v), not sign-in %d's", stored.Cookies.GetCookie(cookieSession), err, newest.signIn)
	}
}

// The narrowest interleaving: an old connection has found it is still current and is
// writing its rotated cookies when a login lands. The login's session must still be
// the one kept, so the login waits for the write rather than slipping in under it.
func TestALoginDuringAnOldWriteStillWins(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	a := New(nil, secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("li"))
	old := login(t, a, secrets)
	newest := 0
	landed := make(chan struct{})
	var once sync.Once
	secrets.beforeStore = func(_, value string) {
		if !strings.Contains(value, `"s1-1"`) {
			return
		}
		once.Do(func() {
			go func() {
				defer close(landed)
				newest = a.newSignIn("li")
				a.keeping.Lock()
				defer a.keeping.Unlock()
				if err := saveSession(secrets, "li", aSession("ACoAA1", fmt.Sprintf("s%d-0", newest))); err != nil {
					t.Error(err)
				}
			}()
			// Give the login every chance to land before the old write goes on.
			select {
			case <-landed:
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
	old.rotate()
	a.keepCookies(old.c)
	<-landed
	stored, _, err := loadSession(secrets, "li")
	if err != nil || stored.Cookies.GetCookie(cookieSession) != fmt.Sprintf("s%d-0", newest) {
		t.Errorf("the stored session is %v (%v), not the login's s%d-0", stored.Cookies.GetCookie(cookieSession), err, newest)
	}
}

type talkScript struct {
	answers []map[string]string
	asked   [][]api.LoginField
	notes   []string
	records []api.LoginRecord
	named   string
}

func (s *talkScript) Ask(_ context.Context, note string, fields ...api.LoginField) (map[string]string, error) {
	s.asked = append(s.asked, fields)
	s.notes = append(s.notes, note)
	if len(s.answers) == 0 {
		return nil, context.Canceled
	}
	next := s.answers[0]
	s.answers = s.answers[1:]
	return next, nil
}

func (s *talkScript) Show(context.Context, string, string) error { return nil }

func (s *talkScript) Configure(_ context.Context, record api.LoginRecord) error {
	s.records = append(s.records, record)
	return nil
}

func (s *talkScript) Named(account string) { s.named = account }

// A new account is named, written into the config and asked for its cookies; a paste
// with no session is asked for again, saying why; a login for an account the config
// does not have is refused.
func TestLoginAsksForTheCookies(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	a.UseConfig(context.Background(), withAccounts("linkedin"))
	talk := &talkScript{answers: []map[string]string{{"name": "work"}, {"cookies": "li_at=only"}}}
	// The written account is not in this adapter's config until the daemon re-reads it.
	if _, err := a.Login(context.Background(), "", talk); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("Login = %v, want no such account yet", err)
	}
	if suggested := talk.asked[0][0].Value; suggested == "" || suggested == "linkedin" {
		t.Errorf("suggested %q, a name the config already has", suggested)
	}
	if talk.named != "work" || len(talk.records) != 1 || talk.records[0].Table != "linkedin.account" ||
		talk.records[0].Values["name"] != "work" {
		t.Errorf("named %q, records %+v", talk.named, talk.records)
	}
	if len(talk.asked) != 1 {
		t.Errorf("asked %d times for an account the config does not have yet", len(talk.asked))
	}

	a.UseConfig(context.Background(), withAccounts("linkedin", "work"))
	talk = &talkScript{answers: []map[string]string{{"cookies": "li_at=only"}}}
	if _, err := a.Login(context.Background(), "work", talk); !errors.Is(err, context.Canceled) {
		t.Fatalf("Login = %v", err)
	}
	if len(talk.asked) != 2 || talk.notes[0] != "" || talk.notes[1] != errNoSession.Error() {
		t.Errorf("notes = %q", talk.notes)
	}
	if f := talk.asked[0][0]; f.Key != "cookies" || !f.Secret || !strings.Contains(f.Help, "Cookie") {
		t.Errorf("field = %+v", f)
	}
	if _, err := a.Login(context.Background(), "nobody", talk); !errors.Is(err, ErrNoAccount) {
		t.Errorf("Login(nobody) = %v", err)
	}
	if n := a.LoginNetwork(); n.Network != "linkedin" || len(n.Accounts) != 2 || n.Accounts[1].Name != "work" {
		t.Errorf("LoginNetwork = %+v", n)
	}
}

// How a profile reads: its name, or its public identifier when it has none.
func TestNameOf(t *testing.T) {
	t.Parallel()

	if got := nameOf(linkedingo.MiniProfile{FirstName: "Dana", LastName: "Reyes"}); got != "Dana Reyes" {
		t.Errorf("nameOf = %q", got)
	}
	if got := nameOf(linkedingo.MiniProfile{FirstName: "Dana"}); got != "Dana" {
		t.Errorf("nameOf a first name = %q", got)
	}
	if got := nameOf(linkedingo.MiniProfile{PublicIdentifier: "dana-reyes-1"}); got != "dana-reyes-1" {
		t.Errorf("nameOf no name = %q", got)
	}
}

// The realtime stream's callbacks: connecting shows the account connected, a drop
// shows it reconnecting, LinkedIn refusing the session or the stream giving up reaches
// the connection, and a closed connection hears nothing.
func TestRealtimeCallbacks(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	a := New(nil, secrets, nil, nil)
	var heard statuses
	a.OnStatus(heard.hear)
	a.UseConfig(context.Background(), withAccounts("li"))
	w := login(t, a, secrets)
	h := a.handlers(w.c)
	ctx := context.Background()

	h.ClientConnection(ctx, &linkedingo.ClientConnection{})
	if st := heard.last(); st.Phase != Connected {
		t.Errorf("after connecting: %+v", st)
	}
	h.TransientDisconnect(ctx, errors.New("EOF"))
	if st := heard.last(); st.Phase != Connecting || st.Detail != "reconnecting" {
		t.Errorf("after a drop: %+v", st)
	}
	// Heartbeats come every few seconds: one with nothing rotated writes nothing.
	writes := 0
	secrets.beforeStore = func(string, string) { writes++ }
	h.Heartbeat(ctx)
	if writes != 0 {
		t.Errorf("a heartbeat with nothing rotated wrote the session %d times", writes)
	}
	w.rotate()
	h.Heartbeat(ctx)
	if stored, _, _ := loadSession(secrets, "li"); stored.Cookies.GetCookie(cookieSession) != "s1-1" {
		t.Errorf("a heartbeat did not keep the rotated cookies: %s", stored.Cookies.GetCookie(cookieSession))
	}
	h.BadCredentials(ctx, errors.New("got 401 on connect"))
	h.UnknownError(ctx, errors.New("not kept: one is waiting already"))
	if err := <-w.c.gaveUp; !errors.Is(err, errSessionEnded) || refusal(err) == "" {
		t.Errorf("gave up with %v", err)
	}
	h.UnknownError(ctx, errors.New("failed to connect"))
	if err := <-w.c.gaveUp; refusal(err) != "" {
		t.Errorf("giving up after many tries ends the session: %v", err)
	}

	before := len(heard.got)
	w.c.close()
	h.ClientConnection(ctx, &linkedingo.ClientConnection{})
	if len(heard.got) != before {
		t.Error("a closed connection still reports")
	}
}

// After a failure the connection either tries again, later each time up to a limit,
// or, when LinkedIn refused the session, signs the account out saying what to do.
func TestRetry(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	var heard statuses
	a.OnStatus(heard.hear)
	c := a.newConn(Account{Name: "li"}, aSession("ACoAA1", "a"), nil)
	ctx := context.Background()

	wait := time.Millisecond
	if !a.retry(ctx, c, errors.New("connection reset"), &wait) || wait != 2*time.Millisecond {
		t.Errorf("a transient failure: wait now %v", wait)
	}
	if st := heard.last(); st.Phase != Connecting || !strings.Contains(st.Detail, "retrying") {
		t.Errorf("while retrying: %+v", st)
	}
	wait = retryMax
	canceled, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(time.Millisecond); cancel() }()
	if a.retry(canceled, c, errors.New("connection reset"), &wait) {
		t.Error("retried after the context ended")
	}
	if a.retry(canceled, c, errors.New("connection reset"), &wait) {
		t.Error("retried with the context already ended")
	}
	wait = time.Millisecond
	if a.retry(ctx, c, &linkedingo.ResponseError{StatusCode: 401}, &wait) {
		t.Error("retried a refused session")
	}
	if st := heard.last(); st.Phase != SignedOut || !strings.Contains(st.Detail, "refused these cookies") ||
		!strings.Contains(st.Detail, "kith login linkedin li") {
		t.Errorf("a refused session: %+v", st)
	}
}

// A panic inside the library fails the one call, not the daemon; a realtime callback
// that panics drops its event.
func TestALibraryPanicIsAnError(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	err := a.guard("reading the profile", func() error {
		var p *linkedingo.UserProfile
		_ = p.MiniProfile // a nil dereference, as in the bridge's handlers
		return nil
	})
	if !errors.Is(err, errLibraryPanicked) || !strings.Contains(err.Error(), "while reading the profile") {
		t.Errorf("guard = %v", err)
	}
	if err := a.guard("nothing", func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Errorf("guard passes errors through: %v", err)
	}
	a.OnStatus(func(domain.AccountStatus) { panic("a listener that fails") })
	c := a.newConn(Account{Name: "li"}, aSession("ACoAA1", "a"), nil)
	a.handlers(c).ClientConnection(context.Background(), nil) // dropped, not raised
}
