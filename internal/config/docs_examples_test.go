package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Every ```toml example in docs/ decodes with no unknown or duplicate keys in one of
// the two files, config.toml or device.toml, since readers paste them into it.
func TestTheDocsTOMLExamplesDecode(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no docs found: %v", err)
	}
	checked := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, block := range tomlBlocks(string(raw)) {
			checked++
			var problems []string
			for _, f := range settingsFiles {
				meta, err := toml.Decode(block, reflect.New(f.typ).Interface())
				switch {
				case err != nil:
					problems = append(problems, fmt.Sprintf("as %s it does not decode: %v", f.name, err))
				case len(meta.Undecoded()) > 0:
					problems = append(problems, fmt.Sprintf("as %s it has keys nothing reads: %v", f.name, meta.Undecoded()))
				}
			}
			if len(problems) == len(settingsFiles) {
				t.Errorf("%s, toml block %d fits neither file: %s\n%s", filepath.Base(file), i+1, strings.Join(problems, "; "), block)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("checked only %d toml blocks; the fence parser no longer matches the docs", checked)
	}
}

// tomlBlocks is the body of every ```toml fence in a Markdown file.
func tomlBlocks(md string) []string {
	var blocks, current []string
	in := false
	for line := range strings.SplitSeq(md, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !in && trimmed == "```toml":
			in, current = true, nil
		case in && trimmed == "```":
			in = false
			blocks = append(blocks, strings.Join(current, "\n"))
		case in:
			current = append(current, line)
		}
	}
	return blocks
}
