package instagram

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// connRig is the adapter with one connection registered, offline.
type connRig struct {
	a       *Adapter
	c       *conn
	secrets *memSecrets

	mu   sync.Mutex
	said []domain.AccountStatus
}

func newConnRig(t *testing.T) *connRig {
	t.Helper()
	r := &connRig{secrets: newSecrets()}
	r.a = New(nil, r.secrets, nil, nil)
	r.a.OnStatus(func(s domain.AccountStatus) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.said = append(r.said, s)
	})
	cookies, err := parseCookies(header)
	if err != nil {
		t.Fatal(err)
	}
	_, stop := context.WithCancel(context.Background())
	r.c = &conn{a: r.a, account: Account{Name: "me"}, self: "1000", stop: stop, done: make(chan struct{}),
		session: &Session{Cookies: cookies, Self: "1000"}}
	close(r.c.done)
	r.a.conns["me"] = r.c
	return r
}

func (r *connRig) last() domain.AccountStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.said) == 0 {
		return domain.AccountStatus{}
	}
	return r.said[len(r.said)-1]
}

// Instagram's connection events become the account's state: connected, reconnecting
// after a drop, signed out (and the connection let go) when Instagram ends the session.
func TestConnectionEventsAreTheAccountsState(t *testing.T) {
	t.Parallel()
	r := newConnRig(t)
	ctx := context.Background()
	steps := []struct {
		evt   slidetypes.ClientEvent
		phase domain.AccountPhase
	}{
		{&slidetypes.Connected{}, Connected},
		{&slidetypes.Disconnected{Error: fmt.Errorf("read: %w", context.DeadlineExceeded)}, Connecting},
		{&slidetypes.Connected{}, Connected},
		{&slidetypes.AuthError{Error: httpclient.ErrRateLimited}, Connected}, // not the session's end
		{&slidetypes.Disconnected{Error: dgw.ErrUnauthorized}, SignedOut},
	}
	for i, s := range steps {
		if err := r.c.handle(ctx, s.evt); err != nil {
			t.Fatal(err)
		}
		if got := r.last(); got.Phase != s.phase {
			t.Errorf("step %d: %+v, want %v", i, got, s.phase)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.a.mu.Lock()
		_, still := r.a.conns["me"]
		r.a.mu.Unlock()
		if !still {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the connection Instagram ended is still kept")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if me := r.a.Me(); len(me) != 0 {
		t.Errorf("Me = %v after the session ended", me)
	}
}

// An auth error that is the session's end signs the account out with the reason.
func TestAnAuthErrorThatEndsTheSession(t *testing.T) {
	t.Parallel()
	r := newConnRig(t)
	if err := r.c.handle(context.Background(), &slidetypes.AuthError{Error: httpclient.ErrCheckpointRequired}); err != nil {
		t.Fatal(err)
	}
	if got := r.last(); got.Phase != SignedOut {
		t.Errorf("state = %+v", got)
	}
}

// The seq ID is kept with the session, written at once the first time and then at most
// every keepEvery (and once more as the connection ends); the account is in Me while
// connected.
func TestTheSeqIDIsKept(t *testing.T) {
	t.Parallel()
	r := newConnRig(t)
	if me := r.a.Me(); len(me) != 1 || me[0] != person("1000") {
		t.Errorf("Me = %v", me)
	}
	ctx := context.Background()
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	if err := r.c.handle(ctx, &slidetypes.SeqIDUpdate{SeqID: 236, Timestamp: at}); err != nil {
		t.Fatal(err)
	}
	s, ok, err := loadSession(r.secrets, "me")
	if err != nil || !ok || s.SeqID != 236 || !s.SeqTS.Equal(at) {
		t.Fatalf("kept %+v (%v, %v)", s, ok, err)
	}
	if err := r.c.handle(ctx, &slidetypes.SeqIDUpdate{SeqID: 237, Timestamp: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if s, _, _ = loadSession(r.secrets, "me"); s.SeqID != 236 {
		t.Errorf("written again within keepEvery: %d", s.SeqID)
	}
	r.c.close()
	if s, _, _ = loadSession(r.secrets, "me"); s.SeqID != 237 {
		t.Errorf("the connection's end did not keep the last seq ID: %d", s.SeqID)
	}
}

// A connection Instagram ends is let go, but the session stays for the state to say
// why; a newer connection for the account is not let go by an older one's end.
func TestForgettingLetsOnlyThatConnectionGo(t *testing.T) {
	t.Parallel()
	r := newConnRig(t)
	old := r.c
	newer := &conn{a: r.a, account: Account{Name: "me"}, self: "1000", stop: func() {}, done: make(chan struct{})}
	r.a.mu.Lock()
	r.a.conns["me"] = newer
	r.a.mu.Unlock()
	r.a.forget(old)
	r.a.mu.Lock()
	kept := r.a.conns["me"]
	r.a.mu.Unlock()
	if kept != newer {
		t.Error("an older connection's end let the newer go")
	}
	r.a.forget(newer)
	r.a.mu.Lock()
	_, still := r.a.conns["me"]
	r.a.mu.Unlock()
	if still {
		t.Error("the connection was not let go")
	}
}
