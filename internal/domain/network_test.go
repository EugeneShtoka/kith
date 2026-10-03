package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Invented IDs in WhatsApp's shapes: an account, a group, a phone chat, a LID chat, a
// message.
const (
	waAccount = "359000000001"
	waGroup   = "120363000000000001@g.us"
	waPhone   = "972500000002@s.whatsapp.net"
	waLID     = "100000000000003@lid"
	waMessage = "3EB0C0FFEE0000000001"
)

func TestIDsSayTheirNetworkAndAccount(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		id         string
		want       domain.ID
		room, user bool
		short      string
	}{
		"matrix room":         {"!abc:example.org", domain.ID{Network: domain.ProtocolMatrix, Native: "!abc:example.org"}, true, false, ""},
		"matrix v12 room":     {"!31hneApxJ_1o-63DmFrpeqnkFfWppnzWso1JvH3ogLM", domain.ID{Network: domain.ProtocolMatrix, Native: "!31hneApxJ_1o-63DmFrpeqnkFfWppnzWso1JvH3ogLM"}, true, false, ""},
		"matrix user":         {"@dana:example.org", domain.ID{Network: domain.ProtocolMatrix, Native: "@dana:example.org"}, false, true, "dana"},
		"matrix event":        {"$ev:example.org", domain.ID{Network: domain.ProtocolMatrix, Native: "$ev:example.org"}, false, false, ""},
		"bridged ghost":       {"@whatsapp_447000000004:example.org", domain.ID{Network: domain.ProtocolMatrix, Native: "@whatsapp_447000000004:example.org"}, false, true, "whatsapp_447000000004"},
		"whatsapp group":      {"whatsapp:" + waAccount + "/" + waGroup, domain.ID{Network: domain.ProtocolWhatsApp, Account: waAccount, Native: waGroup}, true, false, ""},
		"whatsapp dm":         {"whatsapp:" + waAccount + "/" + waPhone, domain.ID{Network: domain.ProtocolWhatsApp, Account: waAccount, Native: waPhone}, true, false, ""},
		"whatsapp message":    {"whatsapp:" + waAccount + "/" + waMessage, domain.ID{Network: domain.ProtocolWhatsApp, Account: waAccount, Native: waMessage}, false, false, ""},
		"whatsapp phone user": {"whatsapp:" + waPhone, domain.ID{Network: domain.ProtocolWhatsApp, Native: waPhone}, false, true, "+972500000002"},
		"whatsapp lid user":   {"whatsapp:" + waLID, domain.ID{Network: domain.ProtocolWhatsApp, Native: waLID}, false, true, "100000000000003"},
		"slack channel":       {"slack:T0000000001/C0000000002", domain.ID{Network: domain.ProtocolSlack, Account: "T0000000001", Native: "C0000000002"}, true, false, ""},
		"slack dm":            {"slack:T0000000001/D0000000004", domain.ID{Network: domain.ProtocolSlack, Account: "T0000000001", Native: "D0000000004"}, true, false, ""},
		"slack message":       {"slack:T0000000001/C0000000002/1700000000.000100", domain.ID{Network: domain.ProtocolSlack, Account: "T0000000001", Native: "C0000000002/1700000000.000100"}, false, false, ""},
		"slack user":          {"slack:T0000000001.U0000000003", domain.ID{Network: domain.ProtocolSlack, Native: "T0000000001.U0000000003"}, false, true, ""},
		// Not a network kith reaches: a word with a colon is Matrix's, and names nothing.
		"unknown prefix": {"room:Standup", domain.ID{Network: domain.ProtocolMatrix, Native: "room:Standup"}, false, false, "room"},
		"empty":          {"", domain.ID{Network: domain.ProtocolMatrix}, false, false, ""},
		"bare prefix":    {"whatsapp:", domain.ID{Network: domain.ProtocolWhatsApp}, false, false, ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := domain.ParseID(tc.id)
			if got != tc.want {
				t.Fatalf("ParseID(%q) = %+v, want %+v", tc.id, got, tc.want)
			}
			if domain.NetworkOf(tc.id) != tc.want.Network {
				t.Errorf("NetworkOf(%q) = %q", tc.id, domain.NetworkOf(tc.id))
			}
			if domain.IsRoomID(tc.id) != tc.room {
				t.Errorf("IsRoomID(%q) = %v, want %v", tc.id, !tc.room, tc.room)
			}
			if domain.IsUserID(tc.id) != tc.user {
				t.Errorf("IsUserID(%q) = %v, want %v", tc.id, !tc.user, tc.user)
			}
			if tc.user {
				if got := domain.ShortName(tc.id); got != tc.short {
					t.Errorf("ShortName(%q) = %q, want %q", tc.id, got, tc.short)
				}
			}
		})
	}
}

// The account is what tells two accounts' views of one group, and of one message,
// apart; a person is the same person from either.
func TestNativeIDsKeepAccountsApart(t *testing.T) {
	t.Parallel()

	for _, native := range []string{waGroup, waMessage} {
		a := domain.ParseID("whatsapp:" + waAccount + "/" + native)
		b := domain.ParseID("whatsapp:972500000009/" + native)
		if a.Native != b.Native || a.Account == b.Account {
			t.Errorf("%s from two accounts: %+v and %+v", native, a, b)
		}
	}
	if id := domain.ParseID("whatsapp:" + waPhone); id.Account != "" {
		t.Errorf("a person's ID carries an account: %+v", id)
	}
}

// A person on a network kith reaches directly is on that network, not on Matrix, and a
// Matrix ID never reads as native whatever its localpart says.
func TestProtocolOfANativePerson(t *testing.T) {
	t.Parallel()

	if got := domain.ProtocolOf("whatsapp:" + waPhone); got != domain.ProtocolWhatsApp {
		t.Errorf("ProtocolOf(native) = %q", got)
	}
	for _, mxid := range []string{"@whatsapp:example.org", "@whatsapp_1:example.org", "!whatsapp:example.org"} {
		if domain.NetworkOf(mxid) != domain.ProtocolMatrix {
			t.Errorf("NetworkOf(%q) = %q; a sigil always means Matrix", mxid, domain.NetworkOf(mxid))
		}
	}
}

// A member with no display name falls back to their phone number, not to the word
// "whatsapp" (what cutting at the first colon gave).
func TestANativeMemberHasAReadableName(t *testing.T) {
	t.Parallel()

	m := domain.Member{UserID: "whatsapp:" + waPhone}
	if got := m.Name(); got != "+972500000002" {
		t.Errorf("Name() = %q", got)
	}
	if m.Matches("+9725") == 0 {
		t.Error("a member should answer to the start of their number")
	}
}

// A native room's ID is a complete list entry, like a Matrix room's, and names that
// room only.
func TestANativeRoomIDIsAnEntry(t *testing.T) {
	t.Parallel()

	room := "whatsapp:" + waAccount + "/" + waGroup
	if kind, ok := domain.ParseEntry(room); !ok || kind != domain.EntryRoom {
		t.Fatalf("ParseEntry(%q) = %v, %v — want one room", room, kind, ok)
	}
	if err := domain.ValidateEntries("agent.read", []string{room}); err != nil {
		t.Errorf("ValidateEntries: %v", err)
	}
	facts := domain.RoomFacts{ID: room, Protocol: domain.ProtocolWhatsApp}
	if !facts.Names(room) {
		t.Errorf("%q should name its own room", room)
	}
	other := domain.RoomFacts{ID: "whatsapp:972500000009/" + waGroup}
	if other.Names(room) {
		t.Errorf("%q names the other account's view of the group too", room)
	}
	// A person's ID is not a room.
	if _, ok := domain.ParseEntry("whatsapp:" + waPhone); ok {
		t.Errorf("a person's ID was taken as a list entry")
	}
}

// A native ID is built as ParseID takes it apart, for rooms, events and people.
func TestNativeIDsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, native := range []string{waGroup, waPhone, waLID, waMessage} {
		id := domain.NativeID(domain.ProtocolWhatsApp, waAccount, native)
		if got := domain.ParseID(id); got != (domain.ID{Network: domain.ProtocolWhatsApp, Account: waAccount, Native: native}) {
			t.Errorf("ParseID(NativeID(%q)) = %+v", native, got)
		}
	}
	for _, native := range []string{"C0000000002", "C0000000002/1700000000.000100"} {
		id := domain.NativeID(domain.ProtocolSlack, "T0000000001", native)
		if got := domain.ParseID(id); got != (domain.ID{Network: domain.ProtocolSlack, Account: "T0000000001", Native: native}) {
			t.Errorf("ParseID(NativeID(%q)) = %+v", native, got)
		}
	}
	person := domain.NativePerson(domain.ProtocolWhatsApp, waPhone)
	if got := domain.ParseID(person); got != (domain.ID{Network: domain.ProtocolWhatsApp, Native: waPhone}) {
		t.Errorf("ParseID(NativePerson) = %+v", got)
	}
	if !domain.IsUserID(person) || domain.IsRoomID(person) {
		t.Errorf("%s should be a person, not a room", person)
	}
}

// Each writer owns exactly its own rooms: Matrix's, and each account's apart, even
// when one account's digits begin another's.
func TestEachWriterOwnsOnlyItsRooms(t *testing.T) {
	t.Parallel()
	a := domain.AccountRooms(domain.ProtocolWhatsApp, "359")
	b := domain.AccountRooms(domain.ProtocolWhatsApp, "3590")
	rooms := map[domain.RoomID]domain.RoomOwner{
		"!abc:example.org":                        domain.MatrixRooms,
		"!v12opaque":                              domain.MatrixRooms,
		domain.RoomID("whatsapp:359/" + waGroup):  a,
		domain.RoomID("whatsapp:3590/" + waGroup): b,
	}
	for room, owner := range rooms {
		for _, o := range []domain.RoomOwner{domain.MatrixRooms, a, b} {
			if got := o.Owns(room); got != (o == owner) {
				t.Errorf("%q owns %s = %v, want %v", o, room, got, o == owner)
			}
		}
	}
	if domain.RoomOwner("").Owns("!abc:example.org") {
		t.Error("the zero owner owns a room")
	}
}
