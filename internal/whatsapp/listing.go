package whatsapp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// listingEvery is the minimum interval between an account's listings: WhatsApp
// answers faster ones with 429 rate-overlimit.
const listingEvery = 30 * time.Second

// untilListable is the wait before an account may be listed again; 0 when now.
func (a *Adapter) untilListable(digits string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	last, ok := a.listedAt[digits]
	if !ok {
		return 0
	}
	return max(0, listingEvery-time.Since(last))
}

// accountRooms is an account's cached rooms filtered by keep.
func (a *Adapter) accountRooms(ctx context.Context, account Account, keep func(domain.RoomID) bool) ([]domain.Room, error) {
	rooms, err := a.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)
	return slices.DeleteFunc(rooms, func(r domain.Room) bool { return !owner.Owns(r.ID) || !keep(r.ID) }), nil
}

// listChannels lists followed channels as rooms. When rate-limited, cached channels
// stand in, or the sweep would drop their history.
func (a *Adapter) listChannels(ctx context.Context, account Account, client *whatsmeow.Client) ([]domain.Room, error) {
	followed, err := client.GetSubscribedNewsletters(ctx)
	if err == nil {
		rooms, known := channelRooms(account.Digits, followed)
		a.useChannels(account, known)
		return rooms, nil
	}
	a.log.Warn("listing the channels failed; keeping the cached ones", "account", account.Name, "err", err)
	cached, cerr := a.accountRooms(ctx, account, isChannel)
	if cerr != nil {
		return nil, fmt.Errorf("whatsapp: %s's channels: %w (and the cached ones: %w)", account.Name, err, cerr)
	}
	return cached, nil
}
