package signal

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/readstate"
)

// Rooms are Signal chats as one linked account sees them, all answered from the cache.
// Each account is a space of its own, derived on read like Google Messages': the rooms'
// home in the rail.

// Rooms is the Signal rooms in the cache.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("signal: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolSignal
	}), nil
}

// RefreshRooms answers from the cache: chats arrive with their messages.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) { return a.Rooms(ctx) }

// RecountUnread counts rooms' unread again, when the cache counts them differently
// (who is hidden changed).
func (a *Adapter) RecountUnread(ctx context.Context, rooms []domain.RoomID) {
	for _, room := range rooms {
		a.recount(ctx, room)
	}
}

// CachedUnread is the cached unread state of Signal rooms, counted locally.
func (a *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("signal: read unread state: %w", err)
	}
	rows = slices.DeleteFunc(rows, func(u domain.Unread) bool {
		return domain.NetworkOf(string(u.RoomID)) != domain.ProtocolSignal
	})
	counts, err := a.cache.CountUnreadAll(ctx, a.Me())
	if err != nil {
		return nil, fmt.Errorf("signal: count unread: %w", err)
	}
	for i := range rows {
		if c, ok := counts[rows[i].RoomID]; ok {
			rows[i].Messages, rows[i].Mentions, rows[i].Counted = c.Messages, c.Mentions, true
		}
	}
	return rows, nil
}

// linkedAccounts is each configured account linked to a Signal account, and its ACI.
func (a *Adapter) linkedAccounts() map[string]string {
	out := map[string]string{}
	for _, account := range a.accountsNow() {
		if aci, ok, err := a.linkedTo(account.Name); err == nil && ok {
			out[account.Name] = aci
		}
	}
	return out
}

// Spaces is a space per linked account, holding its rooms.
func (a *Adapter) Spaces(ctx context.Context) ([]domain.Space, error) {
	rooms, err := a.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	accounts := a.linkedAccounts()
	names := make([]string, 0, len(accounts))
	for name := range accounts {
		names = append(names, name)
	}
	slices.Sort(names)
	spaces := make([]domain.Space, 0, len(names))
	for _, name := range names {
		self := accounts[name]
		var children []domain.RoomID
		for i := range rooms {
			if owner(self).Owns(rooms[i].ID) {
				children = append(children, rooms[i].ID)
			}
		}
		spaces = append(spaces, domain.Space{
			ID: accountSpaceID(self), Name: "Signal " + name, Children: children,
			// The rooms' home, not a space to file into; left by unlinking.
			Bridge: domain.ProtocolSignal, Original: true, Leaving: domain.LeftBySigningOut,
		})
	}
	return spaces, nil
}

// RefreshSpaces is Spaces: they are derived from the cache.
func (a *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) { return a.Spaces(ctx) }

// CanonicalParent is a room's account space.
func (a *Adapter) CanonicalParent(_ context.Context, room domain.RoomID) (domain.SpaceID, error) {
	self, ok := selfOf(room)
	if !ok {
		return "", nil
	}
	return accountSpaceID(self), nil
}

// Members is a room's cached members.
func (a *Adapter) Members(ctx context.Context, room domain.RoomID, limit int) ([]domain.Member, error) {
	members, err := a.cache.Members(ctx, room, limit)
	if err != nil {
		return nil, fmt.Errorf("signal: read members of %s: %w", room, err)
	}
	return members, nil
}

// RefreshMembers answers from the cache: a chat's people come with its messages.
func (a *Adapter) RefreshMembers(ctx context.Context, room domain.RoomID) ([]domain.Member, error) {
	return a.Members(ctx, room, 0)
}

// MentionCandidates is a room's members, recent speakers and the often mentioned first.
func (a *Adapter) MentionCandidates(ctx context.Context, room domain.RoomID, limit int) ([]domain.Member, error) {
	members, err := a.Members(ctx, room, 0)
	if err != nil || len(members) == 0 {
		return domain.RankMembers(members, nil, nil, limit), err
	}
	speakers, err := a.cache.RecentSpeakers(ctx, room, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("signal: rank recent speakers: %w", err)
	}
	mentioned, err := a.cache.FrequentMentions(ctx, room, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("signal: rank mention history: %w", err)
	}
	return domain.RankMembers(members, speakers, mentioned, limit), nil
}

// DirectCandidates returns nobody: chats appear as messages arrive.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }

// RoomEncryption reports every room encrypted: Signal is end-to-end encrypted, which
// keeps them from an assistant unless [agent] allows encrypted rooms.
func (a *Adapter) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = true
	}
	return out, nil
}

// Timeline is a page of the cache: Signal keeps no history to fetch.
func (a *Adapter) Timeline(ctx context.Context, room domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	if from != "" {
		return domain.TimelinePage{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	msgs, err := a.cache.Messages(ctx, room, limit)
	if err != nil {
		return domain.TimelinePage{}, fmt.Errorf("signal: read %s: %w", room, err)
	}
	return domain.TimelinePage{Messages: msgs}, nil
}

// errNotCached is a message the cache does not hold: Signal has no history to ask.
var errNotCached = errors.New("signal: that message is not in the cache")

// FetchEvent reads the cache.
func (a *Adapter) FetchEvent(ctx context.Context, room domain.RoomID, event domain.EventID) (domain.Message, error) {
	msg, ok, err := a.cache.MessageByID(ctx, room, event)
	if err != nil {
		return domain.Message{}, fmt.Errorf("signal: read %s: %w", event, err)
	}
	if !ok {
		return domain.Message{}, fmt.Errorf("%w (%s)", errNotCached, event)
	}
	return msg, nil
}

// MessageHistory returns a message's cached versions.
func (a *Adapter) MessageHistory(ctx context.Context, room domain.RoomID, event domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	revisions, err := a.cache.Revisions(ctx, room, event)
	if err != nil {
		return nil, domain.Deletion{}, fmt.Errorf("signal: versions of %s: %w", event, err)
	}
	return revisions, domain.Deletion{}, nil
}

// MarkRead reads a room through event, in kith (telling the phone comes next).
func (a *Adapter) MarkRead(ctx context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	at, err := readstate.TimeOf(ctx, a.cache, room, event, time.Now())
	if err != nil {
		return fmt.Errorf("signal: %w", err)
	}
	if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, at.UnixMilli()); err != nil {
		return fmt.Errorf("signal: save the read position of %s: %w", room, err)
	}
	a.recount(ctx, room)
	return nil
}

// MarkRoomsRead reads each room through its newest cached message.
func (a *Adapter) MarkRoomsRead(ctx context.Context, rooms []domain.RoomID, _ bool) (domain.ReadResult, error) {
	result, err := readstate.MarkRoomsRead(ctx, a.cache, rooms, func(room domain.RoomID, event domain.EventID) error {
		return a.MarkRead(ctx, room, event, false)
	})
	if err != nil {
		return result, fmt.Errorf("signal: %w", err)
	}
	return result, nil
}

// errNoMarkUnread: marking a chat unread is not supported yet.
var errNoMarkUnread = errors.New("signal: marking a chat unread is not supported yet")

// MarkRoomUnread is not supported yet.
func (a *Adapter) MarkRoomUnread(context.Context, domain.RoomID, bool) error { return errNoMarkUnread }
