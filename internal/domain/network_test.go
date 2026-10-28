package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An invented Matrix account (AccountKey's shape) and the prefix of its rooms and events.
const (
	mxAccount = "0123456789abcdef"
	mxWork    = "fedcba9876543210"
	mx        = "matrix:" + mxAccount + "/"
)

// Invented IDs in WhatsApp's shapes.
const (
	waAccount = "44000000001"
	waGroup   = "120363000000000001@g.us"
	waPhone   = "1500000002@s.whatsapp.net"
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
		"matrix room":         {mx + "!abc:example.org", domain.ID{Network: domain.ProtocolMatrix, Account: mxAccount, Native: "!abc:example.org"}, true, false, ""},
		"matrix v12 room":     {mx + "!31hneApxJ_1o-63DmFrpeqnkFfWppnzWso1JvH3ogLM", domain.ID{Network: domain.ProtocolMatrix, Account: mxAccount, Native: "!31hneApxJ_1o-63DmFrpeqnkFfWppnzWso1JvH3ogLM"}, true, false, ""},
		"matrix user":         {"matrix:@dana:example.org", domain.ID{Network: domain.ProtocolMatrix, Native: "@dana:example.org"}, false, true, "dana"},
		"matrix slash user":   {"matrix:@a/b:example.org", domain.ID{Network: domain.ProtocolMatrix, Native: "@a/b:example.org"}, false, true, "a/b"},
		"matrix event":        {mx + "$ev:example.org", domain.ID{Network: domain.ProtocolMatrix, Account: mxAccount, Native: "$ev:example.org"}, false, false, ""},
		"bridged ghost":       {"matrix:@whatsapp_447000000004:example.org", domain.ID{Network: domain.ProtocolMatrix, Native: "@whatsapp_447000000004:example.org"}, false, true, "whatsapp_447000000004"},
		"whatsapp group":      {"whatsapp:" + waAccount + "/" + waGroup, domain.ID{Network: domain.ProtocolWhatsApp, Account: waAccount, Native: waGroup}, true, false, ""},
		"whatsapp dm":         {"whatsapp:" + waAccount + "/" + waPhone, domain.ID{Network: domain.ProtocolWhatsApp, Account: waAccount, Native: waPhone}, true, false, ""},
		"whatsapp message":    {"whatsapp:" + waAccount + "/" + waMessage, domain.ID{Network: domain.ProtocolWhatsApp, Account: waAccount, Native: waMessage}, false, false, ""},
		"whatsapp phone user": {"whatsapp:" + waPhone, domain.ID{Network: domain.ProtocolWhatsApp, Native: waPhone}, false, true, "+1500000002"},
		"whatsapp lid user":   {"whatsapp:" + waLID, domain.ID{Network: domain.ProtocolWhatsApp, Native: waLID}, false, true, "100000000000003"},
		"slack channel":       {"slack:T0000000001/C0000000002", domain.ID{Network: domain.ProtocolSlack, Account: "T0000000001", Native: "C0000000002"}, true, false, ""},
		"slack dm":            {"slack:T0000000001/D0000000004", domain.ID{Network: domain.ProtocolSlack, Account: "T0000000001", Native: "D0000000004"}, true, false, ""},
		"slack message":       {"slack:T0000000001/C0000000002/1700000000.000100", domain.ID{Network: domain.ProtocolSlack, Account: "T0000000001", Native: "C0000000002/1700000000.000100"}, false, false, ""},
		"slack user":          {"slack:T0000000001.U0000000003", domain.ID{Network: domain.ProtocolSlack, Native: "T0000000001.U0000000003"}, false, true, ""},
		"telegram chat":       {"telegram:111/222", domain.ID{Network: domain.ProtocolTelegram, Account: "111", Native: "222"}, true, false, ""},
		"telegram group":      {"telegram:111/-333", domain.ID{Network: domain.ProtocolTelegram, Account: "111", Native: "-333"}, true, false, ""},
		"telegram channel":    {"telegram:111/-1004444", domain.ID{Network: domain.ProtocolTelegram, Account: "111", Native: "-1004444"}, true, false, ""},
		"telegram message":    {"telegram:111/-1004444/55", domain.ID{Network: domain.ProtocolTelegram, Account: "111", Native: "-1004444/55"}, false, false, ""},
		"telegram user":       {"telegram:222", domain.ID{Network: domain.ProtocolTelegram, Native: "222"}, false, true, ""},
		"linkedin chat":       {"linkedin:ACoAAA1/2-YWJj%2FZA==", domain.ID{Network: domain.ProtocolLinkedIn, Account: "ACoAAA1", Native: "2-YWJj%2FZA=="}, true, false, ""},
		"linkedin message":    {"linkedin:ACoAAA1/2-YWJj%2FZA==/2-bWVz", domain.ID{Network: domain.ProtocolLinkedIn, Account: "ACoAAA1", Native: "2-YWJj%2FZA==/2-bWVz"}, false, false, ""},
		"gmessages chat":      {"gmessages:ph%2F1/42", domain.ID{Network: domain.ProtocolGMessages, Account: "ph%2F1", Native: "42"}, true, false, ""},
		"gmessages person":    {"gmessages:+15550000001", domain.ID{Network: domain.ProtocolGMessages, Native: "+15550000001"}, false, true, "+15550000001"},
		"gmessages message":   {"gmessages:ph%2F1/42/9", domain.ID{Network: domain.ProtocolGMessages, Account: "ph%2F1", Native: "42/9"}, false, false, ""},
		"linkedin person":     {"linkedin:ACoAAA2", domain.ID{Network: domain.ProtocolLinkedIn, Native: "ACoAAA2"}, false, true, ""},
		// An unknown prefix is no network's.
		"unknown prefix": {"room:Standup", domain.ID{Native: "room:Standup"}, false, false, ""},
		"matrix uri":     {"matrix:u/dana:example.org", domain.ID{Native: "matrix:u/dana:example.org"}, false, false, ""},
		"empty":          {"", domain.ID{}, false, false, ""},
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

// The account separates two accounts' views of a group or message; a person is shared.
func TestNativeIDsKeepAccountsApart(t *testing.T) {
	t.Parallel()

	for _, native := range []string{waGroup, waMessage} {
		a := domain.ParseID("whatsapp:" + waAccount + "/" + native)
		b := domain.ParseID("whatsapp:1500000009/" + native)
		if a.Native != b.Native || a.Account == b.Account {
			t.Errorf("%s from two accounts: %+v and %+v", native, a, b)
		}
	}
	if id := domain.ParseID("whatsapp:" + waPhone); id.Account != "" {
		t.Errorf("a person's ID carries an account: %+v", id)
	}
}

// A native person is on their network; a Matrix ID never reads as native.
func TestProtocolOfANativePerson(t *testing.T) {
	t.Parallel()

	if got := domain.ProtocolOf("whatsapp:" + waPhone); got != domain.ProtocolWhatsApp {
		t.Errorf("ProtocolOf(native) = %q", got)
	}
	for _, mxid := range []string{"matrix:@whatsapp:example.org", "matrix:@whatsapp_1:example.org", mx + "!whatsapp:example.org"} {
		if domain.NetworkOf(mxid) != domain.ProtocolMatrix {
			t.Errorf("NetworkOf(%q) = %q; the matrix: prefix always means Matrix", mxid, domain.NetworkOf(mxid))
		}
	}
}

// A member with no display name falls back to their phone number, not "whatsapp".
func TestANativeMemberHasAReadableName(t *testing.T) {
	t.Parallel()

	m := domain.Member{UserID: "whatsapp:" + waPhone}
	if got := m.Name(); got != "+1500000002" {
		t.Errorf("Name() = %q", got)
	}
	if m.Matches("+15") == 0 {
		t.Error("a member should answer to the start of their number")
	}
}

// A native room ID is a complete list entry naming only that room.
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
	other := domain.RoomFacts{ID: "whatsapp:1500000009/" + waGroup}
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

// Each writer owns only its rooms, even when one account's digits prefix another's.
func TestEachWriterOwnsOnlyItsRooms(t *testing.T) {
	t.Parallel()
	a := domain.AccountRooms(domain.ProtocolWhatsApp, "44")
	b := domain.AccountRooms(domain.ProtocolWhatsApp, "440")
	me := domain.AccountRooms(domain.ProtocolMatrix, mxAccount)
	work := domain.AccountRooms(domain.ProtocolMatrix, mxWork)
	rooms := map[domain.RoomID]domain.RoomOwner{
		domain.RoomID(mx + "!abc:example.org"):                  me,
		domain.RoomID(mx + "!v12opaque"):                        me,
		domain.RoomID("matrix:" + mxWork + "/!abc:example.org"): work, // the same room, the other account's view
		domain.RoomID("whatsapp:44/" + waGroup):                 a,
		domain.RoomID("whatsapp:440/" + waGroup):                b,
	}
	for room, owner := range rooms {
		for _, o := range []domain.RoomOwner{me, work, a, b} {
			if got := o.Owns(room); got != (o == owner) {
				t.Errorf("%q owns %s = %v, want %v", o, room, got, o == owner)
			}
		}
	}
	if domain.RoomOwner("").Owns(domain.RoomID(mx + "!abc:example.org")) {
		t.Error("the zero owner owns a room")
	}
	if got := domain.OwnerOf("!abc:example.org"); got != "" {
		t.Errorf("a bare room ID is owned by %q, want no one", got)
	}
}

// A bare Matrix ID belongs to no network: only kith's prefixed form is Matrix's.
func TestBareMatrixIDsAreNoNetworks(t *testing.T) {
	t.Parallel()

	for _, bare := range []string{"@dana:example.org", "!abc:example.org", "$ev:example.org", "@whatsapp_15550100001:x"} {
		if id := domain.ParseID(bare); id != (domain.ID{Native: bare}) {
			t.Errorf("ParseID(%q) = %+v, want no network", bare, id)
		}
		if domain.IsUserID(bare) || domain.IsRoomID(bare) || domain.IsMatrixUserID(bare) || domain.IsMatrixRoomID(bare) ||
			domain.IsMatrixEventID(bare) {
			t.Errorf("%q has an ID's shape", bare)
		}
		if got := domain.MatrixNative(bare); got != bare {
			t.Errorf("MatrixNative(%q) = %q, want it unchanged", bare, got)
		}
	}
	if got := domain.MatrixNative(mx + "$ev/with/slashes"); got != "$ev/with/slashes" {
		t.Errorf("an event ID is cut at its own slash: %q", got)
	}
}

// A matrix: URI is an address, not an ID: it never reads as one, whatever follows the
// scheme.
func TestMatrixURIsAreNotIDs(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{"matrix:u/dana:example.org", "matrix:roomid/abc:example.org",
		"matrix:r/room:example.org", "matrix:roomid/abc:example.org/e/ev", "matrix:0123456789abcde/!abc:x",
		"matrix:0123456789ABCDEF/!abc:x"} {
		if got := domain.MatrixNative(uri); got != uri {
			t.Errorf("MatrixNative(%q) = %q, want it unchanged", uri, got)
		}
		if domain.IsMatrixUserID(uri) || domain.IsMatrixRoomID(uri) || domain.IsMatrixEventID(uri) {
			t.Errorf("%q reads as a Matrix ID", uri)
		}
	}
}

// What someone types is read as Matrix's only when it has a Matrix ID's shape.
func TestTypedIDsAreReadAsMatrixByShape(t *testing.T) {
	t.Parallel()

	for typed, want := range map[string]string{
		"\t@dana:example.org":      "matrix:@dana:example.org",
		"matrix:@dana:example.org": "matrix:@dana:example.org",
		"@dana":                    "@dana", // a name, not an ID
		"whatsapp:" + waPhone:      "whatsapp:" + waPhone,
		"dana":                     "dana",
		mx + "!r:example.org":      mx + "!r:example.org",
	} {
		if got := domain.TypedUserID(typed); got != want {
			t.Errorf("TypedUserID(%q) = %q, want %q", typed, got, want)
		}
	}
	room := domain.RoomID(mx + "!r:example.org")
	if got := domain.TypedEventID(room, "$ev"); got != domain.EventID(mx+"$ev") {
		t.Errorf("a bare event in a Matrix room = %q, want that account's", got)
	}
	if got := domain.TypedEventID(room, mx+"$ev"); got != domain.EventID(mx+"$ev") {
		t.Errorf("a kith event ID = %q, want it as written", got)
	}
	wa := domain.RoomID("whatsapp:" + waAccount + "/" + waGroup)
	if got := domain.TypedEventID(wa, "$ev"); got != "$ev" {
		t.Errorf("a $ word in a WhatsApp room = %q, want it as written", got)
	}
	for address, want := range map[string]bool{
		"!r:example.org": true, "!v12opaque": true, "#lobby:example.org": true,
		"#general": false, "!": false, mx + "!r:example.org": false, "@dana:example.org": false,
	} {
		if got := domain.IsMatrixAddress(address); got != want {
			t.Errorf("IsMatrixAddress(%q) = %v, want %v", address, got, want)
		}
	}
}

// A config entry with a bare Matrix ID is refused, saying how kith writes it; a room
// named like one of Matrix's sigils is a name.
func TestABareMatrixIDInConfigIsRefused(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"!r:example.org", "room:!r:example.org", "room: !r:example.org", "$ev", "@dana:example.org"} {
		if err := domain.ValidateEntries("agent.read", []string{entry}); err == nil {
			t.Errorf("ValidateEntries(%q) accepted a bare ID", entry)
		}
	}
	for _, entry := range []string{mx + "!r:example.org", "room:" + mx + "!r:example.org", "room:#general", "room:Standup"} {
		if err := domain.ValidateEntries("agent.read", []string{entry}); err != nil {
			t.Errorf("ValidateEntries(%q) = %v, want it taken", entry, err)
		}
	}
	if _, _, err := domain.NewTagSet([]domain.Tag{{Name: "Family", Picked: []string{"!mom:example.org"}}}); err == nil {
		t.Error("a tag picking a bare room was accepted")
	}
}
