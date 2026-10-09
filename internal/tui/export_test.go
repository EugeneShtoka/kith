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

// cachedRoomBackend holds a room's cached messages, giving the newest few first and older
// ones on request, as the cache does past its first page.
type cachedRoomBackend struct {
	apitest.Nop
	all    []domain.Message
	newest int
	around *int // how many times older messages were asked for
}

func (b cachedRoomBackend) CachedTimeline(context.Context, domain.RoomID) ([]domain.Message, error) {
	return b.all[len(b.all)-b.newest:], nil
}

func (b cachedRoomBackend) MessagesAround(_ context.Context, _ domain.RoomID, event domain.EventID, before, _ int) ([]domain.Message, error) {
	*b.around++
	for i := range b.all {
		if b.all[i].ID == event {
			return b.all[max(i-before, 0) : i+1], nil
		}
	}
	return nil, nil
}

// /export writes all the cache holds of a room to a Markdown file in the downloads,
// read back past its first page, oldest first; a span stops the reading early; an open
// thread exports just the thread; and a second export never overwrites the first.
func TestExportWritesTheRoomToAFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	around := 0
	var all []domain.Message
	for i, body := range []string{"one", "two", "three", "four", "five"} {
		all = append(all, domain.Message{ID: domain.EventID("$" + body), RoomID: "!a:x", Sender: "@maya:x", SenderName: "Maya", Body: body, Timestamp: at(60 * (i + 1))})
	}
	all[4].ThreadRoot = "$four"
	b := cachedRoomBackend{all: all, newest: 2, around: &around}
	m := New(context.Background(), b, domainDisplayWithDownloads(dir))
	m = withRooms(t, m)
	room, _ := m.roomByID("!a:x")

	run := func(m Model, arg string) (string, Model) {
		t.Helper()
		next, cmd := m.openExport(room, arg)
		msg, ok := msgOf[exportedMsg](t, cmd)
		if !ok || msg.err != nil {
			t.Fatalf("export %q: %+v", arg, msg)
		}
		next = update(t, next, msg)
		data, err := os.ReadFile(msg.path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data), next
	}

	whole, _ := run(m, "")
	if i1, i5 := strings.Index(whole, "one"), strings.Index(whole, "five"); i1 < 0 || i5 < 0 || i1 > i5 || !strings.Contains(whole, "# Alpha") {
		t.Errorf("the whole export is not every message in order:\n%s", whole)
	}
	if around == 0 {
		t.Error("the export never read past the first page")
	}

	around = 0
	last, _ := run(m, "2")
	if strings.Contains(last, "three") || !strings.Contains(last, "four") || around != 0 {
		t.Errorf("the last 2 (read back %d times):\n%s", around, last)
	}

	m.thread.root = "$four"
	thread, _ := run(m, "")
	if strings.Contains(thread, "three") || !strings.Contains(thread, "five") {
		t.Errorf("the thread's export holds more than the thread:\n%s", thread)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	if len(files) != 3 {
		t.Errorf("%d files in the downloads, want one per export: %v", len(files), files)
	}
}

// domainDisplayWithDownloads is a display config saving into dir.
func domainDisplayWithDownloads(dir string) config.Display {
	var d config.Display
	d.Media.DownloadDir = dir
	return d
}
