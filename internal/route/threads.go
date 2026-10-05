package route

import (
	"context"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ListThreads is a room's threads.
func (r *Router) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	return onRoom(r, roomID, "threads", func(c Threads) ([]domain.Thread, error) { return c.ListThreads(ctx, roomID) })
}

// ThreadPage fetches one page of a thread.
func (r *Router) ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int) (domain.TimelinePage, error) {
	return onRoom(r, roomID, "threads", func(c Threads) (domain.TimelinePage, error) {
		return c.ThreadPage(ctx, roomID, root, from, limit)
	})
}

// MarkThreadRead marks a thread read up to eventID.
func (r *Router) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, private bool) error {
	return doOnRoom(r, roomID, "threads", func(c Threads) error {
		return c.MarkThreadRead(ctx, roomID, root, eventID, private)
	})
}

// ThreadParticipant reports whether we took part in a thread; in a room of a network
// without threads (or one that is off), we did not.
func (r *Router) ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool {
	took, _ := onRoom(r, roomID, "threads", func(c Threads) (bool, error) { return c.ThreadParticipant(ctx, roomID, root), nil })
	return took
}
