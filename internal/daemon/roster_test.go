package daemon_test

import (
	"bytes"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/BurntSushi/toml"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// rosterIn is the roster file at path as names by ID: what a person reads, or an
// editor holds.
func rosterIn(t *testing.T, path string) map[string]string {
	t.Helper()
	var f struct {
		Device []struct {
			Name string `toml:"name"`
			ID   string `toml:"id"`
			Role string `toml:"role"`
		} `toml:"device"`
	}
	if _, err := toml.DecodeFile(path, &f); err != nil && !os.IsNotExist(err) {
		t.Fatalf("the roster file: %v", err)
	}
	out := map[string]string{}
	for _, d := range f.Device {
		if d.Role != string(domain.RoleDevice) {
			t.Errorf("the roster file lists %s as %q, want a device", d.ID, d.Role)
		}
		out[d.ID] = d.Name
	}
	return out
}

// writeRosterFile writes names by ID as a hand would: name and ID alone.
func writeRosterFile(t *testing.T, path string, names map[string]string) {
	t.Helper()
	var b strings.Builder
	for _, id := range slices.Sorted(maps.Keys(names)) {
		fmt.Fprintf(&b, "[[device]]\nname = %q\nid = %q\n\n", names[id], id)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// opsIn counts the ops the mesh store at path holds.
func opsIn(t *testing.T, path string, key []byte) int {
	t.Helper()
	db, err := sealeddb.Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM ops").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rosterStep is one thing done to the roster: over the socket (add, readd, remove), in
// an editor (open, drop, put, rename, save), the file deleted, or a listing; pick chooses among what is
// there to choose.
type rosterStep struct {
	kind string
	pick int
}

// rosterModel is what the roster should hold: names by ID, and the IDs unpaired. A save
// is read as the plan has it: an entry taken out is a removal, one put in an add, a
// name changed a rename, and an entry the roster unpaired stays unpaired.
type rosterModel struct {
	paired  map[string]string
	removed map[string]bool
}

func (m rosterModel) save(buffer map[string]string) {
	for id := range m.paired {
		if _, ok := buffer[id]; !ok {
			delete(m.paired, id)
			m.removed[id] = true
		}
	}
	for id, name := range buffer {
		if _, ok := m.paired[id]; ok || !m.removed[id] {
			m.paired[id] = name
		}
	}
}

// rosterRun is a bridge's roster driven step by step against the model.
type rosterRun struct {
	t      *testing.T
	d      *daemon.Devices
	path   string
	model  rosterModel
	buffer map[string]string // an editor's copy of the file; nil while none is open
	saved  map[string]string // a save kithd has not read yet; nil when none
	names  int
}

func (r *rosterRun) freshID() string { return idOf(r.t, newDeviceKey(r.t)).String() }

func (r *rosterRun) freshName() string {
	r.names++
	return fmt.Sprintf("n%d", r.names)
}

// nth is the pick-th of keys, sorted; false when there are none.
func nth[V any](m map[string]V, pick int) (string, bool) {
	if len(m) == 0 {
		return "", false
	}
	keys := slices.Sorted(maps.Keys(m))
	return keys[pick%len(keys)], true
}

// do takes one step and, after each that reads the roster, checks it against the model.
// A step that reads the roster reads a save first, as kithd does.
func (r *rosterRun) do(s rosterStep) {
	r.t.Helper()
	switch s.kind {
	case "add", "readd", "remove", "open", "list":
		if r.saved != nil {
			r.model.save(r.saved)
			r.saved = nil
		}
	}
	switch s.kind {
	case "add":
		id, name := r.freshID(), r.freshName()
		r.add(id, name)
	case "readd":
		if id, ok := nth(r.model.removed, s.pick); ok {
			r.add(id, r.freshName())
		}
	case "remove":
		if id, ok := nth(r.model.paired, s.pick); ok {
			if err := r.d.Remove(id); err != nil {
				r.t.Fatalf("Remove(%s): %v", id, err)
			}
			delete(r.model.paired, id)
			r.model.removed[id] = true
		}
	case "open":
		if r.buffer == nil {
			r.check() // what an editor opens is the file as kithd last wrote it
			r.buffer = rosterIn(r.t, r.path)
		}
		return
	case "drop":
		if id, ok := nth(r.buffer, s.pick); ok {
			delete(r.buffer, id)
		}
		return
	case "put":
		if r.buffer != nil {
			r.buffer[r.freshID()] = r.freshName()
		}
		return
	case "rename":
		if id, ok := nth(r.buffer, s.pick); ok {
			r.buffer[id] = r.freshName()
		}
		return
	case "delete":
		// The file deleted is no edit (nor is a save it took with it): the next read
		// writes it again.
		if err := os.Remove(r.path); err != nil && !os.IsNotExist(err) {
			r.t.Fatal(err)
		}
		r.saved = nil
		return
	case "save":
		// Read by the next step that reads the roster, whichever it is.
		if r.buffer != nil {
			writeRosterFile(r.t, r.path, r.buffer)
			r.saved, r.buffer = r.buffer, nil
		}
		return
	}
	r.check()
}

func (r *rosterRun) add(id, name string) {
	r.t.Helper()
	if _, err := r.d.Add(id, name, nil); err != nil {
		r.t.Fatalf("Add(%s): %v", id, err)
	}
	r.model.paired[id] = name
	delete(r.model.removed, id)
}

// check lists the roster and reads the file: both hold the model, every one a device,
// and none of the unpaired.
func (r *rosterRun) check() {
	r.t.Helper()
	_, devices, err := r.d.List()
	if err != nil {
		r.t.Fatalf("List: %v", err)
	}
	listed := map[string]string{}
	for _, d := range devices {
		if d.Role != domain.RoleDevice {
			r.t.Errorf("%s is listed as %q, want a device", d.ID, d.Role)
		}
		listed[d.ID] = d.Name
	}
	for id := range r.model.removed {
		if _, ok := listed[id]; ok {
			r.t.Fatalf("%s was unpaired, and is paired again without `kith devices add`", id)
		}
	}
	if !maps.Equal(listed, r.model.paired) {
		r.t.Fatalf("listed %v, want %v", listed, r.model.paired)
	}
	if inFile := rosterIn(r.t, r.path); !maps.Equal(inFile, r.model.paired) {
		r.t.Fatalf("the file lists %v, want %v", inFile, r.model.paired)
	}
}

// Changes over the socket and hand edits in any order: an editor holding the file while
// devices are paired and unpaired, then saving. Taking an entry out of the file always
// unpairs it, so a save racing `kith devices add` unpairs what it never held (removing
// is the safer error; pair it again); and a stale copy never pairs again a device
// unpaired meanwhile. What is served, the file and the store reopened agree at every
// step.
func TestHandEditsInterleavedWithChangesKeepTheRosterSafe(t *testing.T) {
	t.Parallel()
	scripts := map[string][]rosterStep{
		"a save racing an add":   {{"add", 0}, {"open", 0}, {"add", 0}, {"rename", 0}, {"save", 0}, {"list", 0}},
		"a save racing a remove": {{"add", 0}, {"add", 0}, {"open", 0}, {"remove", 0}, {"rename", 1}, {"save", 0}, {"add", 0}},
		"a re-add after a stale save": {
			{"add", 0}, {"open", 0}, {"remove", 0}, {"save", 0}, {"list", 0}, {"readd", 0}, {"open", 0}, {"save", 0}, {"list", 0},
		},
		"the last entry taken out by hand": {{"add", 0}, {"open", 0}, {"drop", 0}, {"save", 0}, {"list", 0}},
		"the file deleted":                 {{"add", 0}, {"add", 0}, {"delete", 0}, {"list", 0}, {"delete", 0}, {"add", 0}},
	}
	kinds := []string{"add", "add", "readd", "remove", "open", "drop", "put", "rename", "save", "save", "delete", "list"}
	for seed := range uint64(24) {
		rng := rand.New(rand.NewPCG(seed, seed))
		var steps []rosterStep
		for range 16 {
			steps = append(steps, rosterStep{kinds[rng.IntN(len(kinds))], rng.IntN(8)})
		}
		scripts[fmt.Sprintf("seed %d", seed)] = steps
	}
	for name, steps := range scripts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path, store, key := filepath.Join(dir, "devices.toml"), filepath.Join(dir, "mesh.db"), storeKey(t)
			identity := identityOf(t, newDeviceKey(t))
			r := &rosterRun{
				t: t, path: path, model: rosterModel{paired: map[string]string{}, removed: map[string]bool{}},
				d: daemon.NewDevices(path, meshAt(t, store, key), identity, slog.New(slog.DiscardHandler)),
			}
			for i, s := range steps {
				t.Logf("step %d: %s %d", i, s.kind, s.pick)
				r.do(s)
			}
			r.do(rosterStep{kind: "list"})
			// Another kithd start: the store reopened, the file read again.
			r.d = daemon.NewDevices(path, meshAt(t, store, key), identity, slog.New(slog.DiscardHandler))
			r.check()
		})
	}
}

// A hand edit that does not read (an ID mistyped) changes nothing, not even the parts
// that do read: the file stays as written, the listener stops at the next reload, and
// once the file is fixed every device it lists is served again.
func TestAHandEditThatDoesNotReadChangesNothing(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	laptop, phone := newDeviceKey(t), newDeviceKey(t)
	b.pair(laptop, "laptop")
	b.pair(phone, "phone")
	good, err := os.ReadFile(b.roster)
	if err != nil {
		t.Fatal(err)
	}
	ops := opsIn(t, b.mesh, b.meshKey)
	// The phone taken out, and an ID with a typo put in.
	bad := fmt.Sprintf("[[device]]\nname = \"laptop\"\nid = %q\n\n[[device]]\nname = \"typo\"\nid = %q\n",
		idOf(t, laptop).String(), idOf(t, newDeviceKey(t)).String()[:20])
	if err := os.WriteFile(b.roster, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.socket.Devices(t.Context()); err == nil {
		t.Error("a roster that does not read was listed")
	}
	if _, err := b.socket.AddDevice(t.Context(), idOf(t, newDeviceKey(t)).String(), ""); err == nil {
		t.Error("a device was added over a roster that does not read")
	}
	b.paired.Listen([]string{b.addr})
	b.waitListening(b.addr, false)
	b.waitLog("not listening for paired devices", "not a device ID")
	if body, _ := os.ReadFile(b.roster); string(body) != bad {
		t.Errorf("the roster was rewritten:\n%s", body)
	}
	if n := opsIn(t, b.mesh, b.meshKey); n != ops {
		t.Errorf("the store holds %d ops, want the %d it held", n, ops)
	}
	if err := os.WriteFile(b.roster, good, 0o600); err != nil {
		t.Fatal(err)
	}
	b.paired.Listen([]string{b.addr})
	b.waitListening(b.addr, true)
	from := loopback(t)
	for name, key := range map[string]*ecdsa.PrivateKey{"laptop": laptop, "phone": phone} {
		if err := b.status(device{key: key, from: from}); err != nil {
			t.Errorf("the %s once the file was fixed: %v", name, err)
		}
	}
}

// A mesh store that does not open keeps kithd from listening, as a roster that does not
// read does: the socket is served, the log says why, nothing is paired and nothing is
// written, the store least of all. One that opens later is listened for at the next
// reload.
func TestAMeshStoreThatDoesNotOpenListensNowhere(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, path string, key []byte)
		says    string
	}{
		{"another key's", func(t *testing.T, path string, _ []byte) {
			s, err := mesh.Open(t.Context(), path, storeKey(t))
			if err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
		}, "the key does not open this store"},
		{"a newer kith's", func(t *testing.T, path string, key []byte) {
			db, err := sealeddb.Open(t.Context(), path, key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), "CREATE TABLE ops (x); PRAGMA user_version = 99"); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
		}, "written by a newer kith"},
		{"not a file", func(t *testing.T, path string, _ []byte) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "the mesh store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, key := t.TempDir(), storeKey(t)
			tc.prepare(t, filepath.Join(dir, "mesh.db"), key)
			laptop := newDeviceKey(t)
			roster := fmt.Sprintf("[[device]]\nname = \"laptop\"\nid = %q\n", idOf(t, laptop).String())
			b := serveBridge(t, bridgeOpts{dir: dir, meshKey: key, roster: roster})
			b.waitLog("not listening for paired devices", tc.says)
			if listening(b.addr) {
				t.Error("listening with a mesh store that does not open")
			}
			if _, err := b.socket.AddDevice(t.Context(), idOf(t, newDeviceKey(t)).String(), ""); err == nil {
				t.Error("a device was added with no mesh store")
			}
			if body, _ := os.ReadFile(b.roster); string(body) != roster {
				t.Errorf("the roster was rewritten:\n%s", body)
			}
			if _, err := os.Stat(b.roster + ".pre-mesh"); !os.IsNotExist(err) {
				t.Errorf("the roster was moved aside for a store that did not open: %v", err)
			}
			if tc.name == "a newer kith's" {
				db, err := sealeddb.Open(t.Context(), b.mesh, key)
				if err != nil {
					t.Fatal(err)
				}
				var version int
				if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 99 {
					t.Errorf("the newer store's version = %d, %v; want it untouched", version, err)
				}
				_ = db.Close()
			}
			if tc.name != "not a file" {
				return
			}
			if err := os.Remove(b.mesh); err != nil {
				t.Fatal(err)
			}
			b.paired.Listen([]string{b.addr})
			b.waitListening(b.addr, true)
			if err := b.status(device{key: laptop}); err != nil {
				t.Errorf("the laptop once the store opened: %v", err)
			}
		})
	}
}

// The roster file read into the mesh store once, cut off at each point and run again,
// ends as a migration never cut off does: the same devices, the same file, the old file
// kept as .pre-mesh, and only the genesis in the store.
func TestAMigrationCutOffIsMadeAgainWhole(t *testing.T) {
	t.Parallel()
	bridgeKey := newDeviceKey(t)
	identity := identityOf(t, bridgeKey)
	old := fmt.Sprintf("[[device]]\n  name = \"laptop\"\n  id = %q\n  added_at = 2026-09-01T10:00:00Z\n  added_by = %q\n\n"+
		"[[device]]\n  name = \"phone\"\n  id = %q\n  added_at = 2026-09-02T11:30:00Z\n  added_by = %q\n",
		idOf(t, newDeviceKey(t)).String(), idOf(t, bridgeKey).String(), idOf(t, newDeviceKey(t)).String(), idOf(t, newDeviceKey(t)).String())
	type migrated struct {
		devices []domain.Device
		file    string
	}
	migrate := func(t *testing.T, dir string, key []byte) migrated {
		t.Helper()
		d := daemon.NewDevices(filepath.Join(dir, "devices.toml"), meshAt(t, filepath.Join(dir, "mesh.db"), key), identity, slog.New(slog.DiscardHandler))
		_, devices, err := d.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		file, err := os.ReadFile(filepath.Join(dir, "devices.toml"))
		if err != nil {
			t.Fatal(err)
		}
		return migrated{devices, string(file)}
	}
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	refDir := t.TempDir()
	write(t, filepath.Join(refDir, "devices.toml"), old)
	want := migrate(t, refDir, storeKey(t))
	if len(want.devices) != 2 || !strings.Contains(want.file, `role = "device"`) {
		t.Fatalf("an uncut migration: %+v\n%s", want.devices, want.file)
	}
	for _, tc := range []struct {
		name string
		cut  func(t *testing.T, dir string, key []byte)
	}{
		{"before anything", func(*testing.T, string, []byte) {}},
		{"the old file kept, an older copy beside it", func(t *testing.T, dir string, _ []byte) {
			write(t, filepath.Join(dir, "devices.toml.pre-mesh"), "# an earlier try\n")
		}},
		{"the store laid out, no genesis", func(t *testing.T, dir string, key []byte) {
			write(t, filepath.Join(dir, "devices.toml.pre-mesh"), old)
			s, err := mesh.Open(t.Context(), filepath.Join(dir, "mesh.db"), key)
			if err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
		}},
		{"the genesis kept, the file not written", func(t *testing.T, dir string, key []byte) {
			migrate(t, dir, key)
			write(t, filepath.Join(dir, "devices.toml"), old)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, key := t.TempDir(), storeKey(t)
			write(t, filepath.Join(dir, "devices.toml"), old)
			tc.cut(t, dir, key)
			got := migrate(t, dir, key)
			if !slices.EqualFunc(got.devices, want.devices, func(a, b domain.Device) bool {
				return a.Name == b.Name && a.ID == b.ID && a.Role == b.Role && a.AddedAt.Equal(b.AddedAt) && a.AddedBy == b.AddedBy
			}) {
				t.Errorf("devices %+v, want %+v", got.devices, want.devices)
			}
			if got.file != want.file {
				t.Errorf("the file:\n%s\nwant:\n%s", got.file, want.file)
			}
			if kept, _ := os.ReadFile(filepath.Join(dir, "devices.toml.pre-mesh")); string(kept) != old {
				t.Errorf("devices.toml.pre-mesh:\n%s\nwant the file as it was:\n%s", kept, old)
			}
			if n := opsIn(t, filepath.Join(dir, "mesh.db"), key); n != 1 {
				t.Errorf("the store holds %d ops, want the genesis alone", n)
			}
		})
	}
}

// A bridge whose device key its mesh store does not list (the key changed since the
// store was made) signs nothing into it: it does not listen, says why, and writes
// neither the store nor the file.
func TestABridgeNotInItsMeshStoreListensNowhere(t *testing.T) {
	t.Parallel()
	dir, key := t.TempDir(), storeKey(t)
	before := daemon.NewDevices(filepath.Join(dir, "devices.toml"), meshAt(t, filepath.Join(dir, "mesh.db"), key),
		identityOf(t, newDeviceKey(t)), slog.New(slog.DiscardHandler))
	if _, err := before.Add(idOf(t, newDeviceKey(t)).String(), "laptop", nil); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(dir, "devices.toml"))
	if err != nil {
		t.Fatal(err)
	}
	ops := opsIn(t, filepath.Join(dir, "mesh.db"), key)
	b := serveBridge(t, bridgeOpts{dir: dir, meshKey: key}) // a new key
	b.waitLog("not listening for paired devices", "not a bridge of its mesh store")
	if listening(b.addr) {
		t.Error("listening as a key the store does not list")
	}
	if _, err := b.socket.AddDevice(t.Context(), idOf(t, newDeviceKey(t)).String(), "phone"); err == nil {
		t.Error("a key the store does not list paired a device")
	}
	if err := b.socket.RemoveDevice(t.Context(), idOf(t, newDeviceKey(t)).String()); err == nil {
		t.Error("a key the store does not list unpaired a device")
	}
	if n := opsIn(t, b.mesh, key); n != ops {
		t.Errorf("the store holds %d ops, want the %d it held", n, ops)
	}
	if after, _ := os.ReadFile(b.roster); !bytes.Equal(after, file) {
		t.Errorf("the roster was rewritten:\n%s", after)
	}
}

// The last device unpaired, over the socket or by hand, leaves the bridge pairing
// devices as before; the bridge itself is never unpaired.
func TestTheLastDeviceGoesAndTheBridgeStays(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	laptop := newDeviceKey(t)
	b.pair(laptop, "laptop")
	if err := b.socket.RemoveDevice(t.Context(), b.self.String()); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unpairing the bridge itself: %v, want invalid_argument", err)
	}
	from := loopback(t)
	if err := b.status(device{key: laptop, from: from}); err != nil {
		t.Fatalf("the laptop after the bridge was not unpaired: %v", err)
	}
	b.unpair(laptop)
	if devices := b.devices(); len(devices) != 0 {
		t.Fatalf("after the last device went: %+v", devices)
	}
	if inFile := rosterIn(t, b.roster); len(inFile) != 0 {
		t.Fatalf("the file lists %v", inFile)
	}
	if err := b.status(device{key: laptop, from: from}); err == nil {
		t.Error("the laptop unpaired was served")
	}
	phone := newDeviceKey(t)
	b.pair(phone, "phone")
	if err := b.status(device{key: phone, from: from}); err != nil {
		t.Fatalf("a device paired after the last went: %v", err)
	}
	writeRosterFile(t, b.roster, nil) // the last taken out by hand
	if devices := b.devices(); len(devices) != 0 {
		t.Fatalf("after the last device was taken out by hand: %+v", devices)
	}
	b.pair(laptop, "laptop again")
	if err := b.status(device{key: laptop, from: loopback(t)}); err != nil {
		t.Fatalf("a device paired again after the last went by hand: %v", err)
	}
}
