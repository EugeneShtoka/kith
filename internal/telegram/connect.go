package telegram

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
)

// Retrying an account Telegram cannot be reached for: from firstRetry, doubling to
// lastRetry. (gotd reconnects a running client by itself; this is for one that
// stopped.)
const (
	firstRetry = 5 * time.Second
	lastRetry  = 5 * time.Minute
)

// conn is a logged-in account's connection.
type conn struct {
	account Account
	// gen is the login it runs on (see Adapter.signIns).
	gen    int
	cancel context.CancelFunc
	// user is the account's own user ID, and client its connection, once Telegram
	// said who it is; hashes what its last listing revealed (rooms.go).
	user   int64
	client *telegram.Client
	live   *live
	hashes accessHashes
}

// connectAs connects account on creds, from its login gen, replacing the connection
// an older login made — unless a newer login began meanwhile, or the adapter stopped.
func (a *Adapter) connectAs(account Account, creds Credentials, gen int, dial dialer) {
	ctx, cancel := context.WithCancel(a.runContext())
	a.mu.Lock()
	if a.stopped || a.signIns[account.Name] != gen {
		a.mu.Unlock()
		cancel()
		return
	}
	if old := a.conns[account.Name]; old != nil {
		old.cancel()
	}
	a.conns[account.Name] = &conn{account: account, gen: gen, cancel: cancel}
	a.selves[account.Name] = creds.User
	a.mu.Unlock()
	go a.connect(ctx, account, creds, gen, dial)
}

// connect keeps an account connected, trying again while Telegram cannot be reached,
// until ctx ends or Telegram ends the session.
func (a *Adapter) connect(ctx context.Context, account Account, creds Credentials, gen int, dial dialer) {
	for wait := firstRetry; ; wait = min(2*wait, lastRetry) {
		storage := &keptSession{a: a, account: account, gen: gen, creds: creds}
		live := a.liveFor(account)
		if a.connectOnce(ctx, account, gen, dial(creds.App, storage, live.manager), live) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// connectOnce runs one connection until it ends; true when trying again would not
// help (it was ended, or Telegram ended the session).
func (a *Adapter) connectOnce(ctx context.Context, account Account, gen int, client *telegram.Client, live *live) bool {
	a.session(account, Connecting, "")
	err := client.Run(ctx, func(ctx context.Context) error {
		self, err := client.Self(ctx)
		if err != nil {
			return err //nolint:wrapcheck // read by kind below
		}
		if !a.adopt(account, gen, self.ID, client, live) {
			return nil
		}
		a.log.Info("connected", "account", account.Name, "user", self.ID)
		if _, err := a.list(ctx, account, gen, self.ID, client); err != nil && ctx.Err() == nil {
			a.log.Warn("list the chats failed", "account", account.Name, "err", err)
		}
		return live.run(ctx, client, self.ID, func() { a.session(account, Connected, "") })
	})
	a.disown(account, gen)
	switch {
	case ctx.Err() != nil, err == nil:
		return true // ended here, or overtaken by a newer login
	case auth.IsUnauthorized(err):
		a.log.Info("Telegram ended the session", "account", account.Name, "err", err)
		a.session(account, LoggedOut, "Telegram ended the session; "+loginHint(account))
		return true
	}
	a.log.Warn("reach Telegram failed", "account", account.Name, "err", err)
	a.session(account, Connecting, "Telegram cannot be reached: "+err.Error())
	return false
}

// adopt records whom the account's connection from login gen is, unless a newer login
// replaced it. It reports whether it did.
func (a *Adapter) adopt(account Account, gen int, user int64, client *telegram.Client, l *live) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.conns[account.Name]
	if c == nil || c.gen != gen {
		return false
	}
	c.user, c.client, c.live = user, client, l
	a.selves[account.Name] = user
	return true
}

// connected is the connections Telegram has said whom they are, as they stand.
func (a *Adapter) connected() []conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []conn
	for _, c := range a.conns {
		if c.user != 0 {
			out = append(out, *c)
		}
	}
	return out
}

// selfOf is an account's own user ID, once a login or a kept session said it.
func (a *Adapter) selfOf(account Account) (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	self, ok := a.selves[account.Name]
	return self, ok && self != 0
}

// disown forgets whom the connection from login gen is: it is not connected now.
func (a *Adapter) disown(account Account, gen int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.conns[account.Name]; c != nil && c.gen == gen {
		c.user, c.client, c.live = 0, nil, nil
	}
}

// loginHint is how to log an account in.
func loginHint(account Account) string {
	return ":login telegram in kith, or `kith login telegram " + account.Name + "`"
}

// keptSession is a connection's session, kept with the account's credentials: gotd
// writes it as it changes (a data center moved to, a key renewed).
type keptSession struct {
	a       *Adapter
	account Account
	gen     int

	mu    sync.Mutex
	creds Credentials
}

var _ session.Storage = (*keptSession)(nil)

func (s *keptSession) LoadSession(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.creds.Session) == 0 {
		return nil, session.ErrNotFound
	}
	return slices.Clone(s.creds.Session), nil
}

// StoreSession keeps the session — unless a newer login replaced this one, whose
// session it would overwrite.
func (s *keptSession) StoreSession(_ context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds.Session = slices.Clone(data)
	err := s.a.keepLogin(s.account, s.gen, s.creds)
	if errors.Is(err, errSuperseded) {
		return nil
	}
	return err
}
