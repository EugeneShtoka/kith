package setup_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// home points the XDG directories at a fresh home, and returns a config file there.
func home(t *testing.T, body string) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(xdg.Reload)
	for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME"} {
		t.Setenv(v, filepath.Join(root, strings.ToLower(strings.TrimPrefix(v, "XDG_"))))
	}
	xdg.Reload()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, path
}

func load(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// unchanged fails when reading storage wrote the config: the daemon runs with it
// read-only, so working out where the files are must never need to write it.
func unchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, before) {
		t.Errorf("StorageFor changed the config:\n%s", after)
	}
}

// With nothing configured, files go where they always have. The instance is derived
// from the config file without writing it, the same on every read (by the daemon or
// a client), and a client may record it, which keeps it when the file moves.
func TestStorageDefaultsAndADerivedInstance(t *testing.T) {
	root, path := home(t, "homeserver = \"h\"\nuser = \"@ada:x\"\n")
	before, _ := os.ReadFile(path)
	first, err := setup.StorageFor(load(t, path), path, "")
	if err != nil {
		t.Fatal(err)
	}
	unchanged(t, path, before)
	if first.DataDir != filepath.Join(root, "data_home", "kith") || first.RuntimeDir != filepath.Join(root, "runtime_dir", "kith") ||
		first.KeyringService != "kith" || first.Instance == "" {
		t.Errorf("defaults = %+v", first)
	}
	if first.Instance == domain.AccountKey("@ada:x") {
		t.Error("a new install took its name from the Matrix account")
	}
	second, err := setup.StorageFor(load(t, path), path, "")
	if err != nil || second.Instance != first.Instance {
		t.Errorf("the next read = (%+v, %v), want the same instance (%s)", second, err, first.Instance)
	}

	// Through a symlink, as the daemon may be given it, the same file is the same instance.
	link := filepath.Join(root, "link.toml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if via, err := setup.StorageFor(load(t, link), link, ""); err != nil || via.Instance != first.Instance {
		t.Errorf("through a symlink = (%q, %v), want %q", via.Instance, err, first.Instance)
	}

	// Another file, same directories: its own instance.
	other := filepath.Join(root, "other.toml")
	if err := os.WriteFile(other, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if theirs, err := setup.StorageFor(load(t, other), other, ""); err != nil || theirs.Instance == first.Instance {
		t.Errorf("another config file = (%q, %v), want an instance of its own", theirs.Instance, err)
	}

	// Recorded, the instance moves with the file.
	if err := setup.RememberInstance(load(t, path), path); err != nil {
		t.Fatal(err)
	}
	if got := load(t, path).Storage.Instance; got != first.Instance {
		t.Fatalf("recorded instance = %q, want %q", got, first.Instance)
	}
	moved := filepath.Join(root, "moved.toml")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if after, err := setup.StorageFor(load(t, moved), moved, ""); err != nil || after.Instance != first.Instance {
		t.Errorf("after moving the recorded config = (%q, %v), want %q", after.Instance, err, first.Instance)
	}
}

// An install from before instances keeps its files: its instance is the name they
// were kept under, found without writing the config, and recorded by a client.
func TestAnInstallFromBeforeKeepsItsFiles(t *testing.T) {
	root, path := home(t, "homeserver = \"h\"\nuser = \"@ada:x\"\n")
	legacy := domain.AccountKey("@ada:x")
	dir := filepath.Join(root, "data_home", "kith")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache-"+legacy+".db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	storage, err := setup.StorageFor(load(t, path), path, "")
	if err != nil || storage.Instance != legacy || storage.CachePath() != filepath.Join(dir, "cache-"+legacy+".db") {
		t.Fatalf("storage = (%+v, %v), want the existing files", storage, err)
	}
	unchanged(t, path, before)
	if err := setup.RememberInstance(load(t, path), path); err != nil {
		t.Fatal(err)
	}
	if got := load(t, path).Storage.Instance; got != legacy {
		t.Errorf("config instance = %q, want %q recorded", got, legacy)
	}
}

// A config that cannot be written is still served: reading storage never needs to
// write it (the daemon under systemd sees the home directory read-only).
func TestAReadOnlyConfigIsServed(t *testing.T) {
	root, path := home(t, "homeserver = \"h\"\nuser = \"@ada:x\"\n")
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	storage, err := setup.StorageFor(load(t, path), path, "")
	if err != nil || storage.Instance == "" {
		t.Errorf("StorageFor on a read-only config = (%+v, %v), want an instance", storage, err)
	}
	if err := setup.RememberInstance(load(t, path), path); err == nil {
		t.Error("recording into a read-only directory reported success")
	}
}

// Configured directories are used as given ("~" is home); a relative one is refused;
// two configs sharing them have their own files.
func TestConfiguredDirectoriesAndSharing(t *testing.T) {
	root, path := home(t, "homeserver = \"h\"\nuser = \"@ada:x\"\n[storage]\ninstance = \"test\"\ndata_dir = \""+"/srv/kith"+"\"\nstate_dir = \"~/kith-state\"\nkeyring_service = \"kith-test\"\n")
	storage, err := setup.StorageFor(load(t, path), path, "")
	if err != nil {
		t.Fatal(err)
	}
	homeDir, _ := os.UserHomeDir()
	if storage.DataDir != "/srv/kith" || storage.StateDir != filepath.Join(homeDir, "kith-state") ||
		storage.CacheDir != filepath.Join(root, "cache_home", "kith") || storage.KeyringService != "kith-test" || storage.Instance != "test" {
		t.Errorf("storage = %+v", storage)
	}
	other := storage
	other.Instance = "mine"
	if storage.CachePath() == other.CachePath() || storage.SocketPath() == other.SocketPath() {
		t.Error("two instances in one directory share files")
	}
	if _, err := setup.StorageDirs(config.Config{Storage: config.Storage{DataDir: "relative/dir"}}); err == nil {
		t.Error("a relative data_dir was accepted")
	}
	if _, err := setup.StorageFor(config.Config{Storage: config.Storage{Instance: "../escape"}}, "", ""); err == nil {
		t.Error("an instance that walks out of its directory was accepted")
	}
}

// A profile has its own instance, the same whether or not it is named when it is
// the first; one with files from before keeps them.
func TestProfilesHaveTheirOwnInstance(t *testing.T) {
	root, path := home(t, "[storage]\ninstance = \"base\"\n[[profile]]\nname = \"work\"\nuser = \"@w:x\"\nhomeserver = \"h\"\n[[profile]]\nname = \"home\"\nuser = \"@h:x\"\nhomeserver = \"h\"\n")
	of := func(profile string) string {
		cfg, err := load(t, path).Profile(profile)
		if err != nil {
			t.Fatal(err)
		}
		s, err := setup.StorageFor(cfg, path, profile)
		if err != nil {
			t.Fatal(err)
		}
		return s.Instance
	}
	if unnamed, named := of(""), of("work"); unnamed != "base-work" || named != unnamed {
		t.Errorf("first profile = %q unnamed, %q named; want base-work both ways", unnamed, named)
	}
	legacy := domain.AccountKey("@h:x")
	dir := filepath.Join(root, "data_home", "kith")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "crypto-"+legacy+".db"), nil, 0o600)
	if got := of("home"); got != legacy {
		t.Errorf("a profile with files from before = %q, want %q", got, legacy)
	}
}

// /export writes into export_dir: by default the exports folder beside kith's data, a
// "~" path expanded, and a relative one refused, naming the key.
func TestTheExportFolder(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	cfg.Storage.DataDir = "/data/kith-x"
	dirs, err := setup.StorageDirs(cfg)
	if err != nil || dirs.ExportDir != "/data/kith-x/exports" {
		t.Errorf("default = %q, %v; want the exports folder beside the data", dirs.ExportDir, err)
	}
	cfg.Storage.ExportDir = "~/Documents/ChatHistory"
	home, _ := os.UserHomeDir()
	if dirs, err = setup.StorageDirs(cfg); err != nil || dirs.ExportDir != filepath.Join(home, "Documents", "ChatHistory") {
		t.Errorf("~ path = %q, %v", dirs.ExportDir, err)
	}
	cfg.Storage.ExportDir = "Documents/ChatHistory"
	if _, err = setup.StorageDirs(cfg); err == nil || !strings.Contains(err.Error(), "export_dir") {
		t.Errorf("a relative export_dir = %v, want it refused naming the key", err)
	}
}
