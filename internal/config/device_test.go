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

// How a generated window saves the device file.
const (
	// merged: a window's settings editor (UpdateDevice), its change put onto the file as
	// it is then.
	merged = iota
	// atRevision: the desktop's settings page (UpdateDeviceAt), written only over the
	// file it read; told it changed, it reads it again and writes again those of its
	// changes no one else made meanwhile, conceding the others.
	atRevision
	// byHand: an editor, writing the file whole with no lock, a comment added: its bytes
	// change even where its settings do not.
	byHand
)

// casTries bounds an atRevision window's rewrites: each other window changes the file
// once, so it is told so at most once each.
const casTries = 5

// setPath sets a changed path of dev to value, as valueOf spells it.
func setPath(t *testing.T, dev *Device, path, value string) {
	t.Helper()
	if binding, ok := strings.CutPrefix(path, "keys."); ok {
		dev.Keys.Bindings[binding] = value
		return
	}
	s := Settings{Device: *dev}
	if err := s.SetValue(path, value); err != nil {
		t.Fatal(err)
	}
	*dev = s.Device
}

// saveAtRevision is an atRevision window's save of now, changed from base read at
// revision: it returns the paths it conceded to another writer.
func saveAtRevision(t *testing.T, path, revision string, base, now Device, changed []string) ([]string, error) {
	want := map[string]string{}
	for _, p := range changed {
		if valueOf(now, p) != valueOf(base, p) {
			want[p] = valueOf(now, p)
		}
	}
	var conceded []string
	for range casTries {
		_, _, err := UpdateDeviceAt(path, revision, func(file Device) (Device, error) {
			if !reflect.DeepEqual(file, base) {
				t.Errorf("a change made at revision %s was handed a file changed since", revision)
			}
			for p, v := range want {
				setPath(t, &file, p, v)
			}
			return file, nil
		})
		if !errors.Is(err, ErrDeviceChanged) {
			return conceded, err
		}
		fresh, at, err := ReadDevice(path)
		if err != nil {
			return conceded, err
		}
		for p := range want {
			if valueOf(fresh, p) != valueOf(base, p) {
				delete(want, p)
				conceded = append(conceded, p)
			}
		}
		base, revision = fresh, at
	}
	return conceded, errors.New("the file kept changing")
}

// saveByHand is a byHand window's save: its change put onto the file as it is now,
// written whole with a comment, no lock taken.
func saveByHand(t *testing.T, path string, was, now Device) {
	current, err := LoadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	overlay(reflect.ValueOf(&current).Elem(), reflect.ValueOf(now), reflect.ValueOf(was))
	writeFile(t, path, encodeDevice(current)+"# edited by hand\n")
}

// Over generated windows each changing some settings of the device file it read, saved
// in any order or all at once, by a settings editor, the desktop's settings page or by
// hand: every setting one window changed is the file's after, one changed by several is
// one of theirs (in order, the last one's), and every other keeps the file's. The
// desktop's page concedes a setting exactly when another window changed it after the
// page read the file (in order, one saved before it), and writes every other.
func TestWindowsEditingTheDeviceAtOnceKeepEachOthersChanges(t *testing.T) {
	t.Parallel()
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 0xde71)) // #nosec G404 -- reproducible
		path := filepath.Join(t.TempDir(), DeviceFile)
		writeFile(t, path, tomlText(settingsIn(rng, DeviceFile, 6)))
		base, revision, err := ReadDevice(path)
		if err != nil {
			t.Fatal(err)
		}
		atOnce := seed%2 == 1
		windows := 2 + rng.IntN(2)
		was, now := make([]Device, windows), make([]Device, windows)
		changed, kind := make([][]string, windows), make([]int, windows)
		for w := range windows {
			was[w], now[w] = base.Clone(), base.Clone()
			changed[w] = changeSome(t, rng, &now[w])
			kind[w] = rng.IntN(3)
			if atOnce && kind[w] == byHand { // a hand edit takes no lock: it races every writer
				kind[w] = merged
			}
		}
		order := rng.Perm(windows)
		conceded := make([][]string, windows)
		save := func(w int) (written Device, merge bool) {
			var serr error
			switch kind[w] {
			case merged:
				written, serr = UpdateDevice(path, was[w], now[w])
			case atRevision:
				conceded[w], serr = saveAtRevision(t, path, revision, was[w], now[w], changed[w])
			case byHand:
				saveByHand(t, path, was[w], now[w])
			}
			if serr != nil {
				t.Errorf("seed %d: window %d: %v", seed, w, serr)
			}
			return written, kind[w] == merged
		}
		if atOnce {
			var wg sync.WaitGroup
			for _, w := range order {
				wg.Go(func() { save(w) })
			}
			wg.Wait()
		} else {
			for _, w := range order {
				if written, merge := save(w); merge && !reflect.DeepEqual(mustLoad(t, path), written) {
					t.Fatalf("seed %d: UpdateDevice returned what the file does not hold", seed)
				}
			}
		}
		final, err := LoadDevice(path)
		if err != nil {
			t.Fatal(err)
		}
		checkMerged(t, seed, atOnce, base, final, now, changed, order, kind, conceded)
	}
}

// mustLoad is the device file at path.
func mustLoad(t *testing.T, path string) Device {
	t.Helper()
	dev, err := LoadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	return dev
}

// checkMerged holds TestWindowsEditingTheDeviceAtOnceKeepEachOthersChanges's property.
func checkMerged(t *testing.T, seed uint64, atOnce bool, base, final Device, now []Device, changed [][]string, order, kind []int, conceded [][]string) {
	t.Helper()
	by := map[string][]int{} // path → the windows that changed it, in save order
	for _, w := range order {
		for _, p := range changed[w] {
			if valueOf(now[w], p) != valueOf(base, p) {
				by[p] = append(by[p], w)
			}
		}
	}
	// Each page's concessions: in order, the paths a window saved before it changed; at
	// once, only paths another window changed.
	for _, w := range order {
		if kind[w] != atRevision {
			continue
		}
		for p, ws := range by {
			if !slices.Contains(ws, w) {
				continue
			}
			before := slices.Index(ws, w) > 0
			other := len(ws) > 1
			if got := slices.Contains(conceded[w], p); (!atOnce && got != before) || (atOnce && got && !other) {
				t.Fatalf("seed %d: window %d conceded %s: %v; another window changed it before: %v", seed, w, p, got, before)
			}
		}
	}
	for p, ws := range by {
		ws = slices.DeleteFunc(slices.Clone(ws), func(w int) bool { return slices.Contains(conceded[w], p) })
		got := valueOf(final, p)
		switch {
		case len(ws) == 0:
			t.Fatalf("seed %d: every window that changed %s conceded it", seed, p)
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

// A save at the file's revision answers what it wrote and the revision of those bytes,
// which the next save is made at, even one that changes nothing.
func TestUpdateDeviceAtAnswersTheRevisionItWrote(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), DeviceFile)
	writeFile(t, path, "# by hand\n[display]\nfps = 20\n")
	_, at, err := ReadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fps := range []int{30, 30} {
		written, next, err := UpdateDeviceAt(path, at, func(dev Device) (Device, error) { dev.Display.FPS = fps; return dev, nil })
		if err != nil {
			t.Fatal(err)
		}
		onDisk, want, err := ReadDevice(path)
		if err != nil || next != want || !reflect.DeepEqual(written, onDisk) || onDisk.Display.FPS != fps {
			t.Fatalf("UpdateDeviceAt answered revision %q and fps %d; the file holds %q and fps %d (%v)", next, written.Display.FPS, want, onDisk.Display.FPS, err)
		}
		at = next
	}
}

// A save to a device file that will not load changes nothing, and says why: one made
// at the revision read before a hand edit broke it, too.
func TestUpdateDeviceRefusesAFileThatWillNotLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), DeviceFile)
	_, before, err := ReadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "[display]\nfsp = 3\n")
	now := deviceBaseline()
	now.Display.FPS = 10
	if _, err := UpdateDevice(path, deviceBaseline(), now); err == nil || !strings.Contains(err.Error(), "display.fsp") {
		t.Errorf("UpdateDevice = %v, want the unknown key named", err)
	}
	set := func(dev Device) (Device, error) { dev.Display.FPS = 10; return dev, nil }
	if _, _, err := UpdateDeviceAt(path, before, set); err == nil || !strings.Contains(err.Error(), "display.fsp") {
		t.Errorf("UpdateDeviceAt = %v, want the unknown key named", err)
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
		"":              "profile_in_use terminal",
		"display":       "color_messages direction emoji filing_spaces fps hyperlinks max_name_length media mouse network_colors open_in_insert_mode reactions read_delay read_rule rooms row_numbers send_receipts send_typing show_archived skin_tone theme threads typing unread_line",
		"notifications": "body command desktop enabled sound sound_command sounds timeout title",
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
