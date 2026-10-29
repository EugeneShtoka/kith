package setup_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// A device has joined a bridge only once its device file names one: no device file, and
// one naming none, mean kithd is this machine's. The first named is the one dialed.
// Asking writes nothing.
func TestTheBridgeJoinedIsTheDeviceFilesFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, joined, err := setup.JoinedBridge(path); joined || err != nil {
		t.Fatalf("no device file: joined %v, %v; want none", joined, err)
	}
	if _, err := os.Stat(config.DevicePath(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("asking wrote a device file: %v", err)
	}
	device := config.DevicePath(path)
	if err := os.WriteFile(device, []byte("[display]\nfps = 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, joined, err := setup.JoinedBridge(path); joined || err != nil {
		t.Fatalf("a device file naming no bridge: joined %v, %v; want none", joined, err)
	}
	body := "[[bridge]]\nname = \"home\"\naddresses = [\"192.168.1.5:7443\"]\nid = \"A\"\n" +
		"[[bridge]]\nname = \"vps\"\naddresses = [\"vps.example.org:7443\"]\nid = \"B\"\n"
	if err := os.WriteFile(device, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	b, joined, err := setup.JoinedBridge(path)
	if !joined || err != nil || b.Name != "home" {
		t.Fatalf("JoinedBridge() = %+v, %v, %v; want home", b, joined, err)
	}
	if err := os.WriteFile(device, []byte("[display]\nfps = \"fast\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := setup.JoinedBridge(path); err == nil {
		t.Fatal("a device file that does not read was taken for one naming no bridge")
	}
}

// A machine without a config of its own (a new laptop joining a bridge) keeps its files
// in the default directories, under the instance its config's path derives, and gets no
// config written; one with a config keeps them where it says, as kith always has. Either
// way every client program finds the same device key.
func TestAClientKeepsItsFilesWhereItsConfigSaysOrTheDefaults(t *testing.T) {
	root, path := home(t, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fresh, allowFile, err := setup.ClientStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(path); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("a config was written on a machine without one: %v", serr)
	}
	want, err := setup.StorageFor(config.Config{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if fresh != want || allowFile {
		t.Fatalf("ClientStorage() = %+v, %v; want the defaults %+v and the keyring", fresh, allowFile, want)
	}

	own := filepath.Join(root, "own")
	body := "allow_token_file = true\n[storage]\ninstance = \"laptop\"\ndata_dir = \"" + own + "\"\n"
	if werr := os.WriteFile(path, []byte(body), 0o600); werr != nil {
		t.Fatal(werr)
	}
	configured, allowFile, err := setup.ClientStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Instance != "laptop" || configured.DataDir != own || !allowFile {
		t.Fatalf("ClientStorage() = %+v, %v; want the config's instance, data_dir and allow_token_file", configured, allowFile)
	}
}
