package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An account listed moments ago is not listed again for a client's refresh: the
// cache answers (WhatsApp refuses listings that come faster). Changes arriving
// meanwhile wait for one listing, however many there are.
func TestAListingIsNotRepeatedTooSoon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
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
	// No client at all: a listing would fail; the cache must answer.
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

// When WhatsApp will not list the channels, the cached ones stand in, with what was
// known of them: a listing without them would sweep their history.
func TestARefusedChannelListingKeepsTheChannels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
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

// A direct chat's or channel's members are what is known: refreshing them lists nothing.
func TestRefreshingAChatsMembersListsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
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

// One account WhatsApp will not list (here: never connected) answers from the cache,
// direct chats included, and does not take the other account's rooms with it.
func TestOneAccountsFailedListingKeepsEveryAccountsRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ilDigits = "972000000009"
	bg, il := Account{Name: "bg", Digits: ownDigits}, Account{Name: "il", Digits: ilDigits}
	a, cache, store := offline(t, bg, il)
	bgGroup := roomID(ownDigits, types.NewJID("1203", types.GroupServer))
	bgDM := roomID(ownDigits, pn(danaPhone))
	ilDM := roomID(ilDigits, pn(danaPhone))
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: bgGroup}, {ID: bgDM, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ilDigits), []domain.Room{{ID: ilDM, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	a.clients[ownDigits] = linkedClient(t, store, ownDigits)
	a.clients[ilDigits] = linkedClient(t, store, ilDigits)
	a.mu.Lock()
	a.listedAt[ownDigits] = time.Now() // bg answers from the cache; il's listing fails
	a.mu.Unlock()

	rooms, err := a.RefreshRooms(ctx)
	if err != nil {
		t.Fatalf("RefreshRooms with one account failing: %v", err)
	}
	got := map[domain.RoomID]bool{}
	for _, r := range rooms {
		got[r.ID] = true
	}
	for _, want := range []domain.RoomID{bgGroup, bgDM, ilDM} {
		if !got[want] {
			t.Errorf("RefreshRooms = %v, missing %s", rooms, want)
		}
	}
}
