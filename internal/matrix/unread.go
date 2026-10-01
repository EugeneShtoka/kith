package matrix

import (
	"context"
	"fmt"
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix"
)

// Unread streams per-room unread updates (notification/highlight counts and our
// read position) to the UI as sync delivers them.
func (b *InProc) Unread() <-chan domain.Unread { return b.out.unread }

// CachedUnread returns every room's cached unread state, with local counts applied.
func (b *InProc) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	if b.cache == nil {
		return nil, nil
	}
	u, err := b.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("matrix: read cached unread: %w", err)
	}
	// Only Matrix rooms: another network counts its own, by its own receipts.
	u = slices.DeleteFunc(u, func(x domain.Unread) bool { return !domain.MatrixRooms.Owns(x.RoomID) })
	// Local counts replace the server's where the count query answers (see domain.Unread.Count).
	local, err := b.cache.CountUnreadAll(ctx, b.me())
	if err != nil {
		b.warnIf(ctx, err, "count unread locally; using the server's counts")
		return u, nil // counts are an upgrade; the server's numbers still work
	}
	threads, terr := b.cache.CountThreadUnreadAll(ctx, b.me())
	b.warnIf(ctx, terr, "count thread unread locally")
	for i := range u {
		if l, ok := local[u[i].RoomID]; ok {
			u[i].Messages, u[i].Mentions, u[i].Counted = l.Messages, l.Mentions, true
			u[i] = withThreads(u[i], threads[u[i].RoomID])
		}
	}
	return u, nil
}

// countLocal fills in a room's locally computed unread counts, threads included.
// On error Counted stays false, falling back to the server's count.
func (b *InProc) countLocal(ctx context.Context, u domain.Unread) domain.Unread {
	if b.cache == nil {
		return u
	}
	messages, mentions, counted, err := b.cache.CountUnread(ctx, b.me(), u.RoomID)
	if err != nil {
		b.warnIf(ctx, err, "count unread locally", "room", u.RoomID)
		return u
	}
	u.Messages, u.Mentions, u.Counted = messages, mentions, counted
	return b.addThreadCounts(ctx, u)
}

// recount recomputes a room's local unread counts and emits them when they moved.
// Called wherever the inputs change; the query runs outside the lock.
func (b *InProc) recount(ctx context.Context, roomID domain.RoomID) {
	b.recountAndEmit(ctx, roomID, false)
}

// recountAndEmit is recount, emitting regardless when always is set (the rest of the
// room's state moved). Only the counts are written back, onto the current state.
func (b *InProc) recountAndEmit(ctx context.Context, roomID domain.RoomID, always bool) {
	if roomID == "" {
		return
	}
	if b.cache == nil {
		if always {
			emit(&b.out, b.out.unread, b.unread.get(roomID))
		}
		return
	}
	// The count runs outside the lock, so another writer (a read position placed by a
	// background fetch) can move the room meanwhile: then it is counted again, and a
	// count of the older state is never written over the newer one.
	for range recountTries {
		gen := b.unread.generation(roomID)
		local := b.countLocal(ctx, domain.Unread{RoomID: roomID})
		if b.storeCount(roomID, gen, local, always) {
			return
		}
	}
	// Still moving after every try: the writers moving it recount it themselves.
}

// recountTries bounds how often a recount starts over because the room moved.
const recountTries = 3

// storeCount writes a room's local counts, taken at generation gen, onto its current
// state, unless another writer has moved it since (false then). Only the counts are
// written. It emits them when they moved (or always), under the book's lock: two
// recounts of one room emit in the order they wrote, so the last one seen is the newest.
func (b *InProc) storeCount(roomID domain.RoomID, gen uint64, local domain.Unread, always bool) (current bool) {
	_, _, current = b.unread.updateAt(roomID, gen, func(cur *domain.Unread, _ *int64) bool {
		changed := cur.Messages != local.Messages || cur.Mentions != local.Mentions ||
			cur.Counted != local.Counted || !sameThreadUnread(cur.Threads, local.Threads)
		cur.RoomID = roomID
		cur.Messages, cur.Mentions, cur.Counted = local.Messages, local.Mentions, local.Counted
		cur.Threads = local.Threads
		if changed || always {
			emit(&b.out, b.out.unread, *cur)
		}
		return changed
	})
	return current
}

// seedUnread primes the unread book from the cache, so a first sync carrying only a
// receipt merges onto the persisted counts instead of zeroing them. Best-effort.
func (b *InProc) seedUnread(ctx context.Context) {
	cached, err := b.CachedUnread(ctx)
	if err != nil {
		b.warnIf(ctx, err, "seed unread counts from the cache")
		return
	}
	placed, err := b.cache.ReadPositions(ctx)
	if err != nil {
		b.warnIf(ctx, err, "read cached read positions")
		placed = nil
	}
	b.unread.seed(cached, placed)
	if err == nil {
		b.fetchUnplaced(ctx, cached, placed)
	}
}

// mergeUnread folds a room's sync delta onto its last-known state, applying each
// field by presence (an absent section keeps cached values; a zero still updates),
// and persists a change. read is the newest room receipt (see applyReceipts): it
// moves the marker forward only, and threaded receipts never move it.
func (b *InProc) mergeUnread(ctx context.Context, roomID domain.RoomID, jr *mautrix.SyncJoinedRoom, read readPos, marked *bool) (domain.Unread, bool) {
	return b.unread.update(roomID, func(cur *domain.Unread, ts *int64) bool {
		cur.RoomID = roomID
		changed := false
		if n := jr.UnreadNotifications; n != nil {
			if cur.Notifications != n.NotificationCount || cur.Highlights != n.HighlightCount {
				changed = true
			}
			cur.Notifications, cur.Highlights = n.NotificationCount, n.HighlightCount
		}
		if (readPos{Event: cur.ReadEvent, TS: *ts}).ahead(read) {
			cur.ReadEvent = read.Event
			*ts = read.TS
			changed = true
		}
		// nil: this batch said nothing about the mark.
		if marked != nil && *marked != cur.Marked {
			cur.Marked = *marked
			changed = true
		}
		if changed {
			b.saveUnread(ctx, *cur, *ts)
		}
		return changed
	})
}
