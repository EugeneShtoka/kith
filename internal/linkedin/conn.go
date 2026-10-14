package linkedin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// A connection checks the session by reading the account's own profile, then holds
// linkedingo's realtime stream, which reconnects by itself until it gives up. LinkedIn
// rotates cookies on its responses; they are saved when they change.

// The wait between failed attempts doubles from retryFirst up to retryMax.
const (
	retryFirst = 2 * time.Second
	retryMax   = 5 * time.Minute
)

// profileTimeout bounds reading the account's profile: linkedingo retries a failed
// request five times, two seconds doubling, and its HTTP client has no limit of its own.
const profileTimeout = 2 * time.Minute

// conn is one signed-in account's connection.
type conn struct {
	account Account
	// profile is the LinkedIn account; it never changes, unlike the session's cookies.
	profile string
	// signIn is the account's sign-in the session is from (Adapter.signIns).
	signIn int

	// done closes when the connection is let go (re-sign-in, removal, stop).
	done      chan struct{}
	closeOnce sync.Once
	// gaveUp hears linkedingo's realtime loop ending, which the connection then restarts.
	gaveUp chan error

	mu sync.Mutex
	// session is the session as last saved; client is linkedingo's, once connected.
	session Session
	saved   string
	client  *linkedingo.Client

	// applying serializes taking conversations in: the inbox, and rooms, which is the
	// account's rooms as last written (the cached ones, then every conversation heard
	// of), so a write never sweeps one only an older listing had.
	applying sync.Mutex
	inbox    *inbox
	rooms    map[domain.RoomID]domain.Room
	// synced is the session ID of the realtime connection last synced after; syncing
	// runs one at a time.
	synced  string
	syncing sync.Mutex
}

func (a *Adapter) newConn(account Account, s Session, at *int) *conn {
	a.mu.Lock()
	signIn := a.signIns[account.Name]
	a.mu.Unlock()
	if at != nil {
		signIn = *at
	}
	saved, _ := s.encode() // a session that will not encode is saved again on its first change
	return &conn{
		account: account, profile: s.ProfileID, signIn: signIn, session: s, saved: saved,
		done: make(chan struct{}), gaveUp: make(chan error, 1),
		inbox: newInbox(s.ProfileID), rooms: map[domain.RoomID]domain.Room{},
	}
}

// close lets the connection go; connect then stops the realtime stream, which only it
// starts and stops (linkedingo's start and stop are not safe against each other).
func (c *conn) close() {
	c.closeOnce.Do(func() { close(c.done) })
}

func (c *conn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// newSignIn starts a new sign-in of account: connections from older ones are stale.
func (a *Adapter) newSignIn(account string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signIns[account]++
	return a.signIns[account]
}

// adopt makes c its account's connection, letting an older one go, unless the account
// left the config, a newer sign-in began, or the adapter stopped.
func (a *Adapter) adopt(c *conn) bool {
	a.mu.Lock()
	configured := false
	for _, account := range a.accounts {
		configured = configured || account.Name == c.account.Name
	}
	if a.stopped || !configured || c.signIn != a.signIns[c.account.Name] {
		a.mu.Unlock()
		return false
	}
	old := a.conns[c.account.Name]
	a.conns[c.account.Name] = c
	a.mu.Unlock()
	if old != nil && old != c {
		old.close()
	}
	return true
}

// current reports whether c is still its account's connection.
func (a *Adapter) current(c *conn) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conns[c.account.Name] == c && c.signIn == a.signIns[c.account.Name] && !a.stopped
}

// withLog is ctx carrying linkedingo's logger, which it reads from the context. It logs
// at warnings only, whatever kith's level: its debug lines carry request URLs.
func (a *Adapter) withLog(ctx context.Context) context.Context {
	log := logging.Zerolog(a.log.With("component", "linkedingo"), zerolog.WarnLevel)
	return log.WithContext(ctx)
}

// clientFor is a linkedingo client signed in with s; handlers hear its realtime stream.
func (a *Adapter) clientFor(ctx context.Context, s Session, handlers linkedingo.Handlers) *linkedingo.Client {
	return linkedingo.NewClient(a.withLog(ctx), linkedingo.NewURN(s.ProfileID).AsFsdProfile(), s.Cookies,
		s.PageInstance, s.Track, s.SyncToken, handlers)
}

// profileOf reads the account's own profile with client: the check that its session
// works.
func (a *Adapter) profileOf(ctx context.Context, client *linkedingo.Client) (*linkedingo.UserProfile, error) {
	ctx, cancel := context.WithTimeout(a.withLog(ctx), profileTimeout)
	defer cancel()
	var profile *linkedingo.UserProfile
	err := a.guard("reading the profile", func() (err error) {
		profile, err = client.GetCurrentUserProfile(ctx)
		return err //nolint:wrapcheck // the caller reads its kind (refusal)
	})
	return profile, err
}

// connect checks c's session (unless a login just did), then runs the realtime stream
// until the connection is let go, restarting it when linkedingo gives up. A session
// LinkedIn refuses signs the account out with what to do.
func (a *Adapter) connect(ctx context.Context, c *conn, checked bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	a.seedRooms(ctx, c)
	wait := retryFirst
	for ctx.Err() == nil {
		a.session(c.account, Connecting, "")
		c.mu.Lock()
		s := c.session
		c.mu.Unlock()
		client := a.clientFor(ctx, s, a.handlers(c))
		if !checked {
			if _, err := a.profileOf(ctx, client); err != nil {
				if !a.retry(ctx, c, err, &wait) {
					return
				}
				continue
			}
		}
		checked = false
		if c.closed() {
			return
		}
		_ = client.RealtimeConnect(a.withLog(ctx)) // starts its loops; never fails
		c.mu.Lock()
		c.client = client
		c.mu.Unlock()
		var err error
		select {
		case err = <-c.gaveUp:
		case <-ctx.Done():
			client.RealtimeDisconnect()
			return
		}
		client.RealtimeDisconnect()
		a.keepCookies(c)
		if !a.retry(ctx, c, err, &wait) {
			return
		}
	}
}

// retry waits before the next attempt after err, doubling wait; false when err means
// the session is over (the account is then signed out) or the connection was let go.
func (a *Adapter) retry(ctx context.Context, c *conn, err error, wait *time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if over := refusal(err); over != "" {
		a.log.Warn("the session is over", "account", c.account.Name, "err", err)
		a.session(c.account, SignedOut, over+"; then run `kith login linkedin "+c.account.Name+"`")
		return false
	}
	a.log.Info("retrying", "account", c.account.Name, "in", *wait, "err", err)
	a.session(c.account, Connecting, fmt.Sprintf("LinkedIn did not answer (%v); retrying", err))
	select {
	case <-time.After(*wait):
	case <-ctx.Done():
		return false
	}
	*wait = min(2**wait, retryMax)
	return true
}

// handlers are linkedingo's realtime callbacks for c. They run on its stream's
// goroutine, so each is guarded: a panic drops one event, not the daemon.
func (a *Adapter) handlers(c *conn) linkedingo.Handlers {
	heard := func(what string, fn func()) {
		if c.closed() {
			return
		}
		if err := a.guard(what, func() error { fn(); return nil }); err != nil {
			a.log.Error("an event was dropped", "account", c.account.Name, "err", err)
		}
	}
	gaveUp := func(err error) {
		select {
		case c.gaveUp <- err:
		default:
		}
	}
	return linkedingo.Handlers{
		Heartbeat: func(context.Context) { heard("a heartbeat", func() { a.keepCookies(c) }) },
		ClientConnection: func(ctx context.Context, cc *linkedingo.ClientConnection) {
			heard("connecting", func() {
				a.session(c.account, Connected, "")
				a.keepCookies(c)
				go a.sync(context.WithoutCancel(ctx), c, cc.SessID.String())
				go a.sendOwed(c)
			})
		},
		DecoratedEvent: func(ctx context.Context, e *linkedingo.DecoratedEvent) {
			heard("an event", func() { a.event(context.WithoutCancel(ctx), c, e) })
		},
		ConversationsSyncToken: func(_ context.Context, token string) {
			heard("a sync token", func() {
				c.mu.Lock()
				c.session.SyncToken = token
				c.mu.Unlock()
			})
		},
		TransientDisconnect: func(_ context.Context, err error) {
			heard("a disconnect", func() {
				a.log.Debug("disconnected; linkedingo reconnects", "account", c.account.Name, "err", err)
				a.session(c.account, Connecting, "reconnecting")
			})
		},
		BadCredentials: func(_ context.Context, err error) { gaveUp(fmt.Errorf("%w (%w)", errSessionEnded, err)) },
		UnknownError:   func(_ context.Context, err error) { gaveUp(err) },
	}
}

// errSessionEnded is LinkedIn refusing the session on the realtime stream.
var errSessionEnded = errors.New("linkedin: LinkedIn refused the session")

// refusal is what to tell the person when err means LinkedIn will not take this
// session, and "" when it is worth trying again.
func refusal(err error) string {
	var re *linkedingo.ResponseError
	switch {
	case errors.Is(err, linkedingo.ErrTokenInvalidated), errors.Is(err, errSessionEnded):
		return "LinkedIn ended this session: copy the cookies again from a browser signed in to LinkedIn"
	case errors.As(err, &re):
		switch re.StatusCode {
		case http.StatusUnauthorized, http.StatusFound:
			return "LinkedIn refused these cookies: copy them again from a browser signed in to LinkedIn"
		case http.StatusForbidden, statusAutomated:
			return "LinkedIn wants a check: open linkedin.com in your browser and finish it"
		}
	}
	return ""
}

// statusAutomated is LinkedIn's answer to a client it takes for a robot.
const statusAutomated = 999

// keepCookies saves c's session when LinkedIn has rotated its cookies, unless a newer
// sign-in has taken the account over: its session must not be overwritten by an older one.
func (a *Adapter) keepCookies(c *conn) {
	a.keeping.Lock()
	defer a.keeping.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	blob, err := c.session.encode() // the jar is linkedingo's own, rotated in place
	if err != nil || blob == c.saved {
		return
	}
	if !a.current(c) {
		return
	}
	if err := a.secrets.StoreSecret(sessionRef(c.account.Name), blob); err != nil {
		a.log.Warn("save the rotated cookies failed", "account", c.account.Name, "err", err)
		return
	}
	c.saved = blob
}

// guard runs fn, turning a panic into an error. LinkedIn changes its private API
// without notice and linkedingo dereferences what it is sent: that must fail one call
// or drop one event, not take the daemon and every other network down with it.
func (a *Adapter) guard(what string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("the LinkedIn library panicked", "while", what, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("%w while %s: %v", errLibraryPanicked, what, r)
		}
	}()
	return fn()
}

// errLibraryPanicked is a panic inside linkedingo, caught.
var errLibraryPanicked = errors.New("linkedin: the LinkedIn library failed")
