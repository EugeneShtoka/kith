package gmessages

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memSecrets is an in-memory secret store; fail makes every read fail, and beforeStore
// runs before each write (outside the store's lock).
type memSecrets struct {
	mu          sync.Mutex
	values      map[string]string
	writes      int
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
	m.writes++
	return nil
}

func (m *memSecrets) DeleteSecret(ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, ref)
	return nil
}

// aSession is a finished pairing with phone whose token is token.
func aSession(phone, token string) *Session {
	auth := libgm.NewAuthData()
	auth.Browser = &gmproto.Device{UserID: 1, SourceID: "browser", Network: "Bugle"}
	auth.Mobile = &gmproto.Device{UserID: 2, SourceID: phone, Network: "Bugle"}
	auth.TachyonAuthToken = []byte(token)
	return &Session{PhoneID: phone, Auth: auth}
}

// tokenOf is the token a stored session holds.
func tokenOf(t *testing.T, secrets *memSecrets, account string) string {
	t.Helper()
	s, ok, err := loadSession(secrets, account)
	if err != nil || !ok {
		t.Fatalf("load = %v, %v", ok, err)
	}
	return string(s.Auth.TachyonAuthToken)
}

func withAccounts(names ...string) config.Config {
	var cfg config.Config
	for _, name := range names {
		cfg.GMessages.Accounts = append(cfg.GMessages.Accounts, config.GMessagesAccount{Name: name})
	}
	return cfg
}

// A pairing goes into the store whole, its keys included, and comes back able to sign
// requests; one that is not whole is unusable, which is the account's problem.
func TestSessionsAreKeptWhole(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	s := aSession("phone-1", "token-1")
	s.Auth.SetCookies(map[string]string{"SID": "sid"})
	if err := saveSession(secrets, "phone", s); err != nil {
		t.Fatal(err)
	}
	got, ok, err := loadSession(secrets, "phone")
	if err != nil || !ok {
		t.Fatalf("load = %v, %v", ok, err)
	}
	if got.PhoneID != "phone-1" || string(got.Auth.TachyonAuthToken) != "token-1" || got.Auth.Browser.GetSourceID() != "browser" ||
		got.Auth.Cookies["SID"] != "sid" || got.Auth.RequestCrypto == nil || got.Auth.RefreshKey == nil {
		t.Errorf("loaded %+v", got)
	}
	want, _ := s.Auth.RefreshKey.GetPublicKey()
	back, err := got.Auth.RefreshKey.GetPublicKey()
	if err != nil || !back.Equal(want) {
		t.Errorf("the refresh key did not survive: %v", err)
	}
	if _, ok, err := loadSession(secrets, "other"); ok || err != nil {
		t.Errorf("an account never paired = %v, %v", ok, err)
	}
	for name, bad := range map[string]*Session{
		"no phone":     {Auth: aSession("p", "t").Auth},
		"no pairing":   {PhoneID: "p"},
		"no browser":   {PhoneID: "p", Auth: libgm.NewAuthData()},
		"no token yet": func() *Session { s := aSession("p", "t"); s.Auth.TachyonAuthToken = nil; return s }(),
	} {
		if err := saveSession(secrets, "phone", bad); !errors.Is(err, errIncomplete) {
			t.Errorf("%s: save = %v", name, err)
		}
	}
	for name, blob := range map[string]string{"not json": "{", "empty": `{}`, "no auth": `{"phone_id":"p"}`} {
		secrets.values[sessionRef("phone")] = blob
		if _, _, err := loadSession(secrets, "phone"); !errors.Is(err, errUnusable) {
			t.Errorf("%s: load = %v, want unusable", name, err)
		}
	}

	a := New(secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("phone", "work"))
	_ = saveSession(secrets, "work", aSession("phone-2", "w"))
	if paired, err := a.SavedSessions(context.Background()); err != nil || len(paired) != 1 || paired[0] != "work" {
		t.Errorf("SavedSessions = %v, %v", paired, err)
	}
	secrets.fail = errors.New("locked")
	if _, err := a.SavedSessions(context.Background()); err == nil {
		t.Error("an unreadable store was not an error")
	}
}

// The Cookie header's value or its line is taken; only the cookies a pairing uses are
// kept, and missing ones are named.
func TestCookiesAreRead(t *testing.T) {
	t.Parallel()

	const all = `NID=1; SID=s1; HSID=h1; SSID=ss1; APISID=a1; SAPISID=sa1; OSID=o1; __Secure-1PSIDTS=ts; _ga=x`
	for name, paste := range map[string]string{
		"the value": all, "the line": "Cookie: " + all, "lower case": "cookie:" + all,
		"quoted": "'" + all + "'", "double quoted": `"` + all + `"`, "spaced": "  " + all + "\n",
	} {
		got, err := parseCookies(paste)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(got) != 7 || got["SID"] != "s1" || got["OSID"] != "o1" || got["__Secure-1PSIDTS"] != "ts" || got["NID"] != "" {
			t.Errorf("%s: kept %v", name, got)
		}
	}
	// "Copy as cURL", as Chrome writes it (several lines) and on one line, by -b or a
	// Cookie header.
	curls := map[string]string{
		"-b, several lines": "curl 'https://messages.google.com/web/config' \\\n  -H 'accept: */*' \\\n  -b '" + all + "' \\\n  -H 'dnt: 1'",
		"-b, one line":      "curl 'https://messages.google.com/web/config' -H 'accept: */*' -b '" + all + "'",
		"-H cookie":         "curl \"https://messages.google.com/web/\" -H \"Cookie: " + all + "\" -H \"dnt: 1\"",
		"$'' quoting":       "curl 'https://messages.google.com/web/' -b $'" + all + "'",
		// kith's one-line login field turns a pasted line break into a space.
		"joined by the field": "curl 'https://messages.google.com/web/config' \\  -H 'accept: */*' \\  -b '" + all + "' \\  -H 'dnt: 1'",
	}
	for name, paste := range curls {
		if got, err := parseCookies(paste); err != nil || got["SID"] != "s1" || got["OSID"] != "o1" {
			t.Errorf("cURL %s: %v, %v", name, got, err)
		}
	}
	if _, err := parseCookies("curl 'https://messages.google.com/web/' -H 'accept: */*'"); !errors.Is(err, errNoCookies) {
		t.Errorf("a cURL with no cookies = %v", err)
	}
	// A request to another Google host carries the account's cookies but not OSID.
	other := "curl 'https://instantmessaging-pa.clients6.google.com/$rpc/x' -b 'SID=s; HSID=h; SSID=ss; APISID=a; SAPISID=sa'"
	if _, err := parseCookies(other); !errors.Is(err, errNoOSID) {
		t.Errorf("a request to another host = %v, want OSID named", err)
	}
	if got, err := parseCookies("SID=s; HSID=h; SSID=ss; APISID=a; SAPISID=sa; OSID=o"); err != nil || len(got) != 6 {
		t.Errorf("without the optional one: %v, %v", got, err)
	}
	if _, err := parseCookies("SID=s; HSID=h"); !errors.Is(err, errMissing) || !strings.Contains(err.Error(), "SAPISID") {
		t.Errorf("missing ones = %v", err)
	}
	for _, empty := range []string{"", "  ", "Cookie:", "''"} {
		if _, err := parseCookies(empty); !errors.Is(err, errNoCookies) {
			t.Errorf("%q = %v", empty, err)
		}
	}
	if _, err := parseCookies("no cookies here"); !errors.Is(err, errNoCookies) {
		t.Errorf("words = %v", err)
	}
}

// What Google's answers mean for a pairing: the ones that end it, and the rest worth
// trying again.
func TestRefusal(t *testing.T) {
	t.Parallel()

	resp := func(code int) *http.Response { return &http.Response{StatusCode: code, Body: http.NoBody} }
	for name, tc := range map[string]struct {
		err  error
		over bool
	}{
		"unpaired":            {errUnpaired, true},
		"unpaired, wrapped":   {fmt.Errorf("ping: %w", errUnpaired), true},
		"invalid credentials": {events.ErrInvalidCredentials, true},
		"entity not found":    {fmt.Errorf("connect: %w", events.ErrRequestedEntityNotFound), true},
		"401":                 {events.HTTPError{Resp: resp(401)}, true},  //nolint:bodyclose // a made-up response, never read
		"500":                 {events.HTTPError{Resp: resp(500)}, false}, //nolint:bodyclose // a made-up response, never read
		"no response":         {events.HTTPError{}, false},
		"phone silent":        {libgm.ErrPhoneNotResponding, false},
		"no network":          {errors.New("dial tcp: connection refused"), false},
		"nothing":             {nil, false},
	} {
		if got := refusal(tc.err); got != tc.over {
			t.Errorf("%s: refusal = %v, want %v", name, got, tc.over)
		}
	}
}

func TestCheckConfig(t *testing.T) {
	t.Parallel()

	a := New(newSecrets(), nil, nil)
	if err := a.CheckConfig(context.Background(), withAccounts("phone", "work")); err != nil {
		t.Errorf("two named accounts: %v", err)
	}
	for name, cfg := range map[string]config.Config{"no name": withAccounts(""), "a name twice": withAccounts("p", "p")} {
		if err := a.CheckConfig(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "gmessages.account") {
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

func chanOpen[T any](ch <-chan T) bool {
	_, open := <-ch
	return open
}

// An account with no pairing says how to pair, one whose pairing will not read says
// so; the adapter stops cleanly.
func TestAnAccountWithNoPairingSaysHowToPair(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	secrets.values[sessionRef("broken")] = "{"
	a := New(secrets, nil, nil)
	var heard statuses
	a.OnStatus(heard.hear)
	a.UseConfig(context.Background(), withAccounts("phone", "broken"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- a.Start(ctx) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Start = %v", err)
	}
	a.Stop()
	a.Stop()
	heard.mu.Lock()
	got := heard.got
	heard.mu.Unlock()
	if len(got) != 2 || got[0].Network != domain.ProtocolGMessages || got[0].Phase != SignedOut ||
		!strings.Contains(got[0].Detail, "kith login gmessages phone") || !strings.Contains(got[1].Detail, "could not be read") {
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
	if a.Network() != domain.ProtocolGMessages || a.Me() != nil {
		t.Error("network or me")
	}
}

// A connection belongs to its account's newest pairing while the account is
// configured; a removed account's connection is let go.
func TestConnectionsFollowPairingsAndConfig(t *testing.T) {
	t.Parallel()

	a := New(newSecrets(), nil, nil)
	a.UseConfig(context.Background(), withAccounts("phone", "work"))
	old := a.newConn(Account{Name: "phone"}, aSession("p1", "a"), nil)
	if !a.adopt(old) {
		t.Fatal("the first connection was refused")
	}
	signIn := a.newSignIn("phone")
	if !a.adopt(a.newConn(Account{Name: "phone"}, aSession("p1", "b"), nil)) {
		t.Fatal("a connection on the newest pairing was refused")
	}
	if !old.closed() {
		t.Error("the replaced connection is still open")
	}
	if stale := a.newConn(Account{Name: "phone"}, aSession("p1", "c"), &[]int{signIn - 1}[0]); a.adopt(stale) {
		t.Error("a connection from an older pairing was adopted")
	}
	if a.adopt(a.newConn(Account{Name: "elsewhere"}, aSession("p3", "d"), nil)) {
		t.Error("a connection for an unconfigured account was adopted")
	}
	work := a.newConn(Account{Name: "work"}, aSession("p2", "e"), nil)
	a.adopt(work)
	if other := a.pairedAs("p2", "phone"); other != "work" {
		t.Errorf("pairedAs(p2) = %q", other)
	}
	if other := a.pairedAs("p2", "work"); other != "" {
		t.Errorf("an account is paired as itself elsewhere: %q", other)
	}
	a.UseConfig(context.Background(), withAccounts("phone"))
	if !work.closed() {
		t.Error("a removed account's connection is still open")
	}
	a.Stop()
	if a.adopt(a.newConn(Account{Name: "phone"}, aSession("p1", "f"), nil)) {
		t.Error("a connection was adopted after Stop")
	}
}

// writer is one pairing's connection, as the generator drives it.
type writer struct {
	c      *conn
	signIn int
	n      int
}

// refresh gives w's pairing a new token, tagged with its pairing, as libgm does.
func (w *writer) refresh() {
	w.n++
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	w.c.session.Auth.TachyonAuthToken = []byte(fmt.Sprintf("s%d-%d", w.signIn, w.n))
}

// pair is a pairing landing as finish does: counted, saved under keeping, adopted.
func pair(t *testing.T, a *Adapter, secrets *memSecrets) *writer {
	t.Helper()
	signIn := a.newSignIn("phone")
	s := aSession("p1", fmt.Sprintf("s%d-0", signIn))
	a.keeping.Lock()
	err := saveSession(secrets, "phone", s)
	a.keeping.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c := a.newConn(Account{Name: "phone"}, s, &signIn)
	a.adopt(c)
	return &writer{c: c, signIn: signIn}
}

// Two writers keep one account's pairing: a new pairing saves its own, and every
// connection saves its refreshed token. Whatever the interleaving, the stored pairing
// is always the newest one's, with its latest kept token.
func TestTheStoredPairingIsAlwaysTheNewest(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(11, 5))
	for run := range 200 {
		secrets := newSecrets()
		a := New(secrets, nil, nil)
		a.UseConfig(context.Background(), withAccounts("phone"))
		writers := []*writer{pair(t, a, secrets)}
		var steps []string
		for range 12 {
			w := writers[rng.IntN(len(writers))]
			switch rng.IntN(3) {
			case 0:
				writers = append(writers, pair(t, a, secrets))
				steps = append(steps, "pair")
			case 1:
				w.refresh()
				steps = append(steps, fmt.Sprintf("refresh s%d", w.signIn))
			default:
				a.keep(w.c)
				steps = append(steps, fmt.Sprintf("keep s%d", w.signIn))
			}
			newest := writers[len(writers)-1]
			if got := tokenOf(t, secrets, "phone"); !strings.HasPrefix(got, fmt.Sprintf("s%d-", newest.signIn)) {
				t.Fatalf("run %d: after %v the stored token is %s, not pairing %d's", run, steps, got, newest.signIn)
			}
		}
		newest := writers[len(writers)-1]
		newest.refresh()
		a.keep(newest.c)
		if got, want := tokenOf(t, secrets, "phone"), fmt.Sprintf("s%d-%d", newest.signIn, newest.n); got != want {
			t.Fatalf("run %d: stored %s, want the newest refresh %s", run, got, want)
		}
	}
}

// The same, racing: an old connection keeps saving while pairings land.
func TestPairingWritersRacing(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	a := New(secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("phone"))
	old := pair(t, a, secrets)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			old.refresh()
			a.keep(old.c)
		}
	})
	var newest *writer
	for range 20 {
		newest = pair(t, a, secrets)
	}
	wg.Wait()
	if got := tokenOf(t, secrets, "phone"); !strings.HasPrefix(got, fmt.Sprintf("s%d-", newest.signIn)) {
		t.Errorf("the stored token is %s, not pairing %d's", got, newest.signIn)
	}
}

// libgm's events show in the account: the long poll dropping and recovering, the
// phone falling silent and answering again, another session taking over; the pairing
// ending reaches the connection, a refreshed token is saved, and nothing else is.
func TestEventsShowInTheAccount(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	a := New(secrets, nil, nil)
	var heard statuses
	a.OnStatus(heard.hear)
	a.UseConfig(context.Background(), withAccounts("phone"))
	w := pair(t, a, secrets)
	ctx := context.Background()
	on := func(evt any) { a.handle(ctx, w.c, nil, evt) }
	expect := func(phase domain.AccountPhase, detail string) {
		t.Helper()
		if st := heard.last(); st.Phase != phase || !strings.Contains(st.Detail, detail) {
			t.Errorf("status %+v, want %v %q", st, phase, detail)
		}
	}
	on(&events.ListenTemporaryError{Error: errors.New("EOF")})
	expect(Connecting, reconnecting)
	on(&events.ListenRecovered{})
	expect(Connected, "")
	on(&events.PhoneNotResponding{})
	expect(Connected, "not answering")
	on(&events.PhoneRespondingAgain{})
	expect(Connected, "")
	on(&gmproto.UserAlertEvent{AlertType: gmproto.AlertType_BROWSER_INACTIVE})
	expect(Connecting, "another browser")
	on(&gmproto.UserAlertEvent{AlertType: gmproto.AlertType_BROWSER_ACTIVE})
	expect(Connected, "")
	before := len(heard.got)
	on(&gmproto.UserAlertEvent{AlertType: gmproto.AlertType_MOBILE_BATTERY_LOW})
	on(&gmproto.UserAlertEvent{AlertType: gmproto.AlertType_MOBILE_WIFI_CONNECTION})
	if len(heard.got) != before {
		t.Error("a battery or network alert changed the account")
	}

	writes := secrets.writes
	on(&events.AuthTokenRefreshed{})
	if secrets.writes != writes {
		t.Error("an unchanged pairing was written again")
	}
	w.refresh()
	on(&events.AuthTokenRefreshed{})
	if got := tokenOf(t, secrets, "phone"); got != "s1-1" {
		t.Errorf("the refreshed token was not kept: %s", got)
	}

	ended := func(evt any, over bool) {
		t.Helper()
		on(evt)
		select {
		case err := <-w.c.gaveUp:
			if refusal(err) != over {
				t.Errorf("%T ended it with %v (over: %v)", evt, err, refusal(err))
			}
		default:
			t.Errorf("%T did not reach the connection", evt)
		}
	}
	ended(&gmproto.RevokePairData{}, true)
	ended(&events.GaiaLoggedOut{}, true)
	ended(&events.PingFailed{Error: events.ErrRequestedEntityNotFound, ErrorCount: 1}, true)
	ended(&events.ListenFatalError{Error: events.ErrInvalidCredentials}, true)
	ended(&events.ListenFatalError{Error: errors.New("http 500 while polling")}, false)
	on(&events.PingFailed{Error: errors.New("timeout"), ErrorCount: 3})
	select {
	case err := <-w.c.gaveUp:
		t.Errorf("a failed ping ended the connection: %v", err)
	default:
	}

	// Taking the session back is a request: it goes on its own, and a library failure
	// there is caught.
	on(&events.HackySetActiveMayFail{})
	on(&gmproto.UserAlertEvent{AlertType: gmproto.AlertType_BROWSER_INACTIVE_FROM_TIMEOUT})
	a.takeOver(ctx, w.c, nil)

	w.c.close()
	before = len(heard.got)
	on(&events.ListenRecovered{})
	if len(heard.got) != before {
		t.Error("a closed connection still reports")
	}
}

// After a failure the connection tries again, later each time up to a limit, or, when
// the pairing is over, shows how to pair again.
func TestRetry(t *testing.T) {
	t.Parallel()

	a := New(newSecrets(), nil, nil)
	var heard statuses
	a.OnStatus(heard.hear)
	c := a.newConn(Account{Name: "phone"}, aSession("p1", "a"), nil)
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
	if a.retry(canceled, c, errors.New("x"), &wait) {
		t.Error("retried after the context ended")
	}
	if a.retry(canceled, c, errors.New("x"), &wait) {
		t.Error("retried with the context already ended")
	}
	wait = time.Millisecond
	if a.retry(ctx, c, errUnpaired, &wait) {
		t.Error("retried an ended pairing")
	}
	if st := heard.last(); st.Phase != SignedOut || !strings.Contains(st.Detail, "kith login gmessages phone") {
		t.Errorf("an ended pairing: %+v", st)
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

// A new phone is named and written into the config; cookies without what a pairing
// needs are asked for again, naming what is missing; a pairing for an account the
// config does not have is refused.
func TestPairingAsks(t *testing.T) {
	t.Parallel()

	a := New(newSecrets(), nil, nil)
	a.UseConfig(context.Background(), withAccounts("phone"))
	talk := &talkScript{answers: []map[string]string{{"name": "work"}}}
	if _, err := a.Login(context.Background(), "", talk); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("Login = %v, want no such account yet", err)
	}
	if suggested := talk.asked[0][0].Value; suggested == "" || suggested == "phone" {
		t.Errorf("suggested %q, a name the config already has", suggested)
	}
	if talk.named != "work" || len(talk.records) != 1 || talk.records[0].Table != "gmessages.account" {
		t.Errorf("named %q, records %+v", talk.named, talk.records)
	}

	talk = &talkScript{answers: []map[string]string{{"cookies": "SID=s; HSID=h"}}}
	if _, err := a.Login(context.Background(), "phone", talk); !errors.Is(err, context.Canceled) {
		t.Fatalf("Login = %v", err)
	}
	if len(talk.asked) != 2 || talk.asked[0][0].Key != "cookies" || !talk.asked[0][0].Secret ||
		talk.notes[0] != "" || !strings.Contains(talk.notes[1], "SAPISID") {
		t.Errorf("asked %v, notes %q", talk.asked, talk.notes)
	}
	if _, err := a.Login(context.Background(), "nobody", talk); !errors.Is(err, ErrNoAccount) {
		t.Errorf("Login(nobody) = %v", err)
	}
	if n := a.LoginNetwork(); n.Network != "gmessages" || len(n.Accounts) != 1 || n.Accounts[0].Name != "phone" {
		t.Errorf("LoginNetwork = %+v", n)
	}
}

// Why a Google-account pairing failed, as the login says it; only refused cookies are
// worth asking for again.
func TestGoogleAccountPairingFailures(t *testing.T) {
	t.Parallel()

	for err, want := range map[error]error{
		events.ErrInvalidCredentials:                       errCookiesRefused,
		libgm.ErrNoCookies:                                 errCookiesRefused,
		libgm.ErrNoDevicesFound:                            errNoPhone,
		libgm.ErrPairingInitTimeout:                        errPhoneSilent,
		libgm.ErrIncorrectEmoji:                            errWrongEmoji,
		libgm.ErrPairingCancelled:                          errDeclined,
		libgm.ErrPairingTimeout:                            errTooSlow,
		errors.New("something new"):                        nil,
		fmt.Errorf("wrapped: %w", libgm.ErrNoDevicesFound): errNoPhone,
	} {
		got := gaiaFailed(err)
		if want != nil && !errors.Is(got, want) {
			t.Errorf("%v → %v, want %v", err, got, want)
		}
		if want == nil && !strings.Contains(got.Error(), "something new") {
			t.Errorf("%v → %v", err, got)
		}
		if askAgain(got) != errors.Is(want, errCookiesRefused) {
			t.Errorf("%v: ask again = %v", err, askAgain(got))
		}
	}
}

// A panic inside the library fails the one call, not the daemon.
func TestALibraryPanicIsAnError(t *testing.T) {
	t.Parallel()

	a := New(newSecrets(), nil, nil)
	err := a.guard("connecting", func() error {
		var c *libgm.Client
		return c.SetActiveSession(context.Background())
	})
	if !errors.Is(err, errLibraryPanicked) || !strings.Contains(err.Error(), "while connecting") {
		t.Errorf("guard = %v", err)
	}
	if err := a.guard("nothing", func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Errorf("guard passes errors through: %v", err)
	}
	a.OnStatus(func(domain.AccountStatus) { panic("a listener that fails") })
	c := a.newConn(Account{Name: "phone"}, aSession("p1", "a"), nil)
	a.handle(context.Background(), c, nil, &events.ListenRecovered{}) // dropped, not raised
}

// The narrowest interleaving: an old connection has found it is still current and is
// writing its refreshed token when a pairing lands. The pairing's session must still
// be the one kept, so the pairing waits for the write rather than slipping in under it.
func TestAPairingDuringAnOldWriteStillWins(t *testing.T) {
	t.Parallel()

	secrets := newSecrets()
	a := New(secrets, nil, nil)
	a.UseConfig(context.Background(), withAccounts("phone"))
	old := pair(t, a, secrets)
	newest := 0
	landed := make(chan struct{})
	var once sync.Once
	secrets.beforeStore = func(_, value string) {
		if !strings.Contains(value, `"czEtMQ=="`) { // the old connection's "s1-1", as JSON bytes
			return
		}
		once.Do(func() {
			go func() {
				defer close(landed)
				newest = a.newSignIn("phone")
				a.keeping.Lock()
				defer a.keeping.Unlock()
				if err := saveSession(secrets, "phone", aSession("p1", fmt.Sprintf("s%d-0", newest))); err != nil {
					t.Error(err)
				}
			}()
			select {
			case <-landed:
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
	old.refresh()
	a.keep(old.c)
	<-landed
	if got := tokenOf(t, secrets, "phone"); got != fmt.Sprintf("s%d-0", newest) {
		t.Errorf("the stored token is %s, not the pairing's s%d-0", got, newest)
	}
}
