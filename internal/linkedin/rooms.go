package linkedin

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Rooms are LinkedIn conversations as one account sees them. Each account is a space of
// its own, derived on read like Messenger's: the rooms' home in the rail, left by
// signing out.

// Rooms is the LinkedIn rooms in the cache.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("linkedin: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolLinkedIn
	}), nil
}

// RefreshRooms answers from the cache: LinkedIn pushes conversation changes as they
// happen, and each connection lists what changed when it connects.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) { return a.Rooms(ctx) }

// RecountUnread counts rooms' unread again, when the cache counts them differently
// (who is hidden changed).
func (a *Adapter) RecountUnread(ctx context.Context, rooms []domain.RoomID) {
	for _, room := range rooms {
		a.recount(ctx, room)
	}
}

// CachedUnread is the cached unread state of LinkedIn rooms, counted locally where the
// read position is known.
func (a *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	if a.cache == nil {
		return nil, nil
	}
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("linkedin: read unread state: %w", err)
	}
	rows = slices.DeleteFunc(rows, func(u domain.Unread) bool {
		return domain.NetworkOf(string(u.RoomID)) != domain.ProtocolLinkedIn
	})
	counts, err := a.cache.CountUnreadAll(ctx, a.Me())
	if err != nil {
		return nil, fmt.Errorf("linkedin: count unread: %w", err)
	}
	for i := range rows {
		if c, ok := counts[rows[i].RoomID]; ok {
			rows[i].Messages, rows[i].Mentions, rows[i].Counted = c.Messages, c.Mentions, true
		}
	}
	return rows, nil
}

// signedIn is each configured account with a kept session, and its profile.
func (a *Adapter) signedIn() map[string]string {
	out := map[string]string{}
	for _, account := range a.accountsNow() {
		if s, ok, err := loadSession(a.secrets, account.Name); err == nil && ok {
			out[account.Name] = s.ProfileID
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
			ID: accountSpaceID(self), Name: "LinkedIn " + name, Children: children,
			// The rooms' home, not a space to file into; left by signing out.
			Bridge: domain.ProtocolLinkedIn, Original: true, Leaving: domain.LeftBySigningOut,
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
		return nil, fmt.Errorf("linkedin: read members of %s: %w", room, err)
	}
	return members, nil
}

// RefreshMembers answers from the cache: a conversation always comes with its people.
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
		return nil, fmt.Errorf("linkedin: rank recent speakers: %w", err)
	}
	mentioned, err := a.cache.FrequentMentions(ctx, room, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("linkedin: rank mention history: %w", err)
	}
	return domain.RankMembers(members, speakers, mentioned, limit), nil
}

// DirectCandidates returns nobody: conversations appear as LinkedIn sends them.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }

// RoomEncryption reports no room as encrypted: LinkedIn has no end-to-end encryption.
func (a *Adapter) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = false
	}
	return out, nil
}

// errNotCached is a message the cache does not hold, which cannot be asked for yet.
var errNotCached = errors.New("linkedin: that message is not in the cache")

// FetchEvent reads the cache.
func (a *Adapter) FetchEvent(ctx context.Context, room domain.RoomID, event domain.EventID) (domain.Message, error) {
	if a.cache != nil {
		msg, ok, err := a.cache.MessageByID(ctx, room, event)
		if err != nil {
			return domain.Message{}, fmt.Errorf("linkedin: read %s: %w", event, err)
		}
		if ok {
			return msg, nil
		}
	}
	return domain.Message{}, fmt.Errorf("%w (%s): %w", errNotCached, event, api.ErrNoSuchMessage)
}

// MessageHistory returns a message's cached versions.
func (a *Adapter) MessageHistory(ctx context.Context, room domain.RoomID, event domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	if a.cache == nil {
		return nil, domain.Deletion{}, nil
	}
	revisions, err := a.cache.Revisions(ctx, room, event)
	if err != nil {
		return nil, domain.Deletion{}, fmt.Errorf("linkedin: versions of %s: %w", event, err)
	}
	return revisions, domain.Deletion{}, nil
}

// errNoSuchAccountSpace is a space that is no LinkedIn account's.
var errNoSuchAccountSpace = fmt.Errorf("%w: no LinkedIn account here is that space", api.ErrNotOnNetwork)

// SignOut forgets space's account here: the connection stops and its session is
// deleted. LinkedIn is not told: signing out there would end the session the cookies
// came from, which may be your browser's. forget also drops its rooms.
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
	a.newSignIn(account) // connections on the old session are stale
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
		return fmt.Errorf("linkedin: delete %s's session: %w", account, err)
	}
	a.session(Account{Name: account}, SignedOut, "signed out; run `kith login linkedin "+account+"` to sign in again")
	if forget && a.cache != nil {
		if err := a.cache.ForgetOwned(ctx, owner(self)); err != nil {
			return fmt.Errorf("linkedin: forget %s's rooms: %w", account, err)
		}
	}
	a.roomsChanged()
	return nil
}
