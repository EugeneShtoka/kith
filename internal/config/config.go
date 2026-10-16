// Package config loads kith's XDG TOML configuration. It imports no other internal
// package. default.toml documents every setting for users; Go comments here give only
// type, unit and zero-value meaning.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/adrg/xdg"
)

// defaultConfigTOML is the fully-annotated starter config written on first run.
//
//go:embed default.toml
var defaultConfigTOML string

// profileName is the characters a systemd instance name and a shell argument both take
// unquoted.
var profileName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Config is kith's user configuration, one field per top-level key or table.
type Config struct {
	Homeserver     string `toml:"homeserver"`
	User           string `toml:"user"`
	AllowTokenFile bool   `toml:"allow_token_file"`
	// BridgeContacts is each bridge's provisioning API base URL whose contact lists
	// name people a network shows only by number (domain.Directory).
	BridgeContacts []string      `toml:"bridge_contacts"`
	Terminal       string        `toml:"terminal"` // what `kith --open` starts; empty detects one
	Display        Display       `toml:"display"`
	Notifications  Notifications `toml:"notifications"`
	Keys           Keys          `toml:"keys"`
	Clipboard      Clipboard     `toml:"clipboard"`
	Codes          Codes         `toml:"codes"`
	Composer       Composer      `toml:"composer"`
	Spell          Spell         `toml:"spell"`
	Complete       Complete      `toml:"complete"`
	Assist         Assist        `toml:"assist"`
	Agent          Agent         `toml:"agent"`
	Commands       Commands      `toml:"commands"`
	Schedule       Schedule      `toml:"schedule"`
	Phonebook      Phonebook     `toml:"phonebook"`
	Log            Log           `toml:"log"`
	WhatsApp       WhatsApp      `toml:"whatsapp"`
	Slack          Slack         `toml:"slack"`
	Telegram       Telegram      `toml:"telegram"`
	Messenger      Messenger     `toml:"messenger"`
	LinkedIn       LinkedIn      `toml:"linkedin"`
	GMessages      GMessages     `toml:"gmessages"`
	Storage        Storage       `toml:"storage"`
	Profiles       []Profile     `toml:"profile"`
	Tags           []Tag         `toml:"tag"`
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

// Profile is one [[profile]]: an account this config can run as.
type Profile struct {
	Name       string `toml:"name"` // what --profile takes
	Homeserver string `toml:"homeserver"`
	User       string `toml:"user"`
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
	if _, statErr := os.Stat(path); statErr == nil {
		return false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("config: stat %s: %w", path, statErr)
	}

	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return false, fmt.Errorf("config: create %s: %w", dir, mkErr)
	}
	if err := writeAtomic(path, defaultConfigTOML); err != nil {
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
	return Config{
		Tags: cfg.Tags, Display: Display{Rail: Rail{Order: cfg.Display.Rail.Order}},
		Keys: Keys{Tag: cfg.Keys.Tag},
	}
}

// Decode parses config text as Load does; the daemon uses it to check a client's config
// before writing it.
func Decode(text string) (Config, error) { return decode("the sent config", text) }

// decode is Load and Decode's reading; source names the text in errors.
func decode(source, text string) (Config, error) {
	var cfg Config
	meta, err := toml.Decode(text, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("config: decode %s: %w", source, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		return Config{}, fmt.Errorf("config: %s: unknown keys: %s", source, strings.Join(keys, ", "))
	}
	// Unmentioned fields decode as zero; give every action its default key.
	cfg.Keys.FillDefaults()
	// Absent tags or rail order take the starter's; an explicit `[]` means none.
	switch {
	case !meta.IsDefined("tag"):
		cfg.Tags = starter().Tags
	case len(cfg.Tags) == 0:
		cfg.Tags = nil
	}
	// Absent tag keys take the starter's too; `[keys] tag = []` means none.
	if !meta.IsDefined("keys", "tag") {
		cfg.Keys.Tag = DefaultKeys().Tag
	}
	switch {
	case !meta.IsDefined("display", "rail", "order"):
		cfg.Display.Rail.Order = starter().Display.Rail.Order
	case len(cfg.Display.Rail.Order) == 0:
		cfg.Display.Rail.Order = nil
	}
	if err := cfg.RequireAccount(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// HasMatrix reports whether the (profile-resolved) config names a Matrix account.
func (c Config) HasMatrix() bool { return c.Homeserver != "" && c.User != "" }

// RequireAccount refuses partly named accounts: a [[profile]] missing name, homeserver
// or user, or a homeserver without a user (or the reverse). No account at all is valid.
func (c Config) RequireAccount() error {
	// Profiles supply the account; Profile picks one and refuses a duplicate top-level one.
	if len(c.Profiles) > 0 {
		for _, p := range c.Profiles {
			if p.Name == "" {
				return errors.New("config: every [[profile]] needs a name")
			}
			// The name becomes a systemd instance name (kithd@work) and an argv element.
			if !profileName.MatchString(p.Name) {
				return fmt.Errorf("config: profile name %q may only contain letters, digits, dot, dash and underscore", p.Name)
			}
			if p.Homeserver == "" || p.User == "" {
				return fmt.Errorf("config: profile %q needs a homeserver and a user", p.Name)
			}
		}
		return nil
	}
	if (c.Homeserver == "") != (c.User == "") {
		return errors.New("config: Matrix needs both homeserver and user (or neither, to use only [whatsapp] or [slack])")
	}
	return nil
}

// Profile resolves which account to run as.
func (c Config) Profile(name string) (Config, error) {
	name = strings.TrimSpace(name)
	if len(c.Profiles) == 0 {
		if name != "" {
			return Config{}, fmt.Errorf("config: no profiles are configured, so there is no %q — add a [[profile]] block or drop --profile", name)
		}
		return c, nil
	}
	if c.User != "" || c.Homeserver != "" {
		return Config{}, errors.New("config: the account is set both at the top level and in [[profile]] blocks — keep it in one place")
	}
	chosen := c.Profiles[0]
	if name != "" {
		found := false
		for _, p := range c.Profiles {
			if strings.EqualFold(p.Name, name) {
				chosen, found = p, true
				break
			}
		}
		if !found {
			return Config{}, fmt.Errorf("config: no profile called %q (have %s)", name, strings.Join(c.ProfileNames(), ", "))
		}
	}
	c.Homeserver, c.User = chosen.Homeserver, chosen.User
	return c, nil
}

// ProfileNames lists the profiles in file order; the first is the default.
func (c Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for _, p := range c.Profiles {
		names = append(names, p.Name)
	}
	return names
}
