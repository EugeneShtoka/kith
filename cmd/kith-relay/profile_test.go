package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// withProfiles rewrites the config at path to define the profiles named, in order.
func withProfiles(t *testing.T, path string, names ...string) {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Profiles = nil
	for _, name := range names {
		cfg.Profiles = append(cfg.Profiles, config.Profile{Name: name, Include: []string{"*"}})
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

// handWrite writes the device file beside the config at path, as an editor would.
func handWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(config.DevicePath(path), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func deviceClient(socket string) devicev1connect.DeviceServiceClient {
	return devicev1connect.NewDeviceServiceClient(daemon.Dial(socket), relayBase)
}

// Device reports the profiles as the config defines them now, in order, and the one
// remembered as the config spells it, or "" when none was chosen or the config no
// longer has it: each call reads both files again, so a reload reaches the window.
func TestDeviceReportsTheProfilesAndTheOneRemembered(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	for _, step := range []struct {
		what     string
		change   func()
		profile  string
		profiles string
	}{
		{"first connect, several profiles", func() {}, "", "Home,Work"},
		{"remembered, written in another case", func() { handWrite(t, path, "profile_in_use = \" work \"\n") }, "Work", "Home,Work"},
		{"the config drops the one remembered", func() { withProfiles(t, path, "Home", "Play") }, "", "Home,Play"},
		{"one profile left, none remembered", func() { withProfiles(t, path, "Solo") }, "", "Solo"},
		{"a profile of that name comes back", func() { withProfiles(t, path, "Work", "Solo") }, "Work", "Work,Solo"},
		{"nothing remembered", func() { handWrite(t, path, "") }, "", "Work,Solo"},
	} {
		step.change()
		resp, err := device.Device(t.Context(), connect.NewRequest(&devicev1.DeviceRequest{}))
		if err != nil {
			t.Fatalf("%s: %v", step.what, err)
		}
		if got, profiles := resp.Msg.GetProfile(), strings.Join(resp.Msg.GetProfiles(), ","); got != step.profile || profiles != step.profiles {
			t.Errorf("%s: Device = %q of %s, want %q of %s", step.what, got, profiles, step.profile, step.profiles)
		}
	}

	// A device file that does not load is said, not read as none chosen.
	handWrite(t, path, "profile_in_use = \" \"\n")
	if _, err := device.Device(t.Context(), connect.NewRequest(&devicev1.DeviceRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Device with a blank profile_in_use = %v, want it refused", err)
	}
}

// ChooseProfile remembers a profile the config has, as the config spells it, and refuses
// any other name without touching the device file.
func TestChooseProfileRemembersOnlyAProfileTheConfigHas(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	choose := func(name string) (string, error) {
		resp, err := device.ChooseProfile(t.Context(), connect.NewRequest(&devicev1.ChooseProfileRequest{Name: name}))
		if err != nil {
			return "", err
		}
		return resp.Msg.GetProfile(), nil
	}
	remembered := func() string {
		dev, err := config.LoadDevice(config.DevicePath(path))
		if err != nil {
			t.Fatal(err)
		}
		return dev.ProfileInUse
	}

	if got, err := choose(" WORK"); err != nil || got != "Work" {
		t.Fatalf("ChooseProfile( WORK) = %q, %v; want Work", got, err)
	}
	if got := remembered(); got != "Work" {
		t.Errorf("device.toml remembers %q, want Work", got)
	}
	for _, name := range []string{"Nope", "", " ", "Work,Home"} {
		if _, err := choose(name); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("ChooseProfile(%q) = %v, want it refused", name, err)
		}
		if got := remembered(); got != "Work" {
			t.Errorf("after ChooseProfile(%q) device.toml remembers %q, want Work still", name, got)
		}
	}

	// A device file that does not load is not written over.
	handWrite(t, path, "[display]\nfsp = 3\n")
	if _, err := choose("Home"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("ChooseProfile over a broken device file = %v, want it refused", err)
	}
	if got, _ := os.ReadFile(config.DevicePath(path)); string(got) != "[display]\nfsp = 3\n" {
		t.Errorf("the broken device file was rewritten:\n%s", got)
	}
}

// ChooseProfile and another window's settings editor (config.UpdateDevice, the same
// locked writer) write the device file at once: neither undoes the other's change.
// The editor reads the file before each of its writes, and finds its own last value
// there; ChooseProfile reads its own choice back.
func TestChoosingAProfileKeepsAWriteMadeMeanwhile(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	devicePath := config.DevicePath(path)
	handWrite(t, path, "terminal = \"t-1\"\n")
	const rounds = 60

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range rounds {
			name := []string{"Home", "Work"}[i%2]
			if _, err := device.ChooseProfile(t.Context(), connect.NewRequest(&devicev1.ChooseProfileRequest{Name: name})); err != nil {
				t.Errorf("ChooseProfile(%s): %v", name, err)
				return
			}
		}
	})
	wg.Go(func() {
		for i := range rounds {
			was, err := config.LoadDevice(devicePath)
			if err != nil {
				t.Error(err)
				return
			}
			if want := fmt.Sprintf("t%d", i-1); was.Terminal != want {
				t.Errorf("round %d: terminal = %q, want the editor's last %q: a profile's write undid it", i, was.Terminal, want)
				return
			}
			now := was.Clone()
			now.Terminal = fmt.Sprintf("t%d", i)
			if _, err := config.UpdateDevice(devicePath, was, now); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()

	final, err := config.LoadDevice(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("t%d", rounds-1); final.Terminal != want || final.ProfileInUse != "Work" {
		t.Errorf("finally terminal %q, profile %q; want %s and the last chosen, Work", final.Terminal, final.ProfileInUse, want)
	}
}
