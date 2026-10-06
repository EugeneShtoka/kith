package tui

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
)

// fileStore keeps the config in a file as the daemon does — on the revision it was
// read at — for tests that read back what a change saved.
type fileStore struct{ path string }

func (f fileStore) GetConfig(context.Context) (config.Snapshot, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return config.Snapshot{}, err
	}
	cfg, err := config.Decode(string(data))
	return config.Snapshot{Config: cfg, Revision: config.Revision(data)}, err
}

func (f fileStore) UpdateConfig(_ context.Context, base string, cfg config.Config) (string, error) {
	data, err := os.ReadFile(f.path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if config.Revision(data) != base {
		return "", api.ErrConfigMoved
	}
	if serr := config.Save(f.path, cfg); serr != nil {
		return "", serr
	}
	data, err = os.ReadFile(f.path)
	return config.Revision(data), err
}

func (fileStore) ConfigChanges() <-chan config.Snapshot { return nil }

// keptIn is m with its changes saved to its config file, as the daemon would save them.
func (m Model) keptIn() Model {
	m.conf.writer.store = fileStore{path: m.conf.path}
	if data, err := os.ReadFile(m.conf.path); err == nil {
		m.conf.writer.rev = config.Revision(data)
	}
	return m
}

// madeOn is a fileStore that checks no write lands on a change made elsewhere unless
// it was made on that change: each change says (in Terminal) the revision the window
// had when it was made, and elsewhere holds the revisions written elsewhere.
type madeOn struct {
	fileStore
	t         *testing.T
	elsewhere map[string]bool
}

func (s madeOn) UpdateConfig(ctx context.Context, base string, cfg config.Config) (string, error) {
	rev, err := s.fileStore.UpdateConfig(ctx, base, cfg)
	if err == nil && s.elsewhere[base] && cfg.Terminal != base {
		s.t.Errorf("a change made on %s landed on %s, a change made elsewhere", cfg.Terminal, base)
	}
	return rev, err
}

// Over random interleavings of this window's changes (saved in the order made, each on
// its own goroutine), changes made elsewhere, and their news arriving: no change of
// this window lands over a change made elsewhere that it was not made on — it is
// refused, or dropped as overtaken when the news arrives first.
func TestNoChangeLandsOverOneItWasNotMadeOn(t *testing.T) {
	t.Parallel()
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 31)) // #nosec G404 -- reproducible
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := config.Save(path, config.Config{}); err != nil {
			t.Fatal(err)
		}
		store := madeOn{fileStore: fileStore{path: path}, t: t, elsewhere: map[string]bool{}}
		data, _ := os.ReadFile(path)
		w := &configWriter{store: store, rev: config.Revision(data)}
		type queued struct {
			gen uint64
			cfg config.Config
		}
		var saves []queued
		var news []string
		for step := range 40 {
			switch rng.IntN(4) {
			case 0: // a change here, made on the revision this window has
				w.mu.Lock()
				on := w.rev
				w.mu.Unlock()
				saves = append(saves, queued{w.take(), config.Config{Terminal: on, Display: config.Display{FPS: 10 + step}}})
			case 1: // a save runs: any queued one, as goroutines may
				if len(saves) == 0 {
					continue
				}
				i := rng.IntN(len(saves))
				s := saves[i]
				saves = append(saves[:i], saves[i+1:]...)
				_ = w.save(context.Background(), s.gen, s.cfg)
			case 2: // a change elsewhere: the file rewritten, its news on the way
				if err := config.Save(path, config.Config{Display: config.Display{FPS: 100 + step}}); err != nil {
					t.Fatal(err)
				}
				data, _ := os.ReadFile(path)
				store.elsewhere[config.Revision(data)] = true
				news = append(news, config.Revision(data))
			case 3: // news arrives, in order
				if len(news) == 0 {
					continue
				}
				w.adopt(news[0])
				news = news[1:]
			}
		}
		if t.Failed() {
			t.Fatalf("seed %d", seed)
		}
	}
}

// A change made elsewhere is run here, and said; the echo of this window's own save is
// not news; a save refused as made on an older file reads the file again, runs it, and
// says the change was not saved.
func TestAConfigChangedElsewhereIsRunHere(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	base := config.Config{Homeserver: "https://x", User: "@me:x"}
	if err := config.Save(path, base); err != nil {
		t.Fatal(err)
	}
	m := configured(base).WithConfigFile(path, base).keptIn()
	ctx := context.Background()
	store := fileStore{path: path}

	mine := base
	mine.Display.FPS = 30
	m, cmd := m.applyConfig(mine, "set")
	m = deliver(t, m, cmd).clearStatus()
	echo, _ := store.GetConfig(ctx)
	if echo.Config.Display.FPS != 30 {
		t.Fatalf("the window's save did not reach the file: fps %d", echo.Config.Display.FPS)
	}
	m = update(t, m, configNewsMsg{snap: echo})
	if strings.Contains(m.status(), "elsewhere") {
		t.Errorf("the echo of its own save was news: %q", m.status())
	}

	theirs := echo.Config.Clone()
	theirs.Display.FPS = 50
	if err := config.Save(path, theirs); err != nil {
		t.Fatal(err)
	}
	snap, _ := store.GetConfig(ctx)
	m = update(t, m, configNewsMsg{snap: snap})
	if m.conf.base.Display.FPS != 50 || !strings.Contains(m.status(), "changed elsewhere") {
		t.Errorf("after a change elsewhere: fps %d, status %q", m.conf.base.Display.FPS, m.status())
	}

	behind := m
	behind.conf.writer.rev = echo.Revision // as if the news had not arrived
	next, cmd := behind.handleConfigSaved(configSavedMsg{err: api.ErrConfigMoved})
	next = deliver(t, next, cmd)
	if !strings.Contains(next.status(), "not saved") {
		t.Errorf("a refused save: status %q", next.status())
	}
}
