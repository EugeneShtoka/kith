package gmessages

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// A message is sent by handing it to the phone, which sends it and then pushes it back
// (its echo) under the ID it gave it, carrying the send's temporary ID. kith keeps no
// row of its own for a send: the echo is the message, cached as any other, so there is
// nothing to merge. What the outbox keeps is which sends the phone has, so that a send
// tried again (the send queue retries every failure, with the same TxnID) never hands
// the phone a message it already has: a retry of one the phone echoed is done, and one
// the phone took but has not echoed waits for it.

// outboxSpan is how long the outbox remembers a send; the send queue gives up sooner.
const outboxSpan = 48 * time.Hour

// tmpSpace is the namespace a send's temporary ID is made in from its TxnID.
var tmpSpace = uuid.MustParse("4f0f6d2e-6b8a-4b5e-9a57-6b6974686d73")

// errAwaitingPhone: the phone has the message (or a try of it is under way) and has
// not sent it back yet; it is not handed over again.
var errAwaitingPhone = errors.New("gmessages: the phone has this message and has not sent it yet; it goes when the phone answers")

// The states of a send, by its temporary ID.
type sendState int

const (
	// unsent: the phone does not have it; sending hands it over.
	unsent sendState = iota
	// sending: a try is under way.
	sending
	// handed: the phone took it (or Google holds it for the phone) and has not echoed it.
	handed
	// echoed: the phone sent it back; it is sent.
	echoed
)

// outbox is the sends the phone has or is being handed, by temporary ID.
type outbox struct {
	mu    sync.Mutex
	sends map[string]outgoing
}

// outgoing is one send's state and when it last changed.
type outgoing struct {
	state sendState
	at    time.Time
}

func newOutbox() *outbox { return &outbox{sends: map[string]outgoing{}} }

// tmpFor is a send's temporary ID: the same for every try with txn (after a restart
// too), a fresh one with none.
func tmpFor(txn string) string {
	if txn == "" {
		return uuid.NewString()
	}
	return uuid.NewSHA1(tmpSpace, []byte(txn)).String()
}

func (o *outbox) set(tmp string, state sendState, now time.Time) {
	o.sends[tmp] = outgoing{state: state, at: now}
}

// prune forgets the sends older than outboxSpan.
func (o *outbox) prune(now time.Time) {
	for id, s := range o.sends {
		if now.Sub(s.at) > outboxSpan {
			delete(o.sends, id)
		}
	}
}

// begin claims a try of tmp: true when it is to be handed to the phone; false and nil
// when the phone already sent it; errAwaitingPhone when it has it, or a try is under way.
func (o *outbox) begin(tmp string, now time.Time) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prune(now)
	switch o.sends[tmp].state {
	case echoed:
		return false, nil
	case sending, handed:
		return false, errAwaitingPhone
	case unsent:
	}
	o.set(tmp, sending, now)
	return true, nil
}

// end settles the try begun for tmp with what handing it over returned: nil when the
// phone took it.
func (o *outbox) end(tmp string, resp *gmproto.SendMessageResponse, err error, now time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	echo := o.sends[tmp].state == echoed
	settle := func(state sendState) {
		if !echo {
			o.set(tmp, state, now)
		}
	}
	switch {
	case errors.Is(err, libgm.ErrPhoneNotResponding):
		// Google took it for the phone, which sends it when it is back.
		settle(handed)
		if echo {
			return nil
		}
		return fmt.Errorf("%w (%w)", errAwaitingPhone, err)
	case err != nil:
		settle(unsent)
		if echo {
			return nil
		}
		return fmt.Errorf("gmessages: send: %w", err)
	case resp.GetStatus() != gmproto.SendMessageResponse_SUCCESS:
		settle(unsent)
		if echo {
			return nil
		}
		return fmt.Errorf("gmessages: the phone refused the message (%s)", resp.GetStatus())
	}
	settle(handed)
	return nil
}

// echoed takes a message the phone sent back with its temporary ID: whichever send it
// was is sent. Every one is kept, not only this run's sends: a message in the phone's
// history that still carries it settles a send an earlier run made.
func (o *outbox) echoed(tmp string) {
	if tmp == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.set(tmp, echoed, time.Now())
}
