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
// <export_dir>/backups. Every is a duration ("6h", "7d") and Cron a five-field cron
// expression ("0 3 * * *"), one or the other; neither is every 24h. Keep nil is 7.
type Backup struct {
	Enabled *bool  `toml:"enabled"`
	Key     string `toml:"key"`
	Dir     string `toml:"dir"`
	Every   string `toml:"every"`
	Cron    string `toml:"cron"`
	Keep    *int   `toml:"keep"`
}

// The scheduled backups' defaults.
const (
	DefaultBackupEvery = 24 * time.Hour
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
