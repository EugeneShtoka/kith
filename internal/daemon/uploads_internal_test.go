package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
)

// What a kithd that stopped had staged is gone when the next starts; an upload no
// chunk reaches for an hour is removed, its room freed, each chunk putting that off;
// one sent leaves nothing for its timer to do.
func TestUploadsNeverSentAreRemoved(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "left-from-before"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		u, err := NewUploads(dir)
		if err != nil {
			t.Fatal(err)
		}
		if left, _ := os.ReadDir(dir); len(left) != 0 {
			t.Fatalf("a new start kept %v", left)
		}
		abandoned, err := u.start("a.bin", 10)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := u.start("b.bin", 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _, done, err := u.take(sent)
		if err != nil {
			t.Fatal(err)
		}
		done()
		for range 2 {
			time.Sleep(uploadIdle - time.Minute)
			if err := u.put(abandoned, 0, []byte("hello")); err != nil {
				t.Fatalf("a chunk %s after the last: %v", uploadIdle-time.Minute, err)
			}
		}
		time.Sleep(uploadIdle + time.Minute)
		synctest.Wait()
		if err := u.put(abandoned, 5, []byte("world")); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("a chunk to an upload idle for %s = %v, want not found", uploadIdle+time.Minute, err)
		}
		if left, _ := os.ReadDir(dir); len(left) != 0 {
			t.Errorf("staged after the hour: %v", left)
		}
		for range 2 {
			if _, err := u.start("big", uploadMost); err != nil {
				t.Errorf("the removed upload still counts against the total: %v", err)
			}
		}
	})
}
