package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The instance goes into the file without disturbing anything else in it, and one
// already there (another kith got there first) wins.
func TestSetInstanceKeepsTheFile(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ in, want string }{
		"no section":    {"# mine\nuser = \"@a:x\"\n", "# mine\nuser = \"@a:x\"\n\n[storage]\ninstance = \"abc\"\n"},
		"empty line":    {"[storage]\ninstance = \"\" # set by kith\ndata_dir = \"/d\"\n", "[storage]\ninstance = \"abc\"\ndata_dir = \"/d\"\n"},
		"section, none": {"[storage]\ndata_dir = \"/d\"\n[log]\nlevel = \"info\"\n", "[storage]\ninstance = \"abc\"\ndata_dir = \"/d\"\n[log]\nlevel = \"info\"\n"},
		"elsewhere":     {"[other]\ninstance = \"\"\n", "[other]\ninstance = \"\"\n\n[storage]\ninstance = \"abc\"\n"},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(c.in), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := SetInstance(path, "abc")
		body, _ := os.ReadFile(path)
		if err != nil || got != "abc" || strings.TrimRight(string(body), "\n") != strings.TrimRight(c.want, "\n") {
			t.Errorf("%s: (%q, %v), file:\n%s\nwant:\n%s", name, got, err, body, c.want)
		}
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(path, []byte("[storage]\ninstance = \"first\"\n"), 0o600)
	if got, err := SetInstance(path, "second"); err != nil || got != "first" {
		t.Errorf("an instance already there = (%q, %v), want it kept", got, err)
	}
}
