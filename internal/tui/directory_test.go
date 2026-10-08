package tui

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// bookBackend answers with a directory.
type bookBackend struct {
	apitest.Nop
	dir domain.Directory
}

func (b bookBackend) Directory(context.Context) (domain.Directory, error) { return b.dir, nil }

// numbersNamed is a directory naming each number as one address book saved it.
func numbersNamed(names map[string]string) domain.Directory {
	var rows []domain.PersonName
	for digits, name := range names {
		rows = append(rows, domain.PersonName{Source: "phone", ID: domain.PhoneID(digits), Name: name, Rank: domain.RankSaved})
	}
	return domain.NewDirectory(rows, nil)
}

// A room, a sender and a member a network shows only as a number are named from the
// directory, read with the room list; your own names come first, and a label that is
// more than a number is left alone.
func TestANumberIsNamedFromTheDirectory(t *testing.T) {
	t.Parallel()
	b := bookBackend{dir: numbersNamed(map[string]string{"15550100001": "Dana", "15550100002": "Eli", "15550100003": "Fay"})}
	display := config.Display{
		Names:      []config.DisplayName{{Target: "!e:x", Name: "Eli (mine)"}},
		Identities: []config.Identity{{Alias: "Fay (mine)", IDs: []string{"@whatsapp_15550100003:x"}}},
	}
	m := New(context.Background(), b, display)
	m, cmd := asModel(m.Update(roomsMsg{rooms: []domain.Room{
		{ID: "!d:x", Name: "+15550100001 (WA)", IsDirect: true},
		{ID: "!e:x", Name: "+15550100002", IsDirect: true},
		{ID: "!g:x", Name: "Dana +15550100001"},
	}}))
	read, ok := msgOf[directoryMsg](t, cmd)
	if !ok {
		t.Fatal("a room list read no directory")
	}
	m = update(t, m, read)

	for id, want := range map[domain.RoomID]string{"!d:x": "Dana", "!e:x": "Eli (mine)", "!g:x": "Dana +15550100001"} {
		room, _ := m.roomByID(id)
		if got := m.roomLabel(room); got != want {
			t.Errorf("room %s = %q, want %q", id, got, want)
		}
	}
	for _, c := range []struct{ sender, shown, want string }{
		{"@whatsapp_il_15550100001:x", "+1 555 010 0001 (WA)", "Dana"},
		{"@whatsapp_15550100003:x", "+15550100003 (WA)", "Fay (mine)"},
		{"@whatsapp_15550100001:x", "Dana's old name", "Dana's old name"},
	} {
		if got := m.processedName(domain.Message{RoomID: "!d:x", Sender: c.sender, SenderName: c.shown}); got != c.want {
			t.Errorf("sender %s shown %q = %q, want %q", c.sender, c.shown, got, c.want)
		}
	}
	if _, name := m.personOf(domain.Member{UserID: "@whatsapp_15550100001:x", DisplayName: "+15550100001"}); name != "Dana" {
		t.Errorf("member = %q, want Dana", name)
	}
}
