package whatsapp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// listingEvery is how often one account's groups and channels are listed at most:
// WhatsApp answers listings that come faster with 429 rate-overlimit.
const listingEvery = 30 * time.Second

// untilListable is how long until an account may be listed again; 0 when it may now.
func (a *Adapter) untilListable(digits string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	last, ok := a.listedAt[digits]
	if !ok {
		return 0
	}
	return max(0, listingEvery-time.Since(last))
}

// accountRooms is an account's cached rooms that keep says to.
func (a *Adapter) accountRooms(ctx context.Context, account Account, keep func(domain.RoomID) bool) ([]domain.Room, error) {
	rooms, err := a.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)
	return slices.DeleteFunc(rooms, func(r domain.Room) bool { return !owner.Owns(r.ID) || !keep(r.ID) }), nil
}

// listChannels is the channels an account follows, as rooms. When WhatsApp will not
// say (it rate-limits), the cached ones stand in, and what was known of them stays:
// listed without them, the sweep would take their history.
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
