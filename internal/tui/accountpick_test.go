package tui

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Two Telegram accounts, home and work, and the chat @novosalsa as each sees it.
var (
	tgHome = domain.Account{Network: domain.ProtocolTelegram, On: domain.AccountRooms(domain.ProtocolTelegram, "41"), Name: "home"}
	tgWork = domain.Account{Network: domain.ProtocolTelegram, On: domain.AccountRooms(domain.ProtocolTelegram, "42"), Name: "work"}
	salsa  = func(a domain.Account) domain.Room {
		return domain.Room{ID: domain.RoomID(string(a.On) + "-1000000000021"), Name: "Novo Salsa", Handle: "novosalsa"}
	}
)

func asDefault(a domain.Account) domain.Account { a.Default = true; return a }

// twoTelegram is a model whose room list holds rooms, with both Telegram accounts.
func twoTelegram(t *testing.T, b *linkBackend, rooms ...domain.Room) Model {
	t.Helper()
	m := sized(t, starterNew(b, config.Display{}))
	return update(t, m, roomsMsg{rooms: append(rooms, domain.Room{ID: mx + "!b:x", Name: "Bravo"})})
}

// A chat both accounts are in opens as the default sees it; without a default, kith
// asks which, and opens the one chosen.
func TestALinkToAChatTwoAccountsAreInTakesTheDefaultElseAsks(t *testing.T) {
	t.Parallel()

	b := &linkBackend{accounts: []domain.Account{tgHome, asDefault(tgWork)}}
	m := followed(t, twoTelegram(t, b, salsa(tgHome), salsa(tgWork)), "tg://resolve?domain=novosalsa")
	if m.openRoom != salsa(tgWork).ID {
		t.Errorf("open room = %q, want work's view of the chat (the default)", m.openRoom)
	}

	b = &linkBackend{accounts: []domain.Account{tgHome, tgWork}}
	m = followed(t, twoTelegram(t, b, salsa(tgHome), salsa(tgWork)), "tg://resolve?domain=novosalsa")
	if m.picker.kind != pickerChooseAccount || len(m.picker.items) != 2 {
		t.Fatalf("picker = %v with %d rows, want the two accounts asked between", m.picker.kind, len(m.picker.items))
	}
	m, _ = m.chooseAccount(string(tgHome.On))
	if m.openRoom != salsa(tgHome).ID {
		t.Errorf("open room = %q, want home's view of the chat, as chosen", m.openRoom)
	}
}

// The account already in a chat beats the default, and is taken without asking.
func TestALinkToAChatOneAccountIsInOpensItThere(t *testing.T) {
	t.Parallel()
	b := &linkBackend{accounts: []domain.Account{tgHome, asDefault(tgWork)}}
	m := followed(t, twoTelegram(t, b, salsa(tgHome)), "tg://resolve?domain=novosalsa")
	if m.openRoom != salsa(tgHome).ID {
		t.Errorf("open room = %q, want home's chat: the account already in it", m.openRoom)
	}
}

// A link no listed chat matches is looked up on the default account; with no default,
// on the one chosen.
func TestALinkToAnUnknownChatIsLookedUpOnThePickedAccount(t *testing.T) {
	t.Parallel()

	b := &linkBackend{accounts: []domain.Account{tgHome, asDefault(tgWork)}}
	followed(t, twoTelegram(t, b), "tg://resolve?domain=someone")
	if !slices.Equal(b.on, []domain.RoomOwner{tgWork.On}) {
		t.Errorf("looked up on %v, want the default, work", b.on)
	}

	b = &linkBackend{accounts: []domain.Account{tgHome, tgWork}}
	m := followed(t, twoTelegram(t, b), "tg://resolve?domain=someone")
	if m.picker.kind != pickerChooseAccount || len(b.on) != 0 {
		t.Fatalf("picker = %v, looked up on %v; want the question first", m.picker.kind, b.on)
	}
	next, cmd := m.chooseAccount(string(tgHome.On))
	linkSteps(t, next, cmd)
	if !slices.Equal(b.on, []domain.RoomOwner{tgHome.On}) {
		t.Errorf("looked up on %v, want home, as chosen", b.on)
	}
}

// A Matrix link to a room two Matrix accounts are in opens as the default sees it.
func TestAMatrixLinkToARoomTwoAccountsAreInTakesTheDefault(t *testing.T) {
	t.Parallel()
	other := domain.Account{Network: domain.ProtocolMatrix, On: domain.AccountRooms(domain.ProtocolMatrix, "fedcba9876543210"), Name: "@me:work.x"}
	mine := domain.Account{Network: domain.ProtocolMatrix, On: domain.RoomOwner(mx), Name: "@me:x", Default: true}
	b := &linkBackend{accounts: []domain.Account{other, mine}}
	m := sized(t, starterNew(b, config.Display{}))
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: domain.RoomID(string(other.On) + "!club:x"), Name: "Club"}, {ID: mx + "!club:x", Name: "Club"},
	}})
	m = followed(t, m, "https://matrix.to/#/!club:x")
	if m.openRoom != mx+"!club:x" {
		t.Errorf("open room = %q, want the default account's view", m.openRoom)
	}
}
