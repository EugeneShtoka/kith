package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ownStorage is a config's storage with every directory of its own.
func ownStorage() domain.Storage {
	return domain.Storage{
		Instance: "sandbox-work", DataDir: "/home/u/sb/data", StateDir: "/home/u/sb/state",
		CacheDir: "/home/u/sb/cache", RuntimeDir: "/run/user/1000/sb",
	}
}

// directives is a unit's directives in order, section headers kept, comments and
// blank lines dropped, minus the keys in except.
func directives(unit string, except ...string) []string {
	var out []string
	for line := range strings.Lines(unit) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		if slices.Contains(except, key) {
			continue
		}
		out = append(out, line)
	}
	return out
}

// values is every value of key in unit, split into words.
func values(unit, key string) []string {
	var out []string
	for line := range strings.Lines(unit) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			out = append(out, strings.Fields(v)...)
		}
	}
	return out
}

// A config's own unit runs under the same sandbox as the packaged one: only what
// names the config and its directories differs.
func TestOwnUnitMatchesThePackagedSandbox(t *testing.T) {
	t.Parallel()
	packaged, err := os.ReadFile("../../packaging/systemd/kithd.service")
	if err != nil {
		t.Fatal(err)
	}
	own, err := ownUnit(ownStorage(), "/usr/bin/kithd", Launch{ConfigPath: "/home/u/sb/config.toml", OwnConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	// RuntimeDirectory= reaches only below %t; the own unit creates its runtime
	// directory with the others instead.
	differ := []string{"Description", "ExecStart", "ExecStartPre", "ReadWritePaths", "RuntimeDirectory", "RuntimeDirectoryPreserve"}
	if got, want := directives(own, differ...), directives(string(packaged), differ...); !slices.Equal(got, want) {
		t.Errorf("own unit's directives differ from kithd.service's:\n got %q\nwant %q", got, want)
	}
}

// The unit starts kithd with its config and profile, lets it write exactly the
// config's directories (none of the default ones), and creates each before the
// sandbox is built.
func TestOwnUnitServesItsConfig(t *testing.T) {
	t.Parallel()
	storage := ownStorage()
	unit, err := ownUnit(storage, "/opt/kith/kithd", Launch{ConfigPath: "/home/u/sb/config.toml", Profile: "work", OwnConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := values(unit, "ExecStart"), []string{"/opt/kith/kithd", "--config", "/home/u/sb/config.toml", "--profile", "work"}; !slices.Equal(got, want) {
		t.Errorf("ExecStart = %q, want %q", got, want)
	}
	dirs := []string{storage.DataDir, storage.StateDir, storage.CacheDir, storage.RuntimeDir}
	if got := values(unit, "ReadWritePaths"); !slices.Equal(got, dirs) {
		t.Errorf("ReadWritePaths = %q, want exactly %q", got, dirs)
	}
	if got, want := values(unit, "ExecStartPre"), append([]string{"+mkdir", "-p"}, dirs...); !slices.Equal(got, want) {
		t.Errorf("ExecStartPre = %q, want %q", got, want)
	}
	if strings.Contains(unit, "%h") || strings.Contains(unit, "%t") {
		t.Errorf("own unit names a default directory:\n%s", unit)
	}

	plain, err := ownUnit(storage, "/opt/kith/kithd", Launch{ConfigPath: "/c.toml", OwnConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := values(plain, "ExecStart"); slices.Contains(got, "--profile") {
		t.Errorf("ExecStart without a profile = %q, want no --profile", got)
	}
}

// A path that would split, unescape or expand differently in some directive, or
// that PrivateTmp= would hide, is refused, never written; "%" is doubled so systemd
// reads it literally.
func TestUnitPath(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"rel/dir", "", "/a b", "/a\tb", "/a\nb", `/a"b`, "/a'b", `/a\b`, "/$HOME", "/a\x7fb", "/tmp", "/tmp/kith", "/var/tmp/kith", "/tmp/../tmp/x"} {
		if got, err := unitPath(bad); !errors.Is(err, errUnsafeUnitPath) {
			t.Errorf("unitPath(%q) = %q, %v; want errUnsafeUnitPath", bad, got, err)
		}
	}
	if got, err := unitPath("/home/u/tmp/kith"); err != nil || got != "/home/u/tmp/kith" {
		t.Errorf("unitPath(a tmp below home) = %q, %v; want it kept", got, err)
	}
	if got, err := unitPath("/home/u/100%/kith"); err != nil || got != "/home/u/100%%/kith" {
		t.Errorf("unitPath(100%%) = %q, %v; want the %% doubled", got, err)
	}
	storage := ownStorage()
	storage.CacheDir = "/home/u/my cache"
	if _, err := ownUnit(storage, "/usr/bin/kithd", Launch{ConfigPath: "/c.toml", OwnConfig: true}); !errors.Is(err, errUnsafeUnitPath) {
		t.Errorf("ownUnit with a spaced directory = %v, want errUnsafeUnitPath", err)
	}
}

// The default config is served by the packaged units; any other by its instance's own.
func TestLaunchUnitName(t *testing.T) {
	t.Parallel()
	storage := ownStorage()
	for _, tc := range []struct {
		launch Launch
		want   string
	}{
		{Launch{}, "kithd.service"},
		{Launch{Profile: "work"}, "kithd@work.service"},
		{Launch{ConfigPath: "/c.toml", OwnConfig: true}, "kithd-sandbox-work.service"},
		{Launch{ConfigPath: "/c.toml", Profile: "work", OwnConfig: true}, "kithd-sandbox-work.service"},
	} {
		if got := tc.launch.unitName(storage); got != tc.want {
			t.Errorf("%+v.unitName() = %q, want %q", tc.launch, got, tc.want)
		}
	}
}

// The unit is rewritten only when it changed (each rewrite costs a daemon-reload),
// replaced whole, and readable by systemd.
func TestWriteUnit(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "systemd", "user")
	steps := []struct {
		content string
		changed bool
	}{{"a\n", true}, {"a\n", false}, {"b\n", true}}
	for i, s := range steps {
		changed, err := writeUnit(dir, "kithd-x.service", s.content)
		if err != nil || changed != s.changed {
			t.Fatalf("step %d: writeUnit(%q) = %v, %v; want changed=%v", i, s.content, changed, err, s.changed)
		}
	}
	path := filepath.Join(dir, "kithd-x.service")
	if got, err := os.ReadFile(path); err != nil || string(got) != "b\n" {
		t.Errorf("unit = %q, %v; want the last content", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("unit mode = %v, %v; want 0644", info.Mode().Perm(), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("unit dir holds %d entries, want only the unit (no temporary left)", len(entries))
	}
}
