package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files under testdata")

// flattenKeys lists every binding as "path = value", in keySections' order, then the
// jump and tag lists.
func flattenKeys(k Keys, out *strings.Builder) {
	for _, section := range keySections {
		for _, b := range section.binds {
			fmt.Fprintf(out, "%s = %q\n", b.path, k.Bindings[b.path])
		}
	}
	fmt.Fprintf(out, "jump = %v\ntag = %v\n", k.Jump, k.Tag)
}

// DefaultKeys matches the golden file, binding for binding.
func TestDefaultKeysGolden(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	flattenKeys(DefaultKeys(), &b)
	path := filepath.Join("testdata", "default_keys.golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if b.String() != string(want) {
		t.Errorf("DefaultKeys differs from testdata/default_keys.golden:\n%s", b.String())
	}
}
