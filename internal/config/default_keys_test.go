package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Every key the printed default configuration and device file set is one the loader
// reads.
func TestEveryDefaultKeyIsDecoded(t *testing.T) {
	t.Parallel()

	for _, f := range settingsFiles {
		meta, err := toml.Decode(f.annotated, reflect.New(f.typ).Interface())
		if err != nil {
			t.Fatalf("decode the annotated %s: %v", f.name, err)
		}
		var unread []string
		for _, key := range meta.Undecoded() {
			unread = append(unread, key.String())
		}
		if len(unread) > 0 {
			t.Errorf("the annotated %s sets keys the loader never reads — a table header is "+
				"missing or misplaced:\n  %s", f.name, strings.Join(unread, "\n  "))
		}
	}
}
