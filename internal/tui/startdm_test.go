package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func switcher(t *testing.T) Model {
	t.Helper()
	m := update(t, New(context.Background(), apitest.Nop{}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!work:x", Name: "Work"},
	}})
	return sized(t, m.clearStatus())
}

// A person is not a place, and the switcher is a list of places.
func TestAPersonYouHaveNoRoomWithSortsLastAndSaysWhatItWillDo(t *testing.T) {
	t.Parallel()

	m := switcher(t)
	m.dmCandidates = []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}}
	items := m.jumpItems()

	person, room := -1, -1
	for i, item := range items {
		switch item.value {
		case "dm:@dana:x":
			person = i
		case "room:!work:x":
			room = i
		}
	}
	if person < 0 {
		t.Fatalf("no row for somebody with no room: %+v", items)
	}
	if room < 0 || person < room {
		t.Errorf("person at %d, room at %d — a person must sort after every room", person, room)
	}
	if items[person].detail != "start a DM" {
		t.Errorf("detail = %q, want it to say the row creates something", items[person].detail)
	}
	// The MXID matches as well as the name: somebody you have never shared a room
	// with is exactly the person whose display name you may not know.
	if !strings.Contains(items[person].match, "@dana:x") {
		t.Errorf("match text = %q, want the MXID in it", items[person].match)
	}
}

// The request goes out when the switcher opens, so the answer lands on somebody mid-word.
func TestCandidatesArrivingKeepWhatWasTyped(t *testing.T) {
	t.Parallel()

	m := switcher(t)
	next, _ := m.openJump()
	m = next
	m.picker.filter = "dan"
	m.picker = m.picker.refilter()

	next, _ = m.handleDirectCandidates(directCandidatesMsg{
		people: []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}},
	})
	got := next
	if got.picker.filter != "dan" {
		t.Errorf("filter = %q, want it kept", got.picker.filter)
	}
	if len(got.picker.items) != 1 || got.picker.items[0].value != "dm:@dana:x" {
		t.Errorf("visible rows = %+v, want the new person matching what was typed", got.picker.items)
	}
}

// An error here is not said.
func TestAFailedCandidateLookupIsNotReported(t *testing.T) {
	t.Parallel()

	m := switcher(t)
	next, _ := m.handleDirectCandidates(directCandidatesMsg{err: context.DeadlineExceeded})
	got := next
	if got.st.event != "" {
		t.Errorf("status = %q, want silence", got.st.event)
	}
}

// Choosing a person creates an encrypted DM with no name: encryption because it can
// only be set at creation and there is nobody it could be too soon for, and no name
// because a DM is named after whoever is in it by every client that draws one.
func TestStartingADMCreatesAnEncryptedUnnamedRoomForOnePerson(t *testing.T) {
	t.Parallel()

	spy := &createSpy{}
	m := sized(t, update(t, New(context.Background(), spy, config.Display{}), roomsMsg{}))
	m.dmCandidates = []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}}

	next, cmd := m.acceptJump("dm:@dana:x")
	got := next
	if cmd == nil {
		t.Fatal("choosing a person issued no command")
	}
	cmd()

	spec := spy.spec
	if !spec.Direct {
		t.Error("the room was not created as a direct message")
	}
	if !spec.Encrypted {
		t.Error("the DM was created unencrypted, which cannot be changed later")
	}
	if spec.Name != "" {
		t.Errorf("name = %q, want none — an m.room.name would override the person's name for both of them", spec.Name)
	}
	if len(spec.Invite) != 1 || spec.Invite[0] != "@dana:x" {
		t.Errorf("invite = %v, want [@dana:x]", spec.Invite)
	}
	// The person's name is what the status line says, since the spec has none.
	if !strings.Contains(got.st.standing, "Dana") {
		t.Errorf("status = %q, want it to name Dana", got.st.standing)
	}
}

// Having picked a person to talk to, the next thing wanted is the conversation.
func TestTheNewDMOpensOnceTheRefreshListsIt(t *testing.T) {
	t.Parallel()

	m := switcher(t)
	next, _ := m.handleRoomCreated(roomCreatedMsg{roomID: "!dm:x", name: "Dana", enter: true})
	m = next
	if m.entering != "!dm:x" {
		t.Fatalf("entering = %q, want the new room held until it is listed", m.entering)
	}

	// A refresh that does not have it yet leaves the request standing.
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: "!work:x", Name: "Work"}}})
	if m.entering != "!dm:x" {
		t.Error("the request was cleared against a list that could not have held the room")
	}
	if m.openRoom == "!dm:x" {
		t.Fatal("opened a room the list does not have")
	}

	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!work:x", Name: "Work"},
		{ID: "!dm:x", Name: "Dana", IsDirect: true},
	}})
	if m.openRoom != "!dm:x" {
		t.Errorf("open room = %q, want the DM that was just made", m.openRoom)
	}
	if m.entering != "" {
		t.Error("the pending open was not cleared after it happened")
	}
}

// createSpy records the spec a creation was asked for.
type createSpy struct {
	apitest.Nop
	spec domain.NewRoom
}

func (s *createSpy) CreateRoom(_ context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	s.spec = spec
	return "!dm:x", nil
}
