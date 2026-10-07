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

// Adapter is one chat network. Every adapter runs, streams what it hears, and knows
// who the person is on it; everything else is a capability it has or lacks (below).
// A call about a room asks that room's network for the capability, and a network
// without it refuses with api.ErrNotOnNetwork. What the daemon answers from its cache
// whatever the network is internal/local's.
type Adapter interface {
	// Start connects and runs until ctx ends or Stop is called. It blocks.
	Start(ctx context.Context) error
	Stop()
	Messages() <-chan domain.Message
	Activity() <-chan domain.Activity
	Unread() <-chan domain.Unread
	Reactions() <-chan domain.ReactionUpdate
	// Me is every ID on the network that is this person (none before a session).
	Me() []string
}

// The capabilities an adapter may have.
type (
	// Session is a network that can be there and not usable yet: until LoggedIn turns
	// true (once, before anything reaches it), the router treats it as off.
	Session interface{ LoggedIn() bool }
	// Resync refills an emptied cache from the start.
	Resync interface {
		RewindSync(ctx context.Context) error
	}
	// RoomLister is a network's rooms and their unread state.
	RoomLister interface {
		Rooms(ctx context.Context) ([]domain.Room, error)
		RefreshRooms(ctx context.Context) ([]domain.Room, error)
		CachedUnread(ctx context.Context) ([]domain.Unread, error)
	}
	// Homes is the space a room names as its parent.
	Homes interface {
		CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error)
	}
	// ReadState is read positions and the marked-unread flag.
	ReadState interface {
		MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error
		MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error)
		MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error
	}
	// Leaver is a network whose rooms can be left (or an invitation to one declined).
	Leaver interface {
		LeaveRoom(ctx context.Context, roomID domain.RoomID) error
	}
	// Archiver is a network with an archive of its own (Telegram's Archived folder,
	// WhatsApp's archived chats).
	Archiver interface {
		SetArchived(ctx context.Context, roomID domain.RoomID, archived bool) error
	}
	// Groupings is a network whose people group their chats themselves (Telegram's
	// folders), copied into tags when asked.
	Groupings interface {
		// Groupings is one account's groupings as they are now, and the rooms that
		// account sees (what a copy may change in a tag).
		Groupings(ctx context.Context, account string) (domain.RoomOwner, []domain.Grouping, error)
	}
	// Stars is bookmarks.
	Stars interface {
		StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, starred bool) error
	}
	// SpamReports is spam verdicts.
	SpamReports interface {
		MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error
	}
	// History is a room's past: pages, one message, a message's versions.
	History interface {
		Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error)
		FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error)
		MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error)
	}
	// Sender posts a message into a room.
	Sender interface {
		Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error
	}
	// Typist says one is typing.
	Typist interface {
		SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error
	}
	// Uploader posts a file.
	Uploader interface {
		SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error
	}
	// Reactor reacts to a message.
	Reactor interface {
		SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error
	}
	// Redactor deletes a message.
	Redactor interface {
		Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error
	}
	// People is who is in a room, and whom a message may reach.
	People interface {
		Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error)
		RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error)
		MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error)
		DirectCandidates(ctx context.Context, limit int) ([]domain.Member, error)
	}
	// Media is an attachment's bytes.
	Media interface {
		LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error)
	}
	// Encryption says which rooms are end-to-end encrypted. A network without it is
	// taken to encrypt every room: what reads it decides who may read a room (the
	// agent's scope), and a network that forgot to say must not widen that.
	Encryption interface {
		RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error)
	}
	// SpaceLister is a network with spaces: Matrix's hierarchy, WhatsApp's communities.
	SpaceLister interface {
		Spaces(ctx context.Context) ([]domain.Space, error)
		RefreshSpaces(ctx context.Context) ([]domain.Space, error)
	}
	// SpaceEditor files rooms into spaces (Matrix's: a WhatsApp community is its
	// admins' to fill).
	SpaceEditor interface {
		AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error
		RemoveFromSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error
	}
	// Threads is a network whose rooms have threads.
	Threads interface {
		api.Threads
		// ThreadParticipant reports whether we sent a thread's root or any reply in it.
		ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool
	}
)

// Router is every network served as one. Build it with New.
type Router struct {
	// adapters are the networks, by the network their IDs name.
	adapters map[domain.Protocol]Adapter
	// all is every adapter in the networks' order: started, stopped and streamed
	// from, logged in or not.
	all []Adapter

	// noInvites and noVerifications stand in for the streams of a capability no
	// network has: nothing is sent on them, and Stop closes them.
	noInvites       chan []domain.Room
	noVerifications chan domain.Verification

	// done ends the merged streams' pumps when the router stops, so a reader that
	// went away cannot leave one blocked on a send.
	done     chan struct{}
	stopOnce sync.Once

	streamsOnce   sync.Once
	messages      <-chan domain.Message
	activity      <-chan domain.Activity
	unread        <-chan domain.Unread
	reactions     <-chan domain.ReactionUpdate
	invites       <-chan []domain.Room
	verifications <-chan domain.Verification
}

// New routes adapters, keyed by the network their IDs name. None at all is a daemon
// with no account yet: it answers with nothing until :login sets one up.
func New(adapters map[domain.Protocol]Adapter) *Router {
	r := &Router{
		adapters: maps.Clone(adapters), done: make(chan struct{}),
		noInvites: make(chan []domain.Room), noVerifications: make(chan domain.Verification),
	}
	for _, network := range slices.Sorted(maps.Keys(adapters)) {
		r.all = append(r.all, adapters[network])
	}
	return r
}

// on reports whether a can be asked: it has no session to wait for, or has one.
func on(a Adapter) bool {
	s, ok := a.(Session)
	return !ok || s.LoggedIn()
}

// adapterFor is the adapter for a room, event or person's network.
func (r *Router) adapterFor(id string) (Adapter, error) {
	network := domain.NetworkOf(id)
	a, ok := r.adapters[network]
	switch {
	case !ok:
		return nil, fmt.Errorf("%w: %s (%s)", api.ErrNetworkOff, network, id)
	case !on(a):
		return nil, fmt.Errorf("%w: %s (not logged in) (%s)", api.ErrNetworkOff, network, id)
	}
	return a, nil
}

// onRoom runs call on roomID's network, as the capability C; what names C in the
// refusal of a network without it.
func onRoom[C, T any](r *Router, roomID domain.RoomID, what string, call func(C) (T, error)) (T, error) {
	var zero T
	a, err := r.adapterFor(string(roomID))
	if err != nil {
		return zero, err
	}
	c, ok := a.(C)
	if !ok {
		return zero, fmt.Errorf("%w: %s in %s rooms (%s)", api.ErrNotOnNetwork, what, domain.NetworkOf(string(roomID)), roomID)
	}
	return call(c)
}

// doOnRoom is onRoom for a call that returns only an error.
func doOnRoom[C any](r *Router, roomID domain.RoomID, what string, call func(C) error) error {
	_, err := onRoom(r, roomID, what, func(c C) (struct{}, error) { return struct{}{}, call(c) })
	return err
}

// live is every adapter that may be asked.
func (r *Router) live() []Adapter {
	live := make([]Adapter, 0, len(r.all))
	for _, a := range r.all {
		if on(a) {
			live = append(live, a)
		}
	}
	return live
}

// having is every live adapter with capability C.
func having[C any](r *Router) []C {
	var out []C
	for _, a := range r.live() {
		if c, ok := a.(C); ok {
			out = append(out, c)
		}
	}
	return out
}

// gather is every live answer of the networks with capability C, concatenated in
// order. One failing fails the whole read: a partial list would read as complete.
func gather[C, T any](r *Router, read func(C) ([]T, error)) ([]T, error) {
	var out []T
	for _, c := range having[C](r) {
		got, err := read(c)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// sole is the live network with capability C, for a call that names no room (keys,
// verification, a new room); ErrNetworkOff when none has it.
func sole[C any](r *Router, what string) (C, error) {
	if found := having[C](r); len(found) > 0 {
		return found[0], nil
	}
	var zero C
	return zero, fmt.Errorf("%w: nothing logged in does %s", api.ErrNetworkOff, what)
}

// byNetwork splits rooms by the adapter that serves them with capability C, keeping
// their order; rooms on no such network are returned apart, with why.
func byNetwork[C any](r *Router, rooms []domain.RoomID, what string) (map[Adapter][]domain.RoomID, map[domain.RoomID]error) {
	parts := make(map[Adapter][]domain.RoomID)
	off := map[domain.RoomID]error{}
	for _, room := range rooms {
		a, err := r.adapterFor(string(room))
		if err == nil {
			if _, ok := a.(C); !ok {
				err = fmt.Errorf("%w: %s in %s rooms (%s)", api.ErrNotOnNetwork, what, domain.NetworkOf(string(room)), room)
			}
		}
		if err != nil {
			off[room] = err
			continue
		}
		parts[a] = append(parts[a], room)
	}
	return parts, off
}

// Start runs every adapter until ctx ends, and returns when all have, with what
// each ended with.
func (r *Router) Start(ctx context.Context) error {
	if len(r.all) == 0 {
		<-ctx.Done() // no network: the daemon still runs, for :login to reach
		return nil
	}
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

// RewindSync has every network that can refill the emptied cache, logged in or not.
func (r *Router) RewindSync(ctx context.Context) error {
	var errs []error
	for _, a := range r.all {
		if c, ok := a.(Resync); ok {
			errs = append(errs, c.RewindSync(ctx))
		}
	}
	return errors.Join(errs...)
}

// Rooms is every network's cached rooms.
func (r *Router) Rooms(ctx context.Context) ([]domain.Room, error) {
	return gather(r, func(c RoomLister) ([]domain.Room, error) { return c.Rooms(ctx) })
}

// RefreshRooms refreshes every network's rooms.
func (r *Router) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	return gather(r, func(c RoomLister) ([]domain.Room, error) { return c.RefreshRooms(ctx) })
}

// CachedUnread is every network's unread state.
func (r *Router) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	return gather(r, func(c RoomLister) ([]domain.Unread, error) { return c.CachedUnread(ctx) })
}

// DirectCandidates is every network's people to message, at most limit.
func (r *Router) DirectCandidates(ctx context.Context, limit int) ([]domain.Member, error) {
	out, err := gather(r, func(c People) ([]domain.Member, error) { return c.DirectCandidates(ctx, limit) })
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
	parts, off := byNetwork[ReadState](r, roomIDs, "read state")
	var total domain.ReadResult
	fail := func(n int, why string) {
		total.Failed += n
		if total.FirstError == "" {
			total.FirstError = why
		}
	}
	for _, room := range roomIDs {
		if err, ok := off[room]; ok {
			fail(1, err.Error())
		}
	}
	var refused error
	attempted := false
	for _, a := range r.all {
		rooms := parts[a]
		if len(rooms) == 0 {
			continue
		}
		got, err := a.(ReadState).MarkRoomsRead(ctx, rooms, private)
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

// RoomEncryption asks each room's network. A room on no connected network, or on one
// that does not say, reports as encrypted (the safe direction).
func (r *Router) RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	parts, off := byNetwork[Encryption](r, roomIDs, "encryption")
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for room := range off {
		out[room] = true
	}
	for _, a := range r.all {
		rooms := parts[a]
		if len(rooms) == 0 {
			continue
		}
		got, err := a.(Encryption).RoomEncryption(ctx, rooms)
		if err != nil {
			return nil, err
		}
		maps.Copy(out, got)
	}
	return out, nil
}
