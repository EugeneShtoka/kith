package meta

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	"go.mau.fi/util/exhttp"
	"go.mau.fi/whatsmeow"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// A connection loads the account's messages page with its cookies (which proves the
// session and returns the inbox), then holds messagix's socket, which reconnects by
// itself. Meta rotates cookies on its responses; they are saved when they change.

// The wait between failed page loads doubles from retryFirst up to retryMax.
const (
	retryFirst = 2 * time.Second
	retryMax   = 5 * time.Minute
)

// pageTimeout bounds loading the messages page. It takes seconds; Meta has been seen to
// stop sending one halfway, and messagix's own limit is five minutes of "signing in".
const pageTimeout = 2 * time.Minute

// loadPage loads the messages page with client, under pageTimeout, a panic in the
// library an error.
func (a *Adapter) loadPage(ctx context.Context, client *messagix.Client, account string) (types.UserInfo, *table.LSTable, error) {
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	start := time.Now()
	var user types.UserInfo
	var inbox *table.LSTable
	err := a.guard("loading the messages page", func() (err error) {
		user, inbox, err = client.LoadMessagesPage(ctx)
		return err //nolint:wrapcheck // the caller wraps it and reads its kind (errors.Is)
	})
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		a.log.Warn("load the messages page failed", "account", account, "after", took, "err", err)
		return nil, nil, err
	}
	a.log.Info("loaded the messages page", "account", account, "in", took)
	return user, inbox, nil
}

// conn is one signed-in account's connection.
type conn struct {
	account Account
	// metaID is the Meta account; it never changes, unlike the session's cookies.
	metaID int64
	// signIn is the account's sign-in the session is from (Adapter.signIns).
	signIn int

	// done closes when the connection is let go (re-sign-in, removal, stop).
	done      chan struct{}
	closeOnce sync.Once

	mu sync.Mutex
	// session is the session as last saved; client is messagix's, once loaded.
	session Session
	saved   string
	client  *messagix.Client

	// applying serializes taking tables in: the inbox, and rooms, which is the account's
	// rooms as last written (the cached ones, then every thread heard of), so a write
	// never sweeps a thread only an older page listed.
	applying sync.Mutex
	inbox    *inbox
	rooms    map[domain.RoomID]domain.Room
	// asked is the people already asked Meta for (askContacts). Under applying.
	asked map[int64]bool

	// e2ee is the encrypted chats' connection once up; e2eeBusy marks one being made.
	// Under mu.
	e2ee     *whatsmeow.Client
	e2eeBusy bool
}

// loaded is a messages page a login already loaded, so connecting does not load it again.
type loaded struct {
	client *messagix.Client
	inbox  *table.LSTable
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
		account: account, metaID: s.MetaID, signIn: signIn, session: s, saved: saved, done: make(chan struct{}),
		inbox: newInbox(s.MetaID), rooms: map[domain.RoomID]domain.Room{}, asked: map[int64]bool{},
	}
}

// close lets the connection go; messagix's socket stops.
func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.mu.Lock()
		client, wa := c.client, c.e2ee
		c.mu.Unlock()
		if wa != nil {
			wa.Disconnect()
		}
		client.Disconnect() // nil-safe
	})
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

// zerolog is messagix's logger. It logs at warnings only, whatever kith's level: its
// debug lines carry request payloads, login steps among them.
func (a *Adapter) zerolog() zerolog.Logger {
	return logging.Zerolog(a.log.With("component", "messagix"), zerolog.WarnLevel)
}

// clientFor is a messagix client signed in with s.
func (a *Adapter) clientFor(s Session) *messagix.Client {
	client := messagix.NewClient(s.cookies(), a.zerolog(), &messagix.Config{ClientSettings: exhttp.SensibleClientSettings})
	client.MessengerLite.SetNativeSession(s.Native) // nil-safe for both
	return client
}

// connect loads the messages page (unless the login did) and runs the socket until the
// connection is let go. A failed load is retried, except one that means the session
// is over; that one signs the account out with what to do.
func (a *Adapter) connect(ctx context.Context, c *conn, page *loaded) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	if page == nil {
		page = a.load(ctx, c)
		if page == nil {
			return
		}
	}
	c.mu.Lock()
	c.client = page.client
	c.mu.Unlock()
	if c.closed() {
		page.client.Disconnect()
		return
	}
	a.seedRooms(ctx, c)
	a.take(ctx, c, page.inbox)
	page.client.SetEventHandler(func(ctx context.Context, evt any) { a.handle(ctx, c, evt) })
	if err := page.client.Connect(ctx); err != nil {
		a.log.Warn("connect failed", "account", c.account.Name, "err", err)
		a.session(c.account, SignedOut, "it could not connect: "+err.Error())
		return
	}
	a.keepCookies(c)
	<-ctx.Done()
}

// load loads the messages page with c's session, retrying with backoff; nil when the
// session is over or the connection was let go.
func (a *Adapter) load(ctx context.Context, c *conn) *loaded {
	wait := retryFirst
	for {
		a.session(c.account, Connecting, "")
		c.mu.Lock()
		s := c.session
		c.mu.Unlock()
		client := a.clientFor(s)
		_, inbox, err := a.loadPage(ctx, client, c.account.Name)
		if err == nil {
			return &loaded{client: client, inbox: inbox}
		}
		if ctx.Err() != nil {
			return nil
		}
		if over := sessionOver(err, c.account.Name); over != "" {
			a.log.Warn("the session is over", "account", c.account.Name, "err", err)
			a.session(c.account, SignedOut, over)
			return nil
		}
		a.log.Info("retrying the messages page", "account", c.account.Name, "in", wait)
		a.session(c.account, Connecting, fmt.Sprintf("Meta did not answer (%v); retrying", err))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
		wait = min(2*wait, retryMax)
	}
}

// sessionOver is what to tell the person when err means the session cannot go on, and
// "" when it is worth retrying.
func sessionOver(err error, account string) string {
	again := "run `kith login messenger " + account + "`"
	where := httpclient.GetErrorRedirectURL(err)
	if where == "" {
		where = "facebook.com"
	}
	switch {
	case errors.Is(err, httpclient.ErrCheckpointRequired), errors.Is(err, httpclient.ErrChallengeRequired):
		return "Meta wants a security check: finish it at " + where + ", then " + again
	case errors.Is(err, httpclient.ErrConsentRequired):
		return "Meta asks you to accept its terms: do it at " + where + ", then " + again
	case errors.Is(err, httpclient.ErrAccountSuspended):
		return "Meta suspended the account; see " + where
	case errors.Is(err, httpclient.ErrTokenInvalidated):
		return "Meta ended the session; " + again
	}
	return ""
}

// handle is messagix's events for c: connection state and pushed tables. Each table is
// acknowledged so the sync position moves on.
func (a *Adapter) handle(_ context.Context, c *conn, evt any) {
	if c.closed() {
		return
	}
	if err := a.guard("handling an event", func() error { a.react(c, evt); return nil }); err != nil {
		a.log.Error("an event was dropped", "account", c.account.Name, "err", err)
	}
}

// react is handle's work.
func (a *Adapter) react(c *conn, evt any) {
	switch e := evt.(type) {
	case *messagix.ConnectedEvent, *messagix.ReconnectedEvent:
		a.session(c.account, Connected, "")
		go a.sendOwed(c)
		go a.connectE2EE(context.Background(), c)
	case *messagix.TransientDisconnectEvent:
		a.log.Debug("disconnected; messagix reconnects", "account", c.account.Name, "attempt", e.ConnectionAttempts, "err", e.Err)
		a.session(c.account, Connecting, "reconnecting")
	case *messagix.PermanentErrorEvent:
		a.log.Warn("the connection ended for good", "account", c.account.Name, "err", e.Err)
		a.session(c.account, SignedOut, "Meta closed the connection; run `kith login messenger "+c.account.Name+"`")
	case *table.LSTable:
		a.take(context.Background(), c, e)
		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		client.PostHandlePublishResponse(e)
	}
	a.keepCookies(c)
}

// keepCookies saves c's session when Meta has rotated its cookies, unless a newer
// sign-in has taken the account over: its session must not be overwritten by an older one.
func (a *Adapter) keepCookies(c *conn) {
	a.keeping.Lock()
	defer a.keeping.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return
	}
	next := c.session.withCookies(c.client.GetCookies())
	blob, err := next.encode()
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
	c.session, c.saved = next, blob
}

// loginPlatform is the app a login signs in as. iOS: messagix's Android user agent and
// build number disagree (its own FIXME).
const loginPlatform = types.MessengerLiteIOS

// guard runs fn, turning a panic into an error. Meta changes its login screens and its
// protocol without notice, and messagix's interpreter has panicked on a screen it did
// not know (v0.2609.0, 2026-10-13): that must fail one login or drop one event, not
// take the daemon and every other network down with it.
func (a *Adapter) guard(what string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("the Messenger library panicked", "while", what, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("%w while %s: %v", errLibraryPanicked, what, r)
		}
	}()
	return fn()
}

// errLibraryPanicked is a panic inside messagix, caught.
var errLibraryPanicked = errors.New("meta: the Messenger library failed")

// seedRooms starts c's rooms from the account's cached ones.
func (a *Adapter) seedRooms(ctx context.Context, c *conn) {
	if a.cache == nil {
		return
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		a.log.Warn("read the cached threads failed", "account", c.account.Name, "err", err)
		return
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	for i := range rooms {
		if owner(c.metaID).Owns(rooms[i].ID) {
			c.rooms[rooms[i].ID] = rooms[i]
		}
	}
}

// take applies one table to c's inbox and stores what it changed, one table at a time.
func (a *Adapter) take(ctx context.Context, c *conn, tbl *table.LSTable) {
	if tbl == nil || c.closed() {
		return
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	ch := c.inbox.apply(tbl)
	if len(ch.left) > 0 {
		a.log.Info("threads not shown", "account", c.account.Name, "why", ch.left)
	}
	a.store(ctx, c, ch)
	var ask []int64
	for _, id := range c.inbox.unnamed(ch.threads) {
		if !c.asked[id] {
			c.asked[id] = true
			ask = append(ask, id)
		}
	}
	if len(ask) > 0 {
		go a.askContacts(context.WithoutCancel(ctx), c, ask)
	}
}

// contactSpan bounds asking Meta for one contact; contactWait, waiting for the
// connection to open first.
const (
	contactSpan = 30 * time.Second
	contactWait = 2 * time.Minute
)

// askContacts asks Meta for people whose one-to-one threads came without them, one at
// a time, and takes the answers in as any table. Each is asked once per connection.
func (a *Adapter) askContacts(ctx context.Context, c *conn, ids []int64) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return
	}
	// The first page is taken before the socket opens; asking waits for it.
	if err := client.WaitUntilCanSendMessages(ctx, contactWait); err != nil {
		a.log.Warn("the connection never opened to ask for contacts; asked again later", "account", c.account.Name, "err", err)
		a.unask(c, ids)
		return
	}
	rows := 0
	for _, id := range ids {
		if c.closed() {
			return
		}
		var resp *table.LSTable
		err := a.guard("asking for a contact", func() error {
			asking, cancel := context.WithTimeout(ctx, contactSpan)
			defer cancel()
			var err error
			resp, err = client.ExecuteTasks(asking, &socket.GetContactsFullTask{ContactID: id})
			return err //nolint:wrapcheck // logged below with what it was for
		})
		if err != nil {
			a.log.Warn("ask Meta for a contact failed; asked again later", "account", c.account.Name, "err", err)
			a.unask(c, []int64{id})
			continue
		}
		if resp != nil {
			rows += len(resp.LSDeleteThenInsertContact) + len(resp.LSVerifyContactRowExists)
		}
		a.take(ctx, c, resp)
	}
	a.log.Info("asked Meta for contacts", "account", c.account.Name, "asked", len(ids), "contact_rows", rows)
}

// unask lets people whose asking failed be asked again with a later table.
func (a *Adapter) unask(c *conn, ids []int64) {
	c.applying.Lock()
	defer c.applying.Unlock()
	for _, id := range ids {
		delete(c.asked, id)
	}
}
