package matrix

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// roomNameWorkers bounds concurrent room-name lookups during RefreshRooms.
const roomNameWorkers = 8

// Rooms returns the cached room list, or a live RefreshRooms without a cache.
func (b *InProc) Rooms(ctx context.Context) ([]domain.Room, error) {
	if b.cache == nil {
		return b.RefreshRooms(ctx)
	}
	rooms, err := b.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("matrix: read cached rooms: %w", err)
	}
	// The cache holds every network's rooms; the router adds the others' itself.
	return slices.DeleteFunc(rooms, func(r domain.Room) bool { return !domain.MatrixRooms.Owns(r.ID) }), nil
}

// RefreshRooms fetches joined rooms with their names, DM flags and member names,
// caches and returns them. Members are reused from the cache for known rooms.
func (b *InProc) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	asked := time.Now()
	resp, err := b.client.JoinedRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("matrix: joined rooms: %w", err)
	}
	peers, directRead := b.directPeers(ctx)
	known := b.cachedRoomIndex(ctx)
	// Spaces come back from /joined_rooms too; drop them using the cached space list
	// (asking per room would be a state call each). On a cold cache Cache.Rooms
	// filters them instead.
	spaces := b.cachedSpaceIDs(ctx)
	joined := make([]id.RoomID, 0, len(resp.JoinedRooms))
	for _, roomID := range resp.JoinedRooms {
		if !spaces[domain.RoomID(roomID)] && !b.left.since(domain.RoomID(roomID), asked) {
			joined = append(joined, roomID)
		}
	}
	rooms := make([]domain.Room, len(joined))
	fanOut(len(joined), func(i int) {
		roomID := joined[i]
		peer, isDM := peers[roomID]
		prev, seen := known[roomID]
		if !directRead && seen {
			// m.direct could not be read: which rooms are DMs is what the cache knew.
			// It decides what an agent may see (`except = ["dm"]`), so an unread list
			// must not read as "no DMs".
			isDM = prev.IsDirect
		}
		name, members := b.roomName(ctx, roomID, peer, prev, seen)
		rooms[i] = domain.Room{
			ID:       domain.RoomID(roomID),
			Name:     name,
			Topic:    b.roomTopic(ctx, roomID),
			IsDirect: isDM,
			Members:  members,
		}
	})
	domain.SortRooms(rooms)
	if b.cache != nil {
		if err := b.cache.SaveRooms(ctx, domain.MatrixRooms, rooms); err != nil {
			return nil, fmt.Errorf("matrix: cache rooms: %w", err)
		}
	}
	return rooms, nil
}

// cachedRoomIndex maps cached rooms by ID; empty without a readable cache.
func (b *InProc) cachedRoomIndex(ctx context.Context) map[id.RoomID]domain.Room {
	out := map[id.RoomID]domain.Room{}
	if b.cache == nil {
		return out
	}
	prev, err := b.cache.Rooms(ctx)
	if err != nil {
		b.warnIf(ctx, err, "read cached rooms")
		return out
	}
	for i := range prev {
		out[id.RoomID(prev[i].ID)] = prev[i]
	}
	return out
}

// maxHeroes bounds how many member names a computed room name lists.
const maxHeroes = 5

// roomName resolves a room's label: m.room.name, the DM peer's name, the canonical
// alias, then a name built from members ("heroes"). It also returns member names
// (domain.Room.Members). name is "" only when the member list is unavailable.
func (b *InProc) roomName(ctx context.Context, roomID id.RoomID, peer id.UserID, prev domain.Room, known bool) (name string, members []string) {
	shown, refetch := reusableMembers(prev, known)
	extra, ok := 0, true
	if refetch {
		shown, extra, ok = b.heroMembers(ctx, roomID)
	}

	// A name that cannot be read keeps the cached one: `room:<name>` entries decide
	// what an agent may see, so a failed read must not rename the room out of them.
	var rn event.RoomNameEventContent
	err := b.client.StateEvent(ctx, roomID, event.StateRoomName, "", &rn)
	switch {
	case err == nil && rn.Name != "":
		return rn.Name, shown
	case err != nil && !errors.Is(err, mautrix.MNotFound) && known && prev.Name != "":
		b.warnIf(ctx, err, "read m.room.name; keeping the cached name", "room", roomID)
		return prev.Name, prev.Members
	}
	if peer != "" {
		// A DM's label and member list must agree for the display layer's match.
		p, read := b.userDisplayName(ctx, peer)
		if !read && known && prev.Name != "" {
			return prev.Name, prev.Members
		}
		return p, []string{p}
	}
	var alias event.CanonicalAliasEventContent
	if err := b.client.StateEvent(ctx, roomID, event.StateCanonicalAlias, "", &alias); err == nil && alias.Alias != "" {
		return string(alias.Alias), shown
	}
	// Hero-named: reuse the cached label when we did not refetch.
	if !refetch {
		return prev.Name, shown
	}
	if !ok {
		return "", nil // member fetch failed: caller falls back to the room ID
	}
	if len(shown) == 0 {
		return "Empty room", nil
	}
	return domain.FormatHeroes(shown, extra), shown
}

// roomTopic reads m.room.topic; "" when absent or unreadable. Fetched every refresh
// (cheap, and topics must be current), unlike members.
func (b *InProc) roomTopic(ctx context.Context, roomID id.RoomID) string {
	var topic event.TopicEventContent
	if err := b.client.StateEvent(ctx, roomID, event.StateTopic, "", &topic); err != nil {
		b.debugIf(ctx, err, "read room topic", "room", roomID) // most rooms have none (M_NOT_FOUND)
		return ""
	}
	return topic.Topic
}

// reusableMembers reuses a known room's non-empty cached members; otherwise refetch.
func reusableMembers(prev domain.Room, known bool) (members []string, refetch bool) {
	if known && len(prev.Members) > 0 {
		return prev.Members, false
	}
	return nil, true
}

// heroMembers lists joined members except us, sorted, truncated to maxHeroes with the
// remainder as extra. ok is false only when the fetch fails.
func (b *InProc) heroMembers(ctx context.Context, roomID id.RoomID) (names []string, extra int, ok bool) {
	resp, err := b.client.JoinedMembers(ctx, roomID)
	if err != nil {
		b.warnIf(ctx, err, "fetch joined members for the room name", "room", roomID)
		return nil, 0, false
	}
	names = make([]string, 0, len(resp.Joined))
	for userID, member := range resp.Joined {
		if userID == b.client.UserID {
			continue
		}
		if member.DisplayName != "" {
			names = append(names, member.DisplayName)
		} else {
			names = append(names, userID.Localpart())
		}
	}
	sort.Strings(names) // deterministic hero selection and ordering
	if len(names) > maxHeroes {
		return names[:maxHeroes], len(names) - maxHeroes, true
	}
	return names, 0, true
}

// userDisplayName returns a user's display name, else their localpart.
func (b *InProc) userDisplayName(ctx context.Context, user id.UserID) (name string, read bool) {
	profile, err := b.client.GetProfile(ctx, user)
	if err == nil && profile.DisplayName != "" {
		return profile.DisplayName, true
	}
	// Many servers refuse profiles of strangers; the localpart stands in.
	b.debugIf(ctx, err, "get profile", "user", user)
	return user.Localpart(), err == nil
}

// directPeers maps each DM room (from m.direct) to its other participant; read is
// false when m.direct could not be read (an account with none yet reads as empty).
func (b *InProc) directPeers(ctx context.Context) (peers map[id.RoomID]id.UserID, read bool) {
	var direct map[id.UserID][]id.RoomID
	if err := b.client.GetAccountData(ctx, "m.direct", &direct); err != nil {
		if errors.Is(err, mautrix.MNotFound) {
			return nil, true
		}
		b.warnIf(ctx, err, "read m.direct")
		return nil, false
	}
	peers = make(map[id.RoomID]id.UserID)
	for user, roomIDs := range direct {
		for _, roomID := range roomIDs {
			peers[roomID] = user
		}
	}
	return peers, true
}

// onMember keeps cached membership (joined only) current from the sync stream, so
// the mention dropdown, Space.Keeper and name resolution need no per-room fetch.
func (b *InProc) onMember(ctx context.Context, evt *event.Event) {
	if b.cache == nil || evt.StateKey == nil {
		return
	}
	// Joined rooms only: invite/leave sections would register rooms we are not in.
	if evt.Mautrix.EventSource&event.SourceJoin == 0 {
		return
	}
	content, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok {
		return // unparsed content is not a membership claim worth acting on
	}
	roomID, userID := domain.RoomID(evt.RoomID), *evt.StateKey
	if content.Membership != event.MembershipJoin {
		b.warnIf(ctx, b.cache.RemoveMember(ctx, roomID, userID), "cache member removal", "room", roomID, "user", userID)
		b.names.rename(roomID, userID, "")
		return
	}
	b.warnIf(ctx, b.cache.SaveMember(ctx, roomID, domain.Member{UserID: userID, DisplayName: content.Displayname}),
		"cache member", "room", roomID, "user", userID)
	b.names.rename(roomID, userID, content.Displayname)
}

// senderName resolves a sender's room display name, "" when unknown.
func (b *InProc) senderName(ctx context.Context, roomID domain.RoomID, sender string) string {
	return b.roomMemberNames(ctx, roomID)[sender]
}

// roomMemberNames returns a room's user-ID→display-name map, memoized. A failed
// fetch is not memoized, so it retries.
func (b *InProc) roomMemberNames(ctx context.Context, roomID domain.RoomID) map[string]string {
	if cached, ok := b.names.get(roomID); ok {
		return cached
	}
	seen := b.names.seen(roomID)
	resp, err := b.client.JoinedMembers(ctx, id.RoomID(roomID))
	if err != nil {
		// Not memoized, so the next call retries; names show as IDs meanwhile.
		b.warnIf(ctx, err, "fetch joined members", "room", roomID)
		return nil
	}
	names := make(map[string]string, len(resp.Joined))
	for userID, member := range resp.Joined {
		if member.DisplayName != "" {
			names[string(userID)] = member.DisplayName
		}
	}
	b.names.putAt(roomID, seen, names)
	return names
}
