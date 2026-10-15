package meta

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Rooms are Messenger threads as one account sees them. Each account is a space of its
// own, derived on read like Telegram's: the rooms' home in the rail, left by signing
// out.

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// roomID is a thread's room.
func roomID(self, thread int64) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolMessenger, itoa(self), itoa(thread)))
}

// eventID is a message in a thread.
func eventID(self, thread int64, mid string) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolMessenger, itoa(self), itoa(thread)+"/"+mid))
}

// accountSpaceID is the space an account is.
func accountSpaceID(self int64) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolMessenger, itoa(self), "account"))
}

// owner is the rooms one account writes.
func owner(self int64) domain.RoomOwner {
	return domain.AccountRooms(domain.ProtocolMessenger, itoa(self))
}

// threadOf is a room's account and thread key.
func threadOf(room domain.RoomID) (self, thread int64, ok bool) {
	id := domain.ParseID(string(room))
	if id.Network != domain.ProtocolMessenger {
		return 0, 0, false
	}
	self, err1 := strconv.ParseInt(id.Account, 10, 64)
	thread, err2 := strconv.ParseInt(id.Native, 10, 64)
	return self, thread, err1 == nil && err2 == nil
}

// noteToSelf is the name of the thread with yourself.
const noteToSelf = "Note to self"

// room is a thread as a kith room, named by the inbox's people.
func (in *inbox) room(t *thread) domain.Room {
	r := domain.Room{
		ID: roomID(in.self, t.key), Name: t.name, IsDirect: t.direct(),
		Membership: domain.MembershipJoin, Network: domain.ProtocolMessenger,
	}
	if t.direct() {
		// A one-to-one thread is named after its person, whatever its row says.
		r.Name = in.people[t.key]
		if t.key == in.self {
			r.Name = noteToSelf
		}
	}
	r.Members = append(r.Members, personID(in.self))
	for _, id := range in.membersOf(t) {
		r.Members = append(r.Members, personID(id))
	}
	return r
}

// members is a thread's people as room members, named where known.
func (in *inbox) roomMembers(t *thread) []domain.Member {
	ids := append([]int64{in.self}, in.membersOf(t)...)
	out := make([]domain.Member, 0, len(ids))
	for _, id := range ids {
		out = append(out, domain.Member{UserID: personID(id), DisplayName: in.people[id]})
	}
	return out
}

// Rooms is the Messenger rooms in the cache.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("meta: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolMessenger
	}), nil
}

// RefreshRooms answers from the cache: Meta pushes thread changes as they happen, so
// there is no listing to redo.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) { return a.Rooms(ctx) }

// RecountUnread counts rooms' unread again, when the cache counts them differently
// (who is hidden changed).
func (a *Adapter) RecountUnread(ctx context.Context, rooms []domain.RoomID) {
	for _, room := range rooms {
		a.recount(ctx, room)
	}
}

// CachedUnread is the cached unread state of Messenger rooms, counted locally where
// the read position is known.
func (a *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	if a.cache == nil {
		return nil, nil
	}
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("meta: read unread state: %w", err)
	}
	rows = slices.DeleteFunc(rows, func(u domain.Unread) bool {
		return domain.NetworkOf(string(u.RoomID)) != domain.ProtocolMessenger
	})
	counts, err := a.cache.CountUnreadAll(ctx, a.Me())
	if err != nil {
		return nil, fmt.Errorf("meta: count unread: %w", err)
	}
	for i := range rows {
		if c, ok := counts[rows[i].RoomID]; ok {
			rows[i].Messages, rows[i].Mentions, rows[i].Counted = c.Messages, c.Mentions, true
		}
	}
	return rows, nil
}

// signedIn is each configured account with a kept session, and its Meta ID.
func (a *Adapter) signedIn() map[string]int64 {
	out := map[string]int64{}
	for _, account := range a.accountsNow() {
		if s, ok, err := loadSession(a.secrets, account.Name); err == nil && ok {
			out[account.Name] = s.MetaID
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
			ID: accountSpaceID(self), Name: "Messenger " + name, Children: children,
			// The rooms' home, not a space to file into; left by signing out.
			Bridge: domain.ProtocolMessenger, Original: true, Leaving: domain.LeftBySigningOut,
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
		return nil, fmt.Errorf("meta: read members of %s: %w", room, err)
	}
	return members, nil
}

// RefreshMembers answers from the cache: Meta pushes member changes.
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
		return nil, fmt.Errorf("meta: rank recent speakers: %w", err)
	}
	mentioned, err := a.cache.FrequentMentions(ctx, room, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("meta: rank mention history: %w", err)
	}
	return domain.RankMembers(members, speakers, mentioned, limit), nil
}

// DirectCandidates returns nobody: threads appear as Meta sends them.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }

// RoomEncryption reports no room as encrypted: end-to-end encrypted chats are not shown yet.
func (a *Adapter) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = false
	}
	return out, nil
}

// Timeline is empty: older messages are not fetched from Meta yet; kith reads the cache.
func (a *Adapter) Timeline(context.Context, domain.RoomID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, nil
}

// errNotCached is a message the cache does not hold, which cannot be asked for yet.
var errNotCached = errors.New("meta: that message is not in the cache")

// FetchEvent reads the cache.
func (a *Adapter) FetchEvent(ctx context.Context, room domain.RoomID, event domain.EventID) (domain.Message, error) {
	if a.cache != nil {
		msg, ok, err := a.cache.MessageByID(ctx, room, event)
		if err != nil {
			return domain.Message{}, fmt.Errorf("meta: read %s: %w", event, err)
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
		return nil, domain.Deletion{}, fmt.Errorf("meta: versions of %s: %w", event, err)
	}
	return revisions, domain.Deletion{}, nil
}

// errNoSuchAccountSpace is a space that is no Messenger account's.
var errNoSuchAccountSpace = fmt.Errorf("%w: no Messenger account here is that space", api.ErrNotOnNetwork)

// SignOut forgets space's account: the connection stops and its session is deleted
// here. Meta's side of the session cannot be ended from kith (messagix has no way to);
// end it under Accounts Center → Where you're logged in. forget also drops its rooms.
func (a *Adapter) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	var account string
	var self int64
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
		return fmt.Errorf("meta: delete %s's session: %w", account, err)
	}
	a.session(Account{Name: account}, SignedOut, "signed out; run `kith login messenger "+account+"` to sign in again")
	if forget && a.cache != nil {
		if err := a.cache.ForgetOwned(ctx, owner(self)); err != nil {
			return fmt.Errorf("meta: forget %s's rooms: %w", account, err)
		}
	}
	a.roomsChanged()
	return nil
}
