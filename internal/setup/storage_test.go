package setup_test

import (
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

// With nothing configured, files go where they always have, and the instance chosen
// is kept in the config: the next run finds the same files.
func TestStorageDefaultsAndAKeptInstance(t *testing.T) {
	root, path := home(t, "homeserver = \"h\"\nuser = \"@ada:x\"\n")
	first, err := setup.StorageFor(load(t, path), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.DataDir != filepath.Join(root, "data_home", "kith") || first.RuntimeDir != filepath.Join(root, "runtime_dir", "kith") ||
		first.KeyringService != "kith" || first.Instance == "" {
		t.Errorf("defaults = %+v", first)
	}
	if first.Instance == domain.AccountKey("@ada:x") {
		t.Error("a new install took its name from the Matrix account")
	}
	second, err := setup.StorageFor(load(t, path), path, "")
	if err != nil || second.Instance != first.Instance {
		t.Errorf("the next run = (%+v, %v), want the instance kept (%s)", second, err, first.Instance)
	}
}

// An install from before instances keeps its files: its instance is the name they
// were kept under, written into the config.
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
	storage, err := setup.StorageFor(load(t, path), path, "")
	if err != nil || storage.Instance != legacy || storage.CachePath() != filepath.Join(dir, "cache-"+legacy+".db") {
		t.Fatalf("storage = (%+v, %v), want the existing files", storage, err)
	}
	if got := load(t, path).Storage.Instance; got != legacy {
		t.Errorf("config instance = %q, want %q recorded", got, legacy)
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
