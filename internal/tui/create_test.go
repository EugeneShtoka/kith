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
	m := update(t, New(context.Background(), b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	m = sized(t, m.clearStatus())
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	return m, b
}

// The encrypted private room is offered first: encryption cannot be added later.
func TestNewRoomOffersWholeAnswers(t *testing.T) {
	t.Parallel()

	m, _ := building(t)
	m, _ = press(t, m, keyText("n"))
	if !m.picker.active() {
		t.Fatal("n opened no picker")
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
	m, _ = press(t, m, keyText("n"))
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
	m2, _ = press(t, m2, keyText("n"))
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
	m, _ = press(t, m, keyText("n"))
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
	m2, _ = press(t, m2, keyText("n"))
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
	m, _ = press(t, m, keyText("n"))
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
	m, _ = press(t, m, keyText("n"))
	next, _ := m.chooseNewRoomKind("private")
	m = next
	if _, cmd := m.submitNewRoom("   "); cmd != nil {
		t.Error("an empty name should create nothing")
	}
	if b.spec.Name != "" {
		t.Errorf("created %q", b.spec.Name)
	}
}
