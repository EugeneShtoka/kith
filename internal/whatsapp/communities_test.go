package whatsapp

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func group(jid string) types.JID { return types.NewJID(jid, types.GroupServer) }

// A community is a space holding its groups (the announcement group among them); the
// community itself is no room. A group in none is a plain room; a community the
// listing does not carry is named by asking.
func TestCommunitiesAreSpacesOfTheirGroups(t *testing.T) {
	t.Parallel()
	building, school := group("120363001"), group("120363002")
	groups := []*types.GroupInfo{
		{JID: building, GroupName: types.GroupName{Name: "Building"}, GroupParent: types.GroupParent{IsParent: true}},
		{JID: group("120363011"), GroupName: types.GroupName{Name: "Building announcements"},
			GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: building}, GroupIsDefaultSub: types.GroupIsDefaultSub{IsDefaultSubGroup: true}},
		{JID: group("120363012"), GroupName: types.GroupName{Name: "Parking"}, GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: building}},
		{JID: group("120363021"), GroupName: types.GroupName{Name: "Class 3B"}, GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: school}},
		{JID: group("120363099"), GroupName: types.GroupName{Name: "Family"}},
	}
	asked := []types.JID{}
	spaces, chats := communities(context.Background(), ownDigits, groups, func(_ context.Context, jid types.JID) string {
		asked = append(asked, jid)
		return "School"
	})

	if len(spaces) != 2 || spaces[0].Name != "Building" || spaces[1].Name != "School" {
		t.Fatalf("spaces = %+v, want Building and School", spaces)
	}
	if want := []domain.RoomID{roomID(ownDigits, group("120363011")), roomID(ownDigits, group("120363012"))}; !slices.Equal(spaces[0].Children, want) {
		t.Errorf("Building's rooms = %v, want its announcements and Parking", spaces[0].Children)
	}
	if spaces[0].ID != domain.SpaceID(roomID(ownDigits, building)) || spaces[0].Bridge != domain.ProtocolWhatsApp || !spaces[0].Managed() {
		t.Errorf("Building = %+v, want the community's ID, WhatsApp's to manage", spaces[0])
	}
	if !slices.Equal(asked, []types.JID{school}) {
		t.Errorf("asked for %v, want only the community the listing lacks", asked)
	}
	var names []string
	for _, g := range chats {
		names = append(names, g.Name)
	}
	if !slices.Equal(names, []string{"Building announcements", "Parking", "Class 3B", "Family"}) {
		t.Errorf("chats = %v, want every group but the community itself", names)
	}
}

// A listing's communities are cached with its groups, per account: another account's
// stay, and Matrix's are none of WhatsApp's. An account with rooms is a space too,
// after the communities. A room's community is its canonical parent, else its account.
func TestCommunitiesAreCachedPerAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, work := Account{Name: "home", Digits: ownDigits}, Account{Name: "work", Digits: "1500000001"}
	a, cache, _ := offline(t, home, work)
	if err := cache.SaveSpaces(ctx, domain.MatrixRooms, []domain.Space{{ID: "!work:x", Name: "Work"}}); err != nil {
		t.Fatal(err)
	}
	room := roomID(ownDigits, group("120363012"))
	building := domain.Space{ID: domain.SpaceID(roomID(ownDigits, group("120363001"))), Name: "Building", Children: []domain.RoomID{room}, Bridge: domain.ProtocolWhatsApp}
	theirs := domain.Space{ID: domain.SpaceID(roomID(work.Digits, group("120363005"))), Name: "Theirs", Bridge: domain.ProtocolWhatsApp}
	if err := a.saveListing(ctx, work, groupListing{spaces: []domain.Space{theirs}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.saveListing(ctx, home, groupListing{rooms: []domain.Room{{ID: room}}, spaces: []domain.Space{building}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ids := func() []domain.SpaceID {
		spaces, err := a.Spaces(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []domain.SpaceID
		for _, s := range spaces {
			out = append(out, s.ID)
		}
		return out
	}
	homeSpace := accountSpaceID(ownDigits) // work has no rooms, so no space
	if got := ids(); !slices.Equal(got, []domain.SpaceID{building.ID, theirs.ID, homeSpace}) {
		t.Errorf("Spaces = %v, want both accounts' communities, home's own space and no Matrix space", got)
	}
	if spaces, _ := a.Spaces(ctx); !spaces[0].Managed() {
		t.Error("a cached community reads back as a space to file rooms into")
	}
	// A community is left with its groups; an account's own space by unlinking it.
	if spaces, _ := a.Spaces(ctx); spaces[0].Leaving != domain.LeftWithRooms || spaces[2].Leaving != domain.LeftBySigningOut {
		t.Errorf("leaving a community %v, the account's space %v", spaces[0].Leaving, spaces[2].Leaving)
	}
	if parent, err := a.CanonicalParent(ctx, room); err != nil || parent != building.ID {
		t.Errorf("CanonicalParent = (%q, %v), want the community", parent, err)
	}

	// home leaves its community: its next listing has none, work's stays.
	if err := a.saveListing(ctx, home, groupListing{rooms: []domain.Room{{ID: room}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, []domain.SpaceID{theirs.ID, homeSpace}) {
		t.Errorf("after home's community went = %v, want work's and home's own space", got)
	}
	if parent, _ := a.CanonicalParent(ctx, room); parent != homeSpace {
		t.Errorf("CanonicalParent after leaving = %q, want the account's space", parent)
	}
}

// Each account with rooms is a space named after it holding every room it sees —
// groups and direct chats, not another account's.
func TestAnAccountIsTheSpaceOfItsRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workDigits = "1000000009"
	home, work := Account{Name: "home", Digits: ownDigits}, Account{Name: "work", Digits: workDigits}
	a, cache, _ := offline(t, home, work)
	homeGroup, homeDM := roomID(ownDigits, group("1203")), roomID(ownDigits, pn(danaPhone))
	workDM := roomID(workDigits, pn(danaPhone))
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: homeGroup}, {ID: homeDM, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, workDigits), []domain.Room{{ID: workDM, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	spaces, err := a.Spaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[domain.SpaceID]struct {
		name     string
		children []domain.RoomID
	}{
		accountSpaceID(ownDigits):  {"WhatsApp home", []domain.RoomID{homeGroup, homeDM}},
		accountSpaceID(workDigits): {"WhatsApp work", []domain.RoomID{workDM}},
	}
	if len(spaces) != len(want) {
		t.Fatalf("Spaces = %v, want one per account", spaces)
	}
	for _, s := range spaces {
		w, ok := want[s.ID]
		got := slices.Clone(s.Children)
		slices.Sort(got)
		slices.Sort(w.children)
		if !ok || s.Name != w.name || !slices.Equal(got, w.children) || !s.Managed() {
			t.Errorf("space %+v, want %q holding %v, the rooms' home", s, w.name, w.children)
		}
	}
	if parent, err := a.CanonicalParent(ctx, homeDM); err != nil || parent != accountSpaceID(ownDigits) {
		t.Errorf("CanonicalParent of a direct chat = (%q, %v), want home's space", parent, err)
	}
}

// A private chat is not left on WhatsApp, and a group is left only through its
// account's connection.
func TestOnlyAGroupIsLeft(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _, _ := offline(t, Account{Name: "home", Digits: ownDigits})
	if err := a.LeaveRoom(ctx, roomID(ownDigits, pn(danaPhone))); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("leaving a private chat: %v, want refused as not on the network", err)
	}
	if err := a.LeaveRoom(ctx, roomID(ownDigits, group("1203"))); !errors.Is(err, errNetworkOff) {
		t.Errorf("leaving a group offline: %v, want the network off", err)
	}
}

// An account is unlinked only through its connection, and only its own space is one.
func TestOnlyAConnectedAccountIsSignedOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _, _ := offline(t, Account{Name: "home", Digits: ownDigits})
	if err := a.SignOut(ctx, accountSpaceID("1500000009"), false); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("signing out a space no account is: %v, want refused", err)
	}
	if err := a.SignOut(ctx, accountSpaceID(ownDigits), false); !errors.Is(err, errNetworkOff) {
		t.Errorf("signing out an account offline: %v, want the network off", err)
	}
}
