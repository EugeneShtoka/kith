package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A setting tags replaced is refused with what to write instead, not only as unknown.
func TestRemovedKeysSayWhatReplacedThem(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]string{
		"[display]\narchived = [\"!a:x\"]\n":    "[[tag]] named Archived",
		"[display]\npinned = [\"!a:x\"]\n":      "tag:Pinned",
		"[display]\nspace_priority = [\"W\"]\n": "[display] priority",
		"[display]\nbase_spaces = [\"W\"]\n":    "space_exclusive",
		"[keys.rooms]\narchive = \"A\"\n":       "/tag <name>",
		"[keys.rooms]\npin = \"P\"\n":           "/tag <name>",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		full := "homeserver = \"https://x\"\nuser = \"@me:x\"\n" + body
		if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "is gone") || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want it gone, pointing at %s", body, err, want)
		}
	}
}
