package mesh

import (
	"bytes"
	"context"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// stream is one end of an in-memory sync stream: closing either end ends both, as a
// connection does.
type stream struct {
	in, out chan *meshv1.SyncFrame
	closed  chan struct{}
	once    *sync.Once
}

func newStream() (stream, stream) {
	ab, ba, closed, once := make(chan *meshv1.SyncFrame, 64), make(chan *meshv1.SyncFrame, 64), make(chan struct{}), &sync.Once{}
	return stream{in: ba, out: ab, closed: closed, once: once}, stream{in: ab, out: ba, closed: closed, once: once}
}

func (s stream) Send(f *meshv1.SyncFrame) error {
	select {
	case s.out <- f:
		return nil
	case <-s.closed:
		return io.ErrClosedPipe
	}
}

func (s stream) Receive() (*meshv1.SyncFrame, error) {
	select {
	case f := <-s.in:
		return f, nil
	case <-s.closed:
		return nil, io.EOF
	}
}

func (s stream) close() { s.once.Do(func() { close(s.closed) }) }

// Two bridges dialing each other at once open two streams; both sides keep the one
// dialed by the lower ID and end the other, and the one kept carries what either
// writes from then on.
func TestBothBridgesDialingAtOnceKeepOneStream(t *testing.T) {
	t.Parallel()
	m := newModel(t, 1)
	a, b := m.r[0], m.r[1]
	m.exchange(0, 1, false) // each knows the other
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type end struct {
		dialer string
		err    error
	}
	ends := make(chan end, 4)
	var running sync.WaitGroup
	run := func(r *modelReplica, peer *modelReplica, dialed bool, s stream, dialer string) {
		running.Go(func() {
			err := r.store.Sync(ctx, r.key, peer.id, dialed, s, m.log)
			s.close() // the connection goes with the session
			ends <- end{dialer, err}
		})
	}
	aDials, bAnswers := newStream()
	bDials, aAnswers := newStream()
	run(a, b, true, aDials, "a")
	run(b, a, false, bAnswers, "a")
	run(b, a, true, bDials, "b")
	run(a, b, false, aAnswers, "b")
	lower := "a"
	if bytes.Compare(b.id[:], a.id[:]) < 0 {
		lower = "b"
	}
	for range 2 {
		select {
		case e := <-ends:
			if e.dialer == lower {
				t.Fatalf("the stream %s dialed (the lower ID) ended: %v", e.dialer, e.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("neither of the two streams ended")
		}
	}
	if !a.store.Connected(b.id) || !b.store.Connected(a.id) {
		t.Fatal("no stream is kept")
	}
	_, id := newDeviceID(t)
	m.must(a.store.ChangeRoster(m.ctx, a.key, []domain.Device{{ID: id, Name: "phone", Role: domain.RoleDevice}}, nil))
	deadline := time.Now().Add(5 * time.Second)
	for {
		members, _, err := b.store.Roster()
		m.must(err)
		if slices.ContainsFunc(members, func(d domain.Device) bool { return d.ID == id }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a write made after the streams settled never crossed the one kept")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	running.Wait()
}
