package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// TakeSeat asks for the seat and keeps it until ctx ends (api.Seat), answering once
// the daemon grants or refuses it (ErrSeatTaken naming the holder). After a daemon
// restart the seat is asked for again without force, and lost if taken meanwhile.
func (r *Remote) TakeSeat(ctx context.Context, force bool, where domain.SeatHolder) error {
	// Held until Stop, after the window saved its drafts; ctx ends earlier on a signal,
	// before the last write (tui.Run).
	held, leave := context.WithCancel(context.WithoutCancel(ctx))
	r.mu.Lock()
	r.leaveSeat = leave
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		leave()
	}
	answered := make(chan error, 1)
	go r.keepSeat(held, force, where, answered)
	select {
	case err := <-answered:
		return err
	case <-ctx.Done():
		leave()
		return callErr("take the seat", ctx.Err())
	case <-held.Done():
		return callErr("take the seat", held.Err())
	}
}

// SeatLost is closed when another window takes the seat.
func (r *Remote) SeatLost() <-chan struct{} { return r.lost }

// keepSeat holds the seat stream open, answering the first grant or refusal and
// reporting a later loss on SeatLost.
func (r *Remote) keepSeat(ctx context.Context, force bool, where domain.SeatHolder, answered chan<- error) {
	first := true
	answer := func(err error) {
		if first {
			first = false
			answered <- err
		}
	}
	for attempt := 0; ; {
		began := time.Now()
		err := r.sit(ctx, force && first, where, answer)
		switch {
		case errors.Is(err, errSteppedAside):
			return
		case r.finished(ctx):
			answer(callErr("take the seat", context.Canceled))
			return
		case errors.Is(err, api.ErrSeatTaken):
			answer(err)
			r.lose()
			return
		case first:
			answer(err) // no daemon to sit at
			return
		}
		// The daemon went away and forgot the seat: ask again when it is back, with the
		// backoff restarting after a steady sitting, as in hold.
		if time.Since(began) >= steadyAttach {
			attempt = 0
		} else {
			attempt++
		}
		if !r.waitToRetry(ctx, attempt) {
			return
		}
	}
}

// errSteppedAside is another window taking the seat by force.
var errSteppedAside = errors.New("daemon: another window took the seat")

// sit is one Seat stream, until it ends or another window takes the seat.
func (r *Remote) sit(ctx context.Context, force bool, where domain.SeatHolder, answer func(error)) error {
	st, err := r.c.Seat(ctx, connect.NewRequest(&v1.SeatRequest{
		Client: r.client, Force: force, Where: pc.SeatHolderToProto(where),
	}))
	if err != nil {
		return callErr("take the seat", err)
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		switch event := st.Msg().GetEvent().(type) {
		case *v1.SeatResponse_Granted:
			r.seated.Store(true)
			answer(nil)
		case *v1.SeatResponse_Taken:
			return fmt.Errorf("%w: %s", api.ErrSeatTaken, pc.ProtoToSeatHolder(event.Taken))
		case *v1.SeatResponse_StepAside:
			// The other window waits until this one has saved and closed: keep the stream until
			// Stop.
			r.lose()
			<-ctx.Done()
			return errSteppedAside
		}
	}
	if err := st.Err(); err != nil && ctx.Err() == nil {
		return callErr("seat stream", err)
	}
	return errors.New("daemon: the seat stream ended")
}

// lose reports the seat gone, once. Writes stay named, so the daemon refuses them
// once another window sits.
func (r *Remote) lose() {
	r.loseOnce.Do(func() { close(r.lost) })
}
