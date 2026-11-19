package daemon_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
)

// A config bridge is one kithd's config.toml and mesh store, as the sharing sees them:
// no socket, no loop. A test settles it where the loop (followConfig) or the watcher
// would, so every interleaving of theirs is a sequence of steps.
type configBridge struct {
	t      *testing.T
	name   string
	path   string
	key    *ecdsa.PrivateKey
	store  *mesh.Store
	file   *daemon.ConfigFile
	popups []string
	// buffer is an editor's copy of the file, opened and not saved yet; saved is set
	// once it is saved and until the next settle reads it.
	buffer string
	saved  bool
	// edited is the value the last save gave first_name_only, as the mesh shows it;
	// settled its value in the file as the last settle left it.
	edited, settled string
	// broken is the file before a hand edit left it unreadable; "" while it reads.
	broken string
}

// sharedCheck is the check kithd makes of a config, for these tests: one that names
// both of two values no bridge would run with, so two writes each valid can together
// not be.
func sharedCheck(_ context.Context, cfg config.Config) error {
	if cfg.Display.FirstNameOnly && cfg.Notifications.MaxPerRoom == 7 {
		return errors.New("first names alone and 7 a room do not go together")
	}
	return nil
}

// newConfigBridge is a bridge whose file holds text and whose store is its own mesh's
// genesis, its config shared there as it would be once kithd started.
func newConfigBridge(t *testing.T, name, text string) *configBridge {
	t.Helper()
	dir := t.TempDir()
	b := &configBridge{t: t, name: name, path: filepath.Join(dir, "config.toml"), key: newDeviceKey(t)}
	if err := os.WriteFile(b.path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := mesh.Open(t.Context(), filepath.Join(dir, "mesh.db"), storeKey(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b.store = store
	self := idOf(t, b.key)
	if err := store.ChangeRoster(t.Context(), b.key, []domain.Device{{ID: self.String(), Name: name, Role: domain.RoleBridge}}, nil); err != nil {
		t.Fatal(err)
	}
	paired := daemon.NewDevices(filepath.Join(dir, "devices.toml"), func() (daemon.RosterStore, error) { return store, nil },
		identityOf(t, b.key), slog.New(slog.DiscardHandler))
	b.file = daemon.NewConfigFile(b.path)
	b.file.Share(paired, func(s string) { b.popups = append(b.popups, s) }, slog.New(slog.DiscardHandler))
	if err := b.settle(); err != nil {
		t.Fatal(err)
	}
	return b
}

// settle is the loop's tick: the file and the mesh brought together. A config that does
// not read is no failure here: the step that wrote one says what it expects.
func (b *configBridge) settle() error {
	b.t.Helper()
	_, err := b.file.Settle(b.t.Context(), sharedCheck, nil, false)
	return err
}

// text is the file as it stands.
func (b *configBridge) text() string {
	b.t.Helper()
	data, err := os.ReadFile(b.path)
	if err != nil {
		b.t.Fatal(err)
	}
	return string(data)
}

// cfg is the file as it reads.
func (b *configBridge) cfg() config.Config {
	b.t.Helper()
	cfg, err := config.Decode(b.text())
	if err != nil {
		b.t.Fatalf("%s's file does not read: %v\n%s", b.name, err, b.text())
	}
	return cfg
}

// set is a settings write of one change, made on the file as a client reads it, and
// made again on the file as it reads after, when the file moved meanwhile, as a client
// does.
func (b *configBridge) set(change func(*config.Config)) error {
	b.t.Helper()
	if b.broken != "" {
		return nil // no client reads a file that does not read, so none writes over it
	}
	for range 2 {
		snap, err := b.file.Read()
		if err != nil {
			b.t.Fatal(err)
		}
		cfg := snap.Config.Clone()
		change(&cfg)
		if _, err = b.file.Write(b.t.Context(), snap.Revision, cfg, sharedCheck, nil); !errors.Is(err, api.ErrConfigMoved) {
			return err
		}
	}
	return api.ErrConfigMoved
}

// shared is the file's shared keys, as registers.
func (b *configBridge) shared() map[string]string {
	b.t.Helper()
	leaves, err := config.Leaves(b.cfg())
	if err != nil {
		b.t.Fatal(err)
	}
	out := map[string]string{}
	for path, v := range leaves {
		if !config.IsLocal(path) {
			out[path] = config.LeafText(v)
		}
	}
	return out
}

// fileValue is the file's value of the key at path, as shown; "unset" for none.
func (b *configBridge) fileValue(path string) string {
	b.t.Helper()
	leaves, err := config.Leaves(b.cfg())
	if err != nil {
		b.t.Fatal(err)
	}
	if v, ok := leaves[path]; ok {
		return config.LeafText(v)
	}
	return "unset"
}

// meshValue is the mesh's applied value of the key at path on b, as shown; "unset" for
// none.
func (b *configBridge) meshValue(path string) string {
	values, _ := b.store.Config()
	if v, ok := values[path]; ok {
		return config.LeafText(v)
	}
	return "unset"
}

// framePipe is one end of an in-memory sync stream.
type framePipe struct {
	in   <-chan *meshv1.SyncFrame
	out  chan<- *meshv1.SyncFrame
	done <-chan struct{}
}

func (p framePipe) Send(f *meshv1.SyncFrame) error {
	select {
	case p.out <- proto.CloneOf(f):
		return nil
	case <-p.done:
		return io.EOF
	}
}

func (p framePipe) Receive() (*meshv1.SyncFrame, error) {
	select {
	case f := <-p.in:
		return f, nil
	case <-p.done:
		return nil, io.EOF
	}
}

// pipes is a sync stream's two ends, ending with done.
func pipes(done <-chan struct{}) (framePipe, framePipe) {
	ab, ba := make(chan *meshv1.SyncFrame, 64), make(chan *meshv1.SyncFrame, 64)
	return framePipe{in: ba, out: ab, done: done}, framePipe{in: ab, out: ba, done: done}
}

// joinConfig has b join a's mesh: a admits it as a bridge, and b takes the mesh's
// state as `kith join --bridge` has it, then its config, as JoinMesh does.
func joinConfig(t *testing.T, a, b *configBridge) {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(b.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	bid := idOf(t, b.key)
	if err := a.store.Bridge(t.Context(), a.key, domain.Device{ID: bid.String(), Name: b.name, Role: domain.RoleBridge}, der, b.store.Sealing(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pa, pb := pipes(ctx.Done())
	var served sync.WaitGroup
	served.Go(func() { _ = a.store.Sync(ctx, a.key, bid, false, pa, slog.New(slog.DiscardHandler)) })
	if _, err := b.store.Join(ctx, bid, idOf(t, a.key), b.key, pb); err != nil {
		t.Fatalf("join: %v", err)
	}
	cancel()
	served.Wait()
	_ = b.settle()
}

// exchange syncs a and b until each holds what the other does.
func exchange(t *testing.T, a, b *configBridge) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	pa, pb := pipes(ctx.Done())
	aid, bid := idOf(t, a.key), idOf(t, b.key)
	var running sync.WaitGroup
	running.Go(func() { _ = a.store.Sync(ctx, a.key, bid, true, pa, slog.New(slog.DiscardHandler)) })
	running.Go(func() { _ = b.store.Sync(ctx, b.key, aid, false, pb, slog.New(slog.DiscardHandler)) })
	defer func() {
		cancel()
		running.Wait()
	}()
	deadline := time.Now().Add(settle)
	for !caughtUp(t, a, bid) || !caughtUp(t, b, aid) {
		if time.Now().After(deadline) {
			t.Fatal("the bridges never caught up")
		}
		time.Sleep(time.Millisecond)
	}
}

// caughtUp reports whether a is connected to peer and peer has acknowledged all a holds.
func caughtUp(t *testing.T, a *configBridge, peer fmt.Stringer) bool {
	t.Helper()
	st, err := a.store.Status(idOf(t, a.key))
	if err != nil {
		t.Fatal(err)
	}
	for i := range st.Peers {
		p := &st.Peers[i]
		if p.Bridge.ID == peer.String() {
			return p.Connected && p.Lag == 0 && !p.Heard.IsZero()
		}
	}
	return false
}

// startConfig is the file both bridges of a test start from: a shared key and a local
// one set.
const startConfig = `
[notifications]
max_per_room = 1

[spell]
command = "/usr/bin/hunspell"
`

// configMesh is two bridges of one mesh, b having joined a's with the same config.
func configMesh(t *testing.T) (a, b *configBridge) {
	t.Helper()
	a, b = newConfigBridge(t, "a", startConfig), newConfigBridge(t, "b", startConfig)
	joinConfig(t, a, b)
	a.settled, b.settled = "unset", "unset"
	return a, b
}

// quiesce is every bridge synced and settled, twice over: what the loop reaches once
// nothing more is written.
func (s *configScenario) quiesce(t *testing.T) {
	t.Helper()
	s.mend(t, 0)
	s.mend(t, 1)
	for range 2 {
		exchange(t, s.b[0], s.b[1])
		s.settle(t, 0)
		s.settle(t, 1)
	}
}

// A config step is one thing that happens to a mesh of two bridges: a settings write, an
// editor opening or saving the file, a sync, or the loop's tick.
type configStep struct {
	name string
	do   func(t *testing.T, s *configScenario)
}

// configScenario is a mesh as steps leave it, and what the property needs to know.
type configScenario struct {
	b      [2]*configBridge
	values int // the last value written to max_per_room
}

// configSteps are every step, for each bridge where it is one bridge's.
func configSteps() []configStep {
	var steps []configStep
	for i, name := range []string{"a", "b"} {
		steps = append(steps,
			configStep{"set(" + name + ")", func(t *testing.T, s *configScenario) {
				s.values++
				v := s.values + 10
				if err := s.b[i].set(func(c *config.Config) { c.Notifications.MaxPerRoom = v }); err != nil {
					t.Fatalf("set on %s: %v", name, err)
				}
			}},
			configStep{"same(" + name + ")", func(t *testing.T, s *configScenario) {
				if err := s.b[i].set(func(c *config.Config) { c.Display.TimeFormat = "15:04" }); err != nil {
					t.Fatalf("same on %s: %v", name, err)
				}
			}},
			configStep{"unset(" + name + ")", func(t *testing.T, s *configScenario) {
				if err := s.b[i].set(func(c *config.Config) { c.Notifications.MaxPerRoom = 0 }); err != nil {
					t.Fatalf("unset on %s: %v", name, err)
				}
			}},
			configStep{"open(" + name + ")", func(t *testing.T, s *configScenario) {
				s.mend(t, i)
				s.b[i].buffer = s.b[i].text()
			}},
			configStep{"save(" + name + ")", func(t *testing.T, s *configScenario) {
				s.write(t, i)
				s.settle(t, i)
			}},
			configStep{"write(" + name + ")", func(t *testing.T, s *configScenario) { s.write(t, i) }},
			configStep{"invalid(" + name + ")", func(t *testing.T, s *configScenario) { s.invalid(t, i) }},
			configStep{"settle(" + name + ")", func(t *testing.T, s *configScenario) { s.settle(t, i) }},
		)
	}
	return append(steps, configStep{"sync", func(t *testing.T, s *configScenario) { exchange(t, s.b[0], s.b[1]) }})
}

// write is an editor saving its buffer (the file as it was when opened, or now) with
// first_name_only turned over and the local spell command changed; the next settle
// reads it.
func (s *configScenario) write(t *testing.T, i int) {
	t.Helper()
	s.mend(t, i) // the hand that broke the file mends it in the same editor
	b := s.b[i]
	if b.buffer == "" {
		b.buffer = b.text()
	}
	edited, err := config.Decode(b.buffer)
	if err != nil {
		t.Fatalf("%s's buffer does not read: %v", b.name, err)
	}
	edited.Display.FirstNameOnly = !edited.Display.FirstNameOnly
	edited.Spell.Command = "/opt/" + b.name + "/hunspell"
	b.edited = fmt.Sprint(edited.Display.FirstNameOnly)
	if !edited.Display.FirstNameOnly {
		b.edited = "unset"
	}
	if err := os.WriteFile(b.path, []byte(config.Encode(edited)+"# saved by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.buffer, b.saved = "", true
}

// settle is the loop's tick on bridge i. One that reads a save checks it: what the
// editor never touched keeps its value in the mesh, whatever reached the mesh since it
// opened the file, and what it touched is the mesh's (a shared key) or stays in the
// file (a local one).
func (s *configScenario) settle(t *testing.T, i int) {
	t.Helper()
	b := s.b[i]
	untouched := []string{"notifications.max_per_room", "display.time_format"}
	before := map[string]string{}
	for _, path := range untouched {
		before[path] = b.meshValue(path)
	}
	if b.broken != "" {
		bad, values := b.text(), fmt.Sprint(b.store.Config())
		if err := b.settle(); err == nil {
			t.Errorf("an edit that does not read settled on %s", b.name)
		}
		if got := b.text(); got != bad {
			t.Errorf("an edit that does not read was written over on %s:\n%s", b.name, got)
		}
		if now := fmt.Sprint(b.store.Config()); now != values {
			t.Errorf("an edit that does not read reached the mesh on %s: %s, then %s", b.name, values, now)
		}
		return
	}
	saved, was := b.saved, b.settled
	b.saved = false
	err := b.settle()
	if err == nil {
		b.settled = b.fileValue("display.first_name_only")
	}
	if err != nil || !saved {
		return // a save that does not check with what else the file holds: nothing crosses
	}
	for _, path := range untouched {
		if now := b.meshValue(path); now != before[path] {
			t.Errorf("a save that did not touch %s wrote it on %s: %s, then %s", path, b.name, before[path], now)
		}
	}
	// Saves that left the key as it was when last read, however many, edited nothing.
	if got := b.meshValue("display.first_name_only"); b.edited != was && got != b.edited {
		t.Errorf("the key a save on %s touched is %s in the mesh, want its edit, %s", b.name, got, b.edited)
	}
	if got := b.cfg().Spell.Command; got != "/opt/"+b.name+"/hunspell" {
		t.Errorf("the local key a save on %s touched is %q in the file, want its edit", b.name, got)
	}
}

// invalid is a hand edit that does not read, left standing until the hand puts the
// file back (quiesce): while it stands no settle shares it or writes over it.
func (s *configScenario) invalid(t *testing.T, i int) {
	t.Helper()
	b := s.b[i]
	if b.broken != "" {
		return
	}
	b.broken = b.text()
	if err := os.WriteFile(b.path, []byte(b.broken+"\n[display]\nno_such_key = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.settle(t, i)
}

// mend puts back a file an edit left unreadable.
func (s *configScenario) mend(t *testing.T, i int) {
	t.Helper()
	b := s.b[i]
	if b.broken == "" {
		return
	}
	if err := os.WriteFile(b.path, []byte(b.broken), 0o600); err != nil {
		t.Fatal(err)
	}
	b.broken = ""
}

// checkQuiet is what holds once the mesh is quiet: every bridge renders the same shared
// keys, which are the mesh's, and no local key crossed.
func (s *configScenario) checkQuiet(t *testing.T, trail string) {
	t.Helper()
	a, b := s.b[0], s.b[1]
	if as, bs := a.shared(), b.shared(); fmt.Sprint(as) != fmt.Sprint(bs) {
		t.Errorf("%s: the bridges render different shared keys:\na %v\nb %v", trail, as, bs)
	}
	for _, br := range s.b {
		values, _ := br.store.Config()
		for path := range values {
			if config.IsLocal(path) {
				t.Errorf("%s: local key %s reached the mesh", trail, path)
			}
		}
		if cmd := br.cfg().Spell.Command; cmd != "/usr/bin/hunspell" && cmd != "/opt/"+br.name+"/hunspell" {
			t.Errorf("%s: %s's spell command is %q: another bridge's local key", trail, br.name, cmd)
		}
	}
}

// configStarts are named states the generator extends by every step: each a way a
// merge goes wrong.
var configStarts = []struct {
	name  string
	steps []string
}{
	{"none", nil},
	// An editor opened the file before a remote change arrived and was rendered, then
	// saves.
	{"stale buffer", []string{"open(a)", "set(b)", "sync", "settle(a)", "save(a)"}},
	// The same with the remote change not rendered yet when the editor saves.
	{"stale buffer, not rendered", []string{"open(a)", "set(b)", "sync", "save(a)"}},
	// A hand edit saved and not read yet when a remote change arrives.
	{"pending hand edit", []string{"write(a)", "set(b)", "sync"}},
	// A hand edit read: the next turns its key back to a value an older rendering held.
	{"a hand edit read", []string{"save(a)"}},
	{"concurrent writes", []string{"set(a)", "set(b)"}},
	{"concurrent equal writes", []string{"same(a)", "same(b)"}},
	{"unset against an edit", []string{"unset(a)", "set(b)"}},
	{"hand edits on both", []string{"open(a)", "open(b)", "save(b)", "settle(b)", "sync"}},
	// Each bridge's pending hand edit meets the other's in the mesh.
	{"pending hand edits on both", []string{"write(a)", "write(b)", "set(a)", "sync"}},
	{"an edit that does not read, then a remote change", []string{"invalid(a)", "set(b)", "sync"}},
}

// TestTheSharedConfigSurvivesEveryInterleaving is the shared config's class test (merges
// writers): from each named start, every further step, then quiet. A save that did not touch a key never writes it, an edit that does not read
// never crosses or is written over, every bridge renders the same shared keys, and no
// local key crosses.
func TestTheSharedConfigSurvivesEveryInterleaving(t *testing.T) {
	t.Parallel()
	steps := configSteps()
	byName := map[string]configStep{}
	for _, st := range steps {
		byName[st.name] = st
	}
	for _, start := range configStarts {
		var runs [][]string
		for _, x := range steps {
			runs = append(runs, append(append([]string(nil), start.steps...), x.name))
		}
		t.Run(start.name, func(t *testing.T) {
			t.Parallel()
			for _, run := range runs {
				a, b := configMesh(t)
				s := &configScenario{b: [2]*configBridge{a, b}}
				for _, name := range run {
					byName[name].do(t, s)
				}
				s.quiesce(t)
				s.checkQuiet(t, strings.Join(run, " "))
				if t.Failed() {
					return
				}
			}
		})
	}
}

// TestAMergedConfigThatDoesNotCheckIsLeftUnapplied: two writes each valid, together
// not. Every bridge keeps its last valid file and says so; a later write fixes it.
func TestAMergedConfigThatDoesNotCheckIsLeftUnapplied(t *testing.T) {
	t.Parallel()
	a, b := configMesh(t)
	if err := a.set(func(c *config.Config) { c.Display.FirstNameOnly = true }); err != nil {
		t.Fatal(err)
	}
	if err := b.set(func(c *config.Config) { c.Notifications.MaxPerRoom = 7 }); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	for _, br := range []*configBridge{a, b} {
		before := br.text()
		if err := br.settle(); err != nil {
			t.Fatalf("settle on %s: %v", br.name, err)
		}
		if br.text() != before {
			t.Errorf("%s rendered a config that does not check:\n%s", br.name, br.text())
		}
		if len(br.popups) != 1 || !strings.Contains(br.popups[0], "does not check") {
			t.Errorf("%s said %q, want that the mesh's config does not check", br.name, br.popups)
		}
	}
	if err := b.set(func(c *config.Config) { c.Notifications.MaxPerRoom = 8 }); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	for _, br := range []*configBridge{a, b} {
		if err := br.settle(); err != nil {
			t.Fatalf("settle on %s: %v", br.name, err)
		}
		if c := br.cfg(); !c.Display.FirstNameOnly || c.Notifications.MaxPerRoom != 8 {
			t.Errorf("%s runs first_name_only %v, max_per_room %d after the fix, want true, 8", br.name, c.Display.FirstNameOnly, c.Notifications.MaxPerRoom)
		}
	}
}

// TestAHandEditWhileKithdRendersIsReadNotWrittenOver: the file saved between kithd
// reading it and writing the rendering is left alone, and read as a hand edit next.
func TestAHandEditWhileKithdRendersIsReadNotWrittenOver(t *testing.T) {
	t.Parallel()
	a, b := configMesh(t)
	if err := b.set(func(c *config.Config) { c.Notifications.MaxPerRoom = 20 }); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	var edit string
	// The check runs on the rendering just before it is written: the hand saves then.
	editing := func(ctx context.Context, cfg config.Config) error {
		if edit == "" && cfg.Notifications.MaxPerRoom == 20 {
			edited := a.cfg()
			edited.Display.FirstNameOnly = true
			edit = config.Encode(edited) + "# saved by hand\n"
			if err := os.WriteFile(a.path, []byte(edit), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return sharedCheck(ctx, cfg)
	}
	if _, err := a.file.Settle(t.Context(), editing, nil, false); err != nil {
		t.Fatal(err)
	}
	if edit == "" {
		t.Fatal("no rendering was checked")
	}
	if a.text() != edit {
		t.Fatalf("the rendering was written over a hand edit saved meanwhile:\n%s", a.text())
	}
	if err := a.settle(); err != nil {
		t.Fatal(err)
	}
	if c := a.cfg(); !c.Display.FirstNameOnly || c.Notifications.MaxPerRoom != 20 {
		t.Errorf("a runs first_name_only %v, max_per_room %d, want the hand edit's true and the mesh's 20", c.Display.FirstNameOnly, c.Notifications.MaxPerRoom)
	}
	if got := a.meshValue("display.first_name_only"); got != "true" {
		t.Errorf("the hand edit is %s in the mesh, want true", got)
	}
}

// TestAJoiningBridgeSetsItsOwnConfigAside: a bridge that joins takes the mesh's shared
// keys, keeps its local ones, keeps its whole file in config.toml.pre-mesh, and names
// each shared key that differed: none, or every one it set.
func TestAJoiningBridgeSetsItsOwnConfigAside(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, own string
		keys      []domain.ConfigChange
	}{
		{"no key differs", startConfig, nil},
		{"every key differs", `
[display]
first_name_only = true
time_format = "15:04"

[notifications]
max_per_room = 3

[spell]
command = "/opt/b/hunspell"
`, []domain.ConfigChange{
			{Path: "display.first_name_only", Was: "true", Now: "unset"},
			{Path: "display.time_format", Was: `"15:04"`, Now: "unset"},
			{Path: "notifications.max_per_room", Was: "3", Now: "1"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, b := newConfigBridge(t, "a", startConfig), newConfigBridge(t, "b", tc.own)
			own := b.text()
			joinConfig(t, a, b)
			aside, ok := b.file.SetAside()
			if !ok {
				t.Fatal("nothing set aside")
			}
			if fmt.Sprint(aside.Keys) != fmt.Sprint(tc.keys) {
				t.Errorf("set aside %v, want %v", aside.Keys, tc.keys)
			}
			if kept, err := os.ReadFile(aside.File); err != nil || string(kept) != own {
				t.Errorf("%s holds %q (%v), want the file as it was", aside.File, kept, err)
			}
			if fmt.Sprint(b.shared()) != fmt.Sprint(a.shared()) {
				t.Errorf("b renders %v, want the mesh's %v", b.shared(), a.shared())
			}
			if got, want := b.cfg().Spell.Command, (func() string { c, _ := config.Decode(own); return c.Spell.Command })(); got != want {
				t.Errorf("b's local spell command is %q, want its own %q", got, want)
			}
			if said := len(b.popups) > 0; said != (len(tc.keys) > 0) {
				t.Errorf("b said %q with %d keys set aside", b.popups, len(tc.keys))
			}
		})
	}
}

// TestKeepingAValueEndsAConflictOnEveryBridge: two writes of one key at once are a
// conflict on both bridges until one value is kept, which both then run.
func TestKeepingAValueEndsAConflictOnEveryBridge(t *testing.T) {
	t.Parallel()
	a, b := configMesh(t)
	for _, w := range []struct {
		br *configBridge
		v  int
	}{{a, 30}, {b, 40}} {
		if err := w.br.set(func(c *config.Config) { c.Notifications.MaxPerRoom = w.v }); err != nil {
			t.Fatal(err)
		}
	}
	exchange(t, a, b)
	conflicts := a.file.ConfigConflicts()
	if len(conflicts) != 1 || conflicts[0].Path != "notifications.max_per_room" || len(conflicts[0].Values) != 2 {
		t.Fatalf("conflicts on a: %+v, want max_per_room's two values", conflicts)
	}
	kept := conflicts[0].Values[1].Value // the one not in force
	if err := a.file.KeepConfigValue(t.Context(), "notifications.max_per_room", 1, sharedCheck, nil); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	for _, br := range []*configBridge{a, b} {
		if err := br.settle(); err != nil {
			t.Fatal(err)
		}
		if c := br.file.ConfigConflicts(); len(c) != 0 {
			t.Errorf("%s still has conflicts: %+v", br.name, c)
		}
		if got := fmt.Sprint(br.cfg().Notifications.MaxPerRoom); got != kept {
			t.Errorf("%s runs max_per_room %s, want the kept %s", br.name, got, kept)
		}
	}
	if err := a.file.KeepConfigValue(t.Context(), "notifications.max_per_room", 0, sharedCheck, nil); err == nil {
		t.Error("kept a value of a key in no conflict")
	}
}

// A conflict is ended on the bridge's machine: a paired device is refused, as any
// shared key, [agent]'s scope among them, may be in conflict.
func TestAPairedDeviceKeepsNoConflictsValue(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	key := newDeviceKey(t)
	b.pair(key, "phone")
	_, err := b.client(device{key: key, from: loopback(t)}).KeepConfigValue(t.Context(),
		connect.NewRequest(&v1.KeepConfigValueRequest{Path: "agent.read.rooms"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("KeepConfigValue from a paired device: %v, want permission_denied", err)
	}
}
