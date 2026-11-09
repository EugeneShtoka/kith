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

// An unset [backup] is on, keeping 7; set values are read as written.
func TestBackupDefaults(t *testing.T) {
	t.Parallel()
	var unset Backup
	if !unset.On() || unset.KeepOrDefault() != DefaultBackupKeep {
		t.Errorf("unset [backup]: on %v, keep %d; want on, %d", unset.On(), unset.KeepOrDefault(), DefaultBackupKeep)
	}
	off, three := false, 3
	set := Backup{Enabled: &off, Keep: &three}
	if set.On() || set.KeepOrDefault() != 3 {
		t.Errorf("set [backup]: on %v, keep %d; want off, 3", set.On(), set.KeepOrDefault())
	}
}

// The starter config's [backup] decodes to the defaults: on, every 24h, no cron.
func TestTheStarterBackupIsTheDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Decode(Annotated())
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Backup
	every, err := ParseEvery(b.Every)
	if !b.On() || b.Key != "" || b.Dir != "" || b.Cron != "" || err != nil || every != DefaultBackupEvery || b.KeepOrDefault() != DefaultBackupKeep {
		t.Errorf("the starter [backup] = %+v; want the defaults", b)
	}
}

// [backup.media] is a schedule of its own: on, weekly, keeping 3 when unset, with
// [backup]'s key and dir unless it names its own; [backup] enabled off leaves it on.
func TestMediaBackupsHaveTheirOwnSchedule(t *testing.T) {
	t.Parallel()
	off, five := false, 5
	b := Backup{Enabled: &off, Key: "age1main", Dir: "/backups", Every: "6h", Keep: &five}
	got := b.MediaSchedule()
	if !got.On() || got.Every != DefaultMediaBackupEvery || got.Cron != "" || got.KeepOrDefault() != DefaultMediaBackupKeep ||
		got.Key != "age1main" || got.Dir != "/backups" {
		t.Errorf("unset [backup.media] = %+v; want on, %s, keep %d, [backup]'s key and dir", got, DefaultMediaBackupEvery, DefaultMediaBackupKeep)
	}
	b.Media = MediaBackup{Enabled: &off, Key: "age1media", Dir: "/media", Cron: "0 3 * * 0", Keep: &five}
	got = b.MediaSchedule()
	if got.On() || got.Every != "" || got.Cron != "0 3 * * 0" || got.KeepOrDefault() != 5 || got.Key != "age1media" || got.Dir != "/media" {
		t.Errorf("set [backup.media] = %+v; want it as written", got)
	}
}

// The starter config's [backup.media] decodes to the defaults.
func TestTheStarterMediaBackupIsTheDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Decode(Annotated())
	if err != nil {
		t.Fatal(err)
	}
	got, want := cfg.Backup.MediaSchedule(), (Backup{}).MediaSchedule()
	if got.On() != want.On() || got.Every != want.Every || got.Cron != want.Cron || got.KeepOrDefault() != want.KeepOrDefault() {
		t.Errorf("starter [backup.media] = %+v; want %+v", got, want)
	}
}
