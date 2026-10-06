package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// filer records which way a room was filed, and where.
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

// filing returns a model with two rooms and two spaces (one already holding Alpha),
// the room list focused on Alpha.
func filing(t *testing.T) (Model, *filer) {
	t.Helper()
	f := &filer{}
	// Work and Friends ranked first, so the picker opens on them.
	m := update(t, starterNew(f, config.Display{Priority: []string{"Work", "Friends"}}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
		{ID: "!f:x", Name: "Friends"},
		// A bridge's own space, marked by the bridge bot being in it (not by its name).
		{ID: "!wa:x", Name: "WhatsApp", Keeper: "@whatsappbot_bg:x", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{"!b:x"}},
		// An origin space: a room names it as canonical parent.
		{ID: "!tip:x", Name: "Acme", Original: true, Children: []domain.RoomID{"!b:x"}},
	}})
	m = sized(t, m.clearStatus())
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	return m, f
}

// The picker is checkboxes, ticked where the room already lives.
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
	if !m.picker.ticked("!w:x") {
		t.Error("Work holds Alpha but its row is not ticked")
	}
	if m.picker.ticked("!f:x") {
		t.Error("Friends does not hold Alpha; its row should be clear")
	}
	// Modal: space ticks rather than filtering (space names contain spaces).
	if m.picker.mode != pickerNavigate {
		t.Error("a set has to start in navigate mode for space to mean anything")
	}
}

// Space ticks, enter applies the whole set at once.
func TestSpaceTicksAndEnterAppliesTheSet(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m, _ = press(t, m, keyText("S"))
	// Cursor on Work (ticked) — untick it — then down to Friends and tick that.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	m, _ = press(t, m, keyText("j"))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	if m.picker.ticked("!w:x") || !m.picker.ticked("!f:x") {
		t.Fatalf("ticks = work:%v friends:%v, want the opposite of what it opened with",
			m.picker.ticked("!w:x"), m.picker.ticked("!f:x"))
	}

	next, cmd := m.acceptPick()
	mdl := next
	if mdl.picker.active() {
		t.Error("applying the set should close the picker")
	}
	_ = deliver(t, mdl, cmd)
	if !f.added || f.addedTo != "!f:x" {
		t.Errorf("added to %q, want !f:x", f.addedTo)
	}
	if !f.removed || f.removedFrom != "!w:x" {
		t.Errorf("removed from %q, want !w:x", f.removedFrom)
	}
}

// An unchanged row is not re-sent.
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
	if got := mdl.status(); got != "no change" {
		t.Errorf("status = %q, want it to say nothing changed", got)
	}
}

// Unticking everything applies (a room can belong to no space).
func TestUntickingEverythingTakesTheRoomOut(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m, _ = press(t, m, keyText("S"))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace}) // untick Work, the only one it is in
	next, cmd := m.acceptPick()
	mdl := next
	_ = deliver(t, mdl, cmd)
	if !f.removed || f.removedFrom != "!w:x" {
		t.Errorf("removed from %q, want !w:x", f.removedFrom)
	}
}

// The room is captured when the picker opens.
func TestTheSpacePickerRemembersItsRoom(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m, _ = press(t, m, keyText("S"))
	m, _ = press(t, m, keyText("j"))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace}) // tick Friends
	// Bravo jumps to the top while the picker is open.
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!b:x", Name: "Bravo"},
		{ID: "!a:x", Name: "Alpha"},
	}})
	next, cmd := m.acceptPick()
	mdl := next
	_ = deliver(t, mdl, cmd)
	if f.room != "!a:x" {
		t.Errorf("filed %q, want the room the picker was opened for (!a:x)", f.room)
	}
}

// Managed spaces (bridge or origin) stay in the rail but not in the filing list.
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
		if item.value == "!wa:x" || item.value == "!tip:x" {
			t.Errorf("a managed space was offered for filing: %+v", item)
		}
	}
}

// The rows follow the rail's order and names, renames included.
func TestTheFilingListFollowsTheRail(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	// Reorder and rename the way the rail config does, then rebuild the rail.
	m.prefs.display.Priority = nil
	m.prefs.display.Rail.Order = []string{"Friends", "Work"}
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

// A space the list did not show must not be unfiled.
func TestUnofferedSpacesAreLeftAlone(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	// Bravo lives in the bridge's space, which the picker will not show.
	next, _ := m.selectRoom(domain.Room{ID: "!b:x", Name: "Bravo"})
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

// [display] filing_spaces makes the list explicit, in its order.
func TestFilingSpacesNamesTheList(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m.prefs.display.FilingSpaces = []string{"Friends", "!w:x"}
	m, _ = press(t, m, keyText("S"))

	var got []string
	for _, item := range spaceRows(m.picker.all) {
		got = append(got, item.value)
	}
	if len(got) != 2 || got[0] != "!f:x" || got[1] != "!w:x" {
		t.Errorf("rows = %v, want Friends then Work — the written order, by name and by ID", got)
	}
}

// filing_spaces overrides the managed-space judgment.
func TestFilingSpacesOverridesTheJudgment(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m.prefs.display.FilingSpaces = []string{"WhatsApp"}
	m, _ = press(t, m, keyText("S"))
	if rows := spaceRows(m.picker.all); len(rows) != 1 || rows[0].value != "!wa:x" {
		t.Errorf("rows = %+v, want the named bridge space", rows)
	}
}

// Unmatched entries are skipped; a list matching nothing says so.
func TestFilingSpacesSkipsWhatItCannotFind(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m.prefs.display.FilingSpaces = []string{"Gone", "Work"}
	m, _ = press(t, m, keyText("S"))
	if rows := spaceRows(m.picker.all); len(rows) != 1 || rows[0].value != "!w:x" {
		t.Errorf("rows = %+v, want just Work", rows)
	}

	m2, _ := filing(t)
	m2.prefs.display.FilingSpaces = []string{"Gone", "Also gone"}
	m2, _ = press(t, m2, keyText("S"))
	if m2.picker.active() {
		t.Error("a list matching nothing should open no picker")
	}
	if got := m2.status(); !strings.Contains(got, "filing_spaces") {
		t.Errorf("status = %q, want it to name the setting", got)
	}
}

// A space left out of the list is never touched.
func TestFilingSpacesLeavesUnlistedSpacesAlone(t *testing.T) {
	t.Parallel()

	m, f := filing(t)
	m.prefs.display.FilingSpaces = []string{"Friends"} // Alpha is in Work, which is not listed
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

// spaceRows is the filing picker's space rows, without its tag rows and New tag.
func spaceRows(items []pickerItem) []pickerItem {
	var out []pickerItem
	for _, item := range items {
		if !isTagGroup(item.value) && item.value != tagNew {
			out = append(out, item)
		}
	}
	return out
}

// The rows are in [display] priority, spaces and tags alike, then in the rail's order.
func TestTheFilingListFollowsThePriority(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m.prefs.display.Priority = []string{"tag:Pinned", "Friends"}
	m, _ = press(t, m, keyText("S"))
	var got []string
	for _, item := range m.picker.all {
		got = append(got, m.filingKey(item))
	}
	// All, DMs, Unread and Drafts are filled by their rules: not offered at all.
	want := []string{"tag:Pinned", "Friends", "Work", "tag:Archived"}
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Errorf("rows = %v, want %v first: priority, then the rail", got, want)
	}
}
