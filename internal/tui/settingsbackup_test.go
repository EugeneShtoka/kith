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

	m, path := opened(t, config.Notifications{Enabled: true})
	m = toSetting(t, m, "backup.key")
	m = typeIn(t, m, private)
	if status := m.status(); !strings.Contains(status, "this is the private key") || strings.Contains(status, secret) {
		t.Errorf("status = %q; want the private key refused, without it", status)
	}
	if body, err := os.ReadFile(path); err != nil || strings.Contains(string(body), secret) {
		t.Errorf("the config file holds the private key (read: %v)", err)
	}
	if detail := rowFor(t, m, "Backup · key").detail; strings.Contains(detail, secret) {
		t.Errorf("the key row shows %q", detail)
	}
}
