package config

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A setting written into a test file: its property, and its value as TOML.
type written struct {
	p     Property
	value string
}

// settingsIn picks some of file's properties and gives each a value of its kind.
func settingsIn(rng *rand.Rand, file string, most int) []written {
	var all []Property
	for _, p := range Properties() {
		if p.File == file {
			all = append(all, p)
		}
	}
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	out := make([]written, 0, most)
	for _, p := range all[:rng.IntN(most+1)] {
		out = append(out, written{p, valueOfKind(rng, p.Kind)})
	}
	return out
}

// valueOfKind is a TOML value a property of kind decodes, its default or not.
func valueOfKind(rng *rand.Rand, kind PropertyKind) string {
	switch kind {
	case PropertyBool, PropertyOptionalBool:
		return fmt.Sprint(rng.IntN(2) == 0)
	case PropertyInt, PropertyOptionalInt:
		return fmt.Sprint(rng.IntN(5))
	case PropertyFloat, PropertyOptionalFloat:
		return fmt.Sprintf("%d.5", rng.IntN(3))
	case PropertyList:
		return fmt.Sprintf(`["w%d", "x"]`, rng.IntN(9))
	default:
		return fmt.Sprintf(`"v%d"`, rng.IntN(9))
	}
}

// tomlText writes settings under their tables, top-level keys first.
func tomlText(settings []written) string {
	byTable := map[string][]string{}
	for _, s := range settings {
		table, key := "", s.p.Path
		if i := strings.LastIndex(s.p.Path, "."); i >= 0 {
			table, key = s.p.Path[:i], s.p.Path[i+1:]
		}
		byTable[table] = append(byTable[table], key+" = "+s.value)
	}
	var b strings.Builder
	for _, table := range slices.Sorted(func(yield func(string) bool) {
		for t := range byTable {
			if !yield(t) {
				return
			}
		}
	}) {
		if table != "" {
			b.WriteString("\n[" + table + "]\n")
		}
		b.WriteString(strings.Join(byTable[table], "\n") + "\n")
	}
	return b.String()
}

// deviceValue and bridgeValue are the property at path, as text.
func deviceValue(dev Device, path string) string {
	v, _, _ := Settings{Device: dev}.Value(path)
	return v
}

func bridgeValue(cfg Config, path string) string {
	v, _, _ := Settings{Bridge: cfg}.Value(path)
	return v
}

// splitCase is one generated pair of files: what the config holds of both files'
// settings, and what the device file already holds.
type splitCase struct {
	bridge, inConfig, inDevice []written
	reply                      string // [keys] timeline.reply in the config, if any
}

func genSplitCase(rng *rand.Rand) splitCase {
	c := splitCase{
		bridge:   settingsIn(rng, BridgeFile, 6),
		inConfig: settingsIn(rng, DeviceFile, 8),
	}
	if rng.IntN(2) == 0 {
		c.inDevice = settingsIn(rng, DeviceFile, 8)
	}
	if rng.IntN(3) == 0 {
		c.reply = "R"
	}
	return c
}

// files writes the case's config.toml (and device.toml when it has one) into dir.
func (c splitCase) files(t *testing.T, dir string) (configPath, devicePath string) {
	t.Helper()
	configPath, devicePath = filepath.Join(dir, BridgeFile), filepath.Join(dir, DeviceFile)
	text := tomlText(slices.Concat(c.bridge, c.inConfig))
	if c.reply != "" {
		text += "\n[keys.timeline]\nreply = \"" + c.reply + "\"\n"
	}
	writeFile(t, configPath, text)
	if c.inDevice != nil {
		writeFile(t, devicePath, tomlText(c.inDevice))
	}
	return configPath, devicePath
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// expected is the device a split leaves: each setting the device file gave a value
// other than its default keeps it, every other takes the config's.
func (c splitCase) expected(t *testing.T) Device {
	t.Helper()
	fromConfig, _, err := decodeDeviceLoose("the config", tomlText(c.inConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.reply != "" {
		fromConfig.Keys.Bindings["timeline.reply"] = c.reply
	}
	have, err := decodeDevice("the device file", tomlText(c.inDevice))
	if err != nil {
		t.Fatal(err)
	}
	baseline := deviceBaseline()
	s := Settings{Device: fromConfig}
	for _, p := range Properties() {
		if p.File == DeviceFile && deviceValue(have, p.Path) != deviceValue(baseline, p.Path) {
			if err := s.SetValue(p.Path, deviceValue(have, p.Path)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s.Device
}

// check holds the split's outcome: the config loads with its own settings as written,
// the device holds what expected says, and splitting again changes nothing.
func (c splitCase) check(t *testing.T, seed uint64, configPath, devicePath string) {
	t.Helper()
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("seed %d: the split config does not load: %v", seed, err)
	}
	original, _, err := decodeLoose("the original", tomlText(c.bridge))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range c.bridge {
		if got, want := bridgeValue(cfg, s.p.Path), bridgeValue(original, s.p.Path); got != want {
			t.Fatalf("seed %d: %s = %q after the split, want %q", seed, s.p.Path, got, want)
		}
	}
	dev, err := LoadDevice(devicePath)
	if err != nil {
		t.Fatalf("seed %d: the device file does not load: %v", seed, err)
	}
	want := c.expected(t)
	for _, p := range Properties() {
		if p.File != DeviceFile {
			continue
		}
		if got, w := deviceValue(dev, p.Path), deviceValue(want, p.Path); got != w {
			t.Fatalf("seed %d: device %s = %q, want %q\nconfig had %v\ndevice had %v", seed, p.Path, got, w, c.inConfig, c.inDevice)
		}
	}
	if got := dev.Keys.Bindings["timeline.reply"]; got != want.Keys.Bindings["timeline.reply"] {
		t.Fatalf("seed %d: reply bound to %q, want %q", seed, got, want.Keys.Bindings["timeline.reply"])
	}
	before := snapshot(t, configPath, devicePath)
	if moved, err := SplitDevice(configPath, devicePath); moved || err != nil {
		t.Fatalf("seed %d: a second split = %v, %v; want nothing to move", seed, moved, err)
	}
	if after := snapshot(t, configPath, devicePath); after != before {
		t.Fatalf("seed %d: a second split rewrote a file", seed)
	}
}

func snapshot(t *testing.T, paths ...string) string {
	t.Helper()
	var b strings.Builder
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteString("\x00")
	}
	return b.String()
}

// Over generated configs holding some of each file's settings, and device files
// holding some already: a split moves every device setting out, keeps what the device
// file set, leaves the config's own as they were, and is done once; so is one cut off
// between its two writes, finished by the next; and so are several running at once
// (kithd and a client starting together), while no reader ever finds a setting in
// neither file.
func TestASplitMovesEveryDeviceSettingOnce(t *testing.T) {
	t.Parallel()
	for seed := range uint64(150) {
		rng := rand.New(rand.NewPCG(seed, 0x5917)) // #nosec G404 -- reproducible
		c := genSplitCase(rng)
		dir := t.TempDir()
		configPath, devicePath := c.files(t, dir)
		switch seed % 3 {
		case 0:
			if _, err := SplitDevice(configPath, devicePath); err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
		case 1:
			interrupted(t, c, configPath, devicePath)
			if _, err := SplitDevice(configPath, devicePath); err != nil {
				t.Fatalf("seed %d: the split after the cut-off one: %v", seed, err)
			}
		case 2:
			racing(t, seed, c, configPath, devicePath)
		}
		c.check(t, seed, configPath, devicePath)
	}
}

// interrupted leaves the files as a split stopped between its writes would: the device
// file (and its backup) written, the config not.
func interrupted(t *testing.T, c splitCase, configPath, devicePath string) {
	t.Helper()
	ahead := t.TempDir()
	aheadConfig, aheadDevice := c.files(t, ahead)
	if _, err := SplitDevice(aheadConfig, aheadDevice); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{DeviceFile, DeviceFile + ".bak"} {
		data, err := os.ReadFile(filepath.Join(ahead, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(filepath.Dir(devicePath), name), string(data))
	}
}

// racing splits from several goroutines at once while a reader checks that every
// setting the config held is, at every moment, in the config still or in the device
// file with the value the split gives it.
func racing(t *testing.T, seed uint64, c splitCase, configPath, devicePath string) {
	t.Helper()
	want := c.expected(t)
	stop := make(chan struct{})
	var reader sync.WaitGroup
	reader.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if lost := lostSetting(c, want, configPath, devicePath); lost != "" {
				t.Errorf("seed %d: mid-split, %s is in neither file", seed, lost)
				return
			}
		}
	})
	var splitters sync.WaitGroup
	for range 4 {
		splitters.Go(func() {
			if _, err := SplitDevice(configPath, devicePath); err != nil {
				t.Errorf("seed %d: a racing split: %v", seed, err)
			}
		})
	}
	splitters.Wait()
	close(stop)
	reader.Wait()
}

// lostSetting names a setting the config held that neither file holds now; "" when
// none is lost. The config is read first, as a split writes it last.
func lostSetting(c splitCase, want Device, configPath, devicePath string) string {
	configText, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	_, inConfig, err := decodeDeviceLoose("", string(configText))
	if err != nil {
		return ""
	}
	dev, err := LoadDevice(devicePath)
	if err != nil {
		dev = deviceBaseline()
	}
	for _, s := range c.inConfig {
		p := s.p.Path
		if !inConfig.IsDefined(strings.Split(p, ".")...) && deviceValue(dev, p) != deviceValue(want, p) {
			return p
		}
	}
	return ""
}

// A device setting written into the config by hand after the split is refused there
// as any unknown key is, by name; the next split moves it, unless the device file sets
// it already.
func TestADeviceSettingWrittenIntoTheConfigIsAnUnknownKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath, devicePath := filepath.Join(dir, BridgeFile), filepath.Join(dir, DeviceFile)
	writeFile(t, configPath, "[display]\ntime_format = \"12h\"\n")
	writeFile(t, devicePath, "[display]\nfps = 60\n")
	if moved, err := SplitDevice(configPath, devicePath); moved || err != nil {
		t.Fatalf("a config with no device settings: moved %v, %v", moved, err)
	}

	writeFile(t, configPath, "[display]\ntime_format = \"12h\"\nfps = 24\nmouse = false\n")
	_, err := Load(configPath)
	typo := filepath.Join(dir, "typo.toml")
	writeFile(t, typo, "[display]\ntime_format = \"12h\"\nfsp = 24\n")
	_, typoErr := Load(typo)
	if err == nil || typoErr == nil {
		t.Fatalf("Load = %v; a typo = %v; want both refused", err, typoErr)
	}
	if want := "config: " + configPath + ": unknown keys: display.fps, display.mouse"; err.Error() != want {
		t.Errorf("the device key's error = %q, want %q", err, want)
	}
	if _, derr := Decode("[display]\nfps = 24\n"); derr == nil || !strings.HasSuffix(derr.Error(), "unknown keys: display.fps") {
		t.Errorf("kithd's check of a sent config = %v, want the unknown key", derr)
	}

	if moved, serr := SplitDevice(configPath, devicePath); !moved || serr != nil {
		t.Fatalf("the next split: moved %v, %v", moved, serr)
	}
	dev, err := LoadDevice(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Display.FPS != 60 || dev.Display.UseMouse() {
		t.Errorf("fps %d, mouse %v; want the device file's fps kept and the config's mouse moved", dev.Display.FPS, dev.Display.UseMouse())
	}
	if cfg, err := Load(configPath); err != nil || cfg.Display.TimeFormat != "12h" {
		t.Errorf("the config after = %+v, %v", cfg.Display, err)
	}
}

// A key neither file reads stops the split, named as Load names it, and nothing is
// written: rewriting the config would drop it.
func TestASplitRefusesAKeyNeitherFileReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath, devicePath := filepath.Join(dir, BridgeFile), filepath.Join(dir, DeviceFile)
	text := "[display]\nfps = 24\nfsp = 30\n"
	writeFile(t, configPath, text)
	_, err := SplitDevice(configPath, devicePath)
	if err == nil || !strings.HasSuffix(err.Error(), "unknown keys: display.fsp") {
		t.Errorf("SplitDevice = %v, want the misspelled key named alone", err)
	}
	if got, _ := os.ReadFile(configPath); string(got) != text {
		t.Errorf("the config was rewritten:\n%s", got)
	}
	if _, err := os.Stat(devicePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a device file was written: %v", err)
	}
}

// A config that does not exist yet has nothing to split, and no lock is taken where
// its folder may not exist either.
func TestNoConfigIsNothingToSplit(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "absent")
	if moved, err := SplitDevice(filepath.Join(dir, BridgeFile), filepath.Join(dir, DeviceFile)); moved || err != nil {
		t.Errorf("SplitDevice = %v, %v", moved, err)
	}
}

// The first split keeps the config as it was, comments and all, even when a .bak from
// an older save is there; a later split leaves that copy alone.
func TestASplitKeepsTheConfigAsItWas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath, devicePath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "device.toml")
	original := "# my notes on why\n[display]\nfps = 30\n"
	if err := os.WriteFile(configPath+".bak", []byte("# an older save\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if moved, err := SplitDevice(configPath, devicePath); err != nil || !moved {
		t.Fatalf("SplitDevice = %v, %v; want the fps moved", moved, err)
	}
	if err := os.WriteFile(configPath, []byte("[display]\nfps = 40\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SplitDevice(configPath, devicePath); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(configPath + ".before-device"); err != nil || string(got) != original {
		t.Fatalf("kept %q (%v), want the config as it was before its first split", got, err)
	}
}
