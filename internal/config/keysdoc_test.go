package config

import (
	"os"
	"strings"
	"testing"
)

// The [keys] part of default.toml is rendered from keySections; a hand edit there, or
// a table change not regenerated, fails here. `make keys-doc` rewrites it.
func TestDefaultTOMLKeysSectionIsGenerated(t *testing.T) {
	t.Parallel()

	before, _, ok := strings.Cut(defaultConfigTOML, keysTOMLStart)
	if !ok {
		t.Fatalf("default.toml has no %q line to start the generated [keys] part at", keysTOMLStart)
	}
	want := before + renderKeysTOML()
	if *updateGolden {
		if err := os.WriteFile("default.toml", []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if defaultConfigTOML != want {
		t.Error("default.toml's [keys] part is stale or hand-edited — edit keySections and run `make keys-doc`")
	}
}

// Every binding in Keys appears in keySections exactly once, so none ships without a
// default or a description.
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
	for path := range keyFields {
		if seen[path] != 1 {
			t.Errorf("[keys] %s appears %d times in keySections, want once", path, seen[path])
		}
	}
	if len(seen) != len(keyFields) {
		t.Errorf("keySections has %d bindings, Keys has %d", len(seen), len(keyFields))
	}
}
