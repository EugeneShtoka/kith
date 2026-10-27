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
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := age.Decrypt(bytes.NewReader(archive), identity)
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
	if _, err := backup.Restore(context.Background(), archivePath, there.configPath, true, func() (string, error) { return passphrase, nil }, keyOf); err != nil {
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

// A backup made before caches were encrypted (layout 1: the cache in the clear, no
// key) still restores, and is written encrypted under the machine's key.
func TestABackupFromBeforeEncryptionRestoresEncrypted(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 25)
	archivePath := old.backUp(t)
	archive, err := os.ReadFile(archivePath) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	files, manifest := entries(t, archive)
	key, err := hex.DecodeString(manifest["cache_key"].(string))
	if err != nil {
		t.Fatal(err)
	}

	// The cache as a layout-1 backup held it: a plain copy.
	dir := t.TempDir()
	sealed, plain := filepath.Join(dir, "sealed.db"), filepath.Join(dir, "plain.db")
	if err = os.WriteFile(sealed, files["cache.db"], 0o600); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := sealeddb.OpenWith(context.Background(), sealed, key, sealeddb.Setup{ReadOnly: true, Init: fts5.Register})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sqlDB.ExecContext(context.Background(), "VACUUM INTO ?", "file:"+plain+"?vfs=os"); err != nil {
		t.Fatal(err)
	}
	_ = sqlDB.Close()
	if files["cache.db"], err = os.ReadFile(plain); err != nil { // #nosec G304 -- the test's own file
		t.Fatal(err)
	}
	delete(manifest, "cache_key")
	manifest["format"] = 1
	if files["kith-backup.json"], err = json.Marshal(manifest); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(t.TempDir(), "older.tar.age")
	write(t, older, string(archiveOf(t, files, manifest)), 0o600)

	fresh := newMachine(t).as(t)
	if _, err := backup.Restore(context.Background(), older, fresh.configPath, false, func() (string, error) { return passphrase, nil }, keyOf); err != nil {
		t.Fatalf("Restore(layout 1) error = %v", err)
	}
	s := fresh.storage(t)
	if b := bodies(t, s.CachePath()); len(b) != 25 {
		t.Fatalf("the restored cache holds %d messages; want the 25", len(b))
	}
	if left := clearIn(t, fresh.home); len(left) > 0 {
		t.Errorf("the restore left history in the clear: %v", left)
	}
}

// archiveOf is a backup holding files, in the manifest's order, under passphrase.
func archiveOf(t *testing.T, files map[string][]byte, manifest map[string]any) []byte {
	t.Helper()
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	sealed, err := age.Encrypt(&out, recipient)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(sealed)
	names := []string{"kith-backup.json"}
	for _, n := range manifest["files"].([]any) {
		names = append(names, n.(string))
	}
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sealed.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
