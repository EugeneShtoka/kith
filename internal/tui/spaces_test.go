package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

type filer struct {
	apitest.Nop
	added       bool
	removed     bool
	addedTo     domain.SpaceID
	removedFrom domain.SpaceID
	room        domain.RoomID
}

func (f *filer) AddToSpace(_ context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	f.added, f.addedTo, f.room = true, spaceID, roomID
	return nil
}

func (f *filer) RemoveFromSpace(_ context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	f.removed, f.removedFrom, f.room = true, spaceID, roomID
	return nil
}

// filing has two rooms and two spaces (one holding Alpha), the room list on Alpha.
func filing(t *testing.T) (Model, *filer) {
	t.Helper()
	f := &filer{}
	// Work and Friends ranked first, so the picker opens on them.
	m := update(t, starterNew(f, config.Display{Priority: []string{"space:Work", "space:Friends"}}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
		{ID: mx + "!b:x", Name: "Bravo"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: mx + "!w:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x"}},
		{ID: mx + "!f:x", Name: "Friends"},
		// A bridge's own space, marked by the bridge bot being in it (not by its name).
		{ID: mx + "!wa:x", Name: "WhatsApp", Keeper: "matrix:@whatsappbot_bg:x", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{mx + "!b:x"}},
		// An origin space: a room names it as canonical parent.
		{ID: mx + "!tip:x", Name: "Acme", Original: true, Children: []domain.RoomID{mx + "!b:x"}},
	}})
	m = sized(t, m.clearStatus())
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	next, _ := m.selectRoom(domain.Room{ID: mx + "!a:x", Name: "Alpha"})
	m = next
	return m, f
}

func TestSpacePickerTicksTheSpacesHoldingTheRoom(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m, _ = press(t, m, keyText("S"))
	if !m.picker.active() {
		t.Fatal("S in the room list opened no picker")
	}
	if !m.picker.spec.multi {
		t.Error("filing a room is a set, not a choice")
	}
	if !m.picker.ticked(mx + "!w:x") {
		t.Error("Work holds Alpha but its row is not ticked")
	}
	if m.picker.ticked(mx + "!f:x") {
		t.Error("Friends does not hold Alpha; its row should be clear")
	}
	// Navigate mode: space ticks rather than filtering (names contain spaces).
	if m.picker.mode != pickerNavigate {
		t.Error("a set has to start in navigate mode for space to mean anything")
	}
}

func TestSpaceTicksAndEnterAppliesTheSet(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m, _ = press(t, m, keyText("S"))
	// Cursor on Work (ticked): untick it, then down to Friends and tick it.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	m, _ = press(t, m, keyText("j"))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	if m.picker.ticked(mx+"!w:x") || !m.picker.ticked(mx+"!f:x") {
		t.Fatalf("ticks = work:%v friends:%v, want the opposite of what it opened with",
			m.picker.ticked(mx+"!w:x"), m.picker.ticked(mx+"!f:x"))
	}

	next, cmd := m.acceptPick()
	mdl := next
	if mdl.picker.active() {
		t.Error("applying the set should close the picker")
	}
	_ = deliver(t, mdl, cmd)
	if !f.added || f.addedTo != mx+"!f:x" {
		t.Errorf("added to %q, want !f:x", f.addedTo)
	}
	if !f.removed || f.removedFrom != mx+"!w:x" {
		t.Errorf("removed from %q, want !w:x", f.removedFrom)
	}
}

func TestApplyingAnUnchangedSetWritesNothing(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m, _ = press(t, m, keyText("S"))
	next, cmd := m.acceptPick()
	mdl := next
	if cmd != nil {
		_ = deliver(t, mdl, cmd)
	}
	if f.added || f.removed {
		t.Error("an unchanged set should write nothing")
	}
}

// A room can belong to no space.
func TestUntickingEverythingTakesTheRoomOut(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m, _ = press(t, m, keyText("S"))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace}) // untick Work, the only one it is in
	next, cmd := m.acceptPick()
	mdl := next
	_ = deliver(t, mdl, cmd)
	if !f.removed || f.removedFrom != mx+"!w:x" {
		t.Errorf("removed from %q, want !w:x", f.removedFrom)
	}
}

func TestTheSpacePickerRemembersItsRoom(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m, _ = press(t, m, keyText("S"))
	m, _ = press(t, m, keyText("j"))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace}) // tick Friends
	// Bravo jumps to the top while the picker is open.
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: mx + "!b:x", Name: "Bravo"},
		{ID: mx + "!a:x", Name: "Alpha"},
	}})
	next, cmd := m.acceptPick()
	mdl := next
	_ = deliver(t, mdl, cmd)
	if f.room != mx+"!a:x" {
		t.Errorf("filed %q, want the room the picker was opened for (!a:x)", f.room)
	}
}

func TestManagedSpacesAreNotOffered(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	for _, name := range []string{"WhatsApp", "Acme"} {
		if indexOfGroup(m.rail.groups, name) < 0 {
			t.Errorf("%s should still be in the rail", name)
		}
	}
	m, _ = press(t, m, keyText("S"))
	for _, item := range m.picker.all {
		if item.value == mx+"!wa:x" || item.value == mx+"!tip:x" {
			t.Errorf("a managed space was offered for filing: %+v", item)
		}
	}
}

func TestTheFilingListFollowsTheRail(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	// Reorder and rename the way the rail config does, then rebuild the rail.
	m.prefs.display.Priority = nil
	m.prefs.display.Rail.Order = []string{"space:Friends", "space:Work"}
	m.prefs.display.Names = []config.DisplayName{{Target: config.NameTargetSpace + "Friends", Name: "Mates"}}
	m.rail.groups = railGroups(m.rooms.spaces, m.prefs.display.Rail, m.prefs.display.Names, m.unreadView(), m.rooms.all)

	m, _ = press(t, m, keyText("S"))
	var got []string
	for _, item := range spaceRows(m.picker.all) {
		got = append(got, item.label)
	}
	if len(got) != 2 || got[0] != "Mates" || got[1] != "Work" {
		t.Errorf("rows = %v, want the rail's order and the rail's names", got)
	}
}

func TestUnofferedSpacesAreLeftAlone(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	// Bravo lives in the bridge's space, which the picker won't show.
	next, _ := m.selectRoom(domain.Room{ID: mx + "!b:x", Name: "Bravo"})
	m = next
	m, _ = press(t, m, keyText("S"))
	next, cmd := m.acceptPick()
	mdl := next
	if cmd != nil {
		_ = deliver(t, mdl, cmd)
	}
	if f.removed {
		t.Errorf("removed Bravo from %q, which the picker never offered", f.removedFrom)
	}
}

func TestFilingSpacesNamesTheList(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m.prefs.device.FilingSpaces = []string{"space:Friends", mx + "!w:x"}
	m, _ = press(t, m, keyText("S"))

	var got []string
	for _, item := range spaceRows(m.picker.all) {
		got = append(got, item.value)
	}
	if len(got) != 2 || got[0] != mx+"!f:x" || got[1] != mx+"!w:x" {
		t.Errorf("rows = %v, want Friends then Work — the written order, by name and by ID", got)
	}
}

func TestFilingSpacesOverridesTheJudgment(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m.prefs.device.FilingSpaces = []string{"space:WhatsApp"}
	m, _ = press(t, m, keyText("S"))
	if rows := spaceRows(m.picker.all); len(rows) != 1 || rows[0].value != mx+"!wa:x" {
		t.Errorf("rows = %+v, want the named bridge space", rows)
	}
}

func TestFilingSpacesSkipsWhatItCannotFind(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m.prefs.device.FilingSpaces = []string{"space:Gone", "space:Work"}
	m, _ = press(t, m, keyText("S"))
	if rows := spaceRows(m.picker.all); len(rows) != 1 || rows[0].value != mx+"!w:x" {
		t.Errorf("rows = %+v, want just Work", rows)
	}

	m2, _ := filing(t)
	m2.prefs.device.FilingSpaces = []string{"space:Gone", "place:Also gone"}
	m2, _ = press(t, m2, keyText("S"))
	if m2.picker.active() {
		t.Error("a list matching nothing should open no picker")
	}
	if got := m2.status(); !strings.Contains(got, "filing_spaces") {
		t.Errorf("status = %q, want it to name the setting", got)
	}
}

func TestFilingSpacesLeavesUnlistedSpacesAlone(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m.prefs.device.FilingSpaces = []string{"space:Friends"} // Alpha is in Work, which is not listed
	m, _ = press(t, m, keyText("S"))
	next, cmd := m.acceptPick()
	mdl := next
	if cmd != nil {
		_ = deliver(t, mdl, cmd)
	}
	if f.removed {
		t.Errorf("removed Alpha from %q, which the list never offered", f.removedFrom)
	}
}

// spaceRows is the filing picker's space rows, without tag and New rows.
func spaceRows(items []pickerItem) []pickerItem {
	var out []pickerItem
	for _, item := range items {
		if !isTagGroup(item.value) && item.value != tagNew && item.value != spaceNew {
			out = append(out, item)
		}
	}
	return out
}

// Rows follow [display] priority, spaces and tags alike, then the rail's order.
func TestTheFilingListFollowsThePriority(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m.prefs.display.Priority = []string{"tag:Pinned", "space:Friends"}
	m, _ = press(t, m, keyText("S"))
	var got []string
	for _, item := range m.picker.all {
		got = append(got, m.filingKey(item))
	}
	// All, DMs, Unread and Drafts are filled by their rules, so not offered.
	want := []string{"tag:Pinned", "Friends", "Work", "tag:Archived"}
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Errorf("rows = %v, want %v first: priority, then the rail", got, want)
	}
}

// spaceMaker files rooms and makes spaces, recording both.
type spaceMaker struct {
	filer
	made   domain.NewRoom
	addErr error
}

func (s *spaceMaker) CreateRoom(_ context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	s.made = spec
	return mx + "!svc:x", nil
}

func (s *spaceMaker) AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	if s.addErr != nil {
		return s.addErr
	}
	return s.filer.AddToSpace(ctx, spaceID, roomID)
}

// makingSpaces is a model on a Matrix room and a kith-native WhatsApp room.
func makingSpaces(t *testing.T, addErr error) (Model, *spaceMaker) {
	t.Helper()
	s := &spaceMaker{addErr: addErr}
	m := update(t, starterNew(s, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
		{ID: "whatsapp:1/g@g.us", Name: "Family"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{{ID: mx + "!w:x", Name: "Work"}}})
	m = sized(t, m.clearStatus())
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	return m, s
}

// On a Matrix room, S offers New space: named, unnested, unencrypted, with the room
// filed in. A room of another network's adapter is not offered one.
func TestTheSpacePickerMakesASpaceForAMatrixRoom(t *testing.T) {
	t.Parallel()
	m, s := makingSpaces(t, nil)
	m, _ = m.selectRoom(domain.Room{ID: mx + "!a:x", Name: "Alpha"})
	m, _ = press(t, m, keyText("S"))
	m = pickLabel(t, m, "New space")
	if !m.prompt.active() || m.prompt.kind != promptNewRoom {
		t.Fatalf("New space opened prompt %v", m.prompt.kind)
	}
	m = typeIn(t, m, "Services")
	if s.made.Kind != domain.ChatSpace || s.made.On != domain.RoomOwner(mx) || s.made.Name != "Services" || s.made.Parent != "" {
		t.Errorf("made %+v, want a space named Services", s.made)
	}
	if !s.added || s.addedTo != mx+"!svc:x" || s.room != mx+"!a:x" {
		t.Errorf("filed %v %q into %q, want Alpha into the new space", s.added, s.room, s.addedTo)
	}
	if !strings.Contains(m.status(), "Services") {
		t.Errorf("status = %q", m.status())
	}

	m, _ = m.selectRoom(domain.Room{ID: "whatsapp:1/g@g.us", Name: "Family"})
	m, _ = press(t, m, keyText("S"))
	if slices.ContainsFunc(m.picker.all, func(i pickerItem) bool { return i.value == spaceNew }) {
		t.Error("a WhatsApp room of kith's own was offered a Matrix space")
	}
}

// A space made but not given the room says both halves.
func TestANewSpaceThatCannotTakeTheRoomSaysSo(t *testing.T) {
	t.Parallel()
	m, _ := makingSpaces(t, errors.New("forbidden"))
	m, _ = m.selectRoom(domain.Room{ID: mx + "!a:x", Name: "Alpha"})
	m, _ = press(t, m, keyText("S"))
	m = pickLabel(t, m, "New space")
	m = typeIn(t, m, "Services")
	if got := m.status(); !strings.Contains(got, "Services") || !strings.Contains(got, "forbidden") {
		t.Errorf("status = %q, want the space made and the filing refused", got)
	}
}
