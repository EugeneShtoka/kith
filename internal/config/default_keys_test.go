package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Every key the printed default configuration sets is one the loader reads.
func TestEveryDefaultKeyIsDecoded(t *testing.T) {
	t.Parallel()

	var cfg Config
	meta, err := toml.Decode(defaultConfigTOML, &cfg)
	if err != nil {
		t.Fatalf("decode default.toml: %v", err)
	}
	var unread []string
	for _, key := range meta.Undecoded() {
		unread = append(unread, key.String())
	}
	if len(unread) > 0 {
		t.Errorf("default.toml sets keys the loader never reads — a table header is "+
			"missing or misplaced:\n  %s", strings.Join(unread, "\n  "))
	}
}
