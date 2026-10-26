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

// Start attaches to every daemon stream and pumps each onto its channel, blocking
// until ctx ends or Stop. The daemon syncs regardless, so "start" means "attach". A
// daemon restart is a reconnect, not an ending; the channels close once, on return.
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

// firstAttachTries bounds attempts before deciding there is no daemon (about 2s with
// reattachDelays), covering a restart racing startup.
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

// hold stays attached, reattaching when the daemon goes away; each attempt gets its
// own context because the first stream to end cancels the rest.
//
// Only a client that never reached a daemon may give up. That is a flag set when a
// daemon answers, not an attempt count or a clean end: either once left long-lived
// clients stream-less after an ordinary restart or crash.
func (r *Remote) hold(ctx context.Context) error {
	everAttached := false
	attempt := 0
	for {
		if !everAttached && r.daemonAnswers(ctx) {
			everAttached = true
		}
		attemptCtx, endAttempt := context.WithCancel(ctx)
		current := r.begin(endAttempt)
		began := time.Now()
		err := r.attach(attemptCtx, endAttempt, current.ready)
		endAttempt()
		close(current.ended)
		if r.finished(ctx) {
			return nil
		}
		if r.switched(current) {
			continue // a profile switch, not a loss: open the streams in the new one
		}
		// A first subscribe can fail because the daemon is restarting, so it is retried
		// unless nothing answers at all.
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
		// A loss after a steady attachment restarts the backoff; a quick one keeps climbing.
		// A daemon whose sync failed for good still answers Status but ends every stream,
		// and resetting there reconnected every 250ms forever.
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

// streamAttempt is one attachment of the streams, every one opened in profile: end
// ends it, ready closes once the daemon has subscribed every stream, ended once it has
// ended.
type streamAttempt struct {
	profile      string
	end          context.CancelFunc
	ready, ended chan struct{}
}

// streamOpenedKey carries the call a stream makes once the daemon has subscribed it.
type streamOpenedKey struct{}

// begin records a new attachment in the profile named now, and tells whoever waits for
// the next one. A stream opens in the profile named when it opens; one renamed since
// begin is ended by the switch that renamed it, since its attachment is labeled with
// another profile.
func (r *Remote) begin(end context.CancelFunc) streamAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt = streamAttempt{profile: r.profileName(), end: end, ready: make(chan struct{}), ended: make(chan struct{})}
	if r.begun != nil {
		close(r.begun)
	}
	r.begun = make(chan struct{})
	return r.attempt
}

// switched reports whether attempt ended for a profile switch, or opened in a profile
// no longer named: either way the daemon is there, and the next opens at once.
func (r *Remote) switched(attempt streamAttempt) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.switchedOff == attempt.ended || attempt.profile != r.profileName()
}

// SwitchProfile runs every request from now on in the named [[profile]] and
// re-subscribes every stream in it. It returns once streams opened in another profile
// have ended, what the narrowed ones (unread, invitations) still held is dropped, and
// the daemon has subscribed every stream in the new one, so a read the caller makes
// afterwards misses nothing; or once the streams stop for good. Before Start it only
// names the profile.
func (r *Remote) SwitchProfile(name string) {
	// Named under the lock begin takes, so an attachment is either ended here or opens
	// in the new profile.
	r.mu.Lock()
	r.UseProfile(name)
	current := r.attempt
	r.mu.Unlock()
	if current.end == nil {
		return
	}
	if current.profile != name {
		r.mu.Lock()
		r.switchedOff = current.ended
		r.mu.Unlock()
		current.end()
		<-current.ended
		drain(r.unread.ch)
		drain(r.invites.ch)
	}
	// Waits for an attachment in the profile named now: a later switch overtaking this
	// one is what it waits for too.
	for {
		r.mu.Lock()
		now, begun, named := r.attempt, r.begun, r.profileName()
		r.mu.Unlock()
		if now.profile == named {
			select {
			case <-now.ready:
				select {
				case <-now.ended: // ready once, gone since: wait for the next
				default:
					return
				}
			case <-now.ended: // before it was ready: wait for the next
			}
		}
		select {
		case <-begun:
		case <-r.over:
			return
		}
	}
}

// drain drops what a channel holds, without waiting for more; a closed one (Stop) holds
// nothing.
func drain[T any](ch chan T) {
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
		default:
			return
		}
	}
}

// steadyAttach is how long an attachment must last for its loss to count as a new
// outage: the longest backoff step.
var steadyAttach = reattachDelays[len(reattachDelays)-1]

// daemonAnswers probes with a unary Status call; a stream opening proves nothing,
// since connect dials lazily on the first Receive.
func (r *Remote) daemonAnswers(ctx context.Context) bool {
	probe, cancel := context.WithTimeout(ctx, reattachProbeTimeout)
	defer cancel()
	_, err := r.c.Status(probe, connect.NewRequest(&v1.StatusRequest{}))
	return err == nil
}

// waitToRetry backs off until the socket answers again; false when the client is
// finished.
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

// Attached reports losing the daemon (false) and getting it back (true). true is sent
// just before re-subscribing, so a message in that window reaches neither the
// catch-up read nor the stream; the unread stream still marks its room.
func (r *Remote) Attached() <-chan bool { return r.attached.ch }

// configPump streams the configuration, each change decoded as the file is.
func (r *Remote) configPump(ctx context.Context) error {
	return pumpStream(ctx, "config", r.configs, r.c.ConfigChanged, &v1.ConfigChangedRequest{},
		func(f *v1.ConfigChangedResponse) (config.Snapshot, bool) {
			cfg, err := config.Decode(f.GetConfig())
			return config.Snapshot{Config: cfg, Revision: f.GetRevision()}, err == nil
		})
}

// attach runs one pump per stream until all end. The first to end detaches the rest:
// they share one connection. Channels are not closed here.
func (r *Remote) attach(ctx context.Context, detach context.CancelFunc, ready chan struct{}) error {
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
			// Empty frames are delivered: the stream carries the whole set.
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
	return runPumps(ctx, pumps, detach, ready)
}

// runPumps runs every pump until all end, the first to end detaching the rest; ready
// closes once the daemon has subscribed each pump's stream, or the pump has ended.
func runPumps(ctx context.Context, pumps []func(context.Context) error, detach context.CancelFunc, ready chan struct{}) error {
	errs := make([]error, len(pumps))
	var wg, opening sync.WaitGroup
	opening.Add(len(pumps))
	go func() { opening.Wait(); close(ready) }()
	for i, pump := range pumps {
		opened := sync.OnceFunc(opening.Done)
		wg.Go(func() {
			defer detach()
			defer opened()
			errs[i] = pump(context.WithValue(ctx, streamOpenedKey{}, opened))
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// pumpStream forwards one server stream onto out until it ends or ctx is canceled;
// convert returns false to drop a frame. Our own cancellation is not a failure.
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
	if opened, ok := ctx.Value(streamOpenedKey{}).(func()); ok {
		s.ResponseHeader() // the daemon answers once it has subscribed the stream
		opened()
	}

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

// Stop detaches from the daemon, unblocking Start. Safe before Start, repeatedly and
// concurrently.
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

// finished reports whether this client is leaving (Stop or ctx), as opposed to the
// daemon having gone away.
func (r *Remote) finished(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// arm records the cancel func Stop will call; armStopped if Stop already ran,
// armReattach on a second Start.
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

// closeStreams ends every daemon→UI channel: a listener parked on one nothing will
// close leaks forever.
func (r *Remote) closeStreams() {
	r.overOnce.Do(func() { close(r.over) })
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
