package instagram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
)

// keepEvery bounds how often a connection writes its session (rotated cookies, the seq
// ID) to the keyring; it is written once more as the connection ends. A seq ID lost to
// a crash only replays a few events, which the cache takes once.
const keepEvery = 30 * time.Second

// retryEvery is the wait before trying a connection that failed on its own again
// (not a session Instagram ended).
const retryEvery = time.Minute

// conn is one signed-in account's connection.
type conn struct {
	a       *Adapter
	account Account
	self    string
	stop    context.CancelFunc
	done    chan struct{}

	mu      sync.Mutex
	session *Session
	client  *instameow.Client
	kept    time.Time
}

// connect starts account's connection under ctx, unless it has one.
func (a *Adapter) connect(ctx context.Context, account Account, s *Session) {
	ctx, stop := context.WithCancel(ctx)
	c := &conn{a: a, account: account, self: s.Self, stop: stop, done: make(chan struct{}), session: s}
	a.mu.Lock()
	if a.stopped || a.conns[account.Name] != nil {
		a.mu.Unlock()
		stop()
		return
	}
	a.conns[account.Name] = c
	a.mu.Unlock()
	a.status(account, Connecting, "")
	go c.run(ctx)
}

// run connects until ctx ends or Instagram ends the session, trying again after a
// failure of its own.
func (c *conn) run(ctx context.Context) {
	defer close(c.done)
	for {
		err := c.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if why, over := sessionOver(err); over {
			c.a.status(c.account, SignedOut, why)
			c.a.forget(c)
			return
		}
		c.a.log.Warn("connecting to Instagram failed; trying again", "account", c.account.Name, "err", err, "in", retryEvery)
		c.a.status(c.account, Connecting, "connecting failed; trying again: "+err.Error())
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryEvery):
		}
	}
}

// once loads the inbox (or, resuming, just the index) and connects, returning when
// the connection could not be made; once made, instameow keeps it until ctx ends.
func (c *conn) once(ctx context.Context) error {
	c.mu.Lock()
	s := c.session
	c.mu.Unlock()
	client := instameow.NewClient(instameow.ClientParams{
		Cookies:      s.Cookies,
		Log:          c.a.zerolog(c.account.Name),
		Settings:     exhttp.SensibleClientSettings,
		SeqID:        s.SeqID,
		SeqIDTS:      s.SeqTS,
		EventHandler: c.handle,
	})
	var err error
	if client.HasSeqID() {
		_, err = client.ReloadIndex(ctx)
	} else {
		_, _, err = client.LoadIndex(ctx)
	}
	if err != nil {
		return fmt.Errorf("load the inbox: %w", err)
	}
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
	client.Connect(ctx)
	<-ctx.Done()
	return ctx.Err() //nolint:wrapcheck // our own end
}

// handle takes instameow's events: the connection's state and the seq ID now; the
// inbox's changes come with receiving.
func (c *conn) handle(_ context.Context, evt slidetypes.ClientEvent) error {
	switch e := evt.(type) {
	case *slidetypes.Connected:
		c.a.status(c.account, Connected, "")
	case *slidetypes.Disconnected:
		if dgw.IsUnauthorized(e.Error) {
			c.a.status(c.account, SignedOut, "Instagram ended the session; run `kith login instagram "+c.account.Name+"`")
			go c.a.forget(c)
			return nil
		}
		c.a.status(c.account, Connecting, "disconnected; reconnecting")
	case *slidetypes.AuthError:
		if why, over := sessionOver(e.Error); over {
			c.a.status(c.account, SignedOut, why)
			go c.a.forget(c)
		}
	case *slidetypes.SeqIDUpdate:
		c.keep(e.SeqID, e.Timestamp, false)
	}
	return nil
}

// keep writes the session (the client's cookies as they rotated, the seq ID) to the
// keyring, at most every keepEvery unless now.
func (c *conn) keep(seq int64, ts time.Time, now bool) {
	c.mu.Lock()
	if seq != 0 {
		c.session.SeqID, c.session.SeqTS = seq, ts
	}
	if c.client != nil {
		c.session.Cookies = c.client.GetCookies()
	}
	due := now || time.Since(c.kept) >= keepEvery
	if due {
		c.kept = time.Now()
	}
	s := *c.session
	c.mu.Unlock()
	if !due {
		return
	}
	c.a.keeping.Lock()
	defer c.a.keeping.Unlock()
	if err := saveSession(c.a.secrets, c.account.Name, &s); err != nil {
		c.a.log.Warn("keeping the Instagram session failed", "account", c.account.Name, "err", err)
	}
}

// close ends the connection, keeping the session as it stands.
func (c *conn) close() {
	c.stop()
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client != nil {
		client.Disconnect()
	}
	<-c.done
	c.keep(0, time.Time{}, true)
}

// forget lets c go: Instagram ended its session. The kept session stays until a new
// sign-in replaces it, so the state can say why.
func (a *Adapter) forget(c *conn) {
	a.mu.Lock()
	if a.conns[c.account.Name] == c {
		delete(a.conns, c.account.Name)
	}
	a.mu.Unlock()
	c.stop()
}

// sessionOver says whether err means Instagram ended the session (or wants a step only
// its website can take), and what to tell the person.
func sessionOver(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, httpclient.ErrTokenInvalidated):
		return "Instagram ended the session; sign in again (`kith login instagram`)", true
	case errors.Is(err, httpclient.ErrConsentRequired):
		return "Instagram wants a cookie choice for this session; sign in again with your browser's cookies", true
	case errors.Is(err, httpclient.ErrCheckpointRequired), errors.Is(err, httpclient.ErrChallengeRequired):
		return "Instagram wants a security check: open instagram.com in a browser, finish it, then sign in again", true
	case errors.Is(err, httpclient.ErrAccountSuspended):
		return "Instagram suspended the account", true
	}
	return "", false
}

// selfOf is the account's own ID as kith keeps it.
func selfOf(client *instameow.Client) string {
	return strconv.FormatInt(client.GetOwnFBID(), 10)
}
