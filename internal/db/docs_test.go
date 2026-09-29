package db

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs/database.md tells a reader which version a current cache is at and what each
// migration adds. It said "migrations is empty" for two migrations after v2 and v3
// landed; this holds the prose to the code.
func TestTheDatabaseDocNamesEveryMigration(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "database.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	current := schemaVersion + len(migrations)
	if want := fmt.Sprintf("a current cache is at version %d", current); !strings.Contains(collapse(doc), collapse(want)) {
		t.Errorf("docs/database.md does not say %q (schemaVersion %d + %d migrations)", want, schemaVersion, len(migrations))
	}
	if len(migrations) == 0 && !strings.Contains(doc, "`migrations` is empty") {
		t.Error("docs/database.md does not say the migrations list is empty")
	}
	created := regexp.MustCompile(`CREATE TABLE IF NOT EXISTS (\w+)`)
	for i, stmt := range migrations {
		version := schemaVersion + i + 1
		m := created.FindStringSubmatch(stmt)
		if m == nil {
			continue // a migration that creates no table names nothing to document
		}
		if want := fmt.Sprintf("v%d (`%s`)", version, m[1]); !strings.Contains(doc, want) {
			t.Errorf("docs/database.md does not name migration %s", want)
		}
	}
}

// collapse makes a phrase comparable across the doc's line wrapping.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
