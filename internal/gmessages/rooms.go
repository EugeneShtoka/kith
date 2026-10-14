package gmessages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Rooms are Google Messages conversations as one paired phone sees them. Each account
// is a space of its own, derived on read like Messenger's: the rooms' home in the rail,
// left by signing out.

// Rooms is the Google Messages rooms in the cache.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("gmessages: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolGMessages
	}), nil
}

// RefreshRooms answers from the cache: the phone pushes conversation changes as they
// happen, and each connection lists the inbox when it connects.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) { return a.Rooms(ctx) }

// CachedUnread is the cached unread state of Google Messages rooms, counted locally where the
// read position is known.
func (a *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	if a.cache == nil {
		return nil, nil
	}
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("gmessages: read unread state: %w", err)
	}
	rows = slices.DeleteFunc(rows, func(u domain.Unread) bool {
		return domain.NetworkOf(string(u.RoomID)) != domain.ProtocolGMessages
	})
	counts, err := a.cache.CountUnreadAll(ctx, a.Me())
	if err != nil {
		return nil, fmt.Errorf("gmessages: count unread: %w", err)
	}
	for i := range rows {
		if c, ok := counts[rows[i].RoomID]; ok {
			rows[i].Messages, rows[i].Mentions, rows[i].Counted = c.Messages, c.Mentions, true
		}
	}
	return rows, nil
}

// signedIn is each configured account with a kept pairing, and its account ID part.
func (a *Adapter) signedIn() map[string]string {
	out := map[string]string{}
	for _, account := range a.accountsNow() {
		if s, ok, err := loadSession(a.secrets, account.Name); err == nil && ok {
			out[account.Name] = selfOf(s.PhoneID)
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
			ID: accountSpaceID(self), Name: "Google Messages " + name, Children: children,
			// The rooms' home, not a space to file into; left by signing out.
			Bridge: domain.ProtocolGMessages, Original: true, Leaving: domain.LeftBySigningOut,
		})
	}
	return spaces, nil
}

// RefreshSpaces is Spaces: they are derived from the cache.
func (a *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) { return a.Spaces(ctx) }

// CanonicalParent is a room's account space.
func (a *Adapter) CanonicalParent(_ context.Context, room domain.RoomID) (domain.SpaceID, error) {
	self, _, ok := conversationOf(room)
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
		return nil, fmt.Errorf("gmessages: read members of %s: %w", room, err)
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
		return nil, fmt.Errorf("gmessages: rank recent speakers: %w", err)
	}
	mentioned, err := a.cache.FrequentMentions(ctx, room, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("gmessages: rank mention history: %w", err)
	}
	return domain.RankMembers(members, speakers, mentioned, limit), nil
}

// DirectCandidates returns nobody: conversations appear as the phone sends them.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }

// RoomEncryption reports no room as encrypted: the phone holds whatever RCS encrypts,
// and kith sees it in the clear, as Messages for web does.
func (a *Adapter) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = false
	}
	return out, nil
}

// Timeline is a page of the cache: older messages are not fetched from the phone yet.
func (a *Adapter) Timeline(ctx context.Context, room domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	if a.cache == nil || from != "" {
		return domain.TimelinePage{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	msgs, err := a.cache.Messages(ctx, room, limit)
	if err != nil {
		return domain.TimelinePage{}, fmt.Errorf("gmessages: read %s: %w", room, err)
	}
	return domain.TimelinePage{Messages: msgs}, nil
}

// errNotCached is a message the cache does not hold, which cannot be asked for yet.
var errNotCached = errors.New("gmessages: that message is not in the cache")

// FetchEvent reads the cache.
func (a *Adapter) FetchEvent(ctx context.Context, room domain.RoomID, event domain.EventID) (domain.Message, error) {
	if a.cache != nil {
		msg, ok, err := a.cache.MessageByID(ctx, room, event)
		if err != nil {
			return domain.Message{}, fmt.Errorf("gmessages: read %s: %w", event, err)
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
		return nil, domain.Deletion{}, fmt.Errorf("gmessages: versions of %s: %w", event, err)
	}
	return revisions, domain.Deletion{}, nil
}

// errNoSuchAccountSpace is a space that is no Google Messages account's.
var errNoSuchAccountSpace = fmt.Errorf("%w: no Google Messages account here is that space", api.ErrNotOnNetwork)

// SignOut unpairs space's account: the phone forgets kith (when it can be reached), the
// connection stops and the pairing is deleted here. forget also drops its rooms.
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
	a.newSignIn(account) // connections on the old pairing are stale
	a.mu.Lock()
	c := a.conns[account]
	delete(a.conns, account)
	a.mu.Unlock()
	if c != nil {
		a.unpair(ctx, c)
		c.close()
	}
	a.keeping.Lock()
	err := a.secrets.DeleteSecret(sessionRef(account))
	a.keeping.Unlock()
	if err != nil {
		return fmt.Errorf("gmessages: delete %s's pairing: %w", account, err)
	}
	a.session(Account{Name: account}, SignedOut, "unpaired; run `kith login gmessages "+account+"` to pair again")
	if forget && a.cache != nil {
		if err := a.cache.ForgetOwned(ctx, owner(self)); err != nil {
			return fmt.Errorf("gmessages: forget %s's rooms: %w", account, err)
		}
	}
	a.roomsChanged()
	return nil
}

// unpair tells the phone to forget kith's pairing; a phone that cannot be reached keeps
// it until removed there (Messages → Device pairing).
func (a *Adapter) unpair(ctx context.Context, c *conn) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, unpairSpan)
	defer cancel()
	if err := a.guard("unpairing", func() error { return client.Unpair(ctx) }); err != nil {
		a.log.Warn("the phone did not take the unpairing; remove kith in Messages → Device pairing",
			"account", c.account.Name, "err", err)
	}
}

// unpairSpan bounds telling the phone to unpair.
const unpairSpan = 30 * time.Second
