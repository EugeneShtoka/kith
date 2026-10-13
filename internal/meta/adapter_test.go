package meta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	"maunium.net/go/mautrix/bridgev2"

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

// aSession is a complete session whose xs cookie is xs.
func aSession(metaID int64, xs string) Session {
	return Session{MetaID: metaID, Platform: types.MessengerLiteIOS, Cookies: map[string]string{
		"c_user": fmt.Sprint(metaID), "xs": xs, "datr": "datr-value",
	}}
}

func withAccounts(names ...string) config.Config {
	var cfg config.Config
	for _, name := range names {
		cfg.Messenger.Accounts = append(cfg.Messenger.Accounts, config.MessengerAccount{Name: name})
	}
	return cfg
}

// A session goes into the store whole and comes back the same; one that is not whole
// is unusable, which is the account's problem, not the store's.
func TestSessionsAreKeptWhole(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	want := aSession(100001, "xs-1")
	want.Native = &types.NativeSession{AccessToken: "token", AppID: "app"}
	if err := saveSession(secrets, "fb", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := loadSession(secrets, "fb")
	if err != nil || !ok {
		t.Fatalf("loadSession = %v, %v", ok, err)
	}
	if got.MetaID != want.MetaID || got.Platform != want.Platform || got.Cookies["xs"] != "xs-1" || got.Native.AccessToken != "token" {
		t.Errorf("loaded %+v, want %+v", got, want)
	}

	if bad := saveSession(secrets, "fb", Session{MetaID: 1, Platform: types.MessengerLiteIOS}); !errors.Is(bad, errIncomplete) {
		t.Errorf("saving a session with no cookies = %v, want errIncomplete", bad)
	}
	secrets.values[sessionRef("broken")] = "{not json"
	missing, err := json.Marshal(Session{MetaID: 2, Platform: types.MessengerLiteIOS, Cookies: map[string]string{"c_user": "2"}})
	if err != nil {
		t.Fatal(err)
	}
	secrets.values[sessionRef("partial")] = string(missing)
	for _, name := range []string{"broken", "partial"} {
		if _, _, unusable := loadSession(secrets, name); !errors.Is(unusable, errUnusable) {
			t.Errorf("%s: loadSession = %v, want errUnusable", name, unusable)
		}
	}

	a := New(nil, secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("fb", "broken", "partial", "never"))
	saved, err := a.SavedSessions(context.Background())
	if err != nil || len(saved) != 1 || saved[0] != "fb" {
		t.Errorf("SavedSessions = %v, %v; want [fb]", saved, err)
	}
	secrets.fail = errors.New("keyring locked")
	if _, err := a.SavedSessions(context.Background()); err == nil {
		t.Error("SavedSessions over a failing store did not fail")
	}
}

// Accounts need distinct names; which Meta account each is comes from its login.
func TestCheckConfig(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	for _, tc := range []struct {
		cfg  config.Config
		want string
	}{
		{withAccounts("fb"), ""},
		{withAccounts("fb", "work"), ""},
		{withAccounts("fb", "fb"), "used twice"},
		{withAccounts(""), "name is empty"},
	} {
		err := a.CheckConfig(context.Background(), tc.cfg)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("CheckConfig(%v) = %v, want %q", tc.cfg.Messenger.Accounts, err, tc.want)
		}
	}
}

// statuses collects the states an adapter reports.
type statuses struct {
	mu  sync.Mutex
	got []domain.AccountStatus
}

func (s *statuses) hear(st domain.AccountStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, st)
}

// An account with no session says how to sign in; the adapter stops cleanly.
func TestAnAccountWithNoSessionSaysHowToSignIn(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	var heard statuses
	a.OnStatus(heard.hear)
	a.UseConfig(context.Background(), withAccounts("fb"))
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
	defer heard.mu.Unlock()
	if len(heard.got) != 1 || heard.got[0].Phase != SignedOut || !strings.Contains(heard.got[0].Detail, "kith login messenger fb") {
		t.Errorf("statuses = %+v", heard.got)
	}
	if err := a.Start(context.Background()); !errors.Is(err, errStartedTwice) {
		t.Errorf("a second Start = %v", err)
	}
	if _, open := <-a.Messages(); open {
		t.Error("the message stream is open after Stop")
	}
}

// A connection belongs to its account's newest sign-in while the account is
// configured; a removed account's connection is let go.
func TestConnectionsFollowSignInsAndConfig(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	a.UseConfig(context.Background(), withAccounts("fb", "work"))
	old := a.newConn(Account{Name: "fb"}, aSession(1, "a"), nil)
	if !a.adopt(old) {
		t.Fatal("the first connection was refused")
	}
	signIn := a.newSignIn("fb")
	if a.adopt(a.newConn(Account{Name: "fb"}, aSession(1, "b"), nil)) == false {
		t.Fatal("a connection on the newest sign-in was refused")
	}
	if !old.closed() {
		t.Error("the replaced connection is still open")
	}
	if stale := a.newConn(Account{Name: "fb"}, aSession(1, "c"), &[]int{signIn - 1}[0]); a.adopt(stale) {
		t.Error("a connection from an older sign-in was adopted")
	}
	if a.adopt(a.newConn(Account{Name: "elsewhere"}, aSession(3, "d"), nil)) {
		t.Error("a connection for an unconfigured account was adopted")
	}
	work := a.newConn(Account{Name: "work"}, aSession(2, "e"), nil)
	a.adopt(work)
	if me := a.Me(); len(me) != 2 || me[0] != "messenger:1" || me[1] != "messenger:2" {
		t.Errorf("Me = %v", me)
	}
	if other := a.signedInAs(2, "fb"); other != "work" {
		t.Errorf("signedInAs(2) = %q, want work", other)
	}
	a.UseConfig(context.Background(), withAccounts("fb"))
	if !work.closed() {
		t.Error("a removed account's connection is still open")
	}
}

// writer is one sign-in's connection, as the generator drives it.
type writer struct {
	c      *conn
	signIn int
	n      int // how many times its cookies rotated
}

// rotate gives w's client a new xs cookie, tagged with its sign-in.
func (w *writer) rotate() {
	w.n++
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	w.c.client.GetCookies().Set(cookies.MetaCookieName("xs"), fmt.Sprintf("s%d-%d", w.signIn, w.n))
}

// Two writers keep one account's session: a login saves a new one, and every
// connection saves its cookies when Meta rotates them. Whatever the interleaving,
// the stored session is always the newest sign-in's: an older connection's rotation
// never overwrites it.
func TestTheStoredSessionIsAlwaysTheNewestSignIns(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(7, 99))
	for run := range 300 {
		secrets := newSecrets()
		a := New(nil, secrets, nil, nil)
		a.UseConfig(context.Background(), withAccounts("fb"))
		var writers []*writer
		login := func() {
			signIn := a.newSignIn("fb")
			s := aSession(1, fmt.Sprintf("s%d-0", signIn))
			a.keeping.Lock()
			if err := saveSession(secrets, "fb", s); err != nil {
				t.Fatal(err)
			}
			a.keeping.Unlock()
			c := a.newConn(Account{Name: "fb"}, s, &signIn)
			c.client = a.clientFor(s)
			a.adopt(c)
			writers = append(writers, &writer{c: c, signIn: signIn})
		}
		login()
		var steps []string
		for range 12 {
			w := writers[rng.IntN(len(writers))]
			switch rng.IntN(3) {
			case 0:
				login()
				steps = append(steps, "login")
			case 1:
				w.rotate()
				steps = append(steps, fmt.Sprintf("rotate s%d", w.signIn))
			default:
				a.keepCookies(w.c)
				steps = append(steps, fmt.Sprintf("keep s%d", w.signIn))
			}
			newest := writers[len(writers)-1].signIn
			stored, _, err := loadSession(secrets, "fb")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(stored.Cookies["xs"], fmt.Sprintf("s%d-", newest)) {
				t.Fatalf("run %d: after %v the stored session is %s, not sign-in %d's", run, steps, stored.Cookies["xs"], newest)
			}
		}
	}
}

// The same, with the writers racing: an old connection keeps saving while logins land.
func TestSessionWritersRacing(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	a := New(nil, secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("fb"))
	first := aSession(1, "s1-0")
	signIn := a.newSignIn("fb")
	old := &writer{c: a.newConn(Account{Name: "fb"}, first, &signIn), signIn: signIn}
	old.c.client = a.clientFor(first)
	a.adopt(old.c)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			old.rotate()
			a.keepCookies(old.c)
		}
	})
	newest := 0
	for range 20 {
		newest = a.newSignIn("fb")
		s := aSession(1, fmt.Sprintf("s%d-0", newest))
		a.keeping.Lock()
		_ = saveSession(secrets, "fb", s)
		a.keeping.Unlock()
		c := a.newConn(Account{Name: "fb"}, s, &newest)
		c.client = a.clientFor(s)
		a.adopt(c)
	}
	wg.Wait()
	stored, _, err := loadSession(secrets, "fb")
	if err != nil || !strings.HasPrefix(stored.Cookies["xs"], fmt.Sprintf("s%d-", newest)) {
		t.Errorf("the stored session is %v (%v), not sign-in %d's", stored.Cookies["xs"], err, newest)
	}
}

// What a failed page load means for the session.
func TestSessionOver(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err  error
		want string
	}{
		{httpclient.ErrTokenInvalidated, "ended the session; run `kith login messenger fb`"},
		{httpclient.ErrTokenInvalidatedRedirect, "ended the session"},
		{httpclient.RedirectedError{Type: httpclient.ErrCheckpointRequired, URL: "https://www.facebook.com/checkpoint/1"},
			"security check: finish it at https://www.facebook.com/checkpoint/1"},
		{httpclient.ErrChallengeRequired, "security check: finish it at facebook.com"},
		{httpclient.ErrConsentRequired, "accept its terms"},
		{httpclient.ErrAccountSuspended, "suspended"},
		{httpclient.ErrRateLimited, ""},
		{errors.New("connection refused"), ""},
	} {
		got := sessionOver(fmt.Errorf("load: %w", tc.err), "fb")
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("sessionOver(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// talkScript answers a login's questions from a script and records what it was asked.
type talkScript struct {
	answers []map[string]string
	asked   [][]api.LoginField
	notes   []string
	shown   []string
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

func (s *talkScript) Show(_ context.Context, code, note string) error {
	s.shown = append(s.shown, code+" | "+note)
	return nil
}

func (s *talkScript) Configure(_ context.Context, record api.LoginRecord) error {
	s.records = append(s.records, record)
	return nil
}

func (s *talkScript) Named(account string) { s.named = account }

// messagix's steps become kith's: a password is secret, a field's own check refuses an
// answer and asks again, an approval elsewhere is shown, and a step kith cannot take
// ends the login.
func TestLoginStepsAreAsked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fields := []bridgev2.LoginInputDataField{
		{Type: bridgev2.LoginInputFieldTypeUsername, ID: "username", Name: "Email or phone", Description: "Your login."},
		{Type: bridgev2.LoginInputFieldTypePassword, ID: "password", Name: "Password"},
		{Type: bridgev2.LoginInputFieldTypeSelect, ID: "mfatype", Name: "Method", Options: []string{"sms", "totp"},
			Validate: func(v string) (string, error) {
				if v != "sms" && v != "totp" {
					return "", errors.New("pick sms or totp")
				}
				return v, nil
			}},
	}
	talk := &talkScript{answers: []map[string]string{
		{"username": " me@example.com ", "password": " pass word ", "mfatype": "email"},
		{"username": "me@example.com", "password": " pass word ", "mfatype": "totp"},
	}}
	step := &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeUserInput, Instructions: "Sign in",
		UserInputParams: &bridgev2.LoginUserInputParams{Fields: fields}}
	got, err := answer(ctx, talk, step)
	if err != nil {
		t.Fatal(err)
	}
	if got["username"] != "me@example.com" || got["password"] != " pass word " || got["mfatype"] != "totp" {
		t.Errorf("answers = %q", got)
	}
	if len(talk.asked) != 2 || talk.notes[0] != "Sign in" || !strings.Contains(talk.notes[1], "pick sms or totp") {
		t.Errorf("asked %d times, notes %q", len(talk.asked), talk.notes)
	}
	if asked := talk.asked[0]; !asked[1].Secret || asked[0].Secret || asked[0].Label != "email or phone" ||
		!strings.Contains(asked[2].Help, "One of: sms, totp") {
		t.Errorf("fields asked = %+v", asked)
	}

	wait := &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeDisplayAndWait, Instructions: "Check your phone",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeNothing}}
	if got, err := answer(ctx, talk, wait); err != nil || got != nil {
		t.Errorf("waiting = %v, %v", got, err)
	}
	coded := &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeDisplayAndWait,
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeCode, Data: "123456"}}
	if _, err := answer(ctx, talk, coded); err != nil {
		t.Fatal(err)
	}
	if len(talk.shown) != 2 || !strings.HasPrefix(talk.shown[0], approveElsewhere+" | Check your phone") || !strings.HasPrefix(talk.shown[1], "123456") {
		t.Errorf("shown = %q", talk.shown)
	}
	if _, err := answer(ctx, talk, &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeCookies}); err == nil {
		t.Error("a cookie step was taken")
	}
	if _, err := answer(ctx, &talkScript{}, step); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled ask = %v", err)
	}
}

// A new account is named, written into the config and signed in under that name; a
// login for an account the config does not have is refused.
func TestLoginSetsUpANewAccount(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	a.UseConfig(context.Background(), withAccounts("meta"))
	talk := &talkScript{answers: []map[string]string{{"name": "personal"}}}
	name, err := a.setUp(context.Background(), talk)
	if err != nil {
		t.Fatal(err)
	}
	if suggested := talk.asked[0][0].Value; suggested == "" || suggested == "meta" {
		t.Errorf("suggested %q, a name the config already has", suggested)
	}
	if name != "personal" || talk.named != name || len(talk.records) != 1 ||
		talk.records[0].Table != "messenger.account" || talk.records[0].Values["name"] != name {
		t.Errorf("set up %q, named %q, records %+v", name, talk.named, talk.records)
	}
	if _, err := a.Login(context.Background(), "nobody", talk); !errors.Is(err, ErrNoAccount) {
		t.Errorf("Login(nobody) = %v", err)
	}
	if n := a.LoginNetwork(); n.Network != "messenger" || len(n.Accounts) != 1 || n.Accounts[0].Name != "meta" {
		t.Errorf("LoginNetwork = %+v", n)
	}
}

// The narrowest interleaving: an old connection has found it is still current and is
// writing its rotated cookies when a login lands. The login's session must still be
// the one kept, so the login waits for the write rather than slipping in under it.
func TestALoginDuringAnOldWriteStillWins(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	a := New(nil, secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("fb"))
	signIn := a.newSignIn("fb")
	first := aSession(1, "s1-0")
	old := &writer{c: a.newConn(Account{Name: "fb"}, first, &signIn), signIn: signIn}
	old.c.client = a.clientFor(first)
	a.adopt(old.c)
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
				newest = a.newSignIn("fb")
				a.keeping.Lock()
				defer a.keeping.Unlock()
				if err := saveSession(secrets, "fb", aSession(1, fmt.Sprintf("s%d-0", newest))); err != nil {
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
	stored, _, err := loadSession(secrets, "fb")
	if err != nil || stored.Cookies["xs"] != fmt.Sprintf("s%d-0", newest) {
		t.Errorf("the stored session is %v (%v), not the login's s%d-0", stored.Cookies["xs"], err, newest)
	}
}

// A panic inside the library fails the one call, not the daemon: messagix's login
// interpreter panicked on a screen Meta had changed (v0.2609.0).
func TestALibraryPanicIsAnError(t *testing.T) {
	t.Parallel()

	a := New(nil, newSecrets(), nil, nil)
	err := a.guard("signing in", func() error {
		var page *bridgev2.LoginStep
		_ = page.Instructions // a nil dereference, as in the interpreter
		return nil
	})
	if !errors.Is(err, errLibraryPanicked) || !strings.Contains(err.Error(), "while signing in") {
		t.Errorf("guard = %v", err)
	}
	if err := a.guard("nothing", func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Errorf("guard passes errors through: %v", err)
	}

	// An event the connection cannot take (a table before any client) is dropped.
	a.UseConfig(context.Background(), withAccounts("fb"))
	c := a.newConn(Account{Name: "fb"}, aSession(1, "a"), nil)
	a.handle(context.Background(), c, &table.LSTable{})
}
