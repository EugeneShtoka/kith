package instagram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/readstate"
)

// A history page's Next names where the next older page starts: before a message (the
// oldest the page had), or at Instagram's own cursor.
const (
	olderThan = "older:"
	atCursor  = "cursor:"
)

// Timeline is a page of a thread, newest last: the cache's first, filled from Instagram
// when the cache holds less than a page; then older pages from Instagram, each cached.
func (a *Adapter) Timeline(ctx context.Context, room domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	if a.cache == nil {
		return domain.TimelinePage{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	self, chat, ok := threadOf(room)
	if !ok {
		return domain.TimelinePage{}, fmt.Errorf("instagram: %s is not an Instagram thread", room)
	}
	c, client := a.connected(self)
	if from == "" {
		return a.firstPage(ctx, c, client, room, chat, limit)
	}
	if client == nil {
		return domain.TimelinePage{}, nil // older history waits for the account to connect
	}
	req := &slidetypes.PaginateMessagesRequest{ThreadID: chat, FirstN: limit, InitialMessagePageCount: limit}
	switch {
	case strings.HasPrefix(from, olderThan):
		id := strings.TrimPrefix(from, olderThan)
		req.OlderThanMessageID = &id
	case strings.HasPrefix(from, atCursor):
		cursor := strings.TrimPrefix(from, atCursor)
		req.AfterCursor = &cursor
	default:
		return domain.TimelinePage{}, nil
	}
	resp, err := client.PaginateMessages(ctx, req)
	if err != nil {
		return domain.TimelinePage{}, fmt.Errorf("instagram: page %s's history: %w", room, err)
	}
	t := resp.ThreadInfo.AsIGDirectThread
	if t == nil || t.Messages == nil {
		return domain.TimelinePage{}, nil
	}
	page := domain.TimelinePage{Messages: a.cachePage(ctx, c, chat, t.Messages.Edges)}
	if t.Messages.PageInfo.HasNextPage && t.Messages.PageInfo.EndCursor != "" {
		page.Next = atCursor + t.Messages.PageInfo.EndCursor
	}
	return page, nil
}

// firstPage is a thread's newest page from the cache, filled from Instagram when the
// cache holds less than a page, with where older history starts.
func (a *Adapter) firstPage(ctx context.Context, c *conn, client *instameow.Client, room domain.RoomID, chat string, limit int) (domain.TimelinePage, error) {
	msgs, err := a.cache.Messages(ctx, room, limit)
	if err != nil {
		return domain.TimelinePage{}, fmt.Errorf("instagram: read %s: %w", room, err)
	}
	if len(msgs) < limit && client != nil {
		a.fetchRecent(ctx, c, client, chat)
		if msgs, err = a.cache.Messages(ctx, room, limit); err != nil {
			return domain.TimelinePage{}, fmt.Errorf("instagram: read %s: %w", room, err)
		}
	}
	page := domain.TimelinePage{Messages: msgs}
	if len(msgs) > 0 {
		if oldest, ok := messageOf(msgs[0].ID); ok {
			page.Next = olderThan + oldest
		}
	}
	return page, nil
}

// connected is the connection and client of the account self, when it is connected.
func (a *Adapter) connected(self string) (*conn, *instameow.Client) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.conns {
		if c.self == self {
			c.mu.Lock()
			client := c.client
			c.mu.Unlock()
			return c, client
		}
	}
	return nil, nil
}

// MarkRead reads a room through event in kith (telling Instagram comes with sending).
func (a *Adapter) MarkRead(ctx context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	if a.cache == nil {
		return nil
	}
	at, err := readstate.TimeOf(ctx, a.cache, room, event, time.Now())
	if err != nil {
		return fmt.Errorf("instagram: %w", err)
	}
	if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, at.UnixMilli()); err != nil {
		return fmt.Errorf("instagram: save the read position of %s: %w", room, err)
	}
	a.recount(ctx, room)
	return nil
}

// MarkRoomsRead reads each room through its newest cached message.
func (a *Adapter) MarkRoomsRead(ctx context.Context, rooms []domain.RoomID, _ bool) (domain.ReadResult, error) {
	if a.cache == nil {
		return domain.ReadResult{Skipped: len(rooms)}, nil
	}
	result, err := readstate.MarkRoomsRead(ctx, a.cache, rooms, func(room domain.RoomID, event domain.EventID) error {
		return a.MarkRead(ctx, room, event, false)
	})
	if err != nil {
		return result, fmt.Errorf("instagram: %w", err)
	}
	return result, nil
}

// errNoMarkUnread: marking a thread unread is not supported yet.
var errNoMarkUnread = errors.New("instagram: marking a thread unread is not supported yet")

// MarkRoomUnread is not supported yet.
func (a *Adapter) MarkRoomUnread(context.Context, domain.RoomID, bool) error { return errNoMarkUnread }
