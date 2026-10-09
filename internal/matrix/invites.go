package matrix

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// CachedInvites returns the cached pending invitations.
func (b *InProc) CachedInvites(ctx context.Context) ([]domain.Room, error) {
	return fromCache(b, "read cached invites", func(c *db.Cache) ([]domain.Room, error) {
		return c.Invites(ctx)
	})
}

// Invites streams the complete set of pending invitations on each change.
func (b *InProc) Invites() <-chan []domain.Room { return b.out.invites }

// JoinRoom joins a room by ID or alias (also how an invitation is accepted). via are
// routing servers, needed when this homeserver has no member of the room.
func (b *InProc) JoinRoom(ctx context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error) {
	var req *mautrix.ReqJoinRoom
	if len(via) > 0 {
		req = &mautrix.ReqJoinRoom{Via: via}
	}
	resp, err := b.client.JoinRoom(ctx, roomIDOrAlias, req)
	if err != nil {
		return "", fmt.Errorf("matrix: join %s: %w", roomIDOrAlias, err)
	}
	return domain.RoomID(resp.RoomID), nil
}

// LeaveRoom leaves a room; rejecting an invitation is the same call. The room goes from
// the cache at once, not when a sync says so: a room list read before then would
// bring it back.
func (b *InProc) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	if _, err := b.client.LeaveRoom(ctx, id.RoomID(roomID)); err != nil {
		return fmt.Errorf("matrix: leave %s: %w", roomID, err)
	}
	b.left.mark(roomID, time.Now())
	if b.cache != nil {
		if err := b.cache.ForgetRooms(ctx, []domain.RoomID{roomID}); err != nil {
			b.warnIf(ctx, err, "forget a room left", "room", roomID)
		}
	}
	if b.onRoomsStale != nil {
		b.onRoomsStale()
	}
	return nil
}

// leftRooms is when rooms were left from here.
type leftRooms struct {
	mu sync.Mutex
	at map[domain.RoomID]time.Time
}

// leftKept is how long a leave is remembered: longer than any refresh takes.
const leftKept = 10 * time.Minute

func (l *leftRooms) mark(room domain.RoomID, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at == nil {
		l.at = map[domain.RoomID]time.Time{}
	}
	for r, when := range l.at {
		if at.Sub(when) > leftKept {
			delete(l.at, r)
		}
	}
	l.at[room] = at
}

// since reports whether room was left after since: a room list asked for before then
// still names it.
func (l *leftRooms) since(room domain.RoomID, since time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	when, ok := l.at[room]
	return ok && !when.Before(since)
}

// seedInvites primes the invite set from the cache, so the first sync publishes only
// real changes.
func (b *InProc) seedInvites(ctx context.Context) {
	cached, err := b.CachedInvites(ctx)
	if err != nil {
		// The first sync then republishes every invite; nothing is lost.
		b.warnIf(ctx, err, "seed invites from the cache")
		return
	}
	b.invites.mu.Lock()
	for i := range cached {
		b.invites.rooms[cached[i].ID] = cached[i]
	}
	b.invites.mu.Unlock()
}

// syncInvites folds a sync's invite section onto the pending set and publishes and
// caches the whole set when it changes. An incremental sync is a delta: an invite is
// dropped only when the room shows up in join or leave. An initial sync (no since)
// is a census and replaces the set.
func (b *InProc) syncInvites(ctx context.Context, resp *mautrix.RespSync, since string) {
	invites, changed := b.foldInvites(resp, since == "")
	if !changed {
		return
	}
	if b.cache != nil {
		b.warnIf(ctx, b.cache.SaveInvites(ctx, invites), "cache invites")
	}
	emit(&b.out, b.out.invites, invites)
}

// foldInvites applies one sync response to the pending set, returning it sorted and
// whether the set of room IDs changed (a renamed invite does not republish).
func (b *InProc) foldInvites(resp *mautrix.RespSync, census bool) ([]domain.Room, bool) {
	b.invites.mu.Lock()
	defer b.invites.mu.Unlock()

	next := make(map[domain.RoomID]domain.Room, len(b.invites.rooms)+len(resp.Rooms.Invite))
	if !census {
		maps.Copy(next, b.invites.rooms)
		for roomID := range resp.Rooms.Join {
			delete(next, domain.RoomID(roomID))
		}
		for roomID := range resp.Rooms.Leave {
			delete(next, domain.RoomID(roomID))
		}
	}
	for roomID, ir := range resp.Rooms.Invite {
		if ir == nil {
			continue
		}
		next[domain.RoomID(roomID)] = b.toInvite(roomID, ir)
	}

	changed := len(next) != len(b.invites.rooms)
	if !changed {
		for roomID := range next {
			if _, ok := b.invites.rooms[roomID]; !ok {
				changed = true
				break
			}
		}
	}
	if !changed {
		return nil, false
	}
	b.invites.rooms = next

	invites := slices.Collect(maps.Values(next))
	domain.SortRooms(invites)
	return invites, true
}

// toInvite builds a Room from an invite's stripped state (name, alias, DM flag, inviter).
func (b *InProc) toInvite(roomID id.RoomID, ir *mautrix.SyncInvitedRoom) domain.Room {
	room := domain.Room{ID: domain.RoomID(roomID), Membership: domain.MembershipInvite}
	me := b.client.UserID
	for _, evt := range ir.State.Events {
		// Stripped state has its class unset; ParseRaw refuses already-parsed content.
		evt.Type.Class = event.StateEventType
		if evt.Content.Parsed == nil {
			if err := evt.Content.ParseRaw(evt.Type); err != nil {
				// A malformed stripped-state event: the invite shows without it.
				b.log().Debug("parse invite state failed", "room", roomID, "type", evt.Type.Type, "err", err)
				continue
			}
		}
		switch evt.Type {
		case event.StateRoomName:
			if name, ok := evt.Content.Parsed.(*event.RoomNameEventContent); ok {
				room.Name = name.Name
			}
		case event.StateCanonicalAlias:
			// A named room wins; an alias is a better label than a bare room ID.
			if alias, ok := evt.Content.Parsed.(*event.CanonicalAliasEventContent); ok && room.Name == "" {
				room.Name = string(alias.Alias)
			}
		case event.StateMember:
			member, ok := evt.Content.Parsed.(*event.MemberEventContent)
			if !ok {
				continue
			}
			// Our own member event: sender is the inviter, is_direct marks a DM.
			if evt.GetStateKey() == me.String() && member.Membership == event.MembershipInvite {
				room.InvitedBy = evt.Sender.String()
				room.IsDirect = member.IsDirect
			} else if member.Displayname != "" {
				room.Members = append(room.Members, member.Displayname)
			}
		}
	}
	if room.Name == "" {
		room.Name = b.inviteFallbackName(ir, room.InvitedBy)
	}
	return room
}

// inviteFallbackName labels a nameless invite (usually a DM) by the inviter's display
// name, else their MXID.
func (b *InProc) inviteFallbackName(ir *mautrix.SyncInvitedRoom, invitedBy string) string {
	if invitedBy == "" {
		return ""
	}
	for _, evt := range ir.State.Events {
		if evt.Type != event.StateMember || evt.GetStateKey() != invitedBy {
			continue
		}
		if member, ok := evt.Content.Parsed.(*event.MemberEventContent); ok && member.Displayname != "" {
			return member.Displayname
		}
	}
	return invitedBy
}
