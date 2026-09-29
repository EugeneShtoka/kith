package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files under testdata")

// flattenKeys lists every binding as "path = value", in struct order.
func flattenKeys(prefix string, v reflect.Value, out *strings.Builder) {
	for i := range v.NumField() {
		f := v.Type().Field(i)
		name := prefix + strings.Split(f.Tag.Get("toml"), ",")[0]
		switch v.Field(i).Kind() {
		case reflect.String:
			fmt.Fprintf(out, "%s = %q\n", name, v.Field(i).String())
		case reflect.Struct:
			flattenKeys(name+".", v.Field(i), out)
		default:
			fmt.Fprintf(out, "%s = %v\n", name, v.Field(i).Interface())
		}
	}
}

// DefaultKeys is exactly what it was, binding for binding.
func TestDefaultKeysGolden(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	flattenKeys("", reflect.ValueOf(DefaultKeys()), &b)
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
