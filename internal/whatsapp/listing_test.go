package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Within listingEvery a client's refresh reads the cache, and changes coalesce into
// one trailing listing.
func TestAListingIsNotRepeatedTooSoon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, _ := offline(t, account)
	room := roomID(ownDigits, types.NewJID("1203", types.GroupServer))
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: room, Name: "Family"}}); err != nil {
		t.Fatal(err)
	}
	if wait := a.untilListable(ownDigits); wait != 0 {
		t.Fatalf("an account never listed waits %v", wait)
	}
	a.mu.Lock()
	a.listedAt[ownDigits] = time.Now()
	a.mu.Unlock()
	if wait := a.untilListable(ownDigits); wait <= 0 || wait > listingEvery {
		t.Fatalf("just listed: wait = %v, want up to %v", wait, listingEvery)
	}
	// With no client a listing would fail; the cache must answer.
	rooms, err := a.refreshAccount(ctx, account, nil, true)
	if err != nil || len(rooms) != 1 || rooms[0].ID != room {
		t.Errorf("a refresh just after a listing = (%v, %v), want the cached room", rooms, err)
	}

	a.refreshLater(account, nil)
	a.refreshLater(account, nil)
	a.mu.Lock()
	pending := a.trailing[ownDigits]
	a.mu.Unlock()
	if !pending {
		t.Error("a change just after a listing left no listing waiting")
	}
}

// A refused channel listing keeps the cached channels, or the sweep would drop them.
func TestARefusedChannelListingKeepsTheChannels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits) // never connected: every query fails
	weather := roomID(ownDigits, newsletter("1201"))
	group := roomID(ownDigits, types.NewJID("1203", types.GroupServer))
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: weather, Name: "Weather"}, {ID: group}}); err != nil {
		t.Fatal(err)
	}
	_, known := channelRooms(ownDigits, []*types.NewsletterMetadata{followed("1201", "Weather", types.NewsletterRoleAdmin)})
	a.useChannels(account, known)

	rooms, err := a.listChannels(ctx, account, client)
	if err != nil || len(rooms) != 1 || rooms[0].ID != weather {
		t.Fatalf("listChannels refused = (%v, %v), want the cached channel alone", rooms, err)
	}
	if err := a.mayPost(weather); err != nil {
		t.Errorf("what was known of the channel was lost: %v", err)
	}
}

// Refreshing a direct chat's or channel's members makes no request.
func TestRefreshingAChatsMembersListsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, _ := offline(t, account)
	if err := cache.SaveMembers(ctx, danaChat, []domain.Member{{UserID: "whatsapp:" + danaPhone + "@s.whatsapp.net", DisplayName: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	members, err := a.RefreshMembers(ctx, danaChat)
	if err != nil || len(members) != 1 {
		t.Errorf("RefreshMembers(direct chat) = (%v, %v), want the cached member", members, err)
	}
	if wait := a.untilListable(ownDigits); wait != 0 {
		t.Error("a member refresh counted as a listing")
	}
}

// A failing account answers from the cache without dropping other accounts' rooms.
func TestOneAccountsFailedListingKeepsEveryAccountsRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workDigits = "1000000009"
	home, work := Account{Name: "home", Digits: ownDigits}, Account{Name: "work", Digits: workDigits}
	a, cache, store := offline(t, home, work)
	homeGroup := roomID(ownDigits, types.NewJID("1203", types.GroupServer))
	homeDM := roomID(ownDigits, pn(danaPhone))
	workDM := roomID(workDigits, pn(danaPhone))
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: homeGroup}, {ID: homeDM, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, workDigits), []domain.Room{{ID: workDM, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	a.clients[ownDigits] = linkedClient(t, store, ownDigits)
	a.clients[workDigits] = linkedClient(t, store, workDigits)
	a.mu.Lock()
	a.listedAt[ownDigits] = time.Now() // home answers from the cache; work's listing fails
	a.mu.Unlock()

	rooms, err := a.RefreshRooms(ctx)
	if err != nil {
		t.Fatalf("RefreshRooms with one account failing: %v", err)
	}
	got := map[domain.RoomID]bool{}
	for _, r := range rooms {
		got[r.ID] = true
	}
	for _, want := range []domain.RoomID{homeGroup, homeDM, workDM} {
		if !got[want] {
			t.Errorf("RefreshRooms = %v, missing %s", rooms, want)
		}
	}
}
