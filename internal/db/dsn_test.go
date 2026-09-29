package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The pragmas have to be on the DSN, because a pool does not hand out only the
// connection that was configured.
func TestConnectionSettingsSurviveAReplacedConnection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveRooms(ctx, []domain.Room{{ID: "!r:x", Name: "R"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	mustSave(t, cache, "!r:x", domain.Message{ID: "$e:x", Body: "hi"})

	// Retire the connection Open configured.
	cache.db.SetMaxIdleConns(0)

	for _, tc := range []struct{ pragma, want string }{
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"journal_mode", "wal"},
	} {
		var got string
		if err := cache.db.QueryRowContext(ctx, "PRAGMA "+tc.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s error = %v", tc.pragma, err)
		}
		if got != tc.want {
			t.Errorf("on a fresh connection, %s = %s, want %s", tc.pragma, got, tc.want)
		}
	}

	// The consequence, not just the setting: the cascade must still fire.
	if _, err := cache.db.ExecContext(ctx, "DELETE FROM rooms WHERE id = '!r:x'"); err != nil {
		t.Fatalf("delete room error = %v", err)
	}
	var orphans int
	if err := cache.db.QueryRowContext(ctx,
		"SELECT count(*) FROM messages WHERE room_id = '!r:x'").Scan(&orphans); err != nil {
		t.Fatalf("count error = %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d messages orphaned — the cascade did not fire, so foreign keys were off", orphans)
	}
}

// The cache path comes from an XDG directory, so it can contain characters that
// string-concatenating a DSN would hand to the query parser instead.
func TestOpenHandlesAwkwardPaths(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"plain", "with space", "with#hash", "with?question", "with&amp"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			path := filepath.Join(dir, "cache.db")
			cache, err := Open(context.Background(), path)
			if err != nil {
				t.Fatalf("Open(%q) error = %v", path, err)
			}
			defer func() { _ = cache.Close() }()

			// It opened; check it is the file we asked for and it is usable.
			var fk string
			if err := cache.db.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
				t.Fatalf("PRAGMA foreign_keys error = %v", err)
			}
			if fk != "1" {
				t.Errorf("foreign_keys = %s, want 1", fk)
			}
			if !strings.Contains(dsn(path), "foreign_keys") {
				t.Errorf("dsn(%q) lost its pragmas: %s", path, dsn(path))
			}
		})
	}
}
