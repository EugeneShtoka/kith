package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
)

// loginTimeout bounds one login, from its first step to its last: long enough for a
// code to be typed on a phone, short enough that a forgotten one ends.
const loginTimeout = 15 * time.Minute

var (
	// errNoSuchLogin is an answer to a login that ended or never was.
	errNoSuchLogin = errors.New("daemon: that login has ended; begin it again")
	// errNoSuchNetwork is a login of a network the daemon cannot log in to.
	errNoSuchNetwork = fmt.Errorf("%w: no network here is called that", api.ErrNetworkOff)
)

// Logins runs logins as conversations (api.Logins): each network leads its own
// (api.LoginLeader), and every step it takes is handed to the client one call at a
// time. One login per account runs at once: beginning another ends the first. Build
// it with NewLogins.
type Logins struct {
	leaders func() []api.LoginLeader
	run     context.Context //nolint:containedctx // logins outlive the RPC that began them

	mu    sync.Mutex
	byID  map[string]*conversation
	byKey map[string]*conversation
}

// NewLogins runs logins of what leaders lists (read at each call: a network may come
// and go with the config) until run ends.
func NewLogins(run context.Context, leaders func() []api.LoginLeader) *Logins {
	return &Logins{leaders: leaders, run: run, byID: map[string]*conversation{}, byKey: map[string]*conversation{}}
}

// conversation is one login under way.
type conversation struct {
	id, network string
	// steps carries what the leader says, one at a time; answers what the client
	// answers to a step that asks.
	steps   chan said
	answers chan map[string]string
	cancel  context.CancelFunc
	ended   <-chan struct{}

	// turn lets one call speak at a time; under it, awaiting says the step the
	// client holds asks for an answer.
	turn     sync.Mutex
	awaiting bool

	mu      sync.Mutex
	account string
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

// leader is the network called name.
func (l *Logins) leader(name string) (api.LoginLeader, error) {
	i := slices.IndexFunc(l.leaders(), func(leader api.LoginLeader) bool { return leader.LoginNetwork().Network == name })
	if i < 0 {
		return nil, fmt.Errorf("%w (%s)", errNoSuchNetwork, name)
	}
	return l.leaders()[i], nil
}

// BeginLogin starts a login of account on network, ending one of it under way.
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
		steps: make(chan said), answers: make(chan map[string]string),
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
	go l.lead(run, c, leader)
	return c.next(ctx)
}

// lead runs the leader's side, then says how it ended and forgets the login.
func (l *Logins) lead(ctx context.Context, c *conversation, leader api.LoginLeader) {
	defer l.forget(c)
	end, err := leader.Login(ctx, c.accountNow(), talk{c})
	last := said{err: err}
	if err == nil {
		last.step = api.LoginStep{Done: end.Done, Restart: end.Restart}
	}
	c.say(ctx, last)
}

// forget ends c and drops it from the logins.
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

// AnswerLogin hands the login its answer, if its step asked for one, and is the next
// step.
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

// next is the leader's next step.
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
		return s.step, nil
	case <-c.ended:
		return api.LoginStep{}, errNoSuchLogin
	case <-ctx.Done():
		return api.LoginStep{}, ctx.Err() //nolint:wrapcheck // the caller's own
	}
}

// say hands the client a step, when it next asks.
func (c *conversation) say(ctx context.Context, s said) bool {
	select {
	case c.steps <- s:
		return true
	case <-ctx.Done():
		return false
	}
}

// hear is the client's answer to the step said last.
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

// loginID is a new login's handle.
func loginID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("daemon: name a login: %w", err)
	}
	return hex.EncodeToString(b), nil
}
