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

// /export writes all the cache holds of a room to a Markdown file in export_dir,
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
	m := New(context.Background(), b, config.Display{})
	m.conf.base.Storage.ExportDir = dir
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
		t.Errorf("%d files in export_dir, want one per export: %v", len(files), files)
	}
}

// A place after the span sends the export there: a folder (made if missing, spaces in
// its name and all), or a file when it ends in .md; a name already taken is not
// written over.
func TestExportGoesWhereItIsSent(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	around := 0
	all := []domain.Message{{ID: "$1", RoomID: "!a:x", Sender: "@maya:x", SenderName: "Maya", Body: "hello", Timestamp: at(60)}}
	m := New(context.Background(), cachedRoomBackend{all: all, newest: 1, around: &around}, config.Display{})
	m.conf.base.Storage.ExportDir = filepath.Join(home, "default")
	m = withRooms(t, m)
	room, _ := m.roomByID("!a:x")
	export := func(arg string) string {
		t.Helper()
		_, cmd := m.openExport(room, arg)
		msg, ok := msgOf[exportedMsg](t, cmd)
		if !ok || msg.err != nil {
			t.Fatalf("export %q: %+v", arg, msg)
		}
		return msg.path
	}
	folder := filepath.Join(home, "Chat History")
	file := filepath.Join(home, "backups", "friends-backup.md")
	for _, tc := range []struct{ arg, wantDir, wantName string }{
		{"", filepath.Join(home, "default"), "Alpha "},
		{folder, folder, "Alpha "},
		{"500 " + file, filepath.Dir(file), "friends-backup.md"},
		{file, filepath.Dir(file), "friends-backup (2).md"},
	} {
		got := export(tc.arg)
		if filepath.Dir(got) != tc.wantDir || !strings.HasPrefix(filepath.Base(got), tc.wantName) {
			t.Errorf("/export %s wrote %s, want %s/%s…", tc.arg, got, tc.wantDir, tc.wantName)
		}
	}
	if span, where := splitExportArg("2026-09-18 ~/Chat History/x.md"); span != "2026-09-18" || where != "~/Chat History/x.md" {
		t.Errorf("split = %q, %q", span, where)
	}
}
