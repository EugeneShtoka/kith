package config

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A device file that does not exist is every default; one holding a key it does not
// read, a bridge key among them, is refused naming it, as Load refuses one.
func TestLoadDeviceIsStrictAndAMissingFileIsTheDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing, err := LoadDevice(filepath.Join(dir, DeviceFile))
	if err != nil || !reflect.DeepEqual(missing, deviceBaseline()) {
		t.Errorf("a missing device file = %+v, %v; want the defaults", missing.Display, err)
	}
	path := filepath.Join(dir, "other.toml")
	writeFile(t, path, "[display]\ntime_format = \"12h\"\nfps = 30\n")
	if _, err := LoadDevice(path); err == nil || err.Error() != "config: "+path+": unknown keys: display.time_format" {
		t.Errorf("a bridge key in the device file = %v, want it refused as unknown", err)
	}
}

// changeSome changes some of dev's settings to random values, returning the paths.
func changeSome(t *testing.T, rng *rand.Rand, dev *Device) []string {
	t.Helper()
	s := Settings{Device: *dev}
	var changed []string
	for _, w := range settingsIn(rng, DeviceFile, 5) {
		text, _ := strings.CutPrefix(strings.Trim(w.value, `"`), "[")
		text = strings.TrimSuffix(strings.ReplaceAll(text, `"`, ""), "]")
		if err := s.SetValue(w.p.Path, text); err != nil {
			t.Fatal(err)
		}
		changed = append(changed, w.p.Path)
	}
	if rng.IntN(2) == 0 { // a binding, entry by entry in its map
		binding := []string{"timeline.reply", "nav.up", "quit"}[rng.IntN(3)]
		s.Device.Keys.Bindings[binding] = string(rune('A' + rng.IntN(26)))
		changed = append(changed, "keys."+binding)
	}
	*dev = s.Device
	return changed
}

// valueOf is a changed path's value in dev: a property, or a binding.
func valueOf(dev Device, path string) string {
	if binding, ok := strings.CutPrefix(path, "keys."); ok {
		return dev.Keys.Bindings[binding]
	}
	return deviceValue(dev, path)
}

// Over generated windows each changing some settings of the device file it read, saved
// in any order or all at once: every setting one window changed is the file's after,
// one changed by several is one of theirs (in order, the last one's), and every other
// keeps the file's.
func TestWindowsEditingTheDeviceAtOnceKeepEachOthersChanges(t *testing.T) {
	t.Parallel()
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 0xde71)) // #nosec G404 -- reproducible
		path := filepath.Join(t.TempDir(), DeviceFile)
		writeFile(t, path, tomlText(settingsIn(rng, DeviceFile, 6)))
		base, err := LoadDevice(path)
		if err != nil {
			t.Fatal(err)
		}
		windows := 2 + rng.IntN(2)
		was, now := make([]Device, windows), make([]Device, windows)
		changed := make([][]string, windows)
		for w := range windows {
			was[w], now[w] = base.Clone(), base.Clone()
			changed[w] = changeSome(t, rng, &now[w])
		}
		order := rng.Perm(windows)
		atOnce := seed%2 == 1
		if atOnce {
			var wg sync.WaitGroup
			for _, w := range order {
				wg.Go(func() {
					if _, uerr := UpdateDevice(path, was[w], now[w]); uerr != nil {
						t.Errorf("seed %d: %v", seed, uerr)
					}
				})
			}
			wg.Wait()
		} else {
			for _, w := range order {
				written, uerr := UpdateDevice(path, was[w], now[w])
				if uerr != nil {
					t.Fatal(uerr)
				}
				if onDisk, _ := LoadDevice(path); !reflect.DeepEqual(onDisk, written) {
					t.Fatalf("seed %d: UpdateDevice returned what the file does not hold", seed)
				}
			}
		}
		final, err := LoadDevice(path)
		if err != nil {
			t.Fatal(err)
		}
		checkMerged(t, seed, atOnce, base, final, now, changed, order)
	}
}

// checkMerged holds TestWindowsEditingTheDeviceAtOnceKeepEachOthersChanges's property.
func checkMerged(t *testing.T, seed uint64, atOnce bool, base, final Device, now []Device, changed [][]string, order []int) {
	t.Helper()
	by := map[string][]int{} // path → the windows that changed it, in save order
	for _, w := range order {
		for _, p := range changed[w] {
			if valueOf(now[w], p) != valueOf(base, p) {
				by[p] = append(by[p], w)
			}
		}
	}
	for p, ws := range by {
		got := valueOf(final, p)
		switch {
		case !atOnce && got != valueOf(now[ws[len(ws)-1]], p):
			t.Fatalf("seed %d: %s = %q, want the last save's %q", seed, p, got, valueOf(now[ws[len(ws)-1]], p))
		case atOnce && !slicesHasValue(now, ws, p, got):
			t.Fatalf("seed %d: %s = %q, which no window wrote", seed, p, got)
		}
	}
	for _, p := range Properties() {
		if _, touched := by[p.Path]; p.File == DeviceFile && !touched && deviceValue(final, p.Path) != deviceValue(base, p.Path) {
			t.Fatalf("seed %d: %s = %q, which no window changed (was %q)", seed, p.Path, deviceValue(final, p.Path), deviceValue(base, p.Path))
		}
	}
}

func slicesHasValue(now []Device, ws []int, path, value string) bool {
	for _, w := range ws {
		if valueOf(now[w], path) == value {
			return true
		}
	}
	return false
}

// kithd's split and a client's run at once: the client splits, then saves a change to
// the device file it read, while kithd's split of the same old config is under way.
// The client's change is the file's after, whichever got there first.
func TestASplitAndAWindowsChangeAtOnceKeepTheChange(t *testing.T) {
	t.Parallel()
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 0x51d3)) // #nosec G404 -- reproducible
		c := genSplitCase(rng)
		c.reply = "R" // the config's, which the client's change replaces
		configPath, devicePath := c.files(t, t.TempDir())
		var wg sync.WaitGroup
		wg.Go(func() {
			if _, err := SplitDevice(configPath, devicePath); err != nil {
				t.Errorf("seed %d: kithd's split: %v", seed, err)
			}
		})
		var was, now Device
		wg.Go(func() {
			if _, err := SplitDevice(configPath, devicePath); err != nil {
				t.Errorf("seed %d: the client's split: %v", seed, err)
				return
			}
			loaded, err := LoadDevice(devicePath)
			if err != nil {
				t.Errorf("seed %d: %v", seed, err)
				return
			}
			was, now = loaded, loaded.Clone()
			now.Keys.Bindings["timeline.reply"] = "Z"
			if _, err := UpdateDevice(devicePath, was, now); err != nil {
				t.Errorf("seed %d: the client's save: %v", seed, err)
			}
		})
		wg.Wait()
		if dev, err := LoadDevice(devicePath); err != nil || dev.Keys.Bindings["timeline.reply"] != "Z" {
			t.Fatalf("seed %d: reply bound to %q (%v) after both, want the client's Z", seed, dev.Keys.Bindings["timeline.reply"], err)
		}
	}
}

// A save to a device file that will not load changes nothing, and says why.
func TestUpdateDeviceRefusesAFileThatWillNotLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), DeviceFile)
	writeFile(t, path, "[display]\nfsp = 3\n")
	now := deviceBaseline()
	now.Display.FPS = 10
	if _, err := UpdateDevice(path, deviceBaseline(), now); err == nil || !strings.Contains(err.Error(), "display.fsp") {
		t.Errorf("UpdateDevice = %v, want the unknown key named", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "[display]\nfsp = 3\n" {
		t.Errorf("the file was rewritten:\n%s", got)
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a backup was written: %v", err)
	}
}

// The device holds exactly the keys design-remote.md's table gives it, so a key
// moving file is a decision written there first.
func TestTheDeviceHoldsTheDesignsKeys(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"":              "terminal",
		"display":       "color_messages direction emoji filing_spaces fps hyperlinks max_name_length media mouse network_colors open_in_insert_mode reactions read_delay read_rule rooms row_numbers send_receipts send_typing show_archived skin_tone theme threads typing unread unread_line",
		"notifications": "body command desktop enabled sound_command timeout title",
		"clipboard":     "command copy_downloads focus_command open_command raise_command",
		"commands":      "timeout",
		"complete":      "enabled ghost model ratio scope sources",
		"spell":         "autocorrect check_before_send declined frequencies_declined rare_before_send rare_underline underline",
		"assist":        "name_threads name_threads_after",
		"composer":      "*",
		"keys":          "*",
		"command":       "*",
	}
	got := map[string][]string{}
	for f := range reflect.TypeFor[Device]().Fields() {
		name := tomlName(f)
		switch f.Type.Kind() {
		case reflect.Slice:
			got[name] = nil
			continue
		case reflect.Struct:
		default:
			got[""] = append(got[""], name)
			continue
		}
		for sub := range f.Type.Fields() {
			got[name] = append(got[name], tomlName(sub))
		}
	}
	for table, keys := range want {
		if keys == "*" {
			if _, ok := got[table]; !ok {
				t.Errorf("[%s] is not the device's", table)
			}
			continue
		}
		have := got[table]
		slices.Sort(have)
		if strings.Join(have, " ") != keys {
			t.Errorf("[%s] device keys = %v, want %s", table, have, keys)
		}
	}
	if complete := reflect.TypeFor[DeviceCompleteModel](); complete.NumField() != 1 || tomlName(complete.Field(0)) != "declined" {
		t.Errorf("[complete.model] device keys are %v, want declined alone", complete)
	}
}
