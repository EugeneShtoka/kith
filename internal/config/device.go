package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/BurntSushi/toml"
)

// defaultDeviceTOML is the fully-annotated device file written on a device's first run.
//
//go:embed default_device.toml
var defaultDeviceTOML string

// Device is device.toml: what only a client reads, so a phone and a laptop may differ.
// kithd never reads it; a table split between the files keeps its name in both.
type Device struct {
	Terminal      string              `toml:"terminal"` // what `kith --open` starts; empty detects one
	Display       DeviceDisplay       `toml:"display"`
	Notifications DeviceNotifications `toml:"notifications"`
	Keys          Keys                `toml:"keys"`
	Clipboard     DeviceClipboard     `toml:"clipboard"`
	Composer      Composer            `toml:"composer"`
	Spell         DeviceSpell         `toml:"spell"`
	Complete      DeviceComplete      `toml:"complete"`
	Assist        DeviceAssist        `toml:"assist"`
	Commands      DeviceCommands      `toml:"commands"`
	UserCommands  []Command           `toml:"command"`
	Bridges       []Bridge            `toml:"bridge"`
	// ProfileInUse is the [[profile]] the desktop window runs in, as it was last picked
	// there; "" when none was. A name the config no longer has is as good as none.
	ProfileInUse string `toml:"profile_in_use"`
}

// Bridge is a kithd this device dials over its [remote] listen ([[bridge]]), written by
// `kith join`: Addresses in the order to try them, and ID, the device ID its key must
// have.
type Bridge struct {
	Name      string   `toml:"name"`
	Addresses []string `toml:"addresses"`
	ID        string   `toml:"id"`
}

// The files a setting can be in: the bridge's, which kithd reads, and a device's.
const (
	BridgeFile = "config.toml"
	DeviceFile = "device.toml"
)

// DevicePath is the device file of the config at configPath, beside it: the XDG
// kith/device.toml for the default config. Another config (a second instance) keeps
// its own.
func DevicePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), DeviceFile)
}

// WriteDeviceDefaultIfMissing writes the annotated device file to path if no file
// exists; created reports whether it wrote one.
func WriteDeviceDefaultIfMissing(path string) (created bool, err error) {
	return writeIfMissing(path, defaultDeviceTOML)
}

// AnnotatedDevice is the fully-documented default device file.
func AnnotatedDevice() string { return defaultDeviceTOML }

// LoadDevice reads the device file at path, refusing unknown keys as Load does; a
// missing file is every default.
func LoadDevice(path string) (Device, error) {
	body, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return decodeDevice(path, "")
	}
	if err != nil {
		return Device{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return decodeDevice(path, string(body))
}

// decodeDevice is LoadDevice's reading; source names the text in errors.
func decodeDevice(source, text string) (Device, error) {
	dev, meta, err := decodeDeviceLoose(source, text)
	if err != nil {
		return Device{}, err
	}
	if err := unknownKeys(source, meta.Undecoded()); err != nil {
		return Device{}, err
	}
	return dev, nil
}

// decodeDeviceLoose is decodeDevice leaving the keys Device does not have to the
// caller.
func decodeDeviceLoose(source, text string) (Device, toml.MetaData, error) {
	var dev Device
	meta, err := toml.Decode(text, &dev)
	if err != nil {
		return Device{}, meta, fmt.Errorf("config: decode %s: %w", source, err)
	}
	// Unmentioned fields decode as zero; give every action its default key.
	dev.Keys.FillDefaults()
	// Absent tag keys take the starter's; `[keys] tag = []` means none.
	if !meta.IsDefined("keys", "tag") {
		dev.Keys.Tag = DefaultKeys().Tag
	}
	return dev, meta, nil
}

// starterTagKeys are the annotated device file's tag keys, bound to the starter's tags.
func starterTagKeys() TagKeys {
	var dev Device
	if _, err := toml.Decode(defaultDeviceTOML, &dev); err != nil {
		panic("config: the starter device file does not decode: " + err.Error()) // a build that cannot happen
	}
	return dev.Keys.Tag
}

// Clone returns a deep copy of d, safe to edit while d is written elsewhere.
func (d Device) Clone() Device {
	out, _ := reflect.TypeAssert[Device](deepCopy(reflect.ValueOf(d)))
	return out
}

// saveDevice writes dev to path as Save writes a config: the settings that differ
// from the defaults, atomically, the first file kept once.
func saveDevice(path string, dev Device) error {
	if err := backupOnce(path); err != nil {
		return err
	}
	return writeAtomic(path, encodeDevice(dev))
}

// encodeDevice is dev as saveDevice writes it.
func encodeDevice(dev Device) string {
	return deviceHeader + encodeDiff(reflect.ValueOf(dev), reflect.ValueOf(deviceBaseline()), nil)
}

// deviceHeader opens every saved device file.
const deviceHeader = `# kith device settings — written by kith.
#
# What only this device reads: its look, keys, how it shows a notification. The
# bridge's settings are in config.toml beside it. This file records only the settings
# that differ from the built-in defaults; the app rewrites it when you change a setting
# from inside it, which keeps the values and drops any comments you added.
#
# For every option with its documentation, run: kith --print-device-config

`

// deviceBaseline is what LoadDevice produces from an empty file.
func deviceBaseline() Device {
	var dev Device
	dev.Keys.FillDefaults()
	return dev
}

// UpdateDevice writes one window's change to the device file as the file is now: each
// setting that differs between was and now takes now's value, every other the file's,
// so two windows editing at once both keep their changes. It returns what the file
// then holds.
func UpdateDevice(path string, was, now Device) (Device, error) {
	unlock, err := lockDevice(path)
	if err != nil {
		return Device{}, err
	}
	defer unlock()
	current, err := LoadDevice(path)
	if err != nil {
		return Device{}, err
	}
	next := current.Clone()
	overlay(reflect.ValueOf(&next).Elem(), reflect.ValueOf(now), reflect.ValueOf(was))
	if reflect.DeepEqual(next, current) {
		return current, nil
	}
	if err := saveDevice(path, next); err != nil {
		return Device{}, err
	}
	return next, nil
}

// overlay writes into dst each setting of now that differs from was, entry by entry in
// a map (a key binding) and whole for a list.
func overlay(dst, now, was reflect.Value) {
	switch dst.Kind() {
	case reflect.Struct:
		for i := range dst.NumField() {
			if dst.Type().Field(i).IsExported() {
				overlay(dst.Field(i), now.Field(i), was.Field(i))
			}
		}
	case reflect.Map:
		for _, key := range append(now.MapKeys(), was.MapKeys()...) {
			got, had := now.MapIndex(key), was.MapIndex(key)
			if got.IsValid() == had.IsValid() && (!got.IsValid() || reflect.DeepEqual(got.Interface(), had.Interface())) {
				continue
			}
			if dst.IsNil() {
				dst.Set(reflect.MakeMap(dst.Type()))
			}
			if got.IsValid() {
				dst.SetMapIndex(key, deepCopy(got))
			} else {
				dst.SetMapIndex(key, reflect.Value{})
			}
		}
	case reflect.Slice:
		if !slicesEqual(now, was) { // nil and empty are one list, as in TOML
			dst.Set(deepCopy(now))
		}
	default:
		if !reflect.DeepEqual(now.Interface(), was.Interface()) {
			dst.Set(deepCopy(now))
		}
	}
}
