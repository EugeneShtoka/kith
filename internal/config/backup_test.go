package config

import (
	"testing"
	"time"
)

// An interval is a Go duration or whole days; anything else, zero or negative is not.
func TestParseEvery(t *testing.T) {
	t.Parallel()
	for spelled, want := range map[string]time.Duration{
		"6h": 6 * time.Hour, "90m": 90 * time.Minute, "24h": 24 * time.Hour,
		"7d": 7 * 24 * time.Hour, "1d": 24 * time.Hour,
	} {
		if got, err := ParseEvery(spelled); err != nil || got != want {
			t.Errorf("ParseEvery(%q) = %v, %v; want %v", spelled, got, err, want)
		}
	}
	if got, err := ParseEvery("  7d\n"); err != nil || got != 7*24*time.Hour {
		t.Errorf("ParseEvery with spaces around = %v, %v; want 168h", got, err)
	}
	for _, spelled := range []string{"", "d", "1.5d", "0d", "-2d", "0h", "-1h", "weekly", "7 days"} {
		if got, err := ParseEvery(spelled); err == nil {
			t.Errorf("ParseEvery(%q) = %v; want an error", spelled, got)
		}
	}
}

// An unset [backup] is on, every 24h, keeping 7; set values are read as written.
func TestBackupDefaults(t *testing.T) {
	t.Parallel()
	var unset Backup
	if !unset.On() || unset.EveryOrDefault() != DefaultBackupEvery || unset.KeepOrDefault() != DefaultBackupKeep {
		t.Errorf("unset [backup]: on %v, every %v, keep %d; want on, %v, %d",
			unset.On(), unset.EveryOrDefault(), unset.KeepOrDefault(), DefaultBackupEvery, DefaultBackupKeep)
	}
	off, three := false, 3
	set := Backup{Enabled: &off, Every: "7d", Keep: &three}
	if set.On() || set.EveryOrDefault() != 7*24*time.Hour || set.KeepOrDefault() != 3 {
		t.Errorf("set [backup]: on %v, every %v, keep %d; want off, 168h, 3", set.On(), set.EveryOrDefault(), set.KeepOrDefault())
	}
}

// The starter config's [backup] decodes to the defaults.
func TestTheStarterBackupIsTheDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Decode(Annotated())
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Backup
	if !b.On() || b.Key != "" || b.Dir != "" || b.EveryOrDefault() != DefaultBackupEvery || b.KeepOrDefault() != DefaultBackupKeep {
		t.Errorf("the starter [backup] = %+v; want the defaults", b)
	}
}
