package daemon

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A daemon started without systemd logs to a file in the instance's state directory,
// one per profile, and is told so on its command line — with the config it serves,
// when that is not the default one.
func TestDaemonLogPathAndDetachArgs(t *testing.T) {
	t.Parallel()
	storage := domain.Storage{StateDir: "/state", Instance: "x"}
	if got, want := DaemonLogPath(storage, ""), "/state/kithd.log"; got != want {
		t.Errorf("DaemonLogPath(\"\") = %q, want %q", got, want)
	}
	if got, want := DaemonLogPath(storage, "work"), "/state/kithd-work.log"; got != want {
		t.Errorf("DaemonLogPath(work) = %q, want %q", got, want)
	}
	if got, want := detachArgs(storage, Launch{}), []string{LogFlag, DaemonLogPath(storage, "")}; !slices.Equal(got, want) {
		t.Errorf("detachArgs(default) = %q, want %q", got, want)
	}
	if got, want := detachArgs(storage, Launch{Profile: "work"}), []string{LogFlag, DaemonLogPath(storage, "work"), "--profile", "work"}; !slices.Equal(got, want) {
		t.Errorf("detachArgs(work) = %q, want %q", got, want)
	}
	own := Launch{ConfigPath: "/test/config.toml", OwnConfig: true}
	if got, want := detachArgs(storage, own), []string{LogFlag, DaemonLogPath(storage, ""), "--config", "/test/config.toml"}; !slices.Equal(got, want) {
		t.Errorf("detachArgs(own config) = %q, want %q", got, want)
	}
}
