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

// A room shown only as a number is named from the directory (read with the room list);
// your own names win, and a label that is more than a number is left alone. People are
// kithd's to name.
func TestANumberIsNamedFromTheDirectory(t *testing.T) {
	t.Parallel()
	b := bookBackend{dir: numbersNamed(map[string]string{"15550100001": "Dana", "15550100002": "Eli"})}
	display := config.Display{Names: []config.DisplayName{{Target: mx + "!e:x", Name: "Eli (mine)"}}}
	m := New(context.Background(), b, display, config.DeviceDisplay{})
	m, cmd := asModel(m.Update(roomsMsg{rooms: []domain.Room{
		{ID: mx + "!d:x", Name: "+15550100001 (WA)", IsDirect: true},
		{ID: mx + "!e:x", Name: "+15550100002", IsDirect: true},
		{ID: mx + "!g:x", Name: "Dana +15550100001"},
	}}))
	read, ok := msgOf[directoryMsg](t, cmd)
	if !ok {
		t.Fatal("a room list read no directory")
	}
	m = update(t, m, read)

	for id, want := range map[domain.RoomID]string{mx + "!d:x": "Dana", mx + "!e:x": "Eli (mine)", mx + "!g:x": "Dana +15550100001"} {
		room, _ := m.roomByID(id)
		if got := m.roomLabel(room); got != want {
			t.Errorf("room %s = %q, want %q", id, got, want)
		}
	}
}
