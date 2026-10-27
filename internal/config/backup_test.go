package config

import (
	"testing"
	"time"
)

// An interval is whole days ("7d"), else a Go duration; zero or negative is none.
func TestParseEvery(t *testing.T) {
	t.Parallel()
	for spelled, want := range map[string]time.Duration{
		"7d": 7 * 24 * time.Hour, "  1d\n": 24 * time.Hour, "6h": 6 * time.Hour,
	} {
		if got, err := ParseEvery(spelled); err != nil || got != want {
			t.Errorf("ParseEvery(%q) = %v, %v; want %v", spelled, got, err, want)
		}
	}
	for _, spelled := range []string{"d", "1.5d", "0d", "-2d", "0h", "-1h"} {
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
