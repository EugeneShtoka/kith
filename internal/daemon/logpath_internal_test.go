package daemon

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/adrg/xdg"
)

// A daemon started without systemd logs to a file under the state directory, one per
// profile, and is told so on its command line.
func TestDaemonLogPathAndDetachArgs(t *testing.T) {
	t.Parallel()
	base := filepath.Join(xdg.StateHome, "kith")
	if got, want := DaemonLogPath(""), filepath.Join(base, "kithd.log"); got != want {
		t.Errorf("DaemonLogPath(\"\") = %q, want %q", got, want)
	}
	if got, want := DaemonLogPath("work"), filepath.Join(base, "kithd-work.log"); got != want {
		t.Errorf("DaemonLogPath(work) = %q, want %q", got, want)
	}
	if got, want := detachArgs(""), []string{LogFlag, DaemonLogPath("")}; !slices.Equal(got, want) {
		t.Errorf("detachArgs(\"\") = %q, want %q", got, want)
	}
	if got, want := detachArgs("work"), []string{LogFlag, DaemonLogPath("work"), "--profile", "work"}; !slices.Equal(got, want) {
		t.Errorf("detachArgs(work) = %q, want %q", got, want)
	}
}
