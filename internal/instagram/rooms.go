package instagram

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Rooms are Instagram threads as one signed-in account sees them, answered from the
// cache. Each account is a space of its own, derived on read like Google Messages': the
// rooms' home in the rail, left by signing out.

// Rooms is the Instagram rooms in the cache.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("instagram: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolInstagram
	}), nil
}

// RefreshRooms answers from the cache: each connection lists the inbox when it
// connects, and changes arrive live.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) { return a.Rooms(ctx) }

// RecountUnread counts rooms' unread again, when the cache counts them differently
// (who is hidden changed).
func (a *Adapter) RecountUnread(ctx context.Context, rooms []domain.RoomID) {
	for _, room := range rooms {
		a.recount(ctx, room)
	}
}

// CachedUnread is the cached unread state of Instagram rooms, counted locally where the
// read position is known.
func (a *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	if a.cache == nil {
		return nil, nil
	}
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("instagram: read unread state: %w", err)
	}
	rows = slices.DeleteFunc(rows, func(u domain.Unread) bool {
		return domain.NetworkOf(string(u.RoomID)) != domain.ProtocolInstagram
	})
	counts, err := a.cache.CountUnreadAll(ctx, a.Me())
	if err != nil {
		return nil, fmt.Errorf("instagram: count unread: %w", err)
	}
	for i := range rows {
		if c, ok := counts[rows[i].RoomID]; ok {
			rows[i].Messages, rows[i].Mentions, rows[i].Counted = c.Messages, c.Mentions, true
		}
	}
	return rows, nil
}

// signedIn is each configured account with a kept session, and its own ID.
func (a *Adapter) signedIn() map[string]string {
	out := map[string]string{}
	for _, account := range a.accountsNow() {
		if s, ok, err := loadSession(a.secrets, account.Name); err == nil && ok {
			out[account.Name] = s.Self
		}
	}
	return out
}

// Spaces is a space per signed-in account, holding its rooms.
func (a *Adapter) Spaces(ctx context.Context) ([]domain.Space, error) {
	rooms, err := a.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	accounts := a.signedIn()
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
			ID: accountSpaceID(self), Name: "Instagram " + name, Children: children,
			// The rooms' home, not a space to file into; left by signing out.
			Bridge: domain.ProtocolInstagram, Original: true, Leaving: domain.LeftBySigningOut,
		})
	}
	return spaces, nil
}

// RefreshSpaces is Spaces: they are derived from the cache.
func (a *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) { return a.Spaces(ctx) }

// CanonicalParent is a room's account space.
func (a *Adapter) CanonicalParent(_ context.Context, room domain.RoomID) (domain.SpaceID, error) {
	self, _, ok := threadOf(room)
	if !ok {
		return "", nil
	}
	return accountSpaceID(self), nil
}

// Members is a room's cached members.
func (a *Adapter) Members(ctx context.Context, room domain.RoomID, limit int) ([]domain.Member, error) {
	if a.cache == nil {
		return nil, nil
	}
	members, err := a.cache.Members(ctx, room, limit)
	if err != nil {
		return nil, fmt.Errorf("instagram: read members of %s: %w", room, err)
	}
	return members, nil
}

// RefreshMembers answers from the cache: a thread comes with its people.
func (a *Adapter) RefreshMembers(ctx context.Context, room domain.RoomID) ([]domain.Member, error) {
	return a.Members(ctx, room, 0)
}

// MentionCandidates is a room's members, recent speakers and the often mentioned first.
func (a *Adapter) MentionCandidates(ctx context.Context, room domain.RoomID, limit int) ([]domain.Member, error) {
	members, err := a.Members(ctx, room, 0)
	if err != nil || len(members) == 0 || a.cache == nil {
		return domain.RankMembers(members, nil, nil, limit), err
	}
	speakers, err := a.cache.RecentSpeakers(ctx, room, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("instagram: rank recent speakers: %w", err)
	}
	mentioned, err := a.cache.FrequentMentions(ctx, room, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("instagram: rank mention history: %w", err)
	}
	return domain.RankMembers(members, speakers, mentioned, limit), nil
}

// DirectCandidates returns nobody: threads appear as Instagram lists them.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }

// RoomEncryption reports no room as encrypted: Instagram's DMs reach kith in the clear.
func (a *Adapter) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = false
	}
	return out, nil
}

// errNotCached is a message the cache does not hold.
var errNotCached = errors.New("instagram: that message is not in the cache")

// FetchEvent reads the cache.
func (a *Adapter) FetchEvent(ctx context.Context, room domain.RoomID, event domain.EventID) (domain.Message, error) {
	if a.cache != nil {
		msg, ok, err := a.cache.MessageByID(ctx, room, event)
		if err != nil {
			return domain.Message{}, fmt.Errorf("instagram: read %s: %w", event, err)
		}
		if ok {
			return msg, nil
		}
	}
	return domain.Message{}, fmt.Errorf("%w (%s)", errNotCached, event)
}

// MessageHistory returns a message's cached versions.
func (a *Adapter) MessageHistory(ctx context.Context, room domain.RoomID, event domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	if a.cache == nil {
		return nil, domain.Deletion{}, nil
	}
	revisions, err := a.cache.Revisions(ctx, room, event)
	if err != nil {
		return nil, domain.Deletion{}, fmt.Errorf("instagram: versions of %s: %w", event, err)
	}
	return revisions, domain.Deletion{}, nil
}

// errNoSuchAccountSpace is a space that is no Instagram account's.
var errNoSuchAccountSpace = fmt.Errorf("%w: no Instagram account here is that space", api.ErrNotOnNetwork)

// SignOut signs space's account out: the connection stops and the session is deleted
// here (the browser's session stays signed in). forget also drops its rooms.
func (a *Adapter) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	var account, self string
	for name, id := range a.signedIn() {
		if accountSpaceID(id) == space {
			account, self = name, id
		}
	}
	if account == "" {
		return errNoSuchAccountSpace
	}
	a.mu.Lock()
	c := a.conns[account]
	delete(a.conns, account)
	a.mu.Unlock()
	if c != nil {
		c.close()
	}
	a.keeping.Lock()
	err := a.secrets.DeleteSecret(sessionRef(account))
	a.keeping.Unlock()
	if err != nil {
		return fmt.Errorf("instagram: delete %s's session: %w", account, err)
	}
	a.status(Account{Name: account}, SignedOut, "signed out; run `kith login instagram "+account+"` to sign in again")
	if forget && a.cache != nil {
		if err := a.cache.ForgetOwned(ctx, owner(self)); err != nil {
			return fmt.Errorf("instagram: forget %s's rooms: %w", account, err)
		}
	}
	a.roomsChanged()
	return nil
}
