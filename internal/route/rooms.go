package route

import (
	"context"
	"errors"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every call about one room goes to that room's network.

// MarkRead sends a receipt in roomID.
func (r *Router) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	return doOnRoom(r, roomID, "read state", func(c ReadState) error { return c.MarkRead(ctx, roomID, eventID, private) })
}

// MarkRoomUnread sets or clears a room's marked-unread flag.
func (r *Router) MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error {
	return doOnRoom(r, roomID, "read state", func(c ReadState) error { return c.MarkRoomUnread(ctx, roomID, unread) })
}

// StarMessage adds or removes a bookmark.
func (r *Router) StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, starred bool) error {
	return doOnRoom(r, roomID, "bookmarks", func(c Stars) error { return c.StarMessage(ctx, roomID, eventID, starred) })
}

// MarkSpam records or clears the verdict on its room.
func (r *Router) MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error {
	return doOnRoom(r, verdict.Room, "spam reports", func(c SpamReports) error { return c.MarkSpam(ctx, verdict) })
}

// CanonicalParent is the space a room names as its parent; none on a network
// without homes.
func (r *Router) CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	parent, err := onRoom(r, roomID, "homes", func(c Homes) (domain.SpaceID, error) { return c.CanonicalParent(ctx, roomID) })
	if errors.Is(err, api.ErrNotOnNetwork) {
		return "", nil // a network without homes: the room names no parent
	}
	return parent, err
}

// MessageHistory is every version of a message.
func (r *Router) MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	type history struct {
		versions []domain.Revision
		deletion domain.Deletion
	}
	got, err := onRoom(r, roomID, "history", func(c History) (history, error) {
		versions, deletion, err := c.MessageHistory(ctx, roomID, eventID)
		return history{versions, deletion}, err
	})
	return got.versions, got.deletion, err
}

// Timeline fetches one page of a room's history.
func (r *Router) Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	return onRoom(r, roomID, "history", func(c History) (domain.TimelinePage, error) { return c.Timeline(ctx, roomID, from, limit) })
}

// FetchEvent fetches one message.
func (r *Router) FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	return onRoom(r, roomID, "history", func(c History) (domain.Message, error) { return c.FetchEvent(ctx, roomID, eventID) })
}

// Redact deletes a message.
func (r *Router) Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error {
	return doOnRoom(r, roomID, "deleting", func(c Redactor) error { return c.Redact(ctx, roomID, eventID, reason) })
}

// Send posts a message.
func (r *Router) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	return doOnRoom(r, roomID, "sending", func(c Sender) error { return c.Send(ctx, roomID, draft) })
}

// SendTyping sets or clears the typing notice.
func (r *Router) SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error {
	return doOnRoom(r, roomID, "typing", func(c Typist) error { return c.SendTyping(ctx, roomID, typing, timeout) })
}

// SendFile posts a file.
func (r *Router) SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error {
	return doOnRoom(r, roomID, "files", func(c Uploader) error { return c.SendFile(ctx, roomID, path, caption) })
}

// SendReaction reacts to a message.
func (r *Router) SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	return doOnRoom(r, roomID, "reactions", func(c Reactor) error { return c.SendReaction(ctx, roomID, target, key) })
}

// LoadImage is a message's media bytes.
func (r *Router) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	return onRoom(r, roomID, "media", func(c Media) ([]byte, error) { return c.LoadImage(ctx, roomID, eventID) })
}

// Members is who is in a room.
func (r *Router) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	return onRoom(r, roomID, "members", func(c People) ([]domain.Member, error) { return c.Members(ctx, roomID, limit) })
}

// RefreshMembers refetches who is in a room.
func (r *Router) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	return onRoom(r, roomID, "members", func(c People) ([]domain.Member, error) { return c.RefreshMembers(ctx, roomID) })
}

// MentionCandidates orders a room's members for the mention dropdown.
func (r *Router) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	return onRoom(r, roomID, "mentions", func(c People) ([]domain.Member, error) { return c.MentionCandidates(ctx, roomID, limit) })
}

// Spaces is every network's cached spaces: Matrix's hierarchy and the others'
// (WhatsApp communities), sorted by name.
func (r *Router) Spaces(ctx context.Context) ([]domain.Space, error) {
	return r.allSpaces(func(s SpaceLister) ([]domain.Space, error) { return s.Spaces(ctx) })
}

// RefreshSpaces refetches every network's spaces.
func (r *Router) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	return r.allSpaces(func(s SpaceLister) ([]domain.Space, error) { return s.RefreshSpaces(ctx) })
}

// allSpaces is read over every live network that has spaces; one failing fails the
// read, as gather does: a partial hierarchy would read as complete.
func (r *Router) allSpaces(read func(SpaceLister) ([]domain.Space, error)) ([]domain.Space, error) {
	out, err := gather(r, read)
	if err != nil {
		return nil, err
	}
	domain.SortSpaces(out)
	return out, nil
}
