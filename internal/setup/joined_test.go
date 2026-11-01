package setup_test

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/pairing"
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

func bridgeID(b byte) devicekey.ID { return devicekey.ID(sha256.Sum256([]byte{b})) }

// Joining writes the bridge into device.toml beside the other settings, and joining
// the same bridge again replaces its entry (keeping the name given it) rather than
// adding a second.
func TestJoiningWritesTheBridgeIntoTheDeviceFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "device.toml")
	if err := os.WriteFile(path, []byte("[display]\nfps = 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := pairing.Invite{Addresses: []string{"192.168.1.5:7443", "100.64.0.3:7443"}, ID: bridgeID(1), Token: pairing.NewToken()}
	if _, err := setup.RememberBridge(path, home); err != nil {
		t.Fatal(err)
	}
	dev := deviceFile(t, path)
	want := []config.Bridge{{Name: "192.168.1.5", Addresses: home.Addresses, ID: bridgeID(1).String()}}
	if !reflect.DeepEqual(dev.Bridges, want) || dev.Display.FPS != 30 {
		t.Fatalf("after joining: bridges %+v, fps %d; want %+v and fps kept", dev.Bridges, dev.Display.FPS, want)
	}

	// Renamed by hand, then joined again at a new address, and another bridge joined.
	renamed := strings.Replace(readFile(t, path), `name = "192.168.1.5"`, `name = "home"`, 1)
	if err := os.WriteFile(path, []byte(renamed), 0o600); err != nil {
		t.Fatal(err)
	}
	home.Addresses = []string{"[fd00::5]:7443"}
	if _, err := setup.RememberBridge(path, home); err != nil {
		t.Fatal(err)
	}
	vps := pairing.Invite{Addresses: []string{"vps.example.org:7443"}, ID: bridgeID(2), Token: pairing.NewToken()}
	if _, err := setup.RememberBridge(path, vps); err != nil {
		t.Fatal(err)
	}
	want = []config.Bridge{
		{Name: "home", Addresses: []string{"[fd00::5]:7443"}, ID: bridgeID(1).String()},
		{Name: "vps.example.org", Addresses: vps.Addresses, ID: bridgeID(2).String()},
	}
	if got := deviceFile(t, path).Bridges; !reflect.DeepEqual(got, want) {
		t.Fatalf("bridges %+v, want %+v", got, want)
	}

	// A device file that does not read is left alone.
	if err := os.WriteFile(path, []byte("[display]\nfps = \"fast\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.RememberBridge(path, vps); err == nil {
		t.Fatal("a device file that does not read was written over")
	}
}

// On a new machine, which has no device file yet, joining writes one with the bridge in it.
func TestJoiningANewMachineWritesItsDeviceFile(t *testing.T) {
	t.Parallel()
	path := config.DevicePath(filepath.Join(t.TempDir(), "config.toml"))
	home := pairing.Invite{Addresses: []string{"192.168.1.5:7443"}, ID: bridgeID(1), Token: pairing.NewToken()}
	if _, err := setup.RememberBridge(path, home); err != nil {
		t.Fatal(err)
	}
	if got := deviceFile(t, path).Bridges; len(got) != 1 || got[0].ID != bridgeID(1).String() {
		t.Fatalf("bridges %+v, want the one joined", got)
	}
}

func deviceFile(t *testing.T, path string) config.Device {
	t.Helper()
	dev, err := config.LoadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	return dev
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
