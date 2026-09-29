package matrix

import (
	"context"
	"slices"
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// selves are the configured identities ([[display.identity]]: each the MXIDs shown as
// one person). The one that lists the account is this person, the others the accounts
// bridges post as for them. A reload replaces them while counts run, hence the lock.
type selves struct {
	mu     sync.Mutex
	groups [][]string
}

// UseIdentities takes the configured identities: a message from any MXID of the one
// that lists this account is this person's own to the unread counts. Every room is
// recounted when they changed.
func (b *InProc) UseIdentities(ctx context.Context, identities [][]string) {
	b.selves.mu.Lock()
	changed := !slices.EqualFunc(b.selves.groups, identities, slices.Equal)
	b.selves.groups = identities
	b.selves.mu.Unlock()
	if changed {
		for _, room := range b.unread.known() {
			b.recount(ctx, room)
		}
	}
}

// me is every MXID that is this person: the account, and the MXIDs of its identity.
func (b *InProc) me() []string {
	account := b.accountID()
	me := []string{account}
	b.selves.mu.Lock()
	defer b.selves.mu.Unlock()
	for _, group := range b.selves.groups {
		if !slices.Contains(group, account) {
			continue
		}
		for _, id := range group {
			if !slices.Contains(me, id) {
				me = append(me, id)
			}
		}
	}
	return me
}

// known is every room the book holds.
func (u *unreadBook) known() []domain.RoomID {
	u.mu.Lock()
	defer u.mu.Unlock()
	rooms := make([]domain.RoomID, 0, len(u.rooms))
	for room := range u.rooms {
		rooms = append(rooms, room)
	}
	return rooms
}
