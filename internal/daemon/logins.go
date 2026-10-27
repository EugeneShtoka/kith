package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
)

// loginTimeout bounds one login: long enough to type a code from a phone, short
// enough that a forgotten one ends.
const loginTimeout = 15 * time.Minute

var (
	// errNoSuchLogin answers a login that ended or never was.
	errNoSuchLogin = errors.New("daemon: that login has ended; begin it again")
	// errNoSuchNetwork answers a login of a network the daemon cannot log in to.
	errNoSuchNetwork = fmt.Errorf("%w: no network here is called that", api.ErrNetworkOff)
	// errNoInstead answers InsteadLogin when the login's step offers no other way.
	errNoInstead = errors.New("daemon: this step of the login offers no other way")
)

// Logins runs logins as conversations (api.Logins): each network leads its own
// (api.LoginLeader), handing the client one step per call. One login per account
// runs at once; beginning another ends the first.
type Logins struct {
	leaders func() []api.LoginLeader
	run     context.Context //nolint:containedctx // logins outlive the RPC that began them
	// log hears each login's steps and end (never an answer); nil is silent. Set before use.
	log *slog.Logger

	mu    sync.Mutex
	byID  map[string]*conversation
	byKey map[string]*conversation
}

// NewLogins runs logins of the networks leaders lists (read per call, since networks
// come and go with the config) until run ends.
func NewLogins(run context.Context, leaders func() []api.LoginLeader) *Logins {
	return &Logins{leaders: leaders, run: run, byID: map[string]*conversation{}, byKey: map[string]*conversation{}}
}

// LogTo has the logins log each step they hand out and how each ended: a login leads
// the person through a network's own screens, and without this a stuck one leaves no
// trace. Answers are never logged, only which fields were asked.
func (l *Logins) LogTo(log *slog.Logger) { l.log = log }

func (l *Logins) logger() *slog.Logger {
	if l.log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return l.log
}

type conversation struct {
	id, network string
	// log is the leader's; nil until it runs.
	log *slog.Logger
	// steps carries the leader's steps one at a time; answers carries the client's
	// answers to steps that ask.
	steps   chan said
	answers chan map[string]string
	cancel  context.CancelFunc
	ended   <-chan struct{}

	// turn lets one call speak at a time; under it, awaiting says the client's current
	// step expects an answer.
	turn     sync.Mutex
	awaiting bool

	// instead is closed when the client takes the other way a QR step offered
	// (InsteadLogin), which it may do while an AnswerLogin holds turn.
	instead     chan struct{}
	insteadOnce sync.Once

	mu      sync.Mutex
	account string
	// offered says the client's current step is a QR step offering another way.
	offered bool
}

// said is a step, or how the login failed.
type said struct {
	step api.LoginStep
	err  error
}

var _ api.Logins = (*Logins)(nil)

// LoginNetworks is every network that leads a login.
func (l *Logins) LoginNetworks(context.Context) ([]api.LoginNetwork, error) {
	var out []api.LoginNetwork
	for _, leader := range l.leaders() {
		out = append(out, leader.LoginNetwork())
	}
	return out, nil
}

func (l *Logins) leader(name string) (api.LoginLeader, error) {
	i := slices.IndexFunc(l.leaders(), func(leader api.LoginLeader) bool { return leader.LoginNetwork().Network == name })
	if i < 0 {
		return nil, fmt.Errorf("%w (%s)", errNoSuchNetwork, name)
	}
	return l.leaders()[i], nil
}

// BeginLogin starts a login of account on network, ending one already under way.
func (l *Logins) BeginLogin(ctx context.Context, network, account string) (api.LoginStep, error) {
	leader, err := l.leader(network)
	if err != nil {
		return api.LoginStep{}, err
	}
	id, err := loginID()
	if err != nil {
		return api.LoginStep{}, err
	}
	run, cancel := context.WithTimeout(l.run, loginTimeout)
	c := &conversation{
		id: id, network: network, account: account, cancel: cancel, ended: run.Done(),
		steps: make(chan said), answers: make(chan map[string]string), instead: make(chan struct{}),
	}
	l.mu.Lock()
	if account != "" {
		if old := l.byKey[network+"\x00"+account]; old != nil {
			old.cancel()
		}
		l.byKey[network+"\x00"+account] = c
	}
	l.byID[id] = c
	l.mu.Unlock()
	l.logger().Info("login begun", "network", network, "account", account, "login", id)
	go l.lead(run, c, leader)
	return c.next(ctx)
}

// lead runs the leader's side, then reports how it ended and forgets the login.
func (l *Logins) lead(ctx context.Context, c *conversation, leader api.LoginLeader) {
	defer l.forget(c)
	c.log = l.logger().With("network", c.network, "login", c.id)
	end, err := leader.Login(ctx, c.accountNow(), talk{c})
	last := said{err: err}
	if err == nil {
		last.step = api.LoginStep{Done: end.Done, Restart: end.Restart}
	}
	if !c.say(ctx, last) {
		c.log.Info("login ended with nobody waiting to hear it", "account", c.accountNow(), "err", cmpErr(err, ctx.Err()))
	}
}

// cmpErr is err, else other.
func cmpErr(err, other error) error {
	if err != nil {
		return err
	}
	return other
}

func (l *Logins) forget(c *conversation) {
	c.cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byID, c.id)
	key := c.network + "\x00" + c.accountNow()
	if l.byKey[key] == c {
		delete(l.byKey, key)
	}
}

// AnswerLogin hands the login its answer, if its step asked for one, and returns the
// next step.
func (l *Logins) AnswerLogin(ctx context.Context, id string, values map[string]string) (api.LoginStep, error) {
	l.mu.Lock()
	c := l.byID[id]
	l.mu.Unlock()
	if c == nil {
		return api.LoginStep{}, errNoSuchLogin
	}
	c.turn.Lock()
	defer c.turn.Unlock()
	if c.awaiting {
		select {
		case c.answers <- values:
			c.awaiting = false
		case <-c.ended:
			return api.LoginStep{}, errNoSuchLogin
		case <-ctx.Done():
			return api.LoginStep{}, ctx.Err() //nolint:wrapcheck // the caller's own
		}
	}
	return c.nextLocked(ctx)
}

// CancelLogin ends a login.
func (l *Logins) CancelLogin(_ context.Context, id string) error {
	l.mu.Lock()
	c := l.byID[id]
	l.mu.Unlock()
	if c != nil {
		c.cancel()
	}
	return nil
}

// InsteadLogin takes the other way the login's QR step offers. It takes no turn: the
// client's AnswerLogin is waiting on the QR step, and returns where the other way
// leads.
func (l *Logins) InsteadLogin(_ context.Context, id string) error {
	l.mu.Lock()
	c := l.byID[id]
	l.mu.Unlock()
	if c == nil {
		return errNoSuchLogin
	}
	c.mu.Lock()
	offered := c.offered
	c.mu.Unlock()
	if !offered {
		return errNoInstead
	}
	c.insteadOnce.Do(func() { close(c.instead) })
	return nil
}

func (c *conversation) next(ctx context.Context) (api.LoginStep, error) {
	c.turn.Lock()
	defer c.turn.Unlock()
	return c.nextLocked(ctx)
}

// nextLocked is next under turn.
func (c *conversation) nextLocked(ctx context.Context) (api.LoginStep, error) {
	select {
	case s := <-c.steps:
		if s.err != nil {
			return api.LoginStep{}, s.err
		}
		s.step.Login, s.step.Account = c.id, c.accountNow()
		c.awaiting = s.step.Ask != nil || s.step.Configure != nil
		c.mu.Lock()
		c.offered = s.step.QR != "" && s.step.Instead != ""
		c.mu.Unlock()
		return s.step, nil
	case <-c.ended:
		return api.LoginStep{}, errNoSuchLogin
	case <-ctx.Done():
		return api.LoginStep{}, ctx.Err() //nolint:wrapcheck // the caller's own
	}
}

// say hands the client a step when it next asks.
func (c *conversation) say(ctx context.Context, s said) bool {
	c.logStep(s)
	select {
	case c.steps <- s:
		return true
	case <-ctx.Done():
		return false
	}
}

// logStep logs what a step is, never what it shows: a code or a QR code is a secret to
// use elsewhere, so only that there is one.
func (c *conversation) logStep(s said) {
	if c.log == nil {
		return
	}
	account := c.accountNow()
	switch {
	case s.err != nil:
		c.log.Info("login failed", "account", account, "err", s.err)
	case s.step.Done != "" || s.step.Restart:
		c.log.Info("login done", "account", account, "done", s.step.Done, "restart", s.step.Restart)
	case s.step.Configure != nil:
		c.log.Info("login writes an account", "account", account, "table", s.step.Configure.Table)
	case s.step.Code != "":
		c.log.Info("login shows a code", "account", account)
	case s.step.QR != "":
		c.log.Info("login shows a QR code", "account", account)
	default:
		keys := make([]string, 0, len(s.step.Ask))
		for _, f := range s.step.Ask {
			keys = append(keys, f.Key)
		}
		c.log.Info("login asks", "account", account, "fields", keys, "note", s.step.Note != "")
	}
}

// hear is the client's answer to the last step.
func (c *conversation) hear(ctx context.Context) (map[string]string, error) {
	select {
	case values := <-c.answers:
		return values, nil
	case <-ctx.Done():
		return nil, ctx.Err() //nolint:wrapcheck // the login's own end
	}
}

func (c *conversation) accountNow() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.account
}

// talk is the leader's side of c (api.LoginTalk).
type talk struct{ c *conversation }

func (t talk) Ask(ctx context.Context, note string, fields ...api.LoginField) (map[string]string, error) {
	if !t.c.say(ctx, said{step: api.LoginStep{Note: note, Ask: fields}}) {
		return nil, ctx.Err() //nolint:wrapcheck // the login's own end
	}
	return t.c.hear(ctx)
}

func (t talk) Show(ctx context.Context, code, note string) error {
	if !t.c.say(ctx, said{step: api.LoginStep{Note: note, Code: code}}) {
		return ctx.Err() //nolint:wrapcheck // the login's own end
	}
	return nil
}

func (t talk) ShowQR(ctx context.Context, data, note, instead string) error {
	if !t.c.say(ctx, said{step: api.LoginStep{Note: note, QR: data, Instead: instead}}) {
		return ctx.Err() //nolint:wrapcheck // the login's own end
	}
	return nil
}

func (t talk) Instead() <-chan struct{} { return t.c.instead }

func (t talk) Configure(ctx context.Context, record api.LoginRecord) error {
	if !t.c.say(ctx, said{step: api.LoginStep{Configure: &record}}) {
		return ctx.Err() //nolint:wrapcheck // the login's own end
	}
	_, err := t.c.hear(ctx)
	return err
}

func (t talk) Named(account string) {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	t.c.account = account
}

func loginID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("daemon: name a login: %w", err)
	}
	return hex.EncodeToString(b), nil
}
