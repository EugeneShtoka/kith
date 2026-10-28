package gmessages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/util/exhttp"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// A connection holds libgm's long poll, which reconnects by itself; every request goes
// through the phone, so the phone's state shows in the account's. The pairing's token
// and cookies change as it runs, and are saved when they do.

// The wait between failed connects doubles from retryFirst up to retryMax.
const (
	retryFirst = 5 * time.Second
	retryMax   = 5 * time.Minute
)

// cookieCheck is how often a connection looks for cookies Google changed: libgm takes
// them from every response without saying so.
const cookieCheck = time.Minute

// Account details, as the account's status says them.
const (
	phoneSilent   = "the phone is not answering: is it on and online?"
	otherBrowser  = "Messages for web is active in another browser or a bridge; close it there, then run `kith login gmessages %s`"
	reconnecting  = "reconnecting"
	unpairedAgain = "the phone unpaired kith; run `kith login gmessages %s` to pair again"
	cookiesStale  = "Google no longer takes the account's cookies (the browser they came from signed out, " +
		"or went on using them and Google changed them): copy fresh ones from a private window, close it " +
		"without signing out, and run `kith login gmessages %s`"
)

// conn is one paired account's connection.
type conn struct {
	account Account
	// phone is the paired phone; it never changes, unlike the session's token. self is
	// the account part of its IDs.
	phone, self string
	// signIn is the account's pairing the session is from (Adapter.signIns).
	signIn int

	// done closes when the connection is let go (re-pairing, removal, stop).
	done      chan struct{}
	closeOnce sync.Once
	// gaveUp hears the long poll ending for good, which the connection then restarts or,
	// when the pairing is over, ends.
	gaveUp chan error

	mu sync.Mutex
	// session is the session as last saved; client is libgm's, once connected.
	session *Session
	saved   string
	client  *libgm.Client
	// me is who you are on this phone, as the inbox last knew: read by Me, which may run
	// while the inbox is being written (it counts unread for it).
	me []string
	// sims are the phone's SIMs, by the participant each sends as, as its settings last
	// said.
	sims map[string]*gmproto.SIMCard

	// queue is the conversation and message events libgm handed over, for work to take.
	queue chan any
	// applying serializes taking them in: the inbox, and rooms, which is the account's
	// rooms as last written (the cached ones, then every conversation heard of), so a
	// write never sweeps one only an older listing had.
	applying sync.Mutex
	inbox    *inbox
	rooms    map[domain.RoomID]domain.Room
	// reacted is when each message's reactions last changed as kith knows it (a live
	// update, or a reaction sent): a listing asked for before then is older.
	reacted map[domain.EventID]time.Time
	// syncing is held by the sync running, if any; backfilling by the backfill.
	syncing, backfilling sync.Mutex
	// inactive: another browser or a bridge took the Messages for web session; silent:
	// the phone stopped answering. Either holds the backfill back.
	inactive, silent atomic.Bool
}

func (a *Adapter) newConn(account Account, s *Session, at *int) *conn {
	a.mu.Lock()
	signIn := a.signIns[account.Name]
	a.mu.Unlock()
	if at != nil {
		signIn = *at
	}
	saved, _ := s.encode() // a session that will not encode is saved again on its first change
	return &conn{
		account: account, phone: s.PhoneID, self: selfOf(s.PhoneID), signIn: signIn, session: s, saved: saved,
		done: make(chan struct{}), gaveUp: make(chan error, 1),
		queue: make(chan any, queueCapacity), inbox: newInbox(selfOf(s.PhoneID)), rooms: map[domain.RoomID]domain.Room{},
		reacted: map[domain.EventID]time.Time{},
	}
}

// close lets the connection go; connect then disconnects libgm.
func (c *conn) close() { c.closeOnce.Do(func() { close(c.done) }) }

func (c *conn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// newSignIn starts a new pairing of account: connections from older ones are stale.
func (a *Adapter) newSignIn(account string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signIns[account]++
	return a.signIns[account]
}

// adopt makes c its account's connection, letting an older one go, unless the account
// left the config, a newer pairing began, or the adapter stopped.
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

// zerolog is libgm's logger. It logs at warnings only, whatever kith's level: its
// debug lines carry request payloads.
func (a *Adapter) zerolog() zerolog.Logger {
	return logging.Zerolog(a.log.With("component", "libgm"), zerolog.WarnLevel)
}

// newClient is a libgm client over auth (a pairing, or a new one to pair).
func (a *Adapter) newClient(auth *libgm.AuthData) *libgm.Client {
	return libgm.NewClient(auth, nil, a.zerolog(), exhttp.SensibleClientSettings)
}

// connect runs c's connection until it is let go: connect, hold the long poll, and
// connect again when it ends, unless the pairing is over (the account then shows how
// to pair again). client is a pairing's own, already connected, or nil.
func (a *Adapter) connect(ctx context.Context, c *conn, client *libgm.Client) {
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
	go a.work(ctx, c)
	go a.watchCookies(ctx, c)
	wait := retryFirst
	for ctx.Err() == nil {
		if client == nil {
			a.session(c.account, Connecting, "")
			dialed := a.newClient(c.session.Auth)
			dialed.SetEventHandler(func(evt any) { a.handle(ctx, c, dialed, evt) })
			if err := a.dial(ctx, dialed); err != nil {
				dialed.Disconnect()
				if !a.retry(ctx, c, err, &wait) {
					return
				}
				continue
			}
			client = dialed
		}
		c.mu.Lock()
		c.client = client
		c.mu.Unlock()
		c.inactive.Store(false)
		c.silent.Store(false)
		// The session is saved from libgm's events (a refreshed token), on the goroutine
		// that changes it, never from here.
		a.session(c.account, Connected, "")
		go a.sync(ctx, c)
		go a.sendOwed(ctx, c)
		var err error
		select {
		case err = <-c.gaveUp:
		case <-ctx.Done():
		}
		client.Disconnect()
		client = nil
		if ctx.Err() != nil || !a.retry(ctx, c, err, &wait) {
			return
		}
	}
}

// dial fetches the config and connects client: a refused pairing comes back at once.
func (a *Adapter) dial(ctx context.Context, client *libgm.Client) error {
	return a.guard("connecting", func() error {
		if err := client.FetchConfig(ctx); err != nil {
			a.log.Warn("fetch the Messages config failed; connecting anyway", "err", err)
		}
		return client.Connect(ctx)
	})
}

// retry waits before the next connect after err, doubling wait; false when err means
// the pairing is over (the account then shows how to pair again) or the connection
// was let go.
func (a *Adapter) retry(ctx context.Context, c *conn, err error, wait *time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if over := refusal(err); over != "" {
		a.log.Warn("the pairing is over", "account", c.account.Name, "err", err)
		a.session(c.account, SignedOut, fmt.Sprintf(over, c.account.Name))
		return false
	}
	a.log.Info("retrying", "account", c.account.Name, "in", *wait, "err", err)
	a.session(c.account, Connecting, fmt.Sprintf("Google Messages did not answer (%v); retrying", err))
	select {
	case <-time.After(*wait):
	case <-ctx.Done():
		return false
	}
	*wait = min(2**wait, retryMax)
	return true
}

// errUnpaired is the phone ending the pairing; errSignedOut the Google account
// signing out where the cookies came from.
var (
	errUnpaired  = errors.New("gmessages: the pairing was ended")
	errSignedOut = errors.New("gmessages: the Google account signed out")
)

// refusal is what to tell the person when err means the pairing cannot go on (a format
// taking the account's name), and "" when it is worth trying again: unpaired on the
// phone, or the account's cookies no longer taken.
func refusal(err error) string {
	var httpErr events.HTTPError
	switch {
	case errors.Is(err, errUnpaired), errors.Is(err, events.ErrRequestedEntityNotFound):
		return unpairedAgain
	case errors.Is(err, errSignedOut), errors.Is(err, events.ErrInvalidCredentials):
		return cookiesStale
	case errors.As(err, &httpErr) && httpErr.Resp != nil && httpErr.Resp.StatusCode == http.StatusUnauthorized:
		return cookiesStale
	}
	return ""
}

// handle is libgm's events for c. It runs on libgm's goroutine and must not block:
// what needs a request goes on its own.
func (a *Adapter) handle(ctx context.Context, c *conn, client *libgm.Client, evt any) {
	if c.closed() {
		return
	}
	if err := a.guard("handling an event", func() error { a.react(ctx, c, client, evt); return nil }); err != nil {
		a.log.Error("an event was dropped", "account", c.account.Name, "err", err)
	}
}

// react is handle's work.
func (a *Adapter) react(ctx context.Context, c *conn, client *libgm.Client, evt any) {
	gaveUp := func(err error) {
		select {
		case c.gaveUp <- err:
		default:
		}
	}
	switch e := evt.(type) {
	case *events.ListenTemporaryError:
		a.log.Debug("the long poll failed; libgm retries", "account", c.account.Name, "err", e.Error)
		a.session(c.account, Connecting, reconnecting)
	case *events.ListenRecovered:
		a.session(c.account, Connected, "")
	case *events.PhoneRespondingAgain:
		c.silent.Store(false)
		a.session(c.account, Connected, "")
		go a.backfill(ctx, c)
	case *events.PhoneNotResponding:
		c.silent.Store(true)
		a.session(c.account, Connected, phoneSilent)
	case *events.ListenFatalError:
		gaveUp(e.Error)
	case *events.PingFailed:
		if errors.Is(e.Error, events.ErrRequestedEntityNotFound) {
			gaveUp(fmt.Errorf("%w (%w)", errUnpaired, e.Error))
		}
	case *gmproto.RevokePairData:
		gaveUp(errUnpaired)
	case *events.GaiaLoggedOut:
		gaveUp(errSignedOut)
	case *events.HackySetActiveMayFail:
		go a.takeOver(ctx, c, client)
	case *gmproto.UserAlertEvent:
		a.alert(ctx, c, client, e.GetAlertType())
	case *events.AuthTokenRefreshed:
		a.keep(c)
	case *gmproto.Settings:
		c.keepSIMs(e.GetSIMCards())
	case *gmproto.Conversation, *libgm.WrappedMessage:
		c.enqueue(e)
	}
}

// alert is a change on the phone or in which Messages for web session is active.
func (a *Adapter) alert(ctx context.Context, c *conn, client *libgm.Client, kind gmproto.AlertType) {
	switch kind {
	case gmproto.AlertType_BROWSER_ACTIVE:
		c.inactive.Store(false)
		a.session(c.account, Connected, "")
		a.keep(c)
		go a.sync(ctx, c) // active again: what came meanwhile is on the phone
	case gmproto.AlertType_BROWSER_INACTIVE:
		// Another browser or a bridge took the session: taking it back would fight it.
		c.inactive.Store(true)
		a.session(c.account, Connecting, fmt.Sprintf(otherBrowser, c.account.Name))
	case gmproto.AlertType_BROWSER_INACTIVE_FROM_TIMEOUT, gmproto.AlertType_BROWSER_INACTIVE_FROM_INACTIVITY:
		go a.takeOver(ctx, c, client) // gone idle, not taken: make kith's active again
	case gmproto.AlertType_MOBILE_BATTERY_LOW:
		a.log.Info("the phone's battery is low", "account", c.account.Name)
	default:
	}
}

// takeOver makes kith's the active Messages for web session again.
func (a *Adapter) takeOver(ctx context.Context, c *conn, client *libgm.Client) {
	err := a.guard("taking the session back", func() error {
		return client.SetActiveSession(ctx)
	})
	if err != nil {
		a.log.Warn("make kith's Messages session active failed", "account", c.account.Name, "err", err)
	}
}

// keep saves c's session when its token or cookies changed, unless a newer pairing has
// taken the account over: its session must not be overwritten by an older one.
func (a *Adapter) keep(c *conn) {
	a.keeping.Lock()
	defer a.keeping.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	blob, err := c.session.encode()
	if err != nil || blob == c.saved || !a.current(c) {
		return
	}
	if err := a.secrets.StoreSecret(sessionRef(c.account.Name), blob); err != nil {
		a.log.Warn("save the refreshed pairing failed", "account", c.account.Name, "err", err)
		return
	}
	c.saved = blob
}

// watchCookies saves c's cookies whenever Google has changed them, until c is let go.
func (a *Adapter) watchCookies(ctx context.Context, c *conn) {
	tick := time.NewTicker(cookieCheck)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			a.keepCookies(c)
		case <-ctx.Done():
			return
		}
	}
}

// keepCookies saves c's live cookies over its last saved session, when they differ,
// unless a newer pairing has taken the account over. It runs off libgm's goroutines, so
// it reads only the cookies, under libgm's lock: the token is saved by keep, from the
// goroutine that changes it.
func (a *Adapter) keepCookies(c *conn) {
	a.keeping.Lock()
	defer a.keeping.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	var stored Session
	if err := json.Unmarshal([]byte(c.saved), &stored); err != nil || stored.Auth == nil || !a.current(c) {
		return
	}
	live := c.session.Auth
	live.CookiesLock.RLock()
	cookies := maps.Clone(live.Cookies)
	live.CookiesLock.RUnlock()
	if maps.Equal(cookies, stored.Auth.Cookies) {
		return
	}
	stored.Auth.Cookies = cookies
	blob, err := stored.encode()
	if err != nil {
		return
	}
	if err := a.secrets.StoreSecret(sessionRef(c.account.Name), blob); err != nil {
		a.log.Warn("save the changed cookies failed", "account", c.account.Name, "err", err)
		return
	}
	c.saved = blob
}

// guard runs fn, turning a panic into an error. Google changes Messages for web
// without notice, and libgm dereferences what it is sent: that must fail one call or
// drop one event, not take the daemon and every other network down with it.
func (a *Adapter) guard(what string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("the Google Messages library panicked", "while", what, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("%w while %s: %v", errLibraryPanicked, what, r)
		}
	}()
	return fn()
}

// errLibraryPanicked is a panic inside libgm, caught.
var errLibraryPanicked = errors.New("gmessages: the Google Messages library failed")

// knowMe copies who the inbox knows you are, for Me. Caller holds c.applying.
func (c *conn) knowMe() {
	me := make([]string, 0, len(c.inbox.me))
	for id := range c.inbox.me {
		me = append(me, id)
	}
	c.mu.Lock()
	c.me = me
	c.mu.Unlock()
}

// keepSIMs takes the phone's SIMs from its settings; settings with none leave them.
func (c *conn) keepSIMs(cards []*gmproto.SIMCard) {
	if len(cards) == 0 {
		return
	}
	sims := make(map[string]*gmproto.SIMCard, len(cards))
	for _, card := range cards {
		sims[card.GetSIMParticipant().GetID()] = card
	}
	c.mu.Lock()
	c.sims = sims
	c.mu.Unlock()
}

// simOf is the SIM a conversation sends from: its own (sim), else the phone's for the
// participant it sends as; nil when neither is known (the phone then picks).
func (c *conn) simOf(sim *gmproto.SIMCard, outgoing string) *gmproto.SIMCard {
	if sim != nil {
		return sim
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sims[outgoing]
}
