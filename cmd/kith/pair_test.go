package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

func bridgeID(b byte) devicekey.ID { return devicekey.ID(sha256.Sum256([]byte{b})) }

// What `kith pair` prints is what `kith join` reads: the QR code and the line both
// carry the bridge's ticket whole.
func TestATicketShownIsTheInviteJoined(t *testing.T) {
	t.Parallel()
	token := pairing.NewToken()
	ticket := domain.PairingTicket{
		Token: token.String(), Addresses: []string{"192.168.1.5:7443", "[fd00::5]:7443"},
		ID: bridgeID(1).String(), Expires: time.Unix(1_800_000_000, 0),
	}
	var out bytes.Buffer
	showTicket(&out, ticket)
	_, line, ok := strings.Cut(out.String(), "kith join '")
	uri, _, closed := strings.Cut(line, "'")
	if !ok || !closed {
		t.Fatalf("no kith join line in:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "█") {
		t.Errorf("no QR code drawn:\n%s", out.String())
	}
	got, err := pairing.ParseURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	want := pairing.Invite{Addresses: ticket.Addresses, ID: bridgeID(1), Token: token, Expires: ticket.Expires}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("joined %+v, want %+v", got, want)
	}
	// A ticket that does not read is said, never drawn.
	out.Reset()
	showTicket(&out, domain.PairingTicket{Token: "x", ID: ticket.ID, Addresses: ticket.Addresses})
	if strings.Contains(out.String(), "kith join") {
		t.Errorf("a ticket with a bad token was shown: %s", out.String())
	}
}

func TestOnlyYesConfirms(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]bool{
		"y\n": true, "Y\n": true, " yes \n": true, "YES": true,
		"\n": false, "": false, "n\n": false, "no": false, "yy": false, "y es": false,
	} {
		if got := isYes(line); got != want {
			t.Errorf("isYes(%q) = %v, want %v", line, got, want)
		}
	}
}

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
	if err := rememberBridge(path, home); err != nil {
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
	if err := rememberBridge(path, home); err != nil {
		t.Fatal(err)
	}
	vps := pairing.Invite{Addresses: []string{"vps.example.org:7443"}, ID: bridgeID(2), Token: pairing.NewToken()}
	if err := rememberBridge(path, vps); err != nil {
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
	if err := rememberBridge(path, vps); err == nil {
		t.Fatal("a device file that does not read was written over")
	}
}

// On a new machine, which has no device file yet, joining writes one with the bridge in it.
func TestJoiningANewMachineWritesItsDeviceFile(t *testing.T) {
	t.Parallel()
	path := config.DevicePath(filepath.Join(t.TempDir(), "config.toml"))
	home := pairing.Invite{Addresses: []string{"192.168.1.5:7443"}, ID: bridgeID(1), Token: pairing.NewToken()}
	if err := rememberBridge(path, home); err != nil {
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

// Every unconfirmed end is an error saying why; only confirmed is none.
func TestEveryUnconfirmedEndIsAnError(t *testing.T) {
	t.Parallel()
	if err := outcomeErr(domain.PairingConfirmed); err != nil {
		t.Errorf("confirmed: %v", err)
	}
	for _, o := range []domain.PairingOutcome{domain.PairingRefused, domain.PairingExpired, domain.PairingAbandoned, 0, 99} {
		if err := outcomeErr(o); err == nil {
			t.Errorf("outcome %d is no error", o)
		}
	}
}
