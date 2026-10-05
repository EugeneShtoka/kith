package telegram

import (
	"context"
	"errors"
	"maps"
	"math/big"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"
	"github.com/gotd/td/tgtest/cluster"
	"github.com/gotd/td/transport"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// fakeTelegram is Telegram's login, served by gotd's in-process MTProto server: one
// account (code "12345"), with a two-step password when password is set. Its first
// rejectPasswords password checks fail.
type fakeTelegram struct {
	cluster *cluster.Cluster
	user    *tg.User

	mu              sync.Mutex
	password        bool
	rejectPasswords int
	sentCodes       int
	revoked         bool
}

// newFakeTelegram serves a fake Telegram until the test ends.
func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{
		cluster: cluster.NewCluster(cluster.Options{Protocol: transport.Intermediate}),
		user:    &tg.User{ID: 42, AccessHash: 1, FirstName: "Dana", LastName: "Lee", Self: true},
	}
	d := f.cluster.Dispatch(2, "dc2")
	answer := func(id uint32, decode bin.Decoder, reply func() (bin.Encoder, *tgerr.Error)) {
		d.HandleFunc(id, func(s *tgtest.Server, req *tgtest.Request) error {
			if err := decode.Decode(req.Buf); err != nil {
				return err
			}
			msg, rpcErr := reply()
			if rpcErr != nil {
				return s.SendErr(req, rpcErr)
			}
			return s.SendResult(req, msg)
		})
	}
	var sendCode tg.AuthSendCodeRequest
	answer(tg.AuthSendCodeRequestTypeID, &sendCode, func() (bin.Encoder, *tgerr.Error) {
		f.mu.Lock()
		f.sentCodes++
		f.mu.Unlock()
		return &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: 5}, PhoneCodeHash: "hash"}, nil
	})
	var signIn tg.AuthSignInRequest
	answer(tg.AuthSignInRequestTypeID, &signIn, func() (bin.Encoder, *tgerr.Error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case signIn.PhoneCode != "12345" || signIn.PhoneCodeHash != "hash":
			return nil, tgerr.New(400, "PHONE_CODE_INVALID")
		case f.password:
			return nil, tgerr.New(401, "SESSION_PASSWORD_NEEDED")
		}
		return &tg.AuthAuthorization{User: f.user}, nil
	})
	var getPassword tg.AccountGetPasswordRequest
	answer(tg.AccountGetPasswordRequestTypeID, &getPassword, func() (bin.Encoder, *tgerr.Error) {
		return srpParameters(), nil
	})
	var check tg.AuthCheckPasswordRequest
	answer(tg.AuthCheckPasswordRequestTypeID, &check, func() (bin.Encoder, *tgerr.Error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.rejectPasswords > 0 {
			f.rejectPasswords--
			return nil, tgerr.New(400, "PASSWORD_HASH_INVALID")
		}
		return &tg.AuthAuthorization{User: f.user}, nil
	})
	var users tg.UsersGetUsersRequest
	answer(tg.UsersGetUsersRequestTypeID, &users, func() (bin.Encoder, *tgerr.Error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.revoked {
			return nil, tgerr.New(401, "SESSION_REVOKED")
		}
		return &tg.UserClassVector{Elems: []tg.UserClass{f.user}}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	up := make(chan error, 1)
	go func() { up <- f.cluster.Up(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-up
	})
	select {
	case <-f.cluster.Ready():
	case err := <-up:
		t.Fatalf("the fake Telegram did not start: %v", err)
	}
	return f
}

// dial is a client of the fake Telegram.
func (f *fakeTelegram) dial(app App, storage session.Storage) *telegram.Client {
	return telegram.NewClient(app.ID, app.Hash, telegram.Options{
		PublicKeys: f.cluster.Keys(), Resolver: f.cluster.Resolver(), DCList: f.cluster.List(),
		SessionStorage: storage,
	})
}

func (f *fakeTelegram) set(change func(*fakeTelegram)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// connected waits for heard to say name is connected: a login over the fake server
// computes a password check and exchanges keys, which a loaded machine takes seconds for.
func connected(t *testing.T, heard *sessions, name string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if s, _ := heard.of(name); s == Connected {
			return
		}
	}
	t.Fatalf("%s did not connect", name)
}

// srpParameters is a two-step password's parameters, with Telegram's published prime.
func srpParameters() *tg.AccountPassword {
	p, _ := new(big.Int).SetString("C71CAEB9C6B1C9048E6C522F70F13F73980D40238E3E21C14934D037563D930F"+
		"48198A0AA7C14058229493D22530F4DBFA336F6E0AC925139543AED44CCE7C37"+
		"20FD51F69458705AC68CD4FE6B6B13ABDC9746512969328454F18FAF8C595F64"+
		"2477FE96BB2A941D5BCD1D4AC8CC49880708FA9B378E3C4F3A9060BEE67CF9A4"+
		"A4A695811051907E162753B56B0F6B410DBA74D8A84B2A14B3144E0EF1284754"+
		"FD17ED950D5965B4B9DD46582DB1178D169C6BC465B0D6FF9CA3928FEF5B9AE4"+
		"E418FC15E83EBEA0F87FA9FF5EED70050DED2849F47BF959D956850CE929851F"+
		"0D8115F635B105EE2E4E15D04B2454BF6F4FADF034B10403119CD8E3B92FCC5B", 16)
	algo := &tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{
		Salt1: []byte("salt-one"), Salt2: []byte("salt-two"), G: 3, P: p.Bytes(),
	}
	b := new(big.Int).Exp(big.NewInt(3), big.NewInt(1234567), p)
	pwd := &tg.AccountPassword{
		NewAlgo: algo, NewSecureAlgo: &tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000{},
		SRPB: b.FillBytes(make([]byte, 256)), SRPID: 7,
	}
	pwd.SetCurrentAlgo(algo)
	pwd.SetHasPassword(true)
	return pwd
}

var testApp = App{ID: 1, Hash: "0123456789abcdef0123456789abcdef"}

// logIn runs an adapter for home over secrets, and home's login over the fake,
// answering from answers.
func logIn(t *testing.T, f *fakeTelegram, secrets *memSecrets, answers map[string][]string) (*Adapter, *sessions, *apitest.Talk, api.LoginEnd, error) {
	t.Helper()
	a, heard := started(t, secrets, home)
	talk := &apitest.Talk{Answers: answers}
	end, err := a.login(t.Context(), home, testApp, talk, f.dial)
	return a, heard, talk, end, err
}

// A login asks for the code, then — the account having two-step verification — the
// password; a missing or wrong code or password is asked for again, saying why.
// Signed in, the credentials are kept and the account connects as itself.
func TestALoginTakesTheCodeThenThePassword(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.set(func(f *fakeTelegram) { f.password, f.rejectPasswords = true, 1 })
	secrets := &memSecrets{values: map[string]string{}}
	a, heard, talk, end, err := logIn(t, f, secrets, map[string][]string{
		"code":     {"", "11111", "12345"},
		"password": {"wrong", "right"},
	})
	if err != nil || end.Done != "logged in to Telegram as Dana Lee — its chats arrive over the next minutes" {
		t.Fatalf("login = (%+v, %v)", end, err)
	}
	asked, notes := talk.Asked, talk.Notes
	if want := []string{"code", "code", "code", "password", "password"}; !slices.Equal(asked, want) {
		t.Errorf("asked %v, want %v", asked, want)
	}
	wantNotes := []string{"Telegram sent a code to your Telegram app", "the code Telegram sent is needed",
		"that is not the code Telegram sent; try again", "the account has two-step verification",
		"that is not the password; try again"}
	if !slices.Equal(notes, wantNotes) {
		t.Errorf("notes %q, want %q", notes, wantNotes)
	}
	creds, ok, err := loadCredentials(secrets, home.Digits)
	if !ok || err != nil || creds.User != 42 || creds.App.ID != testApp.ID || len(creds.Session) == 0 {
		t.Fatalf("kept = (%+v, %v, %v), want the app, a session and user 42", creds, ok, err)
	}
	connected(t, heard, "home")
	if me := a.Me(); !slices.Equal(me, []string{"telegram:42"}) {
		t.Errorf("Me = %v, want telegram:42", me)
	}
}

// A login asks for an app until one will do: half an app, a malformed one, or none in
// a build without kith's own, is asked for again.
func TestALoginAsksForAnAppUntilOneWillDo(t *testing.T) {
	t.Parallel()
	answers := map[string][]string{
		"api_id":   {"5", "x", "7"},
		"api_hash": {"", "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"},
	}
	talk := &apitest.Talk{Answers: answers}
	app, err := askApp(t.Context(), talk)
	if err != nil || app != (App{ID: 7, Hash: "0123456789abcdef0123456789abcdef"}) {
		t.Fatalf("app = (%+v, %v), want the third", app, err)
	}
	if notes := talk.Notes; len(notes) != 2 {
		t.Errorf("notes %q, want two refusals", notes)
	}
	if _, ok := BuiltIn(); !ok {
		talk := &apitest.Talk{Answers: map[string][]string{"api_id": {""}, "api_hash": {""}}}
		if _, err := askApp(t.Context(), talk); !errors.Is(err, apitest.ErrNoAnswer) {
			t.Errorf("no app in a build without one = %v, want asked again", err)
		}
	}
}

// A new account is asked for by number and name, written into the config, and logged
// in under its name.
func TestANewAccountIsSetUpFirst(t *testing.T) {
	t.Parallel()
	a := New(nil, &memSecrets{values: map[string]string{}}, []Account{work}, nil)
	talk := &apitest.Talk{Answers: map[string][]string{
		"phone": {"+1 202 555 0100", "+44 7700 900000"}, "name": {""},
		"api_id": {"x"}, "api_hash": {""},
	}}
	talk.OnConfigure = func(r api.LoginRecord) error {
		a.useAccounts([]Account{work, {Name: r.Values["name"], Digits: domain.PhoneDigits(r.Values["phone"])}})
		return nil
	}
	_, err := a.Login(t.Context(), "", talk)
	if !errors.Is(err, apitest.ErrNoAnswer) {
		t.Fatalf("login = %v, want it to reach the app's second ask", err)
	}
	if len(talk.Written) != 1 || talk.Written[0].Table != "telegram.account" ||
		!maps.Equal(talk.Written[0].Values, map[string]string{"name": "gb", "phone": "+44 7700 900000"}) || talk.Account != "gb" {
		t.Errorf("written %+v, named %q; want gb at +44 7700 900000", talk.Written, talk.Account)
	}
	if notes := talk.Notes; len(notes) == 0 || !strings.Contains(notes[0], "work") {
		t.Errorf("notes %q, want work's number refused", notes)
	}
}

// A session Telegram ended leaves the account logged out, saying how to log in.
func TestAnEndedSessionLogsTheAccountOut(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	secrets := &memSecrets{values: map[string]string{}}
	a, heard, _, _, err := logIn(t, f, secrets, map[string][]string{"code": {"12345"}})
	if err != nil {
		t.Fatal(err)
	}
	connected(t, heard, "home")

	f.set(func(f *fakeTelegram) { f.revoked = true })
	creds, _, _ := loadCredentials(secrets, home.Digits)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	// The login's own connection ends first, so only this one speaks for home.
	a.mu.Lock()
	gen := a.signIns[home.Name]
	a.conns[home.Name].cancel()
	a.mu.Unlock()
	if done := a.connectOnce(ctx, home, gen, f.dial(creds.App, &keptSession{a: a, account: home, gen: gen, creds: creds})); !done {
		t.Fatal("an ended session is tried again")
	}
	if s, said := heard.of("home"); s != LoggedOut || said == "" {
		t.Errorf("home = %v %q, want logged out saying how to log in", s, said)
	}
}
