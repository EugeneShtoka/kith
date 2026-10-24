package matrix

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// After kith was away, /sync cuts a busy room's timeline short (limited, with a
// prev_batch): what was said between the cache's newest message and the response's
// oldest is skipped. Each such gap is recorded before the sync token moves on, then
// filled in the background by paging back from prev_batch to the message the cache held
// below it. What arrives is counted, and announced as if live unless the gap came with
// the catch-up after a start (which is never announced). Past gapPages pages a gap is
// kept, capped, and fills when scrolled into.

const (
	// gapPage is how many events one /messages page asks for.
	gapPage = 100
	// gapPages caps how far one gap is filled in the background.
	gapPages = 10
)

// gapFiller runs one background fill at a time; a kick while one runs makes it go again.
type gapFiller struct {
	wg      sync.WaitGroup
	mu      sync.Mutex
	running bool
	again   bool
	// pending: a gap may be unfilled (recorded, resumed after a start, or a fill failed).
	pending atomic.Bool
}

// recordGaps keeps each joined room's skipped stretch. Runs in OnSync, before this
// response's events are cached and before its token is saved.
func (b *InProc) recordGaps(ctx context.Context, resp *mautrix.RespSync, since string) {
	if b.cache == nil {
		return
	}
	// A first sync (or a rewind) has nothing cached to fill up to: rooms load on opening.
	if since != "" {
		announce := b.syncs.Load() > 1
		for roomID, jr := range resp.Rooms.Join {
			if jr == nil || !jr.Timeline.Limited || jr.Timeline.PrevBatch == "" {
				continue
			}
			added, err := b.cache.AddGap(ctx, b.ids.room(roomID), jr.Timeline.PrevBatch, oldestOf(jr.Timeline.Events), announce)
			b.warnIf(ctx, err, "record a gap", "room", roomID)
			if added {
				b.gaps.pending.Store(true)
			}
		}
	}
	if b.gaps.pending.Load() {
		b.fillGaps(ctx)
	}
}

// oldestOf is the earliest time among events, or now when there are none.
func oldestOf(events []*event.Event) time.Time {
	oldest := time.Now()
	for _, evt := range events {
		if at := time.UnixMilli(evt.Timestamp); evt.Timestamp > 0 && at.Before(oldest) {
			oldest = at
		}
	}
	return oldest
}

// fillGaps fills the recorded gaps in the background, one fill at a time.
func (b *InProc) fillGaps(ctx context.Context) {
	b.gaps.mu.Lock()
	defer b.gaps.mu.Unlock()
	if b.gaps.running {
		b.gaps.again = true
		return
	}
	b.gaps.running = true
	b.gaps.wg.Go(func() {
		for {
			b.gaps.pending.Store(false)
			if err := b.fillRecorded(ctx); err != nil {
				// Tried again on the next sync.
				b.gaps.pending.Store(true)
				if ctx.Err() == nil {
					b.warnIf(ctx, err, "fill a gap")
				}
			}
			b.gaps.mu.Lock()
			if !b.gaps.again || ctx.Err() != nil {
				b.gaps.running = false
				b.gaps.mu.Unlock()
				return
			}
			b.gaps.again = false
			b.gaps.mu.Unlock()
		}
	})
}

// fillRecorded fills each uncapped gap, stopping at the first that fails.
func (b *InProc) fillRecorded(ctx context.Context) error {
	gaps, err := b.cache.Gaps(ctx)
	if err != nil {
		return fmt.Errorf("matrix: read the gaps: %w", err)
	}
	for _, g := range gaps {
		if g.Capped {
			continue
		}
		if err := b.fillGap(ctx, g); err != nil {
			return err
		}
	}
	return nil
}

// fillGap pages back from where the gap was left until its boundary, the end of the
// room, or gapPages pages, after which it stays, capped.
func (b *InProc) fillGap(ctx context.Context, g db.Gap) error {
	token := g.Token
	for range gapPages {
		resp, err := b.client.Messages(ctx, b.ids.nativeRoom(g.RoomID), token, "", mautrix.DirectionBackward, nil, gapPage)
		if err != nil {
			if errors.Is(err, mautrix.MForbidden) {
				// Left (or never let in): nothing more to read here.
				return b.cache.DropGap(ctx, g.ID) //nolint:wrapcheck // the cache's own
			}
			return fmt.Errorf("matrix: fill a gap in %s: %w", g.RoomID, err)
		}
		reached := b.fillPage(ctx, g, resp.Chunk)
		if reached || resp.End == "" || len(resp.Chunk) == 0 {
			return b.cache.DropGap(ctx, g.ID) //nolint:wrapcheck // the cache's own
		}
		token = resp.End
		if err := b.cache.MoveGap(ctx, g.ID, token, oldestOf(resp.Chunk), false); err != nil {
			return err //nolint:wrapcheck // the cache's own
		}
	}
	return b.cache.MoveGap(ctx, g.ID, token, g.Until, true) //nolint:wrapcheck // the cache's own
}

// fillPage caches one backward page of a gap and counts, announces and applies what is
// new in it. reached: the page holds the gap's boundary, so the gap is filled.
func (b *InProc) fillPage(ctx context.Context, g db.Gap, chunk []*event.Event) (reached bool) {
	// What the cache held before this page is not new, and reaching the boundary (or
	// anything held from before the gap) ends it. A row an edit made before its original
	// arrived holds the edit's later time: the original is still new.
	ids := make([]domain.EventID, 0, len(chunk))
	for _, evt := range chunk {
		ids = append(ids, b.ids.event(evt.ID))
	}
	times, err := b.cache.MessageTimes(ctx, g.RoomID, ids)
	b.warnIf(ctx, err, "read what a gap's page holds", "room", g.RoomID)
	held := map[domain.EventID]bool{}
	for _, evt := range chunk {
		eventID, sent := b.ids.event(evt.ID), time.UnixMilli(evt.Timestamp)
		at, ok := times[eventID]
		if !ok || at.After(sent) {
			continue
		}
		held[eventID] = true
		if eventID == g.Boundary || !sent.After(g.Since) {
			reached = true
		}
	}
	_, cached := b.cachePage(ctx, g.RoomID, chunk, "")
	// A redaction inside the gap may delete a message from before it (or a reaction).
	for _, evt := range slices.Backward(chunk) {
		if evt.Type == event.EventRedaction {
			b.onRedaction(ctx, evt)
		}
	}
	for i := range cached {
		msg := cached[i]
		if held[msg.ID] {
			continue
		}
		msg.Late = true
		if b.onCached != nil {
			b.onCached(msg)
		}
		if g.Announce {
			emit(&b.out, b.out.msgs, msg)
		}
	}
	// Place held receipts before recounting, or a read message is counted.
	b.settleReceipts(ctx, g.RoomID)
	b.recount(ctx, g.RoomID)
	return reached
}

// dropGapsAbove forgets a room's gaps that scrolling back from its newest message has
// passed: the page reaching back to oldest cached all of them.
func (b *InProc) dropGapsAbove(ctx context.Context, roomID domain.RoomID, oldest time.Time) {
	if b.cache == nil {
		return
	}
	b.warnIf(ctx, b.cache.DropGapsAbove(ctx, roomID, oldest), "drop filled gaps", "room", roomID)
}
