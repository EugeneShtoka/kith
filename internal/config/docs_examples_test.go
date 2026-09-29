package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Every ```toml example in docs/ is something a reader pastes into their config, and
// Load refuses a key nothing reads. So each must decode, into a real table, with no
// unknown keys. Two did not: skin_tone shown under [display.emoji], and one table
// setting the same key twice.
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
			var cfg Config
			meta, err := toml.Decode(block, &cfg)
			if err != nil {
				t.Errorf("%s, toml block %d does not decode: %v\n%s", filepath.Base(file), i+1, err, block)
				continue
			}
			if undecoded := meta.Undecoded(); len(undecoded) > 0 {
				t.Errorf("%s, toml block %d has keys nothing reads: %v\n%s", filepath.Base(file), i+1, undecoded, block)
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
