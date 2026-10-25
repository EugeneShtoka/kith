package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Every commented [[notifications.rule]] example in the shipped config decodes and
// passes the startup check once uncommented.
func TestTheNotificationRuleExamplesPassStartup(t *testing.T) {
	t.Parallel()

	var blocks []string
	var current []string
	flush := func() {
		if len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n"))
		}
		current = nil
	}
	for line := range strings.SplitSeq(config.Annotated(), "\n") {
		body, commented := strings.CutPrefix(line, "# ")
		switch {
		case commented && strings.TrimSpace(body) == "[[notifications.rule]]":
			flush()
			current = []string{"[[notifications.rule]]"}
		case current != nil && commented && strings.Contains(body, "=") && !strings.HasPrefix(body, " "):
			current = append(current, body)
		default:
			flush()
		}
	}
	flush()
	if len(blocks) < 3 {
		t.Fatalf("found %d commented [[notifications.rule]] examples; the parser no longer matches default.toml", len(blocks))
	}

	// Through config.Load, as startup reads it, refusing unknown keys.
	dir := t.TempDir()
	for i, block := range blocks {
		path := filepath.Join(dir, "config-"+string(rune('a'+i))+".toml")
		body := block + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Errorf("example does not load: %v\n%s", err, block)
			continue
		}
		if err := PlaceEntries(cfg); err != nil {
			t.Errorf("example is refused at startup: %v\n%s", err, block)
		}
	}
}
