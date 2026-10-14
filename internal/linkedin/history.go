package linkedin

import (
	"context"
	"fmt"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Older messages come from the cache while it has them, then from LinkedIn: a page
// before the oldest cached message, cached as history (not announced) and read back
// from the cache, so a page is always what the cache holds.

// Timeline is a page of a room's history, oldest first, before from (an event ID; ""
// for the newest); Next continues it, "" at the beginning.
func (a *Adapter) Timeline(ctx context.Context, room domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	if a.cache == nil {
		return domain.TimelinePage{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if from == "" {
		msgs, err := a.cache.Messages(ctx, room, limit)
		if err != nil {
			return domain.TimelinePage{}, fmt.Errorf("linkedin: read %s: %w", room, err)
		}
		return pageOf(msgs, true), nil
	}
	older, err := a.olderThan(ctx, room, domain.EventID(from), limit)
	if err != nil || len(older) >= limit {
		return pageOf(older, true), err
	}
	more, err := a.fetchOlder(ctx, room, domain.EventID(from), older, limit)
	if err != nil {
		if len(older) == 0 {
			return domain.TimelinePage{}, err // not the beginning: scrolling asks again
		}
		return pageOf(older, true), nil
	}
	if older, err = a.olderThan(ctx, room, domain.EventID(from), limit); err != nil {
		return domain.TimelinePage{}, err
	}
	return pageOf(older, more), nil
}

// pageOf is msgs as a page whose Next is its oldest message, when more may come.
func pageOf(msgs []domain.Message, more bool) domain.TimelinePage {
	page := domain.TimelinePage{Messages: msgs}
	if more && len(msgs) > 0 {
		page.Next = string(msgs[0].ID)
	}
	return page
}

// olderThan is up to limit cached messages before from, oldest first.
func (a *Adapter) olderThan(ctx context.Context, room domain.RoomID, from domain.EventID, limit int) ([]domain.Message, error) {
	around, err := a.cache.MessagesAround(ctx, room, from, limit, 0)
	if err != nil {
		return nil, fmt.Errorf("linkedin: read %s before %s: %w", room, from, err)
	}
	if n := len(around); n > 0 && around[n-1].ID == from {
		around = around[:n-1]
	}
	return around, nil
}

// fetchOlder asks LinkedIn for the messages before the oldest the cache has before
// from and caches them; more reports whether LinkedIn had any (so there may be more).
func (a *Adapter) fetchOlder(ctx context.Context, room domain.RoomID, from domain.EventID, older []domain.Message, limit int) (bool, error) {
	c, client, thread, err := a.client(room)
	if err != nil {
		return false, err
	}
	anchor := from
	if len(older) > 0 {
		anchor = older[0].ID
	}
	before, ok, err := a.cache.MessageByID(ctx, room, anchor)
	if err != nil || !ok {
		return false, fmt.Errorf("linkedin: the message to page back from is not cached (%s): %w", anchor, err)
	}
	var got *linkedingo.CollectionResponse[linkedingo.MessageMetadata, linkedingo.Message]
	err = a.ask(ctx, "fetching older messages", func(ctx context.Context) (err error) {
		got, err = client.GetMessagesBefore(ctx, conversationURN(c.profile, thread), before.Timestamp, limit)
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return false, fmt.Errorf("linkedin: fetch older messages: %w", err)
	}
	if got == nil || len(got.Elements) == 0 {
		return false, nil
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	var ch change
	for i := range got.Elements {
		if msg, ok := c.inbox.message(thread, &got.Elements[i]); ok && msg.Timestamp.Before(before.Timestamp) {
			ch.history = append(ch.history, msg)
		}
	}
	a.store(ctx, c, ch)
	return len(ch.history) > 0, nil
}
