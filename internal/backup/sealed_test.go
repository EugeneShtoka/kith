package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/ncruces/go-sqlite3/ext/fts5"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// entries is every file in the backup archive, by name, with the manifest decoded.
func entries(t *testing.T, archive []byte) (map[string][]byte, map[string]any) {
	t.Helper()
	plain, err := age.Decrypt(bytes.NewReader(archive), testKey)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	tr := tar.NewReader(plain)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = body
	}
	var manifest map[string]any
	if err := json.Unmarshal(out["kith-backup.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	return out, manifest
}

// clearIn names every file under dir that is a plain SQLite database: history in the
// clear.
func clearIn(t *testing.T, dir string) []string {
	t.Helper()
	var plain []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			body, _ := os.ReadFile(path) // #nosec G304 -- the test's own folder
			if bytes.HasPrefix(body, []byte("SQLite format 3")) {
				plain = append(plain, path)
			}
		}
		return nil // a folder gone mid-walk holds nothing
	})
	return plain
}

// A backup's cache is encrypted under a key of its own, not the machine's: the
// archive alone, decrypted, holds no history in the clear. Restored on another machine
// it is written under that machine's key, the one its keyring gives, and nothing is
// left in the clear there.
func TestABackupsCacheIsNeverInTheClear(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 40)
	oldKey := keyAt(old.storage(t).DataDir)
	archivePath := old.backUp(t)
	archive, err := os.ReadFile(archivePath) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	files, manifest := entries(t, archive)
	cache := files["cache.db"]
	if bytes.HasPrefix(cache, []byte("SQLite format 3")) {
		t.Fatal("the backup carries the cache in the clear")
	}
	key, err := hex.DecodeString(manifest["cache_key"].(string))
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(t.TempDir(), "cache.db")
	if err := os.WriteFile(staged, cache, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sealeddb.OpenWith(context.Background(), staged, oldKey, sealeddb.Setup{ReadOnly: true, Init: fts5.Register}); !errors.Is(err, sealeddb.ErrWrongKey) {
		t.Errorf("the machine's own key opens the backup's cache: %v", err)
	}
	if bytes.Equal(key, oldKey) {
		t.Error("the backup's key is the machine's")
	}
	if left := clearIn(t, old.home); len(left) > 0 {
		t.Errorf("making the backup left history in the clear: %v", left)
	}

	there := newMachine(t).as(t)
	there.fill(t, "local", 10)
	if _, err := backup.Restore(context.Background(), archivePath, there.configPath, true, func() (*age.X25519Identity, error) { return testKey, nil }, keyOf); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	s := there.storage(t)
	if b := bodies(t, s.CachePath()); len(b) != 40 || b[0] != "kept 0" {
		t.Fatalf("the restored cache holds %d messages under this machine's key; want the 40", len(b))
	}
	if _, err := db.Open(context.Background(), s.CachePath(), oldKey); !errors.Is(err, db.ErrWrongKey) {
		t.Errorf("the old machine's key opens the restored cache: %v", err)
	}
	if left := clearIn(t, there.home); len(left) > 0 {
		t.Errorf("the restore left history in the clear: %v", left)
	}
}
