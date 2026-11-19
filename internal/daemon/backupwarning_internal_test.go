package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// Status warns about a backups folder under /tmp or /var/tmp, never another; dismissed,
// that folder is not warned about again, by this kithd or the next one reading the
// same record, while another folder under /tmp is. Each change of the warning tells
// the clients once.
func TestStatusWarnsAboutABackupsFolderUnderTmpUntilDismissed(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "backup-warnings.toml")
	told := 0
	start := func() *server {
		w := NewBackupWarning(path, func() { told++ }, logging.Discard())
		return &server{Daemon: &Daemon{State: NewState(), BackupWarning: w}}
	}
	warned := func(s *server) string {
		t.Helper()
		got, err := s.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		w := got.Msg.GetBackupWarning()
		if w.GetDir() != "" && !strings.Contains(w.GetWhy(), w.GetDir()) {
			t.Errorf("the warning %q does not name its folder %q", w.GetWhy(), w.GetDir())
		}
		return w.GetDir()
	}
	dismiss := func(s *server, dir string) {
		t.Helper()
		if _, err := s.DismissBackupWarning(t.Context(), connect.NewRequest(&v1.DismissBackupWarningRequest{Dir: dir})); err != nil {
			t.Fatalf("DismissBackupWarning(%q): %v", dir, err)
		}
	}

	s := start()
	for _, c := range []struct {
		dirs []string
		want string
		told int
	}{
		{dirs: []string{"/home/u/backups"}, want: "", told: 0},
		{dirs: []string{"/tmp/kith"}, want: "/tmp/kith", told: 1},
		{dirs: []string{"/home/u/backups", "/var/tmp/kith/"}, want: "/var/tmp/kith", told: 2},
		{dirs: []string{"/home/u/backups", "/home/u/media"}, want: "", told: 3},
		{dirs: []string{"/tmp/kith", "/tmp/kith"}, want: "/tmp/kith", told: 4},
	} {
		s.BackupWarning.Use(c.dirs)
		if got := warned(s); got != c.want || told != c.told {
			t.Errorf("backups in %q: warned about %q, clients told %d times; want %q, %d", c.dirs, got, told, c.want, c.told)
		}
	}

	dismiss(s, "/tmp/elsewhere") // not a folder warned about: nothing to dismiss
	if got := warned(s); got != "/tmp/kith" || told != 4 {
		t.Errorf("after dismissing a folder not warned about: warned about %q, told %d; want /tmp/kith, 4", got, told)
	}
	dismiss(s, "/tmp/kith")
	if got := warned(s); got != "" || told != 5 {
		t.Errorf("after dismissing /tmp/kith: warned about %q, told %d; want nothing, 5", got, told)
	}

	// The next kithd reads the record.
	s = start()
	s.BackupWarning.Use([]string{"/tmp/kith"})
	if got := warned(s); got != "" {
		t.Errorf("a restart warns about the dismissed /tmp/kith again (%q)", got)
	}
	// /tmp/elsewhere was dismissed before the backups went there: nothing was kept.
	s.BackupWarning.Use([]string{"/tmp/kith", "/tmp/elsewhere"})
	if got := warned(s); got != "/tmp/elsewhere" {
		t.Errorf("another folder under /tmp: warned about %q; want /tmp/elsewhere", got)
	}
}
