// Package route serves the chat networks as one: a call about a room goes to the
// adapter for that room's network, a list is every adapter's, and a stream carries
// every adapter's events. Matrix is optional, like every network; it alone has
// spaces, membership, threads, device verification and key backup.
package route

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Adapter is one chat network: everything the daemon serves that reaches a network.
// What the daemon answers from its cache whatever the network is internal/local's.
type Adapter interface {
	// Start connects and runs until ctx ends or Stop is called. It blocks.
	Start(ctx context.Context) error
	Stop()
	Messages() <-chan domain.Message
	Activity() <-chan domain.Activity
	Unread() <-chan domain.Unread
	Reactions() <-chan domain.ReactionUpdate

	// Me is every ID on the network that is this person ("" before a session).
	Me() []string
	// RewindSync refills an emptied cache from the start.
	RewindSync(ctx context.Context) error

	Rooms(ctx context.Context) ([]domain.Room, error)
	RefreshRooms(ctx context.Context) ([]domain.Room, error)
	CachedUnread(ctx context.Context) ([]domain.Unread, error)
	MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error
	MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error)
	MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error
	StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, starred bool) error
	MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error
	CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error)

	MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error)
	Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error)
	FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error)
	Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error
	Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error
	SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error
	SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error
	SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error
	LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error)

	Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error)
	RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error)
	MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error)
	DirectCandidates(ctx context.Context, limit int) ([]domain.Member, error)
	RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error)
}

// Matrix is the Matrix adapter: an Adapter, and the roles only Matrix has.
type Matrix interface {
	Adapter
	api.Spaces
	api.Membership
	api.Threads
	api.Verification
	api.Keys
	// LoggedIn reports whether it has a session: until then the router treats it as
	// off. It turns true once, before anything reaches it.
	LoggedIn() bool
	// ThreadParticipant reports whether we sent a thread's root or any reply in it.
	ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool
}

// Router is every network served as one. Build it with New.
type Router struct {
	// matrix is nil when Matrix is not configured.
	matrix Matrix
	// others are the networks besides Matrix, by the network their IDs name.
	others map[domain.Protocol]Adapter
	// all is Matrix (when configured) then the others in a fixed order: every adapter
	// started, stopped and streamed from, logged in or not.
	all []Adapter

	// noInvites and noVerifications stand in for Matrix's streams when it is not
	// configured: nothing is sent on them, and Stop closes them.
	noInvites       chan []domain.Room
	noVerifications chan domain.Verification

	// done ends the merged streams' pumps when the router stops, so a reader that
	// went away cannot leave one blocked on a send.
	done     chan struct{}
	stopOnce sync.Once

	streamsOnce sync.Once
	messages    <-chan domain.Message
	activity    <-chan domain.Activity
	unread      <-chan domain.Unread
	reactions   <-chan domain.ReactionUpdate
}

// New routes Matrix (nil when not configured) and others (keyed by network; Matrix
// may not be among them). Some network is required.
func New(matrix Matrix, others map[domain.Protocol]Adapter) (*Router, error) {
	if _, twice := others[domain.ProtocolMatrix]; twice {
		return nil, errors.New("route: Matrix is given twice")
	}
	if matrix == nil && len(others) == 0 {
		return nil, errors.New("route: no network")
	}
	r := &Router{
		matrix: matrix, others: others, done: make(chan struct{}),
		noInvites: make(chan []domain.Room), noVerifications: make(chan domain.Verification),
	}
	if matrix != nil {
		r.all = append(r.all, matrix)
	}
	networks := make([]domain.Protocol, 0, len(others))
	for network := range others {
		networks = append(networks, network)
	}
	slices.Sort(networks)
	for _, network := range networks {
		r.all = append(r.all, others[network])
	}
	return r, nil
}

// adapterFor is the adapter for a room, event or person's network.
func (r *Router) adapterFor(id string) (Adapter, error) {
	network := domain.NetworkOf(id)
	if network == domain.ProtocolMatrix {
		if !r.matrixOn() {
			return nil, fmt.Errorf("%w (%s)", errMatrixOff, id)
		}
		return r.matrix, nil
	}
	if a, ok := r.others[network]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("%w: %s (%s)", api.ErrNetworkOff, network, id)
}

// matrixRoom refuses a Matrix-only call about another network's room.
func matrixRoom(roomID domain.RoomID) error {
	if network := domain.NetworkOf(string(roomID)); network != domain.ProtocolMatrix {
		return fmt.Errorf("%w: %s rooms (%s)", api.ErrNotOnNetwork, network, roomID)
	}
	return nil
}

// onRoom runs call on the adapter for roomID.
func onRoom[T any](r *Router, roomID domain.RoomID, call func(Adapter) (T, error)) (T, error) {
	a, err := r.adapterFor(string(roomID))
	if err != nil {
		var zero T
		return zero, err
	}
	return call(a)
}

// doOnRoom is onRoom for a call that returns only an error.
func doOnRoom(r *Router, roomID domain.RoomID, call func(Adapter) error) error {
	_, err := onRoom(r, roomID, func(a Adapter) (struct{}, error) { return struct{}{}, call(a) })
	return err
}

// live is every adapter that may be asked: all but a Matrix with no session.
func (r *Router) live() []Adapter {
	if r.matrix == nil || r.matrix.LoggedIn() {
		return r.all
	}
	return r.all[1:]
}

// gather is every live adapter's answer, concatenated in order. One adapter failing
// fails the whole read: a partial list would read as complete.
func gather[T any](r *Router, read func(Adapter) ([]T, error)) ([]T, error) {
	live := r.live()
	if len(live) == 1 {
		return read(live[0])
	}
	var out []T
	for _, a := range live {
		got, err := read(a)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// byNetwork splits rooms by the adapter that serves them, keeping their order; rooms
// on a network with no adapter are returned apart.
func (r *Router) byNetwork(rooms []domain.RoomID) (map[Adapter][]domain.RoomID, []domain.RoomID) {
	parts := make(map[Adapter][]domain.RoomID)
	var off []domain.RoomID
	for _, room := range rooms {
		a, err := r.adapterFor(string(room))
		if err != nil {
			off = append(off, room)
			continue
		}
		parts[a] = append(parts[a], room)
	}
	return parts, off
}

// Start runs every adapter until ctx ends, and returns when all have, with what
// each ended with.
func (r *Router) Start(ctx context.Context) error {
	if len(r.all) == 1 {
		return r.all[0].Start(ctx)
	}
	errs := make([]error, len(r.all))
	var wg sync.WaitGroup
	for i, a := range r.all {
		wg.Go(func() { errs[i] = a.Start(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Stop stops every adapter, then the merged streams.
func (r *Router) Stop() {
	r.stopOnce.Do(func() {
		for _, a := range r.all {
			a.Stop()
		}
		close(r.done)
		close(r.noInvites)
		close(r.noVerifications)
	})
}

// Me is every ID on every network that is this person.
func (r *Router) Me() []string {
	var me []string
	for _, a := range r.live() {
		for _, id := range a.Me() {
			if !slices.Contains(me, id) {
				me = append(me, id)
			}
		}
	}
	return me
}

// Selves is Me as the API serves it (api.Identity).
func (r *Router) Selves(context.Context) ([]string, error) { return r.Me(), nil }

// RewindSync has every network refill the emptied cache.
func (r *Router) RewindSync(ctx context.Context) error {
	errs := make([]error, 0, len(r.all))
	for _, a := range r.all {
		errs = append(errs, a.RewindSync(ctx))
	}
	return errors.Join(errs...)
}

// Rooms is every network's cached rooms.
func (r *Router) Rooms(ctx context.Context) ([]domain.Room, error) {
	return gather(r, func(a Adapter) ([]domain.Room, error) { return a.Rooms(ctx) })
}

// RefreshRooms refreshes every network's rooms.
func (r *Router) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	return gather(r, func(a Adapter) ([]domain.Room, error) { return a.RefreshRooms(ctx) })
}

// CachedUnread is every network's unread state.
func (r *Router) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	return gather(r, func(a Adapter) ([]domain.Unread, error) { return a.CachedUnread(ctx) })
}

// DirectCandidates is every network's people to message, at most limit.
func (r *Router) DirectCandidates(ctx context.Context, limit int) ([]domain.Member, error) {
	out, err := gather(r, func(a Adapter) ([]domain.Member, error) { return a.DirectCandidates(ctx, limit) })
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkRoomsRead marks each room read through its network. One network refusing
// outright counts its rooms as failed; only when nothing could be attempted at all
// is it an error.
func (r *Router) MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error) {
	if len(r.all) == 1 {
		if _, off := r.byNetwork(roomIDs); len(off) == 0 {
			return r.all[0].MarkRoomsRead(ctx, roomIDs, private)
		}
	}
	parts, off := r.byNetwork(roomIDs)
	var total domain.ReadResult
	fail := func(n int, why string) {
		total.Failed += n
		if total.FirstError == "" {
			total.FirstError = why
		}
	}
	if len(off) > 0 {
		fail(len(off), fmt.Errorf("%w: %s", api.ErrNetworkOff, off[0]).Error())
	}
	var refused error
	attempted := false
	for _, a := range r.all {
		rooms := parts[a]
		if len(rooms) == 0 {
			continue
		}
		got, err := a.MarkRoomsRead(ctx, rooms, private)
		if err != nil {
			refused = cmpOr(refused, err)
			fail(len(rooms), err.Error())
			continue
		}
		attempted = true
		total.Marked += got.Marked
		total.Skipped += got.Skipped
		if got.Failed > 0 {
			fail(got.Failed, got.FirstError)
		}
	}
	// Counting can't tell "nothing was attempted": a network may try every room it
	// has and fail them all.
	if refused != nil && !attempted {
		return domain.ReadResult{}, refused
	}
	return total, nil
}

// cmpOr is the first non-nil error.
func cmpOr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// RoomEncryption asks each room's network; a room on no connected network reports
// as encrypted (the safe direction).
func (r *Router) RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	if len(r.all) == 1 {
		if _, off := r.byNetwork(roomIDs); len(off) == 0 {
			return r.all[0].RoomEncryption(ctx, roomIDs)
		}
	}
	parts, off := r.byNetwork(roomIDs)
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, room := range off {
		out[room] = true
	}
	for _, a := range r.all {
		rooms := parts[a]
		if len(rooms) == 0 {
			continue
		}
		got, err := a.RoomEncryption(ctx, rooms)
		if err != nil {
			return nil, err
		}
		maps.Copy(out, got)
	}
	return out, nil
}
