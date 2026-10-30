package tui

import (
	"fmt"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp chat known by the person's LID, then by their number. Invented.
const (
	byLID    = domain.RoomID("whatsapp:15550100/88001234567@lid")
	byNumber = domain.RoomID("whatsapp:15550100/15550199@s.whatsapp.net")
)

// A window on a chat that merges into another (its LID's into its number's) goes on in
// the one it joined when the rooms are read again, not on whichever room the cursor
// rule would land on.
func TestAWindowFollowsItsChatIntoTheOneItMerged(t *testing.T) {
	t.Parallel()
	others := make([]domain.Room, 0, 3)
	for i := range 3 {
		others = append(others, domain.Room{ID: domain.RoomID(fmt.Sprintf("whatsapp:15550100/1555018%d@s.whatsapp.net", i)), Name: fmt.Sprintf("Bea %d", i)})
	}
	before := append([]domain.Room{{ID: byLID, Name: "Ada"}, {ID: byNumber, Name: "Zoe"}}, others...)
	m := update(t, starterNew(apitest.Nop{}, config.Display{}), roomsMsg{rooms: before})
	m = sized(t, m.clearStatus()).selectRoomForTest(t, byLID)

	m = update(t, m, roomsMsg{rooms: append([]domain.Room{{ID: byNumber, Name: "Zoe", Merged: []domain.RoomID{byLID}}}, others...)})
	if m.openRoom != byNumber {
		t.Errorf("open room = %s, want the chat it merged into, %s", m.openRoom, byNumber)
	}
}
