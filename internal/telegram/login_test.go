package telegram

import (
	"context"
	"errors"
	"math/big"
	"slices"
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

var testApp = api.TelegramApp{ID: 1, Hash: "0123456789abcdef0123456789abcdef"}

// loginOf runs an adapter for home over secrets and has the fake send home a code.
func loginOf(t *testing.T, f *fakeTelegram, secrets *memSecrets) (*Adapter, *sessions) {
	t.Helper()
	a, heard := started(t, secrets, home)
	app, err := appFor(testApp)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := a.beginLogin(t.Context(), home, app, f.dial)
	if err != nil || sent.Via != "your Telegram app" {
		t.Fatalf("code sent = (%+v, %v), want to the app", sent, err)
	}
	return a, heard
}

// A login takes the code, then — the account having two-step verification — asks
// for the password; a wrong code or password is tried again. Signed in, the
// credentials are kept and the account connects as itself.
func TestALoginTakesTheCodeThenThePassword(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.set(func(f *fakeTelegram) { f.password, f.rejectPasswords = true, 1 })
	secrets := &memSecrets{values: map[string]string{}}
	a, heard := loginOf(t, f, secrets)
	ctx := t.Context()

	steps := []struct {
		code, password string
		want           error
	}{
		{"", "", errNeedCode},
		{"11111", "", api.ErrBadCode},
		{"12345", "", api.ErrPasswordNeeded},
		{"12345", "", api.ErrPasswordNeeded}, // the code is taken; a password is still due
		{"", "wrong", api.ErrBadPassword},
	}
	for _, step := range steps {
		if _, err := a.SignInTelegram(ctx, "home", step.code, step.password); !errors.Is(err, step.want) {
			t.Fatalf("answer %+v = %v, want %v", step, err, step.want)
		}
	}
	if _, ok, _ := loadCredentials(secrets, home.Digits); ok {
		t.Fatal("credentials kept before the login finished")
	}
	in, err := a.SignInTelegram(ctx, "home", "", "right")
	if err != nil || in.Name != "Dana Lee" || in.ID != "telegram:42" {
		t.Fatalf("signed in = (%+v, %v), want Dana Lee, telegram:42", in, err)
	}
	creds, ok, err := loadCredentials(secrets, home.Digits)
	if !ok || err != nil || creds.User != 42 || creds.App.ID != testApp.ID || len(creds.Session) == 0 {
		t.Fatalf("kept = (%+v, %v, %v), want the app, a session and user 42", creds, ok, err)
	}
	connected(t, heard, "home")
	if me := a.Me(); !slices.Equal(me, []string{"telegram:42"}) {
		t.Errorf("Me = %v, want telegram:42", me)
	}
	if _, err := a.SignInTelegram(ctx, "home", "12345", ""); !errors.Is(err, errNoLogin) {
		t.Errorf("an answer after the login ended = %v, want no login under way", err)
	}
}

// An answer with no login under way, or for no configured account, is refused; and a
// login needs an app.
func TestALoginNeedsItsCodeSentAndAnApp(t *testing.T) {
	t.Parallel()
	a, _ := started(t, &memSecrets{values: map[string]string{}}, home)
	if _, err := a.SignInTelegram(t.Context(), "home", "12345", ""); !errors.Is(err, errNoLogin) {
		t.Errorf("an answer before the code = %v, want no login under way", err)
	}
	if _, err := a.SendTelegramCode(t.Context(), "nobody", testApp); !errors.Is(err, ErrNoAccount) {
		t.Errorf("a code for nobody = %v, want no such account", err)
	}
	if _, err := a.SendTelegramCode(t.Context(), "home", api.TelegramApp{ID: 5}); !errors.Is(err, errHalfApp) {
		t.Errorf("an api_id without its hash = %v, want refused", err)
	}
	if _, ok := BuiltIn(); !ok {
		if _, err := a.SendTelegramCode(t.Context(), "home", api.TelegramApp{}); !errors.Is(err, errNoBuiltIn) {
			t.Errorf("no app in a build without one = %v, want refused", err)
		}
	}
}

// A session Telegram ended leaves the account logged out, saying how to log in.
func TestAnEndedSessionLogsTheAccountOut(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	secrets := &memSecrets{values: map[string]string{}}
	a, heard := loginOf(t, f, secrets)
	if _, err := a.SignInTelegram(t.Context(), "home", "12345", ""); err != nil {
		t.Fatal(err)
	}
	connected(t, heard, "home")

	f.set(func(f *fakeTelegram) { f.revoked = true })
	creds, _, _ := loadCredentials(secrets, home.Digits)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	a.mu.Lock()
	gen := a.signIns[home.Name]
	a.mu.Unlock()
	if done := a.connectOnce(ctx, home, gen, f.dial(creds.App, &keptSession{a: a, account: home, gen: gen, creds: creds})); !done {
		t.Fatal("an ended session is tried again")
	}
	if s, said := heard.of("home"); s != LoggedOut || said == "" {
		t.Errorf("home = %v %q, want logged out saying how to log in", s, said)
	}
}
