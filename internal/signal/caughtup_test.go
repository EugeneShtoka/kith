package signal_test

import (
	"fmt"
	"strings"
	"testing"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/signal"
)

// What the helper can say after an account first connects. signalmeow says the queue is
// empty once its signed-in socket is up, but connected only once both are, so the two
// come in either order, and only one socket may drop and come back.
type queueStep int

const (
	queueEmpty queueStep = iota
	socketDropped
	socketsUp
	loggedOut
)

func (s queueStep) String() string {
	return [...]string{"queue empty", "dropped", "up", "logged out"}[s]
}

func (s queueStep) event() *signalv1.EventsResponse {
	if s == queueEmpty {
		return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_QueueEmpty{QueueEmpty: &signalv1.QueueEmpty{}}}
	}
	state := map[queueStep]signalv1.Connection_State{
		socketDropped: signalv1.Connection_STATE_DISCONNECTED,
		socketsUp:     signalv1.Connection_STATE_CONNECTED,
		loggedOut:     signalv1.Connection_STATE_LOGGED_OUT,
	}[s]
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Connection{Connection: &signalv1.Connection{State: state}}}
}

// Every sequence of up to three steps after the account connects: it ends caught up
// exactly when it is online and its queue was said empty since it was last logged out,
// whichever came first.
func TestSignalIsCaughtUpWhicheverSocketSaysSoFirst(t *testing.T) {
	var walk func(prefix []queueStep, depth int)
	walk = func(prefix []queueStep, depth int) {
		t.Run(fmt.Sprint(prefix), func(t *testing.T) { checkCaughtUp(t, prefix) })
		if depth == 0 {
			return
		}
		for s := queueEmpty; s <= loggedOut; s++ {
			walk(append(append([]queueStep(nil), prefix...), s), depth-1)
		}
	}
	walk(nil, 3)
}

func checkCaughtUp(t *testing.T, steps []queueStep) {
	events := make([]*signalv1.EventsResponse, 0, len(steps))
	online, drained := true, false // the fake helper says connected before playing
	for _, s := range steps {
		events = append(events, s.event())
		switch s {
		case queueEmpty:
			drained = true
		case socketDropped:
			online = false
		case socketsUp:
			online = true
		case loggedOut:
			online, drained = false, false
		}
	}
	want := online && drained
	r := newRigPlaying(t, "ok", me, me, events)
	if len(steps) == 0 {
		r.until("linked", r.phase(signal.Linked, ""))
	}
	// One queue carries the connect and the steps, so the last step's ack is after all.
	r.until("every step played", func() bool { return len(r.acked()) >= len(steps) })
	if got, trail := r.caughtUp(); got != want {
		t.Errorf("caught up %v, want %v; said %s", got, want, trail)
	}
}

// caughtUp reads the trail as kithd's status does: caught up is heard only while online,
// and lost whenever the account is not.
func (r *rig) caughtUp() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var online, caught bool
	var said []string
	for _, x := range r.trail {
		switch v := x.(type) {
		case domain.AccountStatus:
			online = v.Phase == signal.Linked
			caught = caught && online
			said = append(said, fmt.Sprint(v.Phase))
		case domain.Heard:
			if v.Kind == domain.HeardCaughtUp {
				caught = caught || online
				said = append(said, "caught up")
			}
		}
	}
	return caught, strings.Join(said, ", ")
}
