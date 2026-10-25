package daemon

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A daemon started without systemd logs to a file in the state directory, named on its
// command line, with --config when the config is not the default.
func TestDaemonLogPathAndDetachArgs(t *testing.T) {
	t.Parallel()
	storage := domain.Storage{StateDir: "/state", Instance: "x"}
	if got, want := DaemonLogPath(storage), "/state/kithd.log"; got != want {
		t.Errorf("DaemonLogPath = %q, want %q", got, want)
	}
	if got, want := detachArgs(storage, Launch{}), []string{LogFlag, DaemonLogPath(storage)}; !slices.Equal(got, want) {
		t.Errorf("detachArgs(default) = %q, want %q", got, want)
	}
	own := Launch{ConfigPath: "/test/config.toml", OwnConfig: true}
	if got, want := detachArgs(storage, own), []string{LogFlag, DaemonLogPath(storage), "--config", "/test/config.toml"}; !slices.Equal(got, want) {
		t.Errorf("detachArgs(own config) = %q, want %q", got, want)
	}
}
