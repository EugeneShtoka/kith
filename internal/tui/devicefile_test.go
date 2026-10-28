package tui

import (
	"math/rand/v2"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
)

// devicing is a window whose device settings are saved to path.
func devicing(path string) Model {
	return starterNew(apitest.Nop{}, config.Display{}).WithDevice(path, config.Device{Keys: config.DefaultKeys()})
}

// Over generated runs of device changes in one window whose saves run in any order,
// some never (the last always does), and whose answers arrive in that order: the
// window never goes back to an older change, and the device file ends as the window
// shows it, a setting flipped twice included.
func TestDeviceSavesLandAsTheWindowShowsThem(t *testing.T) {
	t.Parallel()
	for seed := range uint64(150) {
		rng := rand.New(rand.NewPCG(seed, 0xf11e)) // #nosec G404 -- reproducible
		path := filepath.Join(t.TempDir(), config.DeviceFile)
		m := devicing(path)
		var saves []tea.Cmd
		for range 1 + rng.IntN(5) {
			dev := m.conf.device.Clone()
			switch rng.IntN(3) {
			case 0:
				dev.Display.ColorMessages = !dev.Display.ColorMessages
			case 1:
				dev.Display.FPS = 1 + rng.IntN(100)
			case 2:
				dev.Display.RowNumbers = !dev.Display.RowNumbers
			}
			var save tea.Cmd
			m, save = m.applyDevice(dev, "")
			saves = append(saves, save)
		}
		var answers []tea.Msg
		for _, i := range rng.Perm(len(saves)) {
			if i == len(saves)-1 || rng.IntN(2) == 0 {
				answers = append(answers, msgsOf(t, saves[i])...)
			}
		}
		last := m.conf.device.Display
		for _, msg := range answers {
			m = update(t, m, msg)
			if now := m.conf.device.Display; now.ColorMessages != last.ColorMessages || now.FPS != last.FPS || now.RowNumbers != last.RowNumbers {
				t.Fatalf("seed %d: an answer took the window back to color %v fps %d numbers %v", seed, now.ColorMessages, now.FPS, now.RowNumbers)
			}
		}
		saved, err := config.LoadDevice(path)
		if err != nil {
			t.Fatal(err)
		}
		shown := m.conf.device.Display
		if got := saved.Display; got.ColorMessages != shown.ColorMessages || got.FPS != shown.FPS || got.RowNumbers != shown.RowNumbers {
			t.Fatalf("seed %d: the file holds color %v fps %d numbers %v, the window shows %v %d %v",
				seed, got.ColorMessages, got.FPS, got.RowNumbers, shown.ColorMessages, shown.FPS, shown.RowNumbers)
		}
	}
}

// Two windows on one device file each change a setting: both changes are in the file,
// and the window saving second runs the other's change too, as the file now has it.
func TestTwoWindowsOnOneDeviceFileKeepBothChanges(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), config.DeviceFile)
	a, b := devicing(path), devicing(path)

	tinted := a.conf.device.Clone()
	tinted.Display.ColorMessages = true
	a, saveA := a.applyDevice(tinted, "")
	faster := b.conf.device.Clone()
	faster.Display.FPS = 42
	b, saveB := b.applyDevice(faster, "")

	b = deliver(t, b, saveB)
	a = deliver(t, a, saveA)

	saved, err := config.LoadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Display.ColorMessages || saved.Display.FPS != 42 {
		t.Errorf("the file holds color %v and fps %d, want both windows' changes", saved.Display.ColorMessages, saved.Display.FPS)
	}
	if a.conf.device.Display.FPS != 42 || a.prefs.device.FPS != 42 || !a.conf.device.Display.ColorMessages {
		t.Errorf("the window saving second runs fps %d (shown %d), color %v; want the file's",
			a.conf.device.Display.FPS, a.prefs.device.FPS, a.conf.device.Display.ColorMessages)
	}
	if b.conf.device.Display.ColorMessages {
		t.Error("the first window took a change it never heard of")
	}

	// The second window changes what the first had run; the first then changes
	// something else, which must not write its older value back.
	slower := b.conf.device.Clone()
	slower.Display.FPS = 7
	b, saveB = b.applyDevice(slower, "")
	b = deliver(t, b, saveB)
	numbered := a.conf.device.Clone()
	numbered.Display.RowNumbers = true
	a, saveA = a.applyDevice(numbered, "")
	a = deliver(t, a, saveA)
	if saved, err = config.LoadDevice(path); err != nil || saved.Display.FPS != 7 || !saved.Display.RowNumbers {
		t.Errorf("after both again: fps %d, numbers %v (%v); want the second window's 7 kept", saved.Display.FPS, saved.Display.RowNumbers, err)
	}
	if a.conf.device.Display.FPS != 7 {
		t.Errorf("the first window runs fps %d, want the file's 7", a.conf.device.Display.FPS)
	}
}

// A bridge setting goes to kithd and leaves the device file alone; a device setting
// goes to the device file and asks kithd nothing.
func TestEachSettingIsSavedToItsOwnFile(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{})
	device := config.DevicePath(path)
	m = toSetting(t, m, "width") // display.max_name_length, a device setting
	m = typeIn(t, m, "21")
	bridge, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := config.LoadDevice(device)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Display.MaxNameLength != 21 {
		t.Errorf("device max_name_length = %d, want 21", dev.Display.MaxNameLength)
	}
	m = m.closePicker()
	m, _ = press(t, m, keyText(","))
	m = toSetting(t, m, "codes.symbols")
	typeIn(t, m, "-_")
	if bridge, err = config.Load(path); err != nil || bridge.Codes.Symbols != "-_" {
		t.Errorf("config symbols = %q (%v), want -_", bridge.Codes.Symbols, err)
	}
	if again, _ := config.LoadDevice(device); again.Display.MaxNameLength != 21 {
		t.Errorf("a bridge setting changed the device file: max_name_length %d", again.Display.MaxNameLength)
	}
}
