package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// identity is a kithd device key and the certificate it presents.
func identity(t *testing.T) func() (tls.Certificate, error) {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := devicekey.Certificate(key)
	if err != nil {
		t.Fatal(err)
	}
	return func() (tls.Certificate, error) { return cert, nil }
}

// newID is a device ID never seen before.
func newID(t *testing.T) string {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// meshOf is s's mesh store as kithd opens it, under s's cache key, kept open until the
// test ends, as a running kithd keeps it.
func meshOf(t *testing.T, s domain.Storage) (*mesh.Store, func() (daemon.RosterStore, error)) {
	t.Helper()
	store, err := mesh.Open(context.Background(), s.MeshPath(), keyAt(s.DataDir))
	if err != nil {
		t.Fatalf("mesh.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, func() (daemon.RosterStore, error) { return store, nil }
}

// roster is the devices kithd lists for s with the device key in identity.
func roster(t *testing.T, s domain.Storage, store func() (daemon.RosterStore, error), id func() (tls.Certificate, error)) ([]domain.Device, error) {
	t.Helper()
	_, devices, err := daemon.NewDevices(s.DevicesPath(), store, id, slog.New(slog.DiscardHandler)).List()
	return devices, err
}

// A backup holds the mesh store, and restored on another machine the roster there is
// the one backed up: the same devices, names, roles and pairings, under that machine's
// cache key. Its kithd serves it with the device key the roster names, the bridge's.
// A new device key (a keyring that did not come along) is not one of its bridges: that
// kithd does not serve the roster, and says so.
func TestARestoredMeshStoreServesTheSameRoster(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 5)
	s := old.storage(t)
	_, store := meshOf(t, s)
	bridge := identity(t)
	d := daemon.NewDevices(s.DevicesPath(), store, bridge, slog.New(slog.DiscardHandler))
	for _, name := range []string{"laptop", "phone", "tablet"} {
		if _, err := d.Add(newID(t), name, nil); err != nil {
			t.Fatal(err)
		}
	}
	gone := newID(t)
	if _, err := d.Add(gone, "lost", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(gone); err != nil {
		t.Fatal(err)
	}
	want, err := roster(t, s, store, bridge)
	if err != nil {
		t.Fatal(err)
	}
	archive := old.backUp(t, "mesh.db")

	fresh := newMachine(t).as(t)
	asked := 0
	got, err := backup.Restore(context.Background(), archive, fresh.configPath, false, answer(testKey, &asked), keyOf)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	fs := fresh.storage(t)
	if !slices.Contains(got.Files, fs.MeshPath()) {
		t.Errorf("Restore() put back %v; want the mesh store among them", got.Files)
	}
	if plain := clearIn(t, fresh.home); len(plain) > 0 {
		t.Errorf("in the clear after the restore: %v", plain)
	}
	_, restored := meshOf(t, fs)
	served, err := roster(t, fs, restored, bridge)
	if err != nil {
		t.Fatalf("the restored roster: %v", err)
	}
	if !slices.EqualFunc(served, want, func(a, b domain.Device) bool {
		return a.Name == b.Name && a.ID == b.ID && a.Role == b.Role && a.AddedAt.Equal(b.AddedAt) && a.AddedBy == b.AddedBy
	}) {
		t.Errorf("the restored roster:\n%+v\nwant:\n%+v", served, want)
	}
	if slices.ContainsFunc(served, func(d domain.Device) bool { return d.ID == gone }) {
		t.Error("a device unpaired before the backup is paired after the restore")
	}
	if _, err := roster(t, fs, restored, identity(t)); err == nil || !strings.Contains(err.Error(), "not a bridge of its mesh store") {
		t.Errorf("the roster under a new device key: %v; want it refused, saying why", err)
	}
}

// A backup taken while kithd writes the roster holds the store as one commit left it:
// as many ops as its version vector says, and every device each of them paired, never a
// copy torn between two.
func TestABackupTakenMidWriteHoldsAWholeStore(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 5)
	s := m.storage(t)
	_, store := meshOf(t, s)
	d := daemon.NewDevices(s.DevicesPath(), store, identity(t), slog.New(slog.DiscardHandler))
	if _, _, err := d.List(); err != nil { // the genesis
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var writes sync.WaitGroup
	writes.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := d.Add(newID(t), "", nil); err != nil {
				t.Error(err)
				return
			}
		}
	})
	cfg, err := config.Load(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var archives [][]byte
	for i := range 6 {
		out := filepath.Join(t.TempDir(), "b.tar.age")
		if _, err := backup.Create(context.Background(), m.source(t, cfg), out, testKey.Recipient()); err != nil {
			t.Fatalf("Create() #%d: %v", i, err)
		}
		body, err := os.ReadFile(out) // #nosec G304 -- the test's own file
		if err != nil {
			t.Fatal(err)
		}
		archives = append(archives, body)
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	writes.Wait()
	var sizes []int
	for _, archive := range archives {
		files, manifest := entries(t, archive)
		key, err := hex.DecodeString(manifest["cache_key"].(string))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "mesh.db")
		if err = os.WriteFile(path, files["mesh.db"], 0o600); err != nil {
			t.Fatal(err)
		}
		ops, head := opsAndHead(t, path, key)
		copied, err := mesh.Open(context.Background(), path, key)
		if err != nil {
			t.Fatalf("the backup's mesh store does not open: %v", err)
		}
		members, _, err := copied.Roster()
		_ = copied.Close()
		if err != nil {
			t.Fatal(err)
		}
		// The genesis pairs the bridge; each op after it, one device.
		if ops != head || len(members) != ops {
			t.Errorf("a backup's store holds %d ops, its vector says %d, and %d members; want all three equal", ops, head, len(members))
		}
		sizes = append(sizes, ops)
	}
	if slices.Max(sizes) < 2 {
		t.Errorf("no backup was taken while the roster grew: %v", sizes)
	}
}

// opsAndHead is how many ops the store at path holds, and the seq its version vector
// holds of their origin.
func opsAndHead(t *testing.T, path string, key []byte) (ops, head int) {
	t.Helper()
	sqlDB, err := sealeddb.OpenWith(context.Background(), path, key, sealeddb.Setup{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := sqlDB.QueryRowContext(context.Background(), "SELECT count(*), (SELECT max(seq) FROM vv) FROM ops").Scan(&ops, &head); err != nil {
		t.Fatal(err)
	}
	return ops, head
}

// A backup of the layout before the mesh store (format 3, which holds none) restores
// as it did, and leaves a mesh store already there alone.
func TestABackupFromBeforeTheMeshStoreStillRestores(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 5)
	body, err := os.ReadFile(old.backUp(t)) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "old.tar.age")
	if err := os.WriteFile(archive, asFormat(t, body, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := newMachine(t).as(t)
	asked := 0
	if _, err := backup.Restore(context.Background(), archive, fresh.configPath, false, answer(testKey, &asked), keyOf); err != nil {
		t.Fatalf("Restore() of a format 3 backup: %v", err)
	}
	if b := bodies(t, fresh.storage(t).CachePath()); len(b) != 5 {
		t.Errorf("the restored cache holds %d messages, want 5", len(b))
	}
	if _, err := os.Stat(fresh.storage(t).MeshPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a backup with no mesh store left one: %v", err)
	}
	if err := os.WriteFile(archive, asFormat(t, body, 5), 0o600); err != nil {
		t.Fatal(err)
	}
	newer := newMachine(t).as(t)
	if _, err := backup.Restore(context.Background(), archive, newer.configPath, false, answer(testKey, &asked), keyOf); err == nil ||
		!strings.Contains(err.Error(), "layout is 5") {
		t.Errorf("Restore() of a newer layout: %v; want it refused, naming the layout", err)
	}
}

// asFormat is the backup archive with its manifest's layout set to format, encrypted
// again.
func asFormat(t *testing.T, archive []byte, format int) []byte {
	t.Helper()
	plain, err := age.Decrypt(bytes.NewReader(archive), testKey)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	sealed, err := age.Encrypt(&out, testKey.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	tr, tw := tar.NewReader(plain), tar.NewWriter(sealed)
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
		if hdr.Name == "kith-backup.json" {
			body = bytes.Replace(body, []byte(`"format":4`), fmt.Appendf(nil, `"format":%d`, format), 1)
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
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
