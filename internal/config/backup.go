package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Backup is [backup]: kithd's scheduled backups. Enabled nil is on. Key is an age
// public key (age1…); empty is the one the first `kith backup` kept. Dir empty is
// <export_dir>/backups. Every is a duration ("6h", "7d"); empty is 24h. Keep nil is 7.
type Backup struct {
	Enabled *bool  `toml:"enabled"`
	Key     string `toml:"key"`
	Dir     string `toml:"dir"`
	Every   string `toml:"every"`
	Keep    *int   `toml:"keep"`
}

// The scheduled backups' defaults, and the shortest interval allowed: each one copies
// the whole cache.
const (
	DefaultBackupEvery = 24 * time.Hour
	MinBackupEvery     = time.Hour
	DefaultBackupKeep  = 7
)

// On reports whether kithd makes scheduled backups (unset: true).
func (b Backup) On() bool { return enabled(b.Enabled) }

// KeepOrDefault is how many scheduled backups to keep.
func (b Backup) KeepOrDefault() int {
	if b.Keep == nil {
		return DefaultBackupKeep
	}
	return *b.Keep
}

// EveryOrDefault is how long after one scheduled backup the next is due; an unset or
// unreadable value is the default (setup refuses an unreadable one at startup).
func (b Backup) EveryOrDefault() time.Duration {
	if strings.TrimSpace(b.Every) == "" {
		return DefaultBackupEvery
	}
	every, err := ParseEvery(b.Every)
	if err != nil {
		return DefaultBackupEvery
	}
	return every
}

// errNotADuration is an interval that is neither a duration nor a number of days.
var errNotADuration = errors.New("not a duration")

// ParseEvery reads an interval: a duration as Go writes one ("90m", "6h") or whole
// days ("7d").
func ParseEvery(spelled string) (time.Duration, error) {
	s := strings.TrimSpace(spelled)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%q: %w", spelled, errNotADuration)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	every, err := time.ParseDuration(s)
	if err != nil || every <= 0 {
		return 0, fmt.Errorf("%q: %w", spelled, errNotADuration)
	}
	return every, nil
}
