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
	// Media is [backup.media]: the media archive's backups, apart from these.
	Media MediaBackup `toml:"media"`
}

// MediaBackup is [backup.media]: kithd's scheduled backups of the media archive, in
// files of their own. Its keys mean what [backup]'s do; an empty key or dir is
// [backup]'s, and every, cron and keep default to a week and 3.
type MediaBackup struct {
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
	// The media archive's: it only grows, so less often and fewer.
	DefaultMediaBackupEvery = "7d"
	DefaultMediaBackupKeep  = 3
)

// MediaSchedule is [backup.media] as a [backup] of its own, its defaults filled in and
// an empty key or dir taken from b, so one check and one schedule serve both.
func (b Backup) MediaSchedule() Backup {
	m := b.Media
	out := Backup{Enabled: m.Enabled, Key: m.Key, Dir: m.Dir, Every: m.Every, Cron: m.Cron, Keep: m.Keep}
	if out.Key == "" {
		out.Key = b.Key
	}
	if out.Dir == "" {
		out.Dir = b.Dir
	}
	if out.Every == "" && out.Cron == "" {
		out.Every = DefaultMediaBackupEvery
	}
	if out.Keep == nil {
		keep := DefaultMediaBackupKeep
		out.Keep = &keep
	}
	return out
}

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
