package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

const room = domain.RoomID("whatsapp:359000000000/111@s.whatsapp.net")

// testKey is the user's backup key in these tests, made at run time (a key written in
// the source would be a leaked one to a secret scanner).
var testKey = func() *age.X25519Identity {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	return id
}()

// configText keeps every directory under ~, so the same config lands under each
// machine's own home; the instance is left to be derived from where the config is.
const configText = `[storage]
data_dir = "~/data"
state_dir = "~/state"
cache_dir = "~/cache"
runtime_dir = "~/run"

[[whatsapp.account]]
name = "bg"
`

// keyAt is the cache key of the machine whose data dir is dir: each machine's its own,
// as each keyring holds its own.
func keyAt(dir string) []byte {
	sum := sha256.Sum256([]byte("the cache key at " + dir))
	return sum[:]
}

// keyOf is keyAt as Restore asks for it.
func keyOf(s domain.Storage) ([]byte, error) { return keyAt(s.DataDir), nil }

// machine is one home: a config at configPath and the storage it names there.
type machine struct {
	home, configPath string
}

// newMachine is an empty home; the test runs as it until the next one is made.
func newMachine(t *testing.T) machine {
	t.Helper()
	home := t.TempDir()
	return machine{home: home, configPath: filepath.Join(home, "config", "kith", "config.toml")}
}

// as makes m's home the current one, for ~ in its config.
func (m machine) as(t *testing.T) machine {
	t.Helper()
	t.Setenv("HOME", m.home)
	return m
}

// storage is where m's config keeps its files.
func (m machine) storage(t *testing.T) domain.Storage {
	t.Helper()
	cfg, err := config.Load(m.configPath)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	s, err := setup.StorageFor(cfg, m.configPath)
	if err != nil {
		t.Fatalf("StorageFor() error = %v", err)
	}
	return s
}

// write puts body at path under m's home, creating its folder.
func write(t *testing.T, path, body string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// fill gives m a config, n cached messages saying word, an assistant ledger, a
// send-later queue, a command and two pictures. The cache is left open, as a running
// daemon's is.
func (m machine) fill(t *testing.T, word string, n int) {
	t.Helper()
	write(t, m.configPath, configText, 0o600)
	s := m.storage(t)
	write(t, s.LedgerPath(), `{"sent":"`+word+`"}`+"\n", 0o600)
	write(t, s.SchedulePath(), "# "+word+"\n", 0o600)
	write(t, filepath.Join(filepath.Dir(m.configPath), "commands", "zoom"), "#!/bin/sh\necho "+word+"\n", 0o755)
	write(t, filepath.Join(s.MediaDir(), "a1.png"), word+" picture a", 0o600)
	write(t, filepath.Join(s.MediaDir(), "b2.jpg"), word+" picture b", 0o600)
	write(t, filepath.Join(s.MediaDir(), "renders", "a1-80x24"), "a render", 0o600)
	if err := os.MkdirAll(s.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cache, err := db.Open(context.Background(), s.CachePath(), keyAt(s.DataDir))
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	msgs := make([]domain.Message, n)
	for i := range msgs {
		msgs[i] = domain.Message{
			ID: domain.EventID(fmt.Sprintf("$%s%d", word, i)), RoomID: room, Sender: "whatsapp:359000000000/111",
			Body: fmt.Sprintf("%s %d", word, i), Timestamp: time.UnixMilli(int64(1_700_000_000_000 + i)),
		}
	}
	if err := cache.SaveMessages(context.Background(), room, msgs); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
}

// bodies is the messages cached at path, opened as the daemon opens it; it fails the
// test if that rebuilt the cache.
func bodies(t *testing.T, path string) []string {
	t.Helper()
	cache, err := db.Open(context.Background(), path, keyAt(filepath.Dir(path)))
	if err != nil {
		t.Fatalf("db.Open(%s) error = %v", path, err)
	}
	defer func() { _ = cache.Close() }()
	if cache.Rebuilt() {
		t.Fatalf("opening %s rebuilt it", path)
	}
	msgs, err := cache.Messages(context.Background(), room, -1)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(msgs))
	for i := range msgs {
		out[i] = msgs[i].Body
	}
	return out
}

// backUp writes a backup of m (made current) and returns its path; also names what it
// holds beyond what fill gives every machine.
func (m machine) backUp(t *testing.T, also ...string) string {
	t.Helper()
	m.as(t)
	cfg, err := config.Load(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "kith-backup.tar.age")
	made, err := backup.Create(context.Background(), m.source(t, cfg), out, testKey.Recipient())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if made.Path != out || len(made.Entries) != 6+len(also) {
		t.Fatalf("Create() = %+v; want five files, the pictures and %v at %s", made, also, out)
	}
	for _, name := range also {
		if !slices.ContainsFunc(made.Entries, func(e backup.Entry) bool { return e.Name == name }) {
			t.Fatalf("Create() = %+v; want %s in it", made, name)
		}
	}
	return out
}

// source is a backup of m with its pictures.
func (m machine) source(t *testing.T, cfg config.Config) backup.Source {
	t.Helper()
	s := m.storage(t)
	return backup.Source{ConfigPath: m.configPath, Config: cfg, Storage: s, CacheKey: keyAt(s.DataDir), MediaDir: s.MediaDir()}
}

// answer is a key prompt that answers id and counts how often it was asked.
func answer(id *age.X25519Identity, asked *int) func() (*age.X25519Identity, error) {
	return func() (*age.X25519Identity, error) {
		*asked++
		return id, nil
	}
}

// fingerprint is every file under home with its mode and a hash of its bytes, but
// for what any reader changes: the runtime folder (its lock and socket) and a cache's
// shared-memory index.
func fingerprint(t *testing.T, home string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, path)
		if d.IsDir() {
			if rel == "run" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, "-shm") {
			return nil
		}
		body, err := os.ReadFile(path) // #nosec G304 -- the test's own files
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		out[rel] = info.Mode().String() + " " + hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// unchanged fails the test if home's files differ from before.
func unchanged(t *testing.T, home string, before map[string]string) {
	t.Helper()
	after := fingerprint(t, home)
	for name, was := range before {
		if after[name] != was {
			t.Errorf("%s changed: %q → %q", name, was, after[name])
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("%s was left behind", name)
		}
	}
}

// A backup restored on another machine puts every file where that machine's config
// keeps it (its own instance name), and the cache opens with every message.
func TestARestoreOnANewMachineHoldsWhatTheBackupTook(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 500)
	archive := old.backUp(t)

	fresh := newMachine(t).as(t)
	asked := 0
	got, err := backup.Restore(context.Background(), archive, fresh.configPath, false, answer(testKey, &asked), keyOf)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	s := fresh.storage(t)
	if s.Instance == old.as(t).storage(t).Instance {
		t.Fatalf("both machines name their files %q; the test means them to differ", s.Instance)
	}
	fresh.as(t)
	if b := bodies(t, s.CachePath()); len(b) != 500 || b[0] != "kept 0" || b[499] != "kept 499" {
		t.Fatalf("the restored cache holds %d messages; want the 500 backed up", len(b))
	}
	for path, want := range map[string]string{
		fresh.configPath:                      configText,
		s.LedgerPath():                        `{"sent":"kept"}` + "\n",
		s.SchedulePath():                      "# kept\n",
		filepath.Join(s.MediaDir(), "a1.png"): "kept picture a",
		filepath.Join(s.MediaDir(), "b2.jpg"): "kept picture b",
		s.BackupKeyPath():                     testKey.Recipient().String() + "\n",
		filepath.Join(filepath.Dir(fresh.configPath), "commands", "zoom"): "#!/bin/sh\necho kept\n",
	} {
		body, err := os.ReadFile(path) // #nosec G304 -- the test's own file
		if err != nil || string(body) != want {
			t.Errorf("%s = %q, %v; want %q", path, body, err, want)
		}
	}
	if info, err := os.Stat(filepath.Join(filepath.Dir(fresh.configPath), "commands", "zoom")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("the command's mode = %v, %v; want it executable as it was", info.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(s.MediaDir(), "renders")); err == nil {
		t.Error("a render was restored; renders are made again")
	}
	if asked != 1 || len(got.Aside) != 0 || len(got.Left) != 0 || got.Pictures != 2 {
		t.Errorf("Restore() = %+v, asked %d times; want two pictures, nothing set aside or left, asked once", got, asked)
	}
	if to, err := backup.LoadKey(s); err != nil || to.String() != testKey.Recipient().String() {
		t.Errorf("LoadKey() after the restore = %v, %v; want the key the backup opened with, so backups go on to it", to, err)
	}
	if want := []backup.Login{{Network: "whatsapp", Account: "bg"}}; !slices.Equal(got.Logins, want) {
		t.Errorf("Logins = %v; want %v", got.Logins, want)
	}
}

// With --replace, what was there is set aside whole (the cache with every commit,
// the config as it was), and the backup takes its place. Without it, nothing changes
// and the key is not even asked for when the config alone tells.
func TestRestoreSetsAsideOnlyWhenAskedTo(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 50)
	backupPath := old.backUp(t)

	there := newMachine(t).as(t)
	there.fill(t, "local", 20)
	write(t, there.configPath, configText+"# local\n", 0o600)
	before := fingerprint(t, there.home)

	asked := 0
	_, err := backup.Restore(context.Background(), backupPath, there.configPath, false, answer(testKey, &asked), keyOf)
	if !errors.Is(err, backup.ErrWouldReplace) || asked != 0 {
		t.Fatalf("Restore() over a config = %v, asked %d; want ErrWouldReplace before asking", err, asked)
	}
	unchanged(t, there.home, before)

	s := there.storage(t)
	got, err := backup.Restore(context.Background(), backupPath, there.configPath, true, answer(testKey, &asked), keyOf)
	if err != nil {
		t.Fatalf("Restore(--replace) error = %v", err)
	}
	if b := bodies(t, s.CachePath()); len(b) != 50 || b[0] != "kept 0" {
		t.Fatalf("the cache holds %d messages, first %q; want the backup's 50", len(b), b[0])
	}
	asideCache := s.CachePath() + ".before-restore-"
	var found []string
	for _, a := range got.Aside {
		if strings.HasPrefix(a, asideCache) {
			found = append(found, a)
			if b := bodies(t, a); len(b) != 20 || b[0] != "local 0" {
				t.Errorf("the cache set aside holds %d messages; want the 20 that were there", len(b))
			}
		}
		if strings.HasPrefix(a, there.configPath+".before-restore-") {
			body, _ := os.ReadFile(a) // #nosec G304 -- the test's own file
			if !strings.Contains(string(body), "# local") {
				t.Errorf("the config set aside is %q; want the one that was there", body)
			}
		}
	}
	if len(found) != 1 || len(got.Aside) != 5 {
		t.Errorf("set aside %v; want the cache, config, ledger, queue and commands", got.Aside)
	}
}

// With no config there, files the backup's config would put in place still refuse a
// restore, once it is read; nothing is left behind.
func TestRestoreRefusesFilesTheBackupsConfigNames(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 30)
	archive := old.backUp(t)

	there := newMachine(t).as(t)
	there.fill(t, "local", 10)
	if err := os.Remove(there.configPath); err != nil {
		t.Fatal(err)
	}
	before := fingerprint(t, there.home)
	asked := 0
	_, err := backup.Restore(context.Background(), archive, there.configPath, false, answer(testKey, &asked), keyOf)
	if !errors.Is(err, backup.ErrWouldReplace) {
		t.Fatalf("Restore() = %v; want ErrWouldReplace", err)
	}
	unchanged(t, there.home, before)
}

// While kithd holds the lock of the config there, or of the one the backup brings, a
// restore refuses and changes nothing.
func TestRestoreRefusesWhileKithdRuns(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 30)
	archive := old.backUp(t)

	for name, keepConfig := range map[string]bool{"its config there": true, "only the backup's config": false} {
		t.Run(name, func(t *testing.T) {
			there := newMachine(t).as(t)
			there.fill(t, "local", 10)
			lock, held, err := daemon.Acquire(there.storage(t))
			if err != nil || !held {
				t.Fatalf("Acquire() = %v, %v", held, err)
			}
			defer func() { _ = lock.Release() }()
			if !keepConfig {
				if rmErr := os.Remove(there.configPath); rmErr != nil {
					t.Fatal(rmErr)
				}
			}
			before := fingerprint(t, there.home)
			asked := 0
			_, err = backup.Restore(context.Background(), archive, there.configPath, true, answer(testKey, &asked), keyOf)
			if !errors.Is(err, backup.ErrDaemonRunning) {
				t.Fatalf("Restore() = %v; want ErrDaemonRunning", err)
			}
			if keepConfig && asked != 0 {
				t.Errorf("asked for the key %d times before refusing; want none", asked)
			}
			unchanged(t, there.home, before)
		})
	}
}

// A backup that is cut, altered, opened with the wrong key, carries a cache
// this kith would rebuild, or is interrupted fails, and the files there (a config and
// a cache being replaced) stay exactly as they were, with nothing left behind.
func TestAFailedRestoreLeavesEverythingAsItWas(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 400)
	archive := old.backUp(t)
	body, err := os.ReadFile(archive) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	newer := newerCacheBackup(t)
	missing := withoutEntry(t, body, "state/agent-sends.jsonl")

	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	// want is the errors a fault may be reported as: an altered header no longer
	// matches the key, so age may call damage a wrong key.
	type fault struct {
		name    string
		archive []byte
		key     *age.X25519Identity
		cancel  bool
		want    []error
	}
	damagedOrWrong := []error{backup.ErrDamaged, backup.ErrWrongKey}
	faults := []fault{
		{name: "wrong key", archive: body, key: other, want: []error{backup.ErrWrongKey}},
		{name: "cache from a newer kith", archive: newer, key: testKey, want: []error{db.ErrNewerCache}},
		{name: "interrupted", archive: body, key: testKey, cancel: true, want: []error{context.Canceled}},
		{name: "a listed file missing", archive: missing, key: testKey, want: []error{backup.ErrDamaged}},
		{name: "a listed picture missing", archive: withoutEntry(t, body, "media/b2.jpg"), key: testKey, want: []error{backup.ErrDamaged}},
	}
	for _, at := range []int{20, 200, len(body) / 3, len(body) / 2, len(body) - 300, len(body) - 1} {
		faults = append(faults, fault{name: fmt.Sprintf("cut at %d", at), archive: body[:at], key: testKey, want: damagedOrWrong})
	}
	for _, at := range []int{60, 250, len(body) / 2, len(body) - 20, len(body) - 1} {
		flipped := slices.Clone(body)
		flipped[at] ^= 0x40
		faults = append(faults, fault{name: fmt.Sprintf("byte %d altered", at), archive: flipped, key: testKey, want: damagedOrWrong})
	}
	for _, f := range faults {
		t.Run(f.name, func(t *testing.T) {
			there := newMachine(t).as(t)
			there.fill(t, "local", 10)
			before := fingerprint(t, there.home)
			path := filepath.Join(t.TempDir(), "backup.tar.age")
			write(t, path, string(f.archive), 0o600)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err := backup.Restore(ctx, path, there.configPath, true, func() (*age.X25519Identity, error) {
				if f.cancel {
					cancel()
				}
				return f.key, nil
			}, keyOf)
			if !slices.ContainsFunc(f.want, func(want error) bool { return errors.Is(err, want) }) {
				t.Fatalf("Restore() = %v; want one of %v", err, f.want)
			}
			unchanged(t, there.home, before)
		})
	}
}

// newerCacheBackup is a whole backup whose cache claims a schema newer than this kith.
func newerCacheBackup(t *testing.T) []byte {
	t.Helper()
	m := newMachine(t)
	m.as(t).fill(t, "future", 5)
	s := m.storage(t)
	sqlDB, err := sealeddb.OpenWith(context.Background(), s.CachePath(), keyAt(s.DataDir), sealeddb.Setup{Init: fts5.Register})
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := sqlDB.ExecContext(context.Background(), "PRAGMA user_version = 100000"); execErr != nil {
		t.Fatal(execErr)
	}
	_ = sqlDB.Close()
	body, err := os.ReadFile(m.backUp(t)) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// withoutEntry is the backup archive with one file dropped, encrypted again: whole
// and authentic, but not holding what its list says.
func withoutEntry(t *testing.T, archive []byte, drop string) []byte {
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
		if hdr.Name == drop {
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
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

// A backup that fails or is interrupted leaves no file where it was going, and no
// copy of the cache behind; an existing file is never overwritten.
func TestAFailedBackupLeavesNoFile(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 300)
	cfg, err := config.Load(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	src := m.source(t, cfg)
	before := fingerprint(t, m.home)
	outDir := t.TempDir()
	out := filepath.Join(outDir, "kith-backup.tar.age")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backup.Create(ctx, src, out, testKey.Recipient()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create(canceled) = %v; want context.Canceled", err)
	}
	if left, _ := os.ReadDir(outDir); len(left) != 0 {
		t.Fatalf("an interrupted backup left %v", left)
	}
	unchanged(t, m.home, before)

	write(t, out, "mine", 0o600)
	if _, err := backup.Create(context.Background(), src, out, testKey.Recipient()); err == nil {
		t.Fatal("Create() over an existing file succeeded")
	}
	if body, _ := os.ReadFile(out); string(body) != "mine" { // #nosec G304 -- the test's own file
		t.Fatalf("the existing file now holds %q", body)
	}
}

// A commands folder that is a link (home-manager's, say) is not copied, and says so.
func TestABackupLeavesLinkedCommands(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 5)
	commands := filepath.Join(filepath.Dir(m.configPath), "commands")
	elsewhere := filepath.Join(t.TempDir(), "commands")
	if err := os.Rename(commands, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, commands); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	made, err := backup.Create(context.Background(), backup.Source{ConfigPath: m.configPath, Config: cfg, Storage: m.storage(t), CacheKey: keyAt(m.storage(t).DataDir)},
		filepath.Join(t.TempDir(), "b.tar.age"), testKey.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range made.Entries {
		if strings.HasPrefix(e.Name, "config/commands/") {
			t.Errorf("a linked command went in: %s", e.Name)
		}
	}
	if len(made.Left) != 1 || !strings.Contains(made.Left[0], elsewhere) {
		t.Errorf("Left = %v; want the link named", made.Left)
	}
}
