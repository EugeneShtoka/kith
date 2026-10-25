package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// roomsBackend holds several rooms and each one's cached messages.
type roomsBackend struct {
	apitest.Nop
	rooms []domain.Room
	msgs  map[domain.RoomID][]domain.Message
}

func (b roomsBackend) Rooms(context.Context) ([]domain.Room, error) { return b.rooms, nil }

func (b roomsBackend) CachedTimeline(_ context.Context, room domain.RoomID) ([]domain.Message, error) {
	return b.msgs[room], nil
}

// `kith export` writes each room with messages in the span to its own file, named and
// with senders named and times told as the client does (your names and clock); a room
// with nothing in the span gets none; two rooms of one name get two files.
func TestEveryRoomIsExportedToItsOwnFile(t *testing.T) {
	t.Parallel()
	msg := func(room domain.RoomID, id, body string, minute int) domain.Message {
		return domain.Message{ID: domain.EventID(id), RoomID: room, Sender: "matrix:@maya:x", SenderName: "maya", Body: body, Timestamp: at(60 * minute)}
	}
	b := roomsBackend{
		rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo"}, {ID: mx + "!c:x", Name: "Bravo"}, {ID: mx + "!d:x", Name: "Quiet"}},
		msgs: map[domain.RoomID][]domain.Message{
			mx + "!a:x": {msg(mx+"!a:x", mx+"$a1", "old news", 1), msg(mx+"!a:x", mx+"$a2", "fresh news", 3)},
			mx + "!b:x": {msg(mx+"!b:x", mx+"$b1", "first bravo", 2)},
			mx + "!c:x": {msg(mx+"!c:x", mx+"$c1", "second bravo", 2)},
		},
	}
	cfg := config.Config{Display: config.Display{
		TimeFormat: "12h",
		Names:      []config.DisplayName{{Target: mx + "!a:x", Name: "Team"}},
		Identities: []config.Identity{{Alias: "Maya K", IDs: []string{"matrix:@maya:x"}}},
	}}
	files := func(t *testing.T, span domain.SummarySpan) map[string]string {
		t.Helper()
		dir := t.TempDir()
		var said []Exported
		if err := ExportAll(context.Background(), b, cfg, span, dir, func(e Exported) { said = append(said, e) }); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, e := range said {
			data, err := os.ReadFile(e.Path)
			if err != nil {
				t.Fatal(err)
			}
			out[filepath.Base(e.Path)] = string(data)
		}
		if written, _ := filepath.Glob(filepath.Join(dir, "*.md")); len(written) != len(out) {
			t.Errorf("%d files written, %d said", len(written), len(out))
		}
		return out
	}

	all := files(t, domain.SummarySpan{})
	if len(all) != 3 {
		t.Fatalf("files = %v, want Team and two Bravos; Quiet has nothing", keysOf(all))
	}
	var team string
	for name, text := range all {
		if strings.HasPrefix(name, "Team") || strings.Contains(name, "Team") {
			team = text
		}
	}
	if !strings.Contains(team, "old news") || !strings.Contains(team, "Maya K") || !strings.Contains(team, " AM") {
		t.Errorf("Team's file does not hold its messages with your names and clock:\n%s", team)
	}

	last := files(t, domain.SummarySpan{Count: 1})
	for _, text := range last {
		if strings.Contains(text, "old news") {
			t.Errorf("the last 1 holds an older message:\n%s", text)
		}
	}
	since := files(t, domain.SummarySpan{Since: at(60 * 3), Said: "today"})
	if len(since) != 1 {
		t.Errorf("since minute 3: files = %v, want only Team's", keysOf(since))
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
