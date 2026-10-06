package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
)

// configAt is a config file holding text, served as profile.
func configAt(t *testing.T, text, profile string) *ConfigFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewConfigFile(path, profile)
}

// A change made on the file as it is is written and put in force; one made on a file
// that changed since — another writer's change, a hand edit — is refused, the file as
// it was; one the daemon would not run with never reaches the file.
func TestTheConfigIsWrittenOnlyOnTheRevisionItWasMadeOn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := configAt(t, "homeserver = \"https://x\"\nuser = \"@me:x\"\n", "")
	read, err := f.Read()
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	apply := func(context.Context) error { applied++; return nil }

	quiet := read.Config.Clone()
	quiet.Notifications.Enabled = false
	wrote, err := f.Write(ctx, read.Revision, quiet, nil, apply)
	if err != nil || wrote.Config.Notifications.Enabled || wrote.Revision == read.Revision || applied != 1 {
		t.Fatalf("first write = (%+v, %v), applied %d", wrote.Revision, err, applied)
	}
	if _, err := f.Write(ctx, read.Revision, read.Config, nil, apply); !errors.Is(err, api.ErrConfigMoved) {
		t.Errorf("a write on the older revision = %v, want ErrConfigMoved", err)
	}

	data, _ := os.ReadFile(f.path)
	if err := os.WriteFile(f.path, append(data, []byte("\n# by hand\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(ctx, wrote.Revision, wrote.Config, nil, apply); !errors.Is(err, api.ErrConfigMoved) {
		t.Errorf("a write made before a hand edit = %v, want ErrConfigMoved", err)
	}

	now, _ := f.Read()
	refused := errors.New("kithd would not run with it")
	if _, err := f.Write(ctx, now.Revision, now.Config, func(context.Context, config.Config) error { return refused }, apply); !errors.Is(err, refused) {
		t.Errorf("a refused config = %v", err)
	}
	if after, _ := f.Read(); after.Revision != now.Revision || applied != 1 {
		t.Errorf("a refused config changed the file (%s → %s) or was applied (%d)", now.Revision, after.Revision, applied)
	}
}

// A file that keeps the account in [[profile]] blocks keeps it there: a client has the
// config with its profile applied, and what is written does not move the account to
// the top level (which would not load).
func TestAWriteKeepsTheAccountInItsProfile(t *testing.T) {
	t.Parallel()
	f := configAt(t, "[[profile]]\nname = \"home\"\nhomeserver = \"https://x\"\nuser = \"@me:x\"\n", "home")
	read, err := f.Read()
	if err != nil || read.Config.User != "@me:x" {
		t.Fatalf("read = (%+v, %v)", read.Config.User, err)
	}
	changed := read.Config.Clone()
	changed.Notifications.Enabled = false
	if _, werr := f.Write(context.Background(), read.Revision, changed, nil, nil); werr != nil {
		t.Fatal(werr)
	}
	onDisk, err := config.Load(f.path)
	if err != nil || onDisk.User != "" || len(onDisk.Profiles) != 1 {
		t.Errorf("on disk: user %q, %d profiles, %v", onDisk.User, len(onDisk.Profiles), err)
	}
}

// Writers racing on one revision: whatever order they run in, exactly one is written,
// every other is refused, and the file is the one written.
func TestWritersOnOneRevisionAreWrittenOnce(t *testing.T) {
	t.Parallel()
	for round := range 20 {
		f := configAt(t, "homeserver = \"https://x\"\nuser = \"@me:x\"\n", "")
		read, _ := f.Read()
		var mu sync.Mutex
		var won []int
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				cfg := read.Config.Clone()
				cfg.Display.FPS = 10 + i
				if _, err := f.Write(context.Background(), read.Revision, cfg, nil, nil); err == nil {
					mu.Lock()
					won = append(won, i)
					mu.Unlock()
				} else if !errors.Is(err, api.ErrConfigMoved) {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		final, _ := f.Read()
		if len(won) != 1 || final.Config.Display.FPS != 10+won[0] {
			t.Fatalf("round %d: %v written, the file has fps %d", round, won, final.Config.Display.FPS)
		}
	}
}
