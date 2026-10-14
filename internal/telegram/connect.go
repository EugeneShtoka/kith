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

// Reconnect backoff for a stopped client, doubling from firstRetry to lastRetry.
// gotd reconnects a running client itself.
const (
	firstRetry = 5 * time.Second
	lastRetry  = 5 * time.Minute
)

// conn is a logged-in account's connection.
type conn struct {
	account Account
	// gen is its login generation (Adapter.signIns).
	gen    int
	cancel context.CancelFunc
	// user and client are set once Telegram says who the account is; hashes come
	// from the last listing.
	user   int64
	client *telegram.Client
	live   *live
	hashes accessHashes
}

// connectAs replaces an older login's connection, unless a newer login began or the
// adapter stopped.
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

// connect reconnects with backoff until ctx ends or Telegram ends the session.
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

// connectOnce runs one connection; true when retrying is pointless (canceled or
// session ended).
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
		rooms, err := a.list(ctx, account, gen, self.ID, client)
		if err != nil && ctx.Err() == nil {
			a.log.Warn("list the chats failed", "account", account.Name, "err", err)
		}
		go a.backfill(ctx, account, self.ID, rooms) // ends with the connection
		return live.run(ctx, client, self.ID, func() {
			a.session(account, Connected, "")
			go a.sendOwed(ctx, self.ID) // reads made here while Telegram was away
		})
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

// adopt records the user of login gen's connection; false if a newer login replaced it.
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

// connected is a snapshot of the connections whose user is known.
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

// selfOf is an account's user ID, once known.
func (a *Adapter) selfOf(account Account) (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	self, ok := a.selves[account.Name]
	return self, ok && self != 0
}

// disown clears login gen's connection user on disconnect.
func (a *Adapter) disown(account Account, gen int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.conns[account.Name]; c != nil && c.gen == gen {
		c.user, c.client, c.live = 0, nil, nil
	}
}

func loginHint(account Account) string {
	return ":login telegram in kith, or `kith login telegram " + account.Name + "`"
}

// keptSession stores the session with the credentials; gotd rewrites it on DC
// migration or key renewal.
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

// StoreSession stores the session unless a newer login replaced this one.
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
