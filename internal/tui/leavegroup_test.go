package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// leaver records the rooms left.
type leaver struct {
	apitest.Nop
	left *[]domain.RoomID
}

func (l leaver) LeaveRoom(_ context.Context, room domain.RoomID) error {
	*l.left = append(*l.left, room)
	return nil
}

// L on the rail: a Telegram forum is left via its chat after asking, taking its topics; a
// tag is deleted after asking; any other space (e.g. a bridge's account space) is refused.
func TestLeavingFromTheRail(t *testing.T) {
	t.Parallel()
	var left []domain.RoomID
	m := update(t, New(context.Background(), leaver{left: &left}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "telegram:1/-100", Name: "Baking", Forum: true},
		{ID: "telegram:1/-100~7", Name: "Recipes"},
		{ID: "telegram:1/-100~8", Name: "Market"},
		{ID: "telegram:1/42", Name: "Dana", IsDirect: true},
	}})
	m = sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "telegram:1/account", Name: "Telegram home", Children: []domain.RoomID{"telegram:1/42"}, Bridge: domain.ProtocolTelegram},
		{ID: "telegram:1/forum100", Name: "Baking", Bridge: domain.ProtocolTelegram,
			Leaving: domain.LeftByRoom, LeaveBy: "telegram:1/-100",
			Children: []domain.RoomID{"telegram:1/-100", "telegram:1/-100~7", "telegram:1/-100~8"}},
	}}))
	m.focus = paneRail

	m.rail.cursor = indexOfGroup(m.rail.groups, "Baking")
	m = pressKey(t, m, "L")
	if m.confirm.action != pendingLeaveSpace || !strings.Contains(m.confirmPrompt(), "3 rooms") {
		t.Fatalf("L on the forum asked %q (%v)", m.confirmPrompt(), m.confirm.action)
	}
	next, cmd := asModel(m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"}))
	msg, ok := msgOf[leftMsg](t, cmd)
	if !ok {
		t.Fatal("saying yes left nothing")
	}
	next = update(t, next, msg)
	if len(left) != 1 || left[0] != "telegram:1/-100" {
		t.Errorf("left %v, want the forum's chat", left)
	}
	for _, id := range []domain.RoomID{"telegram:1/-100", "telegram:1/-100~7", "telegram:1/-100~8"} {
		if _, still := next.roomByID(id); still {
			t.Errorf("%s is still listed", id)
		}
	}

	m.confirm = confirmState{} // answered above, on the copy that went on
	m.rail.cursor = indexOfGroup(m.rail.groups, "Telegram home")
	if got := pressKey(t, m, "L"); got.confirm.action != pendingNone || !strings.Contains(got.status(), "only the bridge signs out") {
		t.Errorf("L on an account's space: confirm %v, status %q", got.confirm.action, got.status())
	}

	m, _ = m.applyConfig(config.Config{Tags: []config.Tag{{Name: "Friends", Rule: []string{"dm"}}}}, "")
	m.rail.cursor = indexOfGroup(m.rail.groups, tagGroupKey("Friends"))
	if m.rail.cursor < 0 {
		t.Fatal("the tag is not on the rail")
	}
	if got := pressKey(t, m, "L"); got.confirm.action != pendingDeleteTag || got.confirm.group != "Friends" {
		t.Errorf("L on a tag: confirm %v for %q, want deleting the tag", got.confirm.action, got.confirm.group)
	}
}

// L on one room of a forum says it is left only with the whole forum; a room of an ordinary
// space is asked about as usual.
func TestAForumsRoomIsNotLeftAlone(t *testing.T) {
	t.Parallel()
	var left []domain.RoomID
	m := update(t, New(context.Background(), leaver{left: &left}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!topic:x", Name: "Trips"}, {ID: "telegram:1/-100", Name: "General", Forum: true},
		{ID: "telegram:1/-100~7", Name: "Recipes"}, {ID: mx + "!plain:x", Name: "Lounge"},
	}})
	m = sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: mx + "!forum:x", Name: "Hikers", Bridge: domain.ProtocolTelegram, Leaving: domain.LeftWhole, Children: []domain.RoomID{mx + "!topic:x"}},
		{ID: "telegram:1/forum100", Name: "Baking", Bridge: domain.ProtocolTelegram, Leaving: domain.LeftByRoom,
			LeaveBy: "telegram:1/-100", Children: []domain.RoomID{"telegram:1/-100", "telegram:1/-100~7"}},
		{ID: mx + "!mine:x", Name: "Mine", Bridge: domain.ProtocolMatrix, Leaving: domain.LeftAlone, Children: []domain.RoomID{mx + "!plain:x", mx + "!topic:x"}},
	}}))
	for room, forum := range map[domain.RoomID]string{mx + "!topic:x": "Hikers", "telegram:1/-100": "Baking", "telegram:1/-100~7": "Baking", mx + "!plain:x": ""} {
		r, _ := m.roomByID(room)
		got, _ := m.selectRoom(r)
		got.focus = paneRooms
		got = pressKey(t, got, "L")
		if forum == "" {
			if got.confirm.action != pendingLeave {
				t.Errorf("L on %s asked %v, want whether to leave it", room, got.confirm.action)
			}
			continue
		}
		if got.confirm.active() || !strings.Contains(got.status(), "part of "+forum) {
			t.Errorf("L on %s: confirm %v, status %q, want it refused for %s", room, got.confirm.action, got.status(), forum)
		}
	}
	if len(left) > 0 {
		t.Errorf("left %v", left)
	}
}
