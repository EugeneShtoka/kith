package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// builder records the room it was asked to create.
type builder struct {
	apitest.Nop
	spec domain.NewRoom
	id   domain.RoomID
	err  error
}

func (b *builder) CreateRoom(_ context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	b.spec = spec
	return b.id, b.err
}

func building(t *testing.T) (Model, *builder) {
	t.Helper()
	b := &builder{id: "!new:x"}
	m := update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
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
	m, _ = m.runCommandLine("new")
	if !m.picker.active() {
		t.Fatal(":new opened no picker")
	}
	if len(m.picker.items) != 4 {
		t.Errorf("offered %d kinds, want 4", len(m.picker.items))
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
	next, _ := m.chooseNewRoomKind("private")
	m = next
	if !m.prompt.active() {
		t.Fatal("choosing a kind opened no name prompt")
	}
	next, cmd := m.submitNewRoom("Plans")
	mdl := next
	_ = deliver(t, mdl, cmd)
	if b.spec.Name != "Plans" || !b.spec.Encrypted || b.spec.Space || b.spec.Public {
		t.Errorf("spec = %+v, want an encrypted private room called Plans", b.spec)
	}

	m2, b2 := building(t)
	m2, _ = m2.runCommandLine("new")
	next, _ = m2.chooseNewRoomKind("space")
	m2 = next
	next, cmd = m2.submitNewRoom("Side projects")
	mdl2 := next
	_ = deliver(t, mdl2, cmd)
	if !b2.spec.Space || b2.spec.Encrypted {
		t.Errorf("spec = %+v, want an unencrypted space", b2.spec)
	}
}

// A room made while a space is selected lands in it; a new space does not.
func TestANewRoomLandsInTheSelectedSpace(t *testing.T) {
	t.Parallel()

	m, b := building(t)
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	m, _ = m.runCommandLine("new")
	next, _ := m.chooseNewRoomKind("private")
	m = next
	next, cmd := m.submitNewRoom("Standup")
	mdl := next
	_ = deliver(t, mdl, cmd)
	if b.spec.Parent != "!w:x" {
		t.Errorf("parent = %q, want the selected space !w:x", b.spec.Parent)
	}

	m2, b2 := building(t)
	m2.rail.cursor = indexOfGroup(m2.rail.groups, "Work")
	m2, _ = m2.runCommandLine("new")
	next, _ = m2.chooseNewRoomKind("space")
	m2 = next
	next, cmd = m2.submitNewRoom("Nested")
	mdl2 := next
	_ = deliver(t, mdl2, cmd)
	if b2.spec.Parent != "" {
		t.Errorf("parent = %q, want a space to be filed nowhere by default", b2.spec.Parent)
	}
}

// Created but not filed into its space reports both halves.
func TestAPartialCreationSaysBothHalves(t *testing.T) {
	t.Parallel()

	m, b := building(t)
	b.err = errors.New("created it, but could not file it into the space")
	m, _ = m.runCommandLine("new")
	next, _ := m.chooseNewRoomKind("private")
	m = next
	next, cmd := m.submitNewRoom("Plans")
	mdl := next
	mdl = deliver(t, mdl, cmd)
	got := mdl.status()
	if !strings.Contains(got, "was created") || !strings.Contains(got, "could not file") {
		t.Errorf("status = %q, want both halves", got)
	}
}

// An empty name is a cancel.
func TestAnEmptyNameCreatesNothing(t *testing.T) {
	t.Parallel()

	m, b := building(t)
	m, _ = m.runCommandLine("new")
	next, _ := m.chooseNewRoomKind("private")
	m = next
	if _, cmd := m.submitNewRoom("   "); cmd != nil {
		t.Error("an empty name should create nothing")
	}
	if b.spec.Name != "" {
		t.Errorf("created %q", b.spec.Name)
	}
}

// chatting is a model with no Matrix account: a Telegram account, two WhatsApp
// accounts, and a directory naming people on them.
func chatting(t *testing.T) (Model, *builder) {
	t.Helper()
	b := &builder{id: "telegram:42/-1009"}
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

// Only what an account here makes is offered: no Matrix rooms without a Matrix
// account, each Telegram and WhatsApp account's kinds, and the account named where a
// network has two.
func TestNewOffersWhatTheAccountsMake(t *testing.T) {
	t.Parallel()
	m, _ := chatting(t)
	m, _ = m.openNewRoom()
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
	withMatrix, _ = withMatrix.openNewRoom()
	if len(withMatrix.picker.all) != len(newRoomKinds) {
		t.Errorf("with a Matrix account alone: %d rows, want Matrix's %d", len(withMatrix.picker.all), len(newRoomKinds))
	}
}

// A Telegram forum is named, then its account's people are offered (that account's
// alone) and the ticked ones made its members; a WhatsApp group with no one ticked is
// not made.
func TestANewChatIsNamedThenGivenItsPeople(t *testing.T) {
	t.Parallel()
	m, b := chatting(t)
	m, _ = m.openNewRoom()
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
	m, _ = m.openNewRoom()
	m = pickLabel(t, m, "+15550100001 · a group chat")
	m = typeIn(t, m, "Cousins")
	m, cmd = m.acceptPick()
	if cmd != nil || b.spec.Name != "" || !strings.Contains(m.status(), "needs someone") || m.picker.kind != pickerNewMembers {
		t.Errorf("a WhatsApp group with no one: made %+v, said %q, picker %v", b.spec, m.status(), m.picker.kind)
	}
}
