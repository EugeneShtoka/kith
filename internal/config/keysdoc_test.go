package config

import (
	"os"
	"strings"
	"testing"
)

// default_device.toml's [keys] part matches keySections; `make keys-doc` regenerates it.
func TestDefaultTOMLKeysSectionIsGenerated(t *testing.T) {
	t.Parallel()

	before, _, ok := strings.Cut(defaultDeviceTOML, keysTOMLStart)
	if !ok {
		t.Fatalf("default_device.toml has no %q line to start the generated [keys] part at", keysTOMLStart)
	}
	want := before + renderKeysTOML()
	if *updateGolden {
		if err := os.WriteFile("default_device.toml", []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if defaultDeviceTOML != want {
		t.Error("default_device.toml's [keys] part is stale or hand-edited — edit keySections and run `make keys-doc`")
	}
}

// Every binding appears in keySections exactly once, under its own table.
func TestKeySectionsCoverKeys(t *testing.T) {
	t.Parallel()

	seen := map[string]int{}
	for _, section := range keySections {
		for _, b := range section.binds {
			seen[b.path]++
			if b.def == "" || b.doc == "" {
				t.Errorf("%s needs a default and a doc", b.path)
			}
			if strings.Contains(b.doc, "\n") {
				t.Errorf("%s: a doc is one line (the help overlay's label)", b.path)
			}
			table := ""
			if i := strings.LastIndex(b.path, "."); i >= 0 {
				table = b.path[:i]
			}
			if table != section.table {
				t.Errorf("%s is listed under [keys.%s]", b.path, section.table)
			}
		}
	}
	for path, n := range seen {
		if n != 1 {
			t.Errorf("[keys] %s appears %d times in keySections, want once", path, n)
		}
	}
}
