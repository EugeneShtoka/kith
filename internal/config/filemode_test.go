package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenToOthers(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("no mode bits on Windows")
	}
	for _, tc := range []struct {
		mode os.FileMode
		want string
	}{
		{0o600, ""},
		{0o640, "read"},
		{0o644, "read"},
		{0o620, "write"},
		{0o666, "read and write"},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		got, err := OpenToOthers(path)
		if err != nil || got != tc.want {
			t.Errorf("mode %o: OpenToOthers = %q, %v; want %q", tc.mode, got, err, tc.want)
		}
		if warning := ModeWarning(path); (warning != "") != (tc.want != "") || !strings.Contains(warning, tc.want) {
			t.Errorf("mode %o: ModeWarning = %q", tc.mode, warning)
		}
	}
}
