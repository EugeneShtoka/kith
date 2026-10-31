package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/BurntSushi/toml"
	"github.com/gofrs/flock"
)

// SplitDevice moves the device settings a bridge's config holds (written before they
// had a file of their own, or by hand since) into the device file, and reports
// whether there were any. kithd and every client call it before loading, so whichever
// opens the file first splits it: the config refuses a device key as unknown.
//
// A setting the device file already has (other than its default) is kept over the
// config's. The device file is written first and the config second, both atomically,
// the config's first version kept once (Save); a split cut off between the two
// writes is finished by the next, which finds the same keys and the same result.
func SplitDevice(configPath, devicePath string) (moved bool, err error) {
	if _, serr := os.Stat(configPath); errors.Is(serr, os.ErrNotExist) {
		return false, nil
	}
	unlock, err := lockDevice(devicePath)
	if err != nil {
		return false, err
	}
	defer unlock()
	for range splitTries {
		done, err := splitOnce(configPath, devicePath)
		if !errors.Is(err, errConfigMoved) {
			return done, err
		}
	}
	return false, fmt.Errorf("config: %s kept changing while its device settings were moved out", configPath)
}

// splitTries bounds the retries of a split whose config another writer changed meanwhile.
const splitTries = 3

// errConfigMoved is a config another writer changed after the split read it.
var errConfigMoved = errors.New("config: changed while being split")

// splitOnce is one try at SplitDevice, under the device file's lock.
func splitOnce(configPath, devicePath string) (bool, error) {
	body, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		return false, fmt.Errorf("config: read %s: %w", configPath, err)
	}
	cfg, meta, err := decodeLoose(configPath, string(body))
	if err != nil {
		return false, err
	}
	notBridge := meta.Undecoded()
	if len(notBridge) == 0 {
		return false, nil
	}
	fromConfig, deviceMeta, err := decodeDeviceLoose(configPath, string(body))
	if err != nil {
		return false, err
	}
	// [gui] has the same keys in both files: the config's is the shared one and stays,
	// never made this device's override.
	fromConfig.GUI = GUI{}
	if merr := moveRuleSounds(configPath, string(body), &cfg, &fromConfig); merr != nil {
		return false, merr
	}
	// A key neither file reads is a mistake to show, not one to drop with the rewrite;
	// a rule's sound is moved by its rule's name, above.
	notDevice := keyStrings(deviceMeta.Undecoded())
	var unknown []toml.Key
	for _, key := range notBridge {
		if key.String() != ruleSoundKey && slices.Contains(notDevice, key.String()) {
			unknown = append(unknown, key)
		}
	}
	if uerr := unknownKeys(configPath, unknown); uerr != nil {
		return false, uerr
	}
	have, err := LoadDevice(devicePath)
	if err != nil {
		return false, err
	}
	next := fromConfig.Clone()
	overlay(reflect.ValueOf(&next).Elem(), reflect.ValueOf(have), reflect.ValueOf(deviceBaseline()))
	if !reflect.DeepEqual(next, have) {
		if serr := saveDevice(devicePath, next); serr != nil {
			return false, serr
		}
	}
	// kithd may have saved the config since it was read (UpdateConfig); write over that
	// and its change is lost, so read again.
	again, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		return false, fmt.Errorf("config: read %s: %w", configPath, err)
	}
	if Revision(again) != Revision(body) {
		return false, errConfigMoved
	}
	// The rewrite drops the file's comments, and its .bak may be from an older save:
	// the file as it was before its first split is kept on its own.
	if err := keepBefore(configPath, body); err != nil {
		return false, err
	}
	return true, Save(configPath, cfg)
}

// ruleSoundKey is a rule's sound, written when a rule carried its own: now the device's,
// under the rule's name ([notifications.sounds]).
const ruleSoundKey = "notifications.rule.sound"

// moveRuleSounds moves each rule's sound in text to dev's sounds, by the name of its
// rule in cfg: an unnamed rule, or one sharing its name with a rule of another sound (or
// none), is given a name of its own first, so no other rule takes its sound. The same
// text always gives the same names, so a split cut off before the config's rewrite is
// finished alike.
func moveRuleSounds(source, text string, cfg *Config, dev *Device) error {
	var file struct {
		Notifications struct {
			Rules []struct {
				Sound string `toml:"sound"`
			} `toml:"rule"`
		} `toml:"notifications"`
	}
	if _, err := toml.Decode(text, &file); err != nil {
		return fmt.Errorf("config: decode %s: %w", source, err)
	}
	rules, had := cfg.Notifications.Rules, file.Notifications.Rules
	if len(had) != len(rules) {
		return fmt.Errorf("config: %s: its rules read differently twice", source)
	}
	shared := func(i int) bool {
		for j := range rules {
			if j != i && rules[j].Name == rules[i].Name && had[j].Sound != had[i].Sound {
				return true
			}
		}
		return false
	}
	for i := range rules {
		if had[i].Sound == "" {
			continue
		}
		if rules[i].Name == "" {
			rules[i].Name = UniqueRuleName(derivedRuleName(rules[i]), rules)
		} else if shared(i) {
			rules[i].Name = UniqueRuleName(rules[i].Name, rules)
		}
		if dev.Notifications.Sounds == nil {
			dev.Notifications.Sounds = map[string]string{}
		}
		dev.Notifications.Sounds[rules[i].Name] = had[i].Sound
	}
	return nil
}

// derivedRuleName names a rule by what it matches.
func derivedRuleName(r Rule) string {
	switch {
	case r.Sender != "" && r.Match != "":
		return r.Sender + " in " + r.Match
	case r.Sender != "":
		return r.Sender
	case r.Match != "":
		return r.Match
	default:
		return "everything"
	}
}

// keepBefore keeps body as the config before its first split, beside it; a later split
// leaves that copy alone.
func keepBefore(configPath string, body []byte) error {
	before := configPath + ".before-device"
	if _, err := os.Stat(before); err == nil {
		return nil
	}
	if err := os.WriteFile(before, body, 0o600); err != nil { // #nosec G703 -- derived from the caller's own config path
		return fmt.Errorf("config: keep %s: %w", before, err)
	}
	return nil
}

// keyStrings is each key as its dotted path.
func keyStrings(keys []toml.Key) []string {
	out := make([]string, len(keys))
	for i, key := range keys {
		out[i] = key.String()
	}
	return out
}

// lockDevice takes the device file's lock, held by every writer of it (a split, a
// window's change) so none writes over another's read.
func lockDevice(path string) (unlock func(), err error) {
	fl := flock.New(path + ".lock")
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("config: lock %s: %w", path, err)
	}
	return func() { _ = fl.Unlock() }, nil
}
