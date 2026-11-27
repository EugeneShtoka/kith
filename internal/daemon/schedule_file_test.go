package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/schedule"
)

// staged is a file as an upload leaves it, outside the queue's folder.
func staged(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upload")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// kept lists the files the queue keeps.
func kept(t *testing.T, store *schedule.Store) []string {
	t.Helper()
	entries, err := os.ReadDir(store.Path() + ".files")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func fileMessage(id string, at, now time.Time) domain.ScheduledMessage {
	return domain.ScheduledMessage{ID: id, RoomID: mx + "!a:x", Body: "the view", FileName: "photo.jpg",
		TxnID: "txn-" + id, At: at, Written: now}
}

// A queued file outlives a restart (the uploads folder does not), goes out when due
// with its name and caption, and is removed once sent.
func TestAQueuedFileWaitsAcrossARestartAndGoesOut(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s, _, store := newScheduler(t, now, 24*time.Hour)
	upload := staged(t, "pixels")
	if _, err := s.AddFile(fileMessage("f", now.Add(time.Hour), now), upload); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(upload); !os.IsNotExist(err) {
		t.Error("the upload was left where the next start empties it")
	}

	later := NewScheduler(store, &recordingSender{}, func() time.Duration { return 24 * time.Hour }, nil)
	sender := later.sender.(*recordingSender)
	later.now = func() time.Time { return now.Add(2 * time.Hour) }
	t.Cleanup(later.Stop)
	later.startNow(context.Background())

	if len(sender.files) != 1 || sender.files[0] != (sentFile{data: "pixels", name: "photo.jpg"}) || sender.sent[0].Body != "the view" {
		t.Fatalf("files %+v, sent %+v: want the file under its name with its caption", sender.files, sender.sent)
	}
	if left := kept(t, store); len(left) != 0 {
		t.Errorf("kept %v after the send, want nothing", left)
	}
}

// A file that fails stays kept, queued for the retry; a canceled one is removed.
func TestAFileIsKeptUntilSentOrCanceled(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s, sender, store := newScheduler(t, now, 24*time.Hour)
	sender.fail = errors.New("homeserver down")
	id, err := s.AddFile(fileMessage("f", now.Add(-time.Minute), now), staged(t, "pixels"))
	if err != nil {
		t.Fatal(err)
	}
	if left := kept(t, store); len(left) != 1 || len(s.List()) != 1 {
		t.Fatalf("kept %v, queue %+v after a failed send: want both kept", left, s.List())
	}
	if _, err := s.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if left := kept(t, store); len(left) != 0 {
		t.Errorf("kept %v after the cancel, want nothing", left)
	}
}

// The same file asked for twice (the first answer lost) is queued and kept once.
func TestAFileAskedForTwiceIsKeptOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s, _, store := newScheduler(t, now, 24*time.Hour)
	first, err := s.AddFile(fileMessage("a", now.Add(time.Hour), now), staged(t, "pixels"))
	if err != nil {
		t.Fatal(err)
	}
	again := fileMessage("b", now.Add(time.Hour), now)
	again.TxnID = "txn-a"
	second, err := s.AddFile(again, staged(t, "pixels"))
	if err != nil || second != first {
		t.Fatalf("second ask = %q (%v), want the first's %q", second, err, first)
	}
	if left := kept(t, store); len(left) != 1 {
		t.Errorf("kept %v, want one copy", left)
	}
}

// A start removes kept files no queued message sends: one sent or canceled by a
// daemon that stopped before removing it.
func TestAStartSweepsFilesNothingSends(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s, _, store := newScheduler(t, now, 24*time.Hour)
	if _, err := s.AddFile(fileMessage("f", now.Add(time.Hour), now), staged(t, "pixels")); err != nil {
		t.Fatal(err)
	}
	stray, err := store.Keep(staged(t, "orphan"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Error("a file no message sends survived the start")
	}
	if left := kept(t, store); len(left) != 1 {
		t.Errorf("kept %v, want the queued message's file", left)
	}
}
