package tui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// matrixOffers is what the Matrix account offers, as its adapter does.
var matrixOffers = []domain.ChatOffer{
	{On: domain.RoomOwner(mx), Kind: domain.ChatRoom, Label: "Private room", Detail: "encrypted, invitation only"},
	{On: domain.RoomOwner(mx), Kind: domain.ChatRoomUnencrypted, Label: "Private room, unencrypted", Detail: "invitation only"},
	{On: domain.RoomOwner(mx), Kind: domain.ChatRoomPublic, Label: "Public room", Detail: "anyone who finds it can join"},
	{On: domain.RoomOwner(mx), Kind: domain.ChatSpace, Label: "Space", Detail: "a container for rooms"},
}

// matrixKind is the :new row value of the Matrix account's kind.
func matrixKind(kind domain.ChatKind) string { return mx + chatValueSep + strconv.Itoa(int(kind)) }

// builder records the room it was asked to create.
type builder struct {
	apitest.Nop
	spec     domain.NewRoom
	id       domain.RoomID
	err      error
	offers   []domain.ChatOffer
	accounts []domain.Account
}

func (b *builder) ChatOffers(context.Context) ([]domain.ChatOffer, error) { return b.offers, nil }

func (b *builder) Accounts(context.Context) ([]domain.Account, error) { return b.accounts, nil }

// openedNew is m with :new's picker open, once the daemon has said what can be made.
func openedNew(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.openNewRoom()
	return deliver(t, next, cmd)
}

func (b *builder) CreateRoom(_ context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	b.spec = spec
	return b.id, b.err
}

func building(t *testing.T) (Model, *builder) {
	t.Helper()
	b := &builder{id: mx + "!new:x", offers: matrixOffers}
	m := update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: mx + "!w:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x"}},
	}})
	m = sized(t, m.clearStatus())
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	return m, b
}

// The encrypted private room is offered first: encryption cannot be added later.
func TestNewRoomOffersWholeAnswers(t *testing.T) {
	t.Parallel()

	m, _ := building(t)
	next, cmd := m.runCommandLine("new")
	m = deliver(t, next, cmd)
	if !m.picker.active() {
		t.Fatal(":new opened no picker")
	}
	if len(m.picker.items) != 5 || m.picker.items[4].value != tagNew {
		t.Errorf("offered %d kinds, want Matrix's 4 and a tag", len(m.picker.items))
	}
	if first := m.picker.items[0]; !strings.Contains(first.detail, "encrypted") {
		t.Errorf("first kind = %+v, want the encrypted private room", first)
	}
}

// The kind decides the spec and the prompt the name; encryption only where chosen.
func TestCreatingAnEncryptedRoomAndASpace(t *testing.T) {
	t.Parallel()

	m, b := building(t)
	m, _ = m.runCommandLine("new")
	next, _ := m.chooseNewRoomKind(matrixKind(domain.ChatRoom))
	m = next
	if !m.prompt.active() {
		t.Fatal("choosing a kind opened no name prompt")
	}
	next, cmd := m.submitNewRoom("Plans")
	mdl := next
	_ = deliver(t, mdl, cmd)
	if b.spec.Name != "Plans" || b.spec.Kind != domain.ChatRoom || b.spec.On != domain.RoomOwner(mx) {
		t.Errorf("spec = %+v, want an encrypted private room called Plans, on the Matrix account", b.spec)
	}

	m2, b2 := building(t)
	m2, _ = m2.runCommandLine("new")
	next, _ = m2.chooseNewRoomKind(matrixKind(domain.ChatSpace))
	m2 = next
	next, cmd = m2.submitNewRoom("Side projects")
	mdl2 := next
	_ = deliver(t, mdl2, cmd)
	if b2.spec.Kind != domain.ChatSpace {
		t.Errorf("spec = %+v, want a space", b2.spec)
	}
}

// A room made while a space is selected lands in it; a new space does not.
func TestANewRoomLandsInTheSelectedSpace(t *testing.T) {
	t.Parallel()

	m, b := building(t)
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	m, _ = m.runCommandLine("new")
	next, _ := m.chooseNewRoomKind(matrixKind(domain.ChatRoom))
	m = next
	next, cmd := m.submitNewRoom("Standup")
	mdl := next
	_ = deliver(t, mdl, cmd)
	if b.spec.Parent != mx+"!w:x" {
		t.Errorf("parent = %q, want the selected space !w:x", b.spec.Parent)
	}

	m2, b2 := building(t)
	m2.rail.cursor = indexOfGroup(m2.rail.groups, "Work")
	m2, _ = m2.runCommandLine("new")
	next, _ = m2.chooseNewRoomKind(matrixKind(domain.ChatSpace))
	m2 = next
	next, cmd = m2.submitNewRoom("Nested")
	mdl2 := next
	_ = deliver(t, mdl2, cmd)
	if b2.spec.Parent != "" {
		t.Errorf("parent = %q, want a space to be filed nowhere by default", b2.spec.Parent)
	}
}

// Created but not filed into its space reports both.
func TestAPartialCreationSaysBothHalves(t *testing.T) {
	t.Parallel()

	m, b := building(t)
	b.err = errors.New("created it, but could not file it into the space")
	m, _ = m.runCommandLine("new")
	next, _ := m.chooseNewRoomKind(matrixKind(domain.ChatRoom))
	m = next
	next, cmd := m.submitNewRoom("Plans")
	mdl := next
	mdl = deliver(t, mdl, cmd)
	got := mdl.status()
	if !strings.Contains(got, "Plans") || !strings.Contains(got, "could not file") {
		t.Errorf("status = %q, want both halves", got)
	}
}

// An empty name is a cancel.
func TestAnEmptyNameCreatesNothing(t *testing.T) {
	t.Parallel()

	m, b := building(t)
	m, _ = m.runCommandLine("new")
	next, _ := m.chooseNewRoomKind(matrixKind(domain.ChatRoom))
	m = next
	if _, cmd := m.submitNewRoom("   "); cmd != nil {
		t.Error("an empty name should create nothing")
	}
	if b.spec.Name != "" {
		t.Errorf("created %q", b.spec.Name)
	}
}

// chatting is a model with no Matrix account: one Telegram and two WhatsApp accounts,
// and a directory naming people on them.
func chatting(t *testing.T) (Model, *builder) {
	t.Helper()
	tg := domain.AccountRooms(domain.ProtocolTelegram, "42")
	b := &builder{id: "telegram:42/-1009", offers: []domain.ChatOffer{
		{On: tg, Account: "42", Kind: domain.ChatGroup, Label: "Telegram group", Detail: "a group chat"},
		{On: tg, Account: "42", Kind: domain.ChatForum, Label: "Telegram forum", Detail: "a group with topics"},
		{On: tg, Account: "42", Kind: domain.ChatChannel, Label: "Telegram channel", Detail: "only admins post"},
	}}
	for _, number := range []string{"15550100001", "15550100002"} {
		wa := domain.AccountRooms(domain.ProtocolWhatsApp, number)
		b.offers = append(b.offers,
			domain.ChatOffer{On: wa, Account: "+" + number, Kind: domain.ChatGroup, Label: "WhatsApp group", Detail: "a group chat", MinMembers: 1},
			domain.ChatOffer{On: wa, Account: "+" + number, Kind: domain.ChatCommunity, Label: "WhatsApp community", Detail: "groups under one community"},
		)
	}
	m := update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "telegram:42/-11", Name: "Book club"},
		{ID: "whatsapp:15550100001/g@g.us", Name: "Family"},
		{ID: "whatsapp:15550100002/h@g.us", Name: "Work"},
	}})
	m.selves = []string{"telegram:42"}
	m.dir.dir = domain.NewDirectory([]domain.PersonName{
		{Source: "telegram:42", ID: "telegram:7", Name: "Dana", Rank: domain.RankSaved},
		{Source: "telegram:42", ID: "telegram:8", Name: "Eli", Rank: domain.RankSaved},
		{Source: "whatsapp:15550100001", ID: domain.PhoneID("15550100009"), Name: "Zoe", Rank: domain.RankSaved},
	}, nil)
	m = sized(t, m.clearStatus())
	m.focus = paneRooms
	return m, b
}

// Only what an account here makes is offered: no Matrix rooms without Matrix, the
// kinds each network's accounts say they make, with the account named where a
// network has two.
func TestNewOffersWhatTheAccountsMake(t *testing.T) {
	t.Parallel()
	m, _ := chatting(t)
	m = openedNew(t, m)
	var labels []string
	for _, item := range m.picker.all {
		labels = append(labels, item.label+" ["+item.detail+"]")
	}
	got := strings.Join(labels, "\n")
	for _, want := range []string{"Telegram group [a group chat]", "Telegram forum", "Telegram channel", "WhatsApp group [+15550100001", "WhatsApp group [+15550100002", "WhatsApp community [+15550100001"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q among:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Private room") || strings.Contains(got, "Space") || strings.Contains(got, "Slack") {
		t.Errorf("offered what no account here makes:\n%s", got)
	}

	withMatrix, _ := building(t)
	withMatrix = openedNew(t, withMatrix)
	if len(withMatrix.picker.all) != len(matrixOffers)+1 {
		t.Errorf("with a Matrix account alone: %d rows, want Matrix's %d and a tag", len(withMatrix.picker.all), len(matrixOffers))
	}
	if !strings.Contains(got, "Tag [kith's own group") {
		t.Errorf("no tag offered without a Matrix account:\n%s", got)
	}
}

// When the daemon cannot say what the accounts make, a tag is still offered.
func TestNewOffersATagWhenTheDaemonCannotSay(t *testing.T) {
	t.Parallel()
	m, _ := building(t)
	m, _ = m.handleChatOffers(chatOffersMsg{err: errors.New("daemon gone")})
	if len(m.picker.all) != 1 || m.picker.all[0].value != tagNew {
		t.Errorf("offered %d rows, want a tag", len(m.picker.all))
	}
}

// :new makes a tag as the tag list does: named, then opened in the editor.
func TestNewMakesATag(t *testing.T) {
	t.Parallel()
	m, _ := opened(t, config.Notifications{})
	m = openedNew(t, m)
	m = pickLabel(t, m, "Tag")
	m = typeIn(t, m, "Family")
	if m.configTag("Family") < 0 || !onTagPage(m, "Family") {
		t.Errorf("tags %+v, picker %v on %q: want Family made and open", m.conf.base.Tags, m.picker.kind, m.choosing.settingGroup)
	}
}

// A Telegram forum is named, then its account's people are offered and the ticked
// ones made members; a kind that needs someone (a WhatsApp group) is not made with
// nobody ticked.
func TestANewChatIsNamedThenGivenItsPeople(t *testing.T) {
	t.Parallel()
	m, b := chatting(t)
	m = openedNew(t, m)
	m = pickLabel(t, m, "Telegram forum")
	m = typeIn(t, m, "Hikers")
	if m.picker.kind != pickerNewMembers {
		t.Fatalf("after the name: picker %v, want the people", m.picker.kind)
	}
	var offered []string
	for _, item := range m.picker.all {
		offered = append(offered, item.value)
	}
	if strings.Join(offered, ",") != "telegram:7,telegram:8" {
		t.Errorf("offered %v, want the Telegram account's people", offered)
	}
	m.picker.checked = map[string]bool{"telegram:7": true}
	next, cmd := m.acceptPick()
	_ = deliver(t, next, cmd)
	want := domain.NewRoom{Name: "Hikers", On: domain.AccountRooms(domain.ProtocolTelegram, "42"), Kind: domain.ChatForum, Invite: []string{"telegram:7"}}
	if b.spec.Name != want.Name || b.spec.On != want.On || b.spec.Kind != want.Kind || strings.Join(b.spec.Invite, ",") != "telegram:7" {
		t.Errorf("made %+v, want %+v", b.spec, want)
	}

	m, b = chatting(t)
	m = openedNew(t, m)
	m = pickLabel(t, m, "+15550100001 · a group chat")
	m = typeIn(t, m, "Cousins")
	m, cmd = m.acceptPick()
	if cmd != nil || b.spec.Name != "" || !strings.Contains(m.status(), "needs someone") || m.picker.kind != pickerNewMembers {
		t.Errorf("a WhatsApp group with no one: made %+v, said %q, picker %v", b.spec, m.status(), m.picker.kind)
	}
}

// With a network's default set, :new offers only what that account makes: it makes
// chats without asking which account. Networks with no default still name each row's.
func TestNewOffersOnlyTheDefaultsChats(t *testing.T) {
	t.Parallel()
	m, b := chatting(t)
	second := domain.AccountRooms(domain.ProtocolWhatsApp, "15550100002")
	b.accounts = []domain.Account{{Network: domain.ProtocolWhatsApp, On: second, Name: "work", Default: true}}
	m = openedNew(t, m)
	for _, item := range m.picker.all {
		if strings.HasPrefix(item.value, string(domain.AccountRooms(domain.ProtocolWhatsApp, "15550100001"))) {
			t.Errorf("offered %q on the account that is not the default", item.label)
		}
	}
	if !slices.ContainsFunc(m.picker.all, func(i pickerItem) bool { return strings.HasPrefix(i.value, string(second)) }) {
		t.Error("the default's chats are not offered")
	}
	if !slices.ContainsFunc(m.picker.all, func(i pickerItem) bool { return i.label == "Telegram group" }) {
		t.Error("a network with no default lost its offers")
	}
}
