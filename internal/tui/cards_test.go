package tui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The people the card tests know: Dana has a DM, Eli a room with him and a bridge bot (and a
// group with Dana too), Zed no room but can be messaged, Noa only a number nobody wrote from.
var folderCards = map[string]string{
	"Dana.md": "---\nmatrix: [\"@dana:x\"]\nrole: Family Attorney\ncompany: Meta\n---\nnotes\n",
	"Eli.md":  "---\nmatrix: [\"@eli:x\"]\nrole: architect\ncompany: [Acme, Initech]\n---\n",
	"Noa.md":  "---\nphone: [\"+15550100009\"]\nrole: attorney\n---\n",
	"Zed.md":  "---\nmatrix: [\"@zed:x\"]\nrole: attorney at law\n---\n",
}

// writeCards puts cards into a new folder.
func writeCards(t *testing.T, cards map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range cards {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// cardRooms answers who is in a room, which rooms a person is in, who posted, and records
// each search.
type cardRooms struct {
	apitest.Nop
	members map[domain.RoomID][]domain.Member
	with    map[string][]domain.Room
	senders []domain.Member
	asked   *searches
}

type searches struct {
	sync.Mutex
	reqs []domain.SearchRequest
}

func (b cardRooms) Members(_ context.Context, id domain.RoomID, _ int) ([]domain.Member, error) {
	return b.members[id], nil
}

func (b cardRooms) RoomsWith(_ context.Context, ids []string, _ domain.RoomSet, _ int) ([]domain.Room, error) {
	return b.with[ids[0]], nil
}

func (b cardRooms) SearchSenders(context.Context, domain.RoomSet, int) ([]domain.Member, error) {
	return b.senders, nil
}

func (b cardRooms) SearchMessages(_ context.Context, req domain.SearchRequest) ([]domain.SearchHit, error) {
	b.asked.Lock()
	defer b.asked.Unlock()
	b.asked.reqs = append(b.asked.reqs, req)
	return nil, nil
}

var (
	danaDM   = domain.Room{ID: mx + "!d:x", Name: "Dana Levi", IsDirect: true, Members: []string{"Dana Levi"}}
	eliRoom  = domain.Room{ID: mx + "!e:x", Name: "Eli", Members: []string{"Eli", "WhatsApp bridge bot"}}
	crowd    = domain.Room{ID: mx + "!g:x", Name: "Crowd", Members: []string{"Dana Levi", "Eli"}}
	me       = domain.Member{UserID: "matrix:@me:x", DisplayName: "Me"}
	dana     = domain.Member{UserID: "matrix:@dana:x", DisplayName: "Dana Levi"}
	eli      = domain.Member{UserID: "matrix:@eli:x", DisplayName: "Eli"}
	cardBack = func() cardRooms {
		return cardRooms{
			members: map[domain.RoomID][]domain.Member{
				danaDM.ID:  {me, dana},
				eliRoom.ID: {me, {UserID: "matrix:@whatsappbot:x"}, eli},
				crowd.ID:   {me, dana, eli},
			},
			with: map[string][]domain.Room{
				"matrix:@dana:x": {crowd, danaDM},
				"matrix:@eli:x":  {crowd, eliRoom},
			},
			senders: []domain.Member{dana, eli, {UserID: "matrix:@whatsapp_15550100009:x", DisplayName: "Noa"}},
			asked:   &searches{},
		}
	}
)

// carded is a model over the card folder, with Dana's DM, Eli's room and a crowd filed in the
// Friends space (which shows company beside names), and the crowd and Eli's room also in Work.
func carded(t *testing.T, back cardRooms, rules ...config.SpaceRule) (Model, string) {
	t.Helper()
	dir := writeCards(t, folderCards)
	base := config.Config{Profiles: starter(t).Profiles, Matrix: config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://x", User: "@me:x"}}}, Display: config.Display{SpaceRules: rules}}
	base.Phonebook.Dir = dir
	m := New(t.Context(), back, base.Display, config.DeviceDisplay{}).WithConfigFile("", asStarted(base))
	m.selves = []string{"matrix:@me:x"}
	next, cmd := asModel(m.Update(roomsMsg{rooms: []domain.Room{danaDM, eliRoom, crowd}}))
	m = deliver(t, next, cmd)
	next, cmd = asModel(m.Update(spacesMsg{spaces: []domain.Space{
		{ID: mx + "!f:x", Name: "Friends", Children: []domain.RoomID{danaDM.ID, eliRoom.ID, crowd.ID}},
		{ID: mx + "!w:x", Name: "Work", Children: []domain.RoomID{crowd.ID, eliRoom.ID}},
	}}))
	m = sized(t, deliver(t, next, cmd))
	// The rail's rows are known now; a refresh reads the members show_field needs.
	next, cmd = m.refreshFieldMembers()
	return deliver(t, next, cmd), dir
}

// rowText is a room's room-list row as read: no styling, no isolates.
func rowText(m Model, room domain.Room) string {
	return strings.TrimSpace(stripIsolates(ansi.Strip(m.roomListLine(roomRow{room: room}, 60, false, true))))
}

// A field term in the switcher lists the people whose card meets it, after the usual rows;
// plain words never list cards. Choosing one goes to their DM, else their room with them
// alone, else starts a DM, else opens their card.
func TestTheSwitcherFindsPeopleByCardFields(t *testing.T) {
	t.Parallel()
	m, _ := carded(t, cardBack())
	m = update(t, m, directCandidatesMsg{people: []domain.Member{{UserID: "matrix:@zed:x", DisplayName: "Zed"}}})
	m.accounts = []domain.Account{{Network: domain.ProtocolMatrix, On: domain.RoomOwner(mx)}}
	cards := func(m Model, filter string) []string {
		m, _ = m.openJump()
		m = typeInto(t, m, filter)
		var out []string
		for _, item := range m.picker.items {
			if item.card != nil {
				out = append(out, stripIsolates(item.label))
			}
		}
		return out
	}
	if got := cards(m, "dana"); len(got) != 0 {
		t.Errorf("a plain word listed cards: %v", got)
	}
	if got := strings.Join(cards(m, "ROLE:attorney"), ","); got != "Dana,Noa,Zed" {
		t.Errorf("role:attorney lists %q", got)
	}
	if got := strings.Join(cards(m, "role:a company:acme"), ","); got != "Eli" {
		t.Errorf("two terms list %q", got)
	}
	choose := func(m Model, filter, name string) Model {
		t.Helper()
		m, _ = m.openJump()
		m = typeInto(t, m, filter)
		m.picker.cursor = slices.IndexFunc(m.picker.items, func(i pickerItem) bool { return stripIsolates(i.label) == name && i.card != nil })
		next, cmd := m.acceptPick()
		return deliver(t, next, cmd)
	}
	if got := choose(m, "role:family", "Dana"); got.openRoom != danaDM.ID {
		t.Errorf("Dana: opened %q, want her DM", got.openRoom)
	}
	if got := choose(m, "role:arch", "Eli"); got.openRoom != eliRoom.ID {
		t.Errorf("Eli: opened %q, want the room with him alone (the crowd is not)", got.openRoom)
	}
	if got := choose(m, "role:law", "Zed"); !strings.Contains(got.status(), "Zed") {
		t.Errorf("Zed: said %q, want a DM started", got.status())
	}
	if got := choose(m, "phone:0009", "Noa"); got.picker.kind != pickerPerson || !strings.Contains(got.picker.spec.title, "Noa.md") {
		t.Errorf("Noa: picker %v %q, want her card", got.picker.kind, got.picker.spec.title)
	}
}

// :people lists every card, narrowed by names and field terms, typed after it or in the list;
// choosing one opens it.
func TestPeopleListsTheCards(t *testing.T) {
	t.Parallel()
	m, _ := carded(t, cardBack())
	labels := func(m Model) string {
		var out []string
		for _, item := range m.picker.items {
			out = append(out, stripIsolates(item.label))
		}
		return strings.Join(out, ",")
	}
	next, cmd := m.runCommandLine(":people")
	m = deliver(t, next, cmd)
	if m.picker.kind != pickerCards || labels(m) != "Dana,Eli,Noa,Zed" {
		t.Fatalf("picker %v lists %q", m.picker.kind, labels(m))
	}
	m = typeInto(t, m, "role:attorney")
	if labels(m) != "Dana,Noa,Zed" {
		t.Errorf("role:attorney lists %q", labels(m))
	}
	m = typeInto(t, m, " z")
	if labels(m) != "Zed" {
		t.Errorf("and a name lists %q", labels(m))
	}
	next, cmd = m.runCommandLine(":people company:meta")
	m = deliver(t, next, cmd)
	if labels(m) != "Dana" {
		t.Fatalf(":people company:meta lists %q", labels(m))
	}
	next, cmd = m.acceptPick()
	m = deliver(t, next, cmd)
	if m.picker.kind != pickerPerson || !strings.Contains(m.picker.spec.title, "Dana.md") {
		t.Errorf("choosing Dana: picker %v %q", m.picker.kind, m.picker.spec.title)
	}
}

// from:key:value searches the messages of everyone whose card meets it, by every ID kith
// knows them by; a term no card meets searches nothing and says so.
func TestFromAFieldSearchesThePeopleOnTheCards(t *testing.T) {
	t.Parallel()
	back := cardBack()
	m, _ := carded(t, back)
	next, cmd := m.openSearchFor(true, "")
	m = deliver(t, next, cmd)
	next, cmd = m.runSearch("deploy from:role:attorney")
	m = deliver(t, next, cmd)
	back.asked.Lock()
	reqs := slices.Clone(back.asked.reqs)
	back.asked.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("searched %d times", len(reqs))
	}
	got := slices.Sorted(slices.Values(reqs[0].Filter.Senders))
	if strings.Join(got, " ") != "matrix:@dana:x matrix:@whatsapp_15550100009:x matrix:@zed:x" || reqs[0].Filter.Terms != "deploy" {
		t.Errorf("searched %q from %v", reqs[0].Filter.Terms, got)
	}

	next, _ = m.runSearch("from:role:pilot")
	if !strings.Contains(next.status(), "role:pilot") || next.search.hits != nil {
		t.Errorf("a term no card meets: said %q", next.status())
	}
	back.asked.Lock()
	defer back.asked.Unlock()
	if len(back.asked.reqs) != 1 {
		t.Errorf("a term no card meets was searched: %+v", back.asked.reqs[1:])
	}
}

// A place's show_field puts the field after the name of a DM's or one-person room's person in
// its room list, and in the member list; a room of several, and a place without it, show none.
func TestAPlaceShowsAFieldBesideNames(t *testing.T) {
	t.Parallel()
	m, _ := carded(t, cardBack(), config.SpaceRule{Space: "space:Friends", ShowField: "company"})
	m.rail.cursor = indexOfGroup(m.rail.groups, "Friends")
	for _, c := range []struct {
		room domain.Room
		want string
	}{{danaDM, "Dana Levi · Meta"}, {eliRoom, "Eli · Acme, Initech"}, {crowd, "Crowd"}} {
		if got := rowText(m, c.room); got != c.want {
			t.Errorf("in Friends, %s's row reads %q, want %q", c.room.Name, got, c.want)
		}
	}
	m.timeline.members = []domain.Member{me, dana, eli}
	m.openRoom = crowd.ID
	m, _ = m.openPeopleForRoom()
	var people []string
	for _, item := range m.picker.all {
		people = append(people, stripIsolates(item.label))
	}
	if strings.Join(people, ",") != "Me,Dana Levi · Meta,Eli · Acme, Initech" {
		t.Errorf("the member list reads %v", people)
	}
	m = m.closePicker()

	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	if got := rowText(m, eliRoom); got != "Eli" {
		t.Errorf("in Work, which shows nothing, Eli's room reads %q", got)
	}
	m.rail.cursor = indexOfGroup(m.rail.groups, "Friends")
	m.prefs.display.SpaceRules = nil
	if got := rowText(m, danaDM); got != "Dana Levi" {
		t.Errorf("with no show_field, Dana's DM reads %q", got)
	}
}

// Settings has a row per place choosing the field: none, or a key the cards use; choosing
// writes the place's space_rule, and none takes it out.
func TestTheFieldBesideNamesIsChosenInSettings(t *testing.T) {
	t.Parallel()
	m, _ := carded(t, cardBack())
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, m.conf.base); err != nil {
		t.Fatal(err)
	}
	m = m.WithConfigFile(path, m.conf.base).keptIn()
	m = m.settingsIn(placePage("Friends"), "")
	if got := rowFor(t, m, "Field beside names").detail; got != "none" {
		t.Errorf("Friends reads %q, want none", got)
	}
	m = pickLabel(t, m, "Field beside names")
	var offered []string
	for _, item := range m.picker.all {
		offered = append(offered, item.label)
	}
	if strings.Join(offered, ",") != "none,company,role" {
		t.Errorf("offered %v, want none and the cards' keys", offered)
	}
	m = pickLabel(t, m, "company")
	if !slices.Contains(m.conf.base.Display.SpaceRules, config.SpaceRule{Space: "space:Friends", ShowField: "company"}) {
		t.Fatalf("rules = %+v", m.conf.base.Display.SpaceRules)
	}
	if reloaded, err := config.Load(path); err != nil || reloaded.Display.ShowFieldIn(setup.HomeNamer(reloaded.Display, "Friends")) != "company" {
		t.Errorf("saved: %+v, %v", reloaded.Display.SpaceRules, err)
	}
	// The place's rooms' members are read as it is set.
	m.rail.cursor = indexOfGroup(m.rail.groups, "Friends")
	if got := rowText(m, danaDM); got != "Dana Levi · Meta" {
		t.Errorf("once set, Dana's DM reads %q", got)
	}
	m = pickLabel(t, m, "Field beside names")
	m = pickLabel(t, m, "none")
	if len(m.conf.base.Display.SpaceRules) != 0 {
		t.Errorf("none kept %+v", m.conf.base.Display.SpaceRules)
	}
}

// A card kith writes is read into the cards held, so the row shows the new value at once.
func TestTheCardsAreReadAgainAfterAWrite(t *testing.T) {
	t.Parallel()
	m, _ := carded(t, cardBack(), config.SpaceRule{Space: "space:Friends", ShowField: "company"})
	m.rail.cursor = indexOfGroup(m.rail.groups, "Friends")
	next, cmd := m.openPerson("matrix:@dana:x", "Dana")
	m = deliver(t, next, cmd)
	m = pickLabel(t, m, "company")
	m.prompt.input = ""
	m = typeInto(t, m, "Initech")
	next, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	// The write, then the read it starts.
	m = settle(t, next, cmd)
	if got := rowText(m, danaDM); got != "Dana Levi · Initech" {
		t.Errorf("after writing, Dana's DM reads %q", got)
	}
}

// Reads of the folder land in any order: an older one landing after a newer is dropped, as
// is one of a folder no longer configured; members read for a room's old shape are too.
func TestAnOlderReadNeverReplacesANewer(t *testing.T) {
	t.Parallel()
	m, dir := carded(t, cardBack(), config.SpaceRule{Space: "space:Friends", ShowField: "company"})
	m.rail.cursor = indexOfGroup(m.rail.groups, "Friends")
	m, older := m.loadCards(false, "")
	oldRead := older()
	if err := os.WriteFile(filepath.Join(dir, "Dana.md"), []byte("---\nmatrix: [\"@dana:x\"]\ncompany: Initech\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, newer := m.loadCards(false, "")
	m = update(t, m, newer())
	m = update(t, m, oldRead)
	if got := rowText(m, danaDM); got != "Dana Levi · Initech" {
		t.Errorf("an older read replaced a newer: %q", got)
	}
	m, elsewhere := m.loadCards(false, "")
	read := elsewhere().(cardsMsg)
	read.dir = t.TempDir()
	if m = update(t, m, read); rowText(m, danaDM) != "Dana Levi · Initech" {
		t.Errorf("a read of another folder was kept: %q", rowText(m, danaDM))
	}

	// Dana's DM gains Eli: the read for the old shape lands after the new one.
	grown := danaDM
	grown.Members = []string{"Dana Levi", "Eli"}
	stale := fieldMembersMsg{members: map[domain.RoomID]shapedMembers{danaDM.ID: {shape: roomShape(danaDM), members: []domain.Member{me, dana}}}}
	m = update(t, m, roomsMsg{rooms: []domain.Room{grown, eliRoom, crowd}})
	m.rail.cursor = indexOfGroup(m.rail.groups, "Friends")
	fresh := fieldMembersMsg{members: map[domain.RoomID]shapedMembers{danaDM.ID: {shape: roomShape(grown), members: []domain.Member{me, dana, eli}}}}
	m = update(t, update(t, m, fresh), stale)
	if got := rowText(m, grown); got != "Dana Levi" {
		t.Errorf("a stale read of the room's members came back: %q", got)
	}

	// A read that failed is asked again at the next refresh; one failed at an old shape is not.
	m = update(t, m, fieldMembersMsg{failed: []failedRead{{id: eliRoom.ID, shape: "old"}}})
	if _, cmd := m.refreshFieldMembers(); cmd != nil {
		t.Error("a failure at an old shape asked again")
	}
	m = update(t, m, fieldMembersMsg{failed: []failedRead{{id: eliRoom.ID, shape: roomShape(eliRoom)}}})
	if _, cmd := m.refreshFieldMembers(); cmd == nil {
		t.Error("a failed read is never asked again")
	}
}
