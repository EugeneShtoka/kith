package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// schemaObject is one sqlite_master row, its SQL with spacing normalized.
type schemaObject struct{ kind, name, sql string }

func schemaOf(t *testing.T, sqlDB *sql.DB) []schemaObject {
	t.Helper()
	rows, err := sqlDB.QueryContext(context.Background(), `
		SELECT type, name, coalesce(sql, '') FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			t.Fatal(err)
		}
		o.sql = strings.Join(strings.Fields(o.sql), " ")
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A cache upgraded from the first release and a cache built today must be the same
// schema: every table, index, trigger and FTS table. The upgrade runs the migrations
// over the v1 base as it shipped (testdata/schema-v1.sql, frozen; never edit it); a
// rebuild creates today's baseSchema and runs them again over it. So a migration must
// both reach baseSchema from v1 and be a no-op against baseSchema.
func TestAnUpgradedCacheMatchesAFreshOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	v1, err := os.ReadFile("testdata/schema-v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "upgraded.db")
	old, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecContext(ctx, string(v1)); err != nil {
		t.Fatalf("create the v1 schema: %v", err)
	}
	if _, err = old.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open the v1 cache: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if upgraded.Rebuilt() {
		t.Fatal("a v1 cache was rebuilt instead of migrated")
	}
	fresh := openTemp(t)

	got, want := schemaOf(t, upgraded.db), schemaOf(t, fresh.db)
	gotByName := map[string]schemaObject{}
	for _, o := range got {
		gotByName[o.kind+" "+o.name] = o
	}
	for _, w := range want {
		g, ok := gotByName[w.kind+" "+w.name]
		switch {
		case !ok:
			t.Errorf("the upgraded cache lacks %s %s", w.kind, w.name)
		case g.sql != w.sql:
			t.Errorf("%s %s differs after upgrade:\n  got  %s\n  want %s", w.kind, w.name, g.sql, w.sql)
		}
		delete(gotByName, w.kind+" "+w.name)
	}
	for key := range gotByName {
		t.Errorf("the upgraded cache has %s, which a fresh one does not", key)
	}
}
