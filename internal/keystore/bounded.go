package keystore

import (
	"errors"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// go-keyring's calls take no context and no timeout, and a keyring that never answers
// blocks them for good: a Secret Service D-Bus is still trying to start, or a locked
// collection waiting on a prompt nothing shows. The daemon reads its keys on its way
// to serving, so every read here, and the write that makes a missing key, goes through
// bounded: a keyring that does not answer is said, never waited on.

// Wait is how long one keyring call is waited for. A running Secret Service
// answers in milliseconds, and one D-Bus starts on first use in about a second, a few
// on a cold boot; longer is a keyring that will not answer without someone. Five
// seconds lets a slow start through and still tells a client, which waits a minute
// for the daemon, why it is not ready well within that.
const Wait = 5 * time.Second

// ErrKeyringNotAnswering is a keyring that did not answer within Wait. It is
// not "no key": a key is never made for it.
var ErrKeyringNotAnswering = errors.New("session: the keyring is not answering (is it locked?)")

// call is a keyring call still running, or finished and not yet taken.
type call struct {
	done  chan struct{}
	value string
	err   error
}

// calls is every keyring call that outlived its wait, by what it does to which entry.
var calls = struct {
	sync.Mutex
	running map[string]*call
}{running: map[string]*call{}}

// bounded runs op, a keyring call, waiting at most wait. One that has not answered by
// then is ErrKeyringNotAnswering and keeps running: the next bounded call with the same
// name waits on it instead of asking again, so a keyring that never answers holds one
// goroutine per name, not one per try, and takes its answer however late it comes. A
// late failure is not taken (the keyring may answer now); a late value is.
func bounded(name string, wait time.Duration, op func() (string, error)) (string, error) {
	calls.Lock()
	c, ok := calls.running[name]
	if ok && failed(c) {
		ok = false
	}
	if !ok {
		c = &call{done: make(chan struct{})}
		calls.running[name] = c
		go func() {
			value, err := op()
			calls.Lock()
			c.value, c.err = value, err
			calls.Unlock()
			close(c.done)
		}()
	}
	calls.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-c.done:
		calls.Lock()
		defer calls.Unlock()
		if calls.running[name] == c {
			delete(calls.running, name)
		}
		return c.value, c.err
	case <-timer.C:
		return "", ErrKeyringNotAnswering
	}
}

// failed reports whether c finished with an error. Call with calls locked.
func failed(c *call) bool {
	select {
	case <-c.done:
		return c.err != nil
	default:
		return false
	}
}

// Get reads a keyring entry, waiting at most Wait (bounded).
func Get(service, acct string) (string, error) {
	return bounded("get\x00"+service+"\x00"+acct, Wait, func() (string, error) {
		return keyring.Get(service, acct)
	})
}

// Set writes value to a keyring entry, waiting at most Wait (bounded), and returns the
// value kept: a write still waiting from an earlier call is joined, and its value is the
// one kept.
func Set(service, acct, value string) (string, error) {
	return bounded("set\x00"+service+"\x00"+acct, Wait, func() (string, error) {
		return value, keyring.Set(service, acct, value)
	})
}
