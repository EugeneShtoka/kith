package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// A private key typed into [backup] key in Settings is refused, said without itself,
// and reaches neither the config file nor the row. (That a public key is taken is
// setup's test: this package may not make one.)
func TestSettingsRefuseAPrivateBackupKey(t *testing.T) {
	t.Parallel()
	// Built here, so no private-looking key is ever a literal in the repository.
	secret := strings.Repeat("Q", 58)
	private := "AGE-SECRET-KEY-1" + secret

	m, path := opened(t, config.Notifications{})
	m = toSetting(t, m, "backup.key")
	m = typeIn(t, m, private)
	if status := m.status(); status == "" || strings.Contains(status, secret) {
		t.Errorf("status = %q; want the refusal told, without the key", status)
	}
	if body, err := os.ReadFile(path); err != nil || strings.Contains(string(body), secret) {
		t.Errorf("the config file holds the private key (read: %v)", err)
	}
	if detail := rowFor(t, m, "Backup · key").detail; strings.Contains(detail, secret) {
		t.Errorf("the key row shows %q", detail)
	}
}

// The schedule row writes a five-field cron expression to [backup] cron and anything
// else to every, clearing the other, so only one is ever set; empty clears both.
func TestTheBackupScheduleRowSetsEveryOrCron(t *testing.T) {
	t.Parallel()
	for typed, want := range map[string]config.Backup{
		"0 3 * * *":                      {Cron: "0 3 * * *"},
		" 6h ":                           {Every: "6h"},
		"7d":                             {Every: "7d"},
		"":                               {},
		"30 2 * * 1 ":                    {Cron: "30 2 * * 1"},
		"@daily":                         {Cron: "@daily"},
		"CRON_TZ=Europe/Sofia 0 3 * * *": {Cron: "CRON_TZ=Europe/Sofia 0 3 * * *"},
	} {
		c := config.Settings{Bridge: config.Config{Backup: config.Backup{Every: "12h", Cron: "0 1 * * *"}}}
		if err := setBackupSchedule(&c, typed); err != nil {
			t.Fatalf("setBackupSchedule(%q) = %v", typed, err)
		}
		if c.Bridge.Backup.Every != want.Every || c.Bridge.Backup.Cron != want.Cron {
			t.Errorf("setBackupSchedule(%q): every %q, cron %q; want every %q, cron %q",
				typed, c.Bridge.Backup.Every, c.Bridge.Backup.Cron, want.Every, want.Cron)
		}
	}
}
