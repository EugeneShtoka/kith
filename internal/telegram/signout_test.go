package telegram

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Signing an account out ends its session on Telegram, stops its connection and
// deletes its credentials, its rooms staying readable or going as asked; neither a
// session its old connection saves afterwards nor a connection its old login makes
// brings it back; signed out, it is not connected, and another space is no account.
func TestSigningOutEndsTheSessionForGood(t *testing.T) {
	t.Parallel()
	for _, forget := range []bool{false, true} {
		f := newFakeTelegram(t)
		var mu sync.Mutex
		chats := []*tg.Chat{{ID: 11, Title: "Book club"}}
		f.dialogsOf(func() []*tg.Chat { mu.Lock(); defer mu.Unlock(); return slices.Clone(chats) }, nil)
		var loggedOut atomic.Int32
		f.cluster.Dispatch(2, "dc2").HandleFunc(tg.AuthLogOutRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
			loggedOut.Add(1)
			return sendResult(s, r, &tg.AuthLoggedOut{})
		})
		secrets := &memSecrets{values: map[string]string{}}
		a, _ := cachedAdapter(t, secrets)
		heard := &sessions{seen: map[string]domain.AccountPhase{}, said: map[string]string{}}
		a.OnStatus(heard.hear)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- a.Start(ctx) }()
		t.Cleanup(func() { cancel(); <-done; a.Stop() })
		if _, err := a.login(ctx, home, testApp, &apitest.Talk{Answers: map[string][]string{"code": {"12345"}}}, f.dial); err != nil {
			t.Fatal(err)
		}
		connected(t, heard, "home")
		creds, _, _ := loadCredentials(secrets, home.Digits)
		a.mu.Lock()
		oldGen := a.signIns[home.Name]
		a.mu.Unlock()

		if err := a.SignOut(ctx, accountSpaceID(999), forget); !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("signing out a space no account is = %v, want refused", err)
		}
		if err := a.SignOut(ctx, accountSpaceID(42), forget); err != nil {
			t.Fatal(err)
		}
		if n := loggedOut.Load(); n != 1 {
			t.Errorf("forget %v: Telegram was asked to end the session %d times, want once", forget, n)
		}
		if _, kept, _ := loadCredentials(secrets, home.Digits); kept {
			t.Errorf("forget %v: the credentials are still kept", forget)
		}
		if s, said := heard.of("home"); s != LoggedOut {
			t.Errorf("forget %v: home = %v %q, want logged out", forget, s, said)
		}

		// What was under way before: its old connection saving the session, its old
		// login connecting.
		stale := &keptSession{a: a, account: home, gen: oldGen, creds: creds}
		if err := stale.StoreSession(ctx, []byte("renewed")); err != nil {
			t.Fatal(err)
		}
		a.connectAs(home, creds, oldGen, f.dial)
		time.Sleep(50 * time.Millisecond)
		if _, kept, _ := loadCredentials(secrets, home.Digits); kept {
			t.Errorf("forget %v: an old connection's session brought the credentials back", forget)
		}
		a.mu.Lock()
		conn := a.conns[home.Name]
		a.mu.Unlock()
		if conn != nil {
			t.Errorf("forget %v: an old login connected again", forget)
		}

		if got := cachedNames(t, a); slices.Equal(got, []string{"Book club"}) == forget {
			t.Errorf("forget %v: cached rooms = %v", forget, got)
		}
		if err := a.SignOut(ctx, accountSpaceID(42), forget); !errors.Is(err, api.ErrNetworkOff) {
			t.Errorf("forget %v: signing out again = %v, want not connected", forget, err)
		}
	}
}
