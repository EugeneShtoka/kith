// Package config loads kith's XDG TOML configuration: the bridge's config.toml and a
// device's device.toml. It imports no other internal package. default.toml and
// default_device.toml document every setting for users; Go comments here give only
// type, unit and zero-value meaning.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/adrg/xdg"
)

// defaultConfigTOML is the fully-annotated starter config written on first run.
//
//go:embed default.toml
var defaultConfigTOML string

// Config is the bridge's configuration, config.toml: what kithd reads, one field per
// top-level key or table. What only a client reads is a device's (Device).
type Config struct {
	AllowTokenFile bool `toml:"allow_token_file"`
	// BridgeContacts is each bridge's provisioning API base URL whose contact lists
	// name people a network shows only by number (domain.Directory).
	BridgeContacts []string      `toml:"bridge_contacts"`
	Display        Display       `toml:"display"`
	Notifications  Notifications `toml:"notifications"`
	Clipboard      Clipboard     `toml:"clipboard"`
	Codes          Codes         `toml:"codes"`
	Spell          Spell         `toml:"spell"`
	Complete       Complete      `toml:"complete"`
	Assist         Assist        `toml:"assist"`
	Agent          Agent         `toml:"agent"`
	Commands       Commands      `toml:"commands"`
	Schedule       Schedule      `toml:"schedule"`
	Phonebook      Phonebook     `toml:"phonebook"`
	Log            Log           `toml:"log"`
	Matrix         Matrix        `toml:"matrix"`
	WhatsApp       WhatsApp      `toml:"whatsapp"`
	Slack          Slack         `toml:"slack"`
	Telegram       Telegram      `toml:"telegram"`
	Messenger      Messenger     `toml:"messenger"`
	LinkedIn       LinkedIn      `toml:"linkedin"`
	GMessages      GMessages     `toml:"gmessages"`
	Signal         Signal        `toml:"signal"`
	Instagram      Instagram     `toml:"instagram"`
	Storage        Storage       `toml:"storage"`
	Backup         Backup        `toml:"backup"`
	Tags           []Tag         `toml:"tag"`
	Profiles       []Profile     `toml:"profile"`
	Filters        []Filter      `toml:"filter"`
}

// Log is `[log]`: how much each binary writes to its log, and where. Level empty is
// "info" (`--log-level` and $KITH_LOG_LEVEL override it); Target empty is "auto": the
// journal when there is one, else a file (`--log-target` and $KITH_LOG_TARGET override
// it). kith-mcp always logs to its stderr.
type Log struct {
	Level  string `toml:"level"`
	Target string `toml:"target"`
}

// enabled reads a tri-state boolean: unset means on.
func enabled(p *bool) bool { return p == nil || *p }

// durationOr parses a spelled duration, falling back to a default.
func durationOr(spelled string, fallback time.Duration) time.Duration {
	if trimmed := strings.TrimSpace(spelled); trimmed != "" {
		if parsed, err := time.ParseDuration(trimmed); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

// Path returns the XDG config-file path for kith (creating parent dirs).
func Path() (string, error) {
	p, err := xdg.ConfigFile("kith/config.toml")
	if err != nil {
		return "", fmt.Errorf("config: resolve path: %w", err)
	}
	return p, nil
}

// WriteDefaultIfMissing writes the annotated default config to path if no file exists;
// created reports whether it wrote one.
func WriteDefaultIfMissing(path string) (created bool, err error) {
	return writeIfMissing(path, defaultConfigTOML)
}

// writeIfMissing writes body to path if no file exists.
func writeIfMissing(path, body string) (created bool, err error) {
	if _, statErr := os.Stat(path); statErr == nil {
		return false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("config: stat %s: %w", path, statErr)
	}

	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return false, fmt.Errorf("config: create %s: %w", dir, mkErr)
	}
	if err := writeAtomic(path, body); err != nil {
		return false, err
	}
	return true, nil
}

// Annotated is the fully-documented default config — every option with its prose.
func Annotated() string { return defaultConfigTOML }

// Load reads the TOML config at path, refusing unknown keys so a misspelled setting
// is not silently ignored.
func Load(path string) (Config, error) {
	body, err := os.ReadFile(path) //nolint:gosec // G304: the config file the user named
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return decode(path, string(body))
}

// starter is the default config's tags and rail order, freshly decoded each call.
func starter() Config {
	var cfg Config
	if _, err := toml.Decode(defaultConfigTOML, &cfg); err != nil {
		panic("config: the starter config does not decode: " + err.Error()) // a build that cannot happen
	}
	return Config{Tags: cfg.Tags, Display: Display{Rail: Rail{Order: cfg.Display.Rail.Order}}}
}

// Decode parses config text as Load does; the daemon uses it to check a client's config
// before writing it.
func Decode(text string) (Config, error) { return decode("the sent config", text) }

// decode is Load and Decode's reading; source names the text in errors.
func decode(source, text string) (Config, error) {
	cfg, meta, err := decodeLoose(source, text)
	if err != nil {
		return Config{}, err
	}
	if err := unknownKeys(source, meta.Undecoded()); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// decodeLoose is decode leaving the keys Config does not have to the caller.
func decodeLoose(source, text string) (Config, toml.MetaData, error) {
	var cfg Config
	meta, err := toml.Decode(text, &cfg)
	if err != nil {
		return Config{}, meta, fmt.Errorf("config: decode %s: %w", source, err)
	}
	// Absent tags or rail order take the starter's; an explicit `[]` means none.
	switch {
	case !meta.IsDefined("tag"):
		cfg.Tags = starter().Tags
	case len(cfg.Tags) == 0:
		cfg.Tags = nil
	}
	switch {
	case !meta.IsDefined("display", "rail", "order"):
		cfg.Display.Rail.Order = starter().Display.Rail.Order
	case len(cfg.Display.Rail.Order) == 0:
		cfg.Display.Rail.Order = nil
	}
	return cfg, meta, nil
}

// unknownKeys refuses the keys a file holds that its type does not read, so a
// misspelled setting is not silently ignored.
func unknownKeys(source string, undecoded []toml.Key) error {
	if len(undecoded) == 0 {
		return nil
	}
	keys := make([]string, 0, len(undecoded))
	for _, key := range undecoded {
		keys = append(keys, key.String())
	}
	return fmt.Errorf("config: %s: unknown keys: %s", source, strings.Join(keys, ", "))
}
