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

// configAt is a config file holding text.
func configAt(t *testing.T, text string) *ConfigFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewConfigFile(path)
}

// A change on the current revision is written and applied; one on a changed file is
// refused, leaving it as it was; one the daemon would not run with is never written.
func TestTheConfigIsWrittenOnlyOnTheRevisionItWasMadeOn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := configAt(t, "allow_token_file = true\n")
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

// Of writers racing on one revision exactly one is written, the rest refused.
func TestWritersOnOneRevisionAreWrittenOnce(t *testing.T) {
	t.Parallel()
	for round := range 20 {
		f := configAt(t, "allow_token_file = true\n")
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

// An unwritable config is the daemon's own failure and changes nothing.
func TestAConfigThatCannotBeWrittenSaysSo(t *testing.T) {
	t.Parallel()
	f := configAt(t, "allow_token_file = true\n")
	read, _ := f.Read()
	dir := filepath.Dir(f.path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err := f.Write(context.Background(), read.Revision, read.Config, nil, nil)
	if _, ok := errors.AsType[unwritable](err); !ok {
		t.Errorf("writing into a read-only directory = %v, want it said as the daemon's failure", err)
	}
}
