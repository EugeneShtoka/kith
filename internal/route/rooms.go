package route

import (
	"context"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every call about one room goes to that room's network.

// MarkRead sends a receipt in roomID.
func (r *Router) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	return doOnRoom(r, roomID, func(a Adapter) error { return a.MarkRead(ctx, roomID, eventID, private) })
}

// MarkRoomUnread sets or clears a room's marked-unread flag.
func (r *Router) MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error {
	return doOnRoom(r, roomID, func(a Adapter) error { return a.MarkRoomUnread(ctx, roomID, unread) })
}

// StarMessage adds or removes a bookmark.
func (r *Router) StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, starred bool) error {
	return doOnRoom(r, roomID, func(a Adapter) error { return a.StarMessage(ctx, roomID, eventID, starred) })
}

// MarkSpam records or clears the verdict on its room.
func (r *Router) MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error {
	return doOnRoom(r, verdict.Room, func(a Adapter) error { return a.MarkSpam(ctx, verdict) })
}

// CanonicalParent is the space a room names as its parent.
func (r *Router) CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	return onRoom(r, roomID, func(a Adapter) (domain.SpaceID, error) { return a.CanonicalParent(ctx, roomID) })
}

// MessageHistory is every version of a message.
func (r *Router) MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	type history struct {
		versions []domain.Revision
		deletion domain.Deletion
	}
	got, err := onRoom(r, roomID, func(a Adapter) (history, error) {
		versions, deletion, err := a.MessageHistory(ctx, roomID, eventID)
		return history{versions, deletion}, err
	})
	return got.versions, got.deletion, err
}

// Timeline fetches one page of a room's history.
func (r *Router) Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	return onRoom(r, roomID, func(a Adapter) (domain.TimelinePage, error) { return a.Timeline(ctx, roomID, from, limit) })
}

// FetchEvent fetches one message.
func (r *Router) FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	return onRoom(r, roomID, func(a Adapter) (domain.Message, error) { return a.FetchEvent(ctx, roomID, eventID) })
}

// Redact deletes a message.
func (r *Router) Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error {
	return doOnRoom(r, roomID, func(a Adapter) error { return a.Redact(ctx, roomID, eventID, reason) })
}

// Send posts a message.
func (r *Router) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	return doOnRoom(r, roomID, func(a Adapter) error { return a.Send(ctx, roomID, draft) })
}

// SendTyping sets or clears the typing notice.
func (r *Router) SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error {
	return doOnRoom(r, roomID, func(a Adapter) error { return a.SendTyping(ctx, roomID, typing, timeout) })
}

// SendFile posts a file.
func (r *Router) SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error {
	return doOnRoom(r, roomID, func(a Adapter) error { return a.SendFile(ctx, roomID, path, caption) })
}

// SendReaction reacts to a message.
func (r *Router) SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	return doOnRoom(r, roomID, func(a Adapter) error { return a.SendReaction(ctx, roomID, target, key) })
}

// LoadImage is a message's media bytes.
func (r *Router) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	return onRoom(r, roomID, func(a Adapter) ([]byte, error) { return a.LoadImage(ctx, roomID, eventID) })
}

// Members is who is in a room.
func (r *Router) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	return onRoom(r, roomID, func(a Adapter) ([]domain.Member, error) { return a.Members(ctx, roomID, limit) })
}

// RefreshMembers refetches who is in a room.
func (r *Router) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	return onRoom(r, roomID, func(a Adapter) ([]domain.Member, error) { return a.RefreshMembers(ctx, roomID) })
}

// MentionCandidates orders a room's members for the mention dropdown.
func (r *Router) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	return onRoom(r, roomID, func(a Adapter) ([]domain.Member, error) { return a.MentionCandidates(ctx, roomID, limit) })
}
