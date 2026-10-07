package daemon

import (
	"context"
	"errors"
	"sync"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Start attaches to every daemon stream and pumps each onto its channel,
// blocking until ctx is canceled or Stop is called. It starts no sync loop: the
// daemon syncs regardless, so "start" means "attach".
//
// It survives the daemon restarting: losing the streams is a reconnect, not an
// ending (see hold and Attached). The channels close once, when Start returns.
func (r *Remote) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	switch r.arm(cancel) {
	case armStopped:
		r.closeStreams()
		return nil
	case armReattach:
		// The channels closed when the first Start returned.
		return errors.New("daemon: Start called twice; build a new Remote to re-attach")
	case armOK:
	}
	defer r.closeStreams()
	return r.hold(ctx)
}

// firstAttachTries bounds attempts before deciding there is no daemon (about two
// seconds with reattachDelays): long enough to cover a restart racing startup.
const firstAttachTries = 4

// reattachDelays backs off to five seconds and stays there.
var reattachDelays = []time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
}

// reattachProbeTimeout bounds one readiness probe.
const reattachProbeTimeout = time.Second

func reattachDelay(attempt int) time.Duration {
	return reattachDelays[min(attempt, len(reattachDelays)-1)]
}

// hold stays attached for as long as this client is meant to be, reattaching when
// the daemon goes away. Each attempt gets its own context because the first
// stream to end cancels the rest.
//
// Only a client that has never reached a daemon may give up, after firstAttachTries.
// This must be a flag, not the attempt count: a count spent by ordinary restarts
// once left long-lived clients permanently stream-less. And it is set when a daemon
// answers, before the streams open, not when an attachment ends cleanly: one ended by
// a crash ends in an error, and a client attached for hours then gave up two seconds
// into the restart.
func (r *Remote) hold(ctx context.Context) error {
	everAttached := false
	attempt := 0
	for {
		if !everAttached && r.daemonAnswers(ctx) {
			everAttached = true
		}
		attemptCtx, endAttempt := context.WithCancel(ctx)
		began := time.Now()
		err := r.attach(attemptCtx, endAttempt)
		endAttempt()
		if r.finished(ctx) {
			return nil
		}
		// A first subscribe can fail because the daemon that answered Attach's
		// probe is restarting, so it is retried unless nothing answers at all.
		if !everAttached && err != nil && !r.daemonAnswers(ctx) {
			if attempt >= firstAttachTries-1 {
				return err
			}
			select {
			case <-time.After(reattachDelay(attempt)):
			case <-ctx.Done():
				return nil
			}
			if r.finished(ctx) {
				return nil
			}
			attempt++
			continue
		}
		everAttached = true
		// A loss after a steady attachment starts its backoff over; one that ended at
		// once keeps climbing. A daemon whose sync failed for good still answers Status
		// but ends every stream, and resetting there reconnected every 250ms forever.
		if time.Since(began) >= steadyAttach {
			attempt = 0
		} else {
			attempt++
		}
		r.say(false)
		if !r.waitToRetry(ctx, attempt) {
			return nil
		}
		r.say(true)
	}
}

// steadyAttach is how long an attachment must last for its loss to count as new
// rather than as the same outage continuing: the longest backoff step.
var steadyAttach = reattachDelays[len(reattachDelays)-1]

// daemonAnswers probes the socket with a unary Status call. A stream opening
// proves nothing: connect dials lazily, on the first Receive.
func (r *Remote) daemonAnswers(ctx context.Context) bool {
	probe, cancel := context.WithTimeout(ctx, reattachProbeTimeout)
	defer cancel()
	_, err := r.c.Status(probe, connect.NewRequest(&v1.StatusRequest{}))
	return err == nil
}

// waitToRetry backs off until the socket answers again, so a long outage is one
// reconnect rather than many. It reports false when the client is finished.
func (r *Remote) waitToRetry(ctx context.Context, attempt int) bool {
	for {
		select {
		case <-time.After(reattachDelay(attempt)):
		case <-ctx.Done():
			return false
		}
		if r.finished(ctx) {
			return false
		}
		if r.daemonAnswers(ctx) {
			return true
		}
		attempt++
	}
}

// say publishes an attachment change without blocking: a UI that stopped reading
// must not wedge the reconnect.
func (r *Remote) say(attached bool) {
	select {
	case r.attached.ch <- attached:
	default:
	}
}

// Attached reports losing the daemon (false) and getting it back (true); see
// api.Backend. true is sent just before re-subscribing, so a message in that
// window reaches neither the catch-up read nor the stream; the unread stream
// still marks its room.
func (r *Remote) Attached() <-chan bool { return r.attached.ch }

// configPump is the configuration's stream, each change decoded as the file is.
func (r *Remote) configPump(ctx context.Context) error {
	return pumpStream(ctx, "config", r.configs, r.c.ConfigChanged, &v1.ConfigChangedRequest{},
		func(f *v1.ConfigChangedResponse) (config.Snapshot, bool) {
			cfg, err := config.Decode(f.GetConfig())
			return config.Snapshot{Config: cfg, Revision: f.GetRevision()}, err == nil
		})
}

// attach runs one pump per stream until they have all ended. The first to end
// detaches the rest: they share one connection, so one ending means this client
// is no longer served. Channels are not closed here.
func (r *Remote) attach(ctx context.Context, detach context.CancelFunc) error {
	pumps := []func(context.Context) error{
		func(ctx context.Context) error {
			return pumpStream(ctx, "messages", r.msgs, r.c.Messages, &v1.MessagesRequest{},
				func(f *v1.MessagesResponse) (domain.Message, bool) {
					return pc.ProtoToMessage(f.GetMessage()), f.GetMessage() != nil
				})
		},
		func(ctx context.Context) error {
			return pumpStream(ctx, "unread", r.unread, r.c.UnreadStream, &v1.UnreadStreamRequest{},
				func(f *v1.UnreadStreamResponse) (domain.Unread, bool) {
					return pc.ProtoToUnread(f.GetUnread()), f.GetUnread() != nil
				})
		},
		func(ctx context.Context) error {
			return pumpStream(ctx, "reactions", r.reactions, r.c.Reactions, &v1.ReactionsRequest{},
				func(f *v1.ReactionsResponse) (domain.ReactionUpdate, bool) {
					return pc.ProtoToReactionUpdate(f.GetUpdate()), f.GetUpdate() != nil
				})
		},
		func(ctx context.Context) error {
			// Empty frames are delivered: the stream carries the whole set, so empty
			// means the last invitation was answered.
			return pumpStream(ctx, "invites", r.invites, r.c.Invites, &v1.InvitesRequest{},
				func(f *v1.InvitesResponse) ([]domain.Room, bool) {
					return pc.ProtoToRooms(f.GetInvites()), true
				})
		},
		func(ctx context.Context) error {
			// Unknown kinds are dropped (see ProtoToVerification).
			return pumpStream(ctx, "verifications", r.verifications, r.c.Verifications, &v1.VerificationsRequest{},
				func(f *v1.VerificationsResponse) (domain.Verification, bool) {
					return pc.ProtoToVerification(f.GetVerification())
				})
		},
		func(ctx context.Context) error {
			return pumpStream(ctx, "activity", r.activity, r.c.ActivityStream, &v1.ActivityStreamRequest{},
				func(f *v1.ActivityStreamResponse) (domain.Activity, bool) {
					return pc.ProtoToActivity(f.GetActivity()), f.GetActivity() != nil
				})
		},
		func(ctx context.Context) error {
			return pumpStream(ctx, "rooms", r.rooms, r.c.RoomsChanged, &v1.RoomsChangedRequest{},
				func(*v1.RoomsChangedResponse) (struct{}, bool) { return struct{}{}, true })
		},
		func(ctx context.Context) error {
			return pumpStream(ctx, "follows", r.follows, r.c.FollowStream, &v1.FollowStreamRequest{},
				func(f *v1.FollowStreamResponse) (string, bool) {
					return f.GetUri(), f.GetUri() != ""
				})
		},
	}
	pumps = append(pumps, r.configPump)
	errs := make([]error, len(pumps))
	var wg sync.WaitGroup
	for i, pump := range pumps {
		wg.Go(func() {
			defer detach()
			errs[i] = pump(ctx)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// pumpStream forwards one server stream onto out until it ends or ctx is
// canceled. convert returns false for a frame to drop. Errors caused by our own
// cancellation are not failures.
func pumpStream[Req, F, T any](
	ctx context.Context,
	name string,
	out *stream[T],
	open func(context.Context, *connect.Request[Req]) (*connect.ServerStreamForClient[F], error),
	msg *Req,
	convert func(*F) (T, bool),
) error {
	s, err := open(ctx, connect.NewRequest(msg))
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return callErr("subscribe to "+name, err)
	}
	defer func() { _ = s.Close() }() // the stream's own error (below) is the report

	for s.Receive() {
		v, ok := convert(s.Msg())
		if !ok {
			continue
		}
		select {
		case out.ch <- v:
		case <-ctx.Done():
			return nil
		}
	}
	if err := s.Err(); err != nil && ctx.Err() == nil {
		return callErr(name+" stream", err)
	}
	return nil
}

// Stop detaches from the daemon, unblocking Start. Safe before Start, more than
// once, and concurrently.
func (r *Remote) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	if r.cancel != nil {
		r.cancel()
	}
	if r.leaveSeat != nil {
		r.leaveSeat()
	}
}

func (r *Remote) Messages() <-chan domain.Message           { return r.msgs.ch }
func (r *Remote) Unread() <-chan domain.Unread              { return r.unread.ch }
func (r *Remote) Activity() <-chan domain.Activity          { return r.activity.ch }
func (r *Remote) Reactions() <-chan domain.ReactionUpdate   { return r.reactions.ch }
func (r *Remote) Invites() <-chan []domain.Room             { return r.invites.ch }
func (r *Remote) Verifications() <-chan domain.Verification { return r.verifications.ch }

// Follows streams matrix URIs handed over by `kith --open`.
func (r *Remote) Follows() <-chan string { return r.follows.ch }

// ConfigChanges is the configuration after each change, however made.
func (r *Remote) ConfigChanges() <-chan config.Snapshot { return r.configs.ch }

// RoomsChanged says a network rewrote its rooms: read the list again.
func (r *Remote) RoomsChanged() <-chan struct{} { return r.rooms.ch }

type armState int

const (
	armOK armState = iota
	armStopped
	armReattach
)

// finished reports whether this client is leaving (Stop, or ctx ended), as
// opposed to the daemon having gone away.
func (r *Remote) finished(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// arm records the cancel func Stop will call, reporting armStopped if Stop
// already ran (so an early Stop is not lost) and armReattach on a second Start.
func (r *Remote) arm(cancel context.CancelFunc) armState {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.stopped:
		return armStopped
	case r.started:
		return armReattach
	default:
		r.started = true
		r.cancel = cancel
		return armOK
	}
}

// closeStreams ends every daemon→UI channel, attached and follows included:
// a listener parked on a channel nothing will close again leaks forever.
func (r *Remote) closeStreams() {
	r.msgs.done()
	r.unread.done()
	r.reactions.done()
	r.invites.done()
	r.verifications.done()
	r.activity.done()
	r.attached.done()
	r.follows.done()
	r.rooms.done()
	r.configs.done()
}
