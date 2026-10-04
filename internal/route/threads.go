package route

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// threadSource is a network whose rooms have threads: Matrix's and Slack's.
type threadSource interface {
	api.Threads
	// ThreadParticipant reports whether we sent a thread's root or any reply in it.
	ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool
}

// onThreads runs call on the room's network, refusing a network without threads.
func onThreads[T any](r *Router, roomID domain.RoomID, call func(threadSource) (T, error)) (T, error) {
	return onRoom(r, roomID, func(a Adapter) (T, error) {
		source, ok := a.(threadSource)
		if !ok {
			var zero T
			return zero, fmt.Errorf("%w: threads in %s rooms (%s)", api.ErrNotOnNetwork, domain.NetworkOf(string(roomID)), roomID)
		}
		return call(source)
	})
}

// ListThreads is a room's threads.
func (r *Router) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	return onThreads(r, roomID, func(s threadSource) ([]domain.Thread, error) { return s.ListThreads(ctx, roomID) })
}

// ThreadPage fetches one page of a thread.
func (r *Router) ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int) (domain.TimelinePage, error) {
	return onThreads(r, roomID, func(s threadSource) (domain.TimelinePage, error) {
		return s.ThreadPage(ctx, roomID, root, from, limit)
	})
}

// MarkThreadRead marks a thread read up to eventID.
func (r *Router) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, private bool) error {
	_, err := onThreads(r, roomID, func(s threadSource) (struct{}, error) {
		return struct{}{}, s.MarkThreadRead(ctx, roomID, root, eventID, private)
	})
	return err
}

// ThreadParticipant reports whether we took part in a thread; in a room of a network
// without threads (or one that is off), we did not.
func (r *Router) ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool {
	took, _ := onThreads(r, roomID, func(s threadSource) (bool, error) { return s.ThreadParticipant(ctx, roomID, root), nil })
	return took
}
