// slackemoji turns the :name: emoji shortcodes in the Slack messages kithd's cache
// already holds into the emoji, as the Slack adapter does for every message it receives
// since (slack.Emojify): the words, their formatting and the versions kept of edited
// ones, and with them the search index. Only Slack's own rooms are read: elsewhere a
// typed :name: is what someone typed. A second run changes nothing.
//
// kithd must be stopped first: this takes kithd's own single-instance lock and refuses
// while kithd holds it. Before it writes, it keeps a copy of the cache beside it
// (<cache>.before-slackemoji-<time>, under the same key); to undo, stop kithd and move
// that copy over the cache. Clients reload what they hold once kithd is back.
//
// From a kith checkout, on the machine whose cache it is:
//
//	systemctl --user stop kithd
//	GOFLAGS=-tags=goolm go run ./scripts/slackemoji -dry-run   # what would change
//	GOFLAGS=-tags=goolm go run ./scripts/slackemoji            # change it
//	systemctl --user start kithd
//
// Or build it once and copy the one file to a machine of the same OS and architecture:
//
//	CGO_ENABLED=0 GOFLAGS=-tags=goolm go build -o slackemoji ./scripts/slackemoji
//
// -config names another config.toml than the usual one (as kithd's --config).
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ncruces/go-sqlite3/ext/fts5"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/slack"
)

func main() {
	configPath := flag.String("config", "", "path to config file (default: XDG config dir)")
	dryRun := flag.Bool("dry-run", false, "list what would change and change nothing")
	flag.Parse()
	if err := run(context.Background(), *configPath, *dryRun, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "slackemoji:", err)
		os.Exit(1)
	}
}

// run finds the cache as kithd does and converts it while holding kithd's lock.
func run(ctx context.Context, configPath string, dryRun bool, out io.Writer) error {
	path := configPath
	if path == "" {
		resolved, err := config.Path()
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
		path = resolved
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	storage, err := setup.StorageFor(cfg, path)
	if err != nil {
		return err
	}
	lock, held, err := daemon.Acquire(storage)
	if err != nil {
		return fmt.Errorf("take kithd's lock: %w", err)
	}
	if !held {
		return errors.New("kithd is running: stop it first (systemctl --user stop kithd), then run this again")
	}
	defer func() { _ = lock.Release() }()
	cache := storage.CachePath()
	if _, serr := os.Stat(cache); serr != nil {
		return fmt.Errorf("no cache here: %w", serr)
	}
	key, err := session.LoadOrCreateStoreKey(storage.KeyringService, storage.Instance, "cache")
	if err != nil {
		return fmt.Errorf("the cache's key (unlock the keyring): %w", err)
	}
	return convert(ctx, cache, key, dryRun, out)
}

// stored is one message's words and formatting as the cache keeps them: the message
// itself (revision "") or one version kept of it.
type stored struct {
	room, event, revision string
	body, html            string
}

// table is where a version lives.
func (s stored) table() string {
	if s.revision == "" {
		return "message"
	}
	return "version " + s.revision + " of"
}

// convert opens the cache at path as kithd does (sealed, with FTS5, so the index's
// triggers run) and converts every Slack message in one transaction.
func convert(ctx context.Context, path string, key []byte, dryRun bool, out io.Writer) error {
	sqlDB, err := sealeddb.OpenWith(ctx, path, key, sealeddb.Setup{TxLock: "immediate", Init: fts5.Register})
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)

	all, err := slackBodies(ctx, sqlDB)
	if err != nil {
		return err
	}
	var changed []stored
	skipped := 0
	for _, s := range all {
		f := richtext.FromMarkup(s.html)
		if s.html != "" && f.Text() != s.body {
			skipped++ // words and formatting that disagree: no telling which to follow
			continue
		}
		body, format := slack.Emojify(s.body, f)
		if body == s.body && format.Markup() == s.html {
			continue
		}
		say(out, "%s %s in %s:\n  %q\n→ %q\n", s.table(), s.event, s.room, s.body, body)
		s.body, s.html = body, format.Markup()
		changed = append(changed, s)
	}
	if skipped > 0 {
		say(out, "left %d whose formatting does not draw their words\n", skipped)
	}
	switch {
	case len(changed) == 0:
		say(out, "nothing to change in %d Slack messages and versions\n", len(all))
		return nil
	case dryRun:
		say(out, "would change %d of %d Slack messages and versions (dry run: nothing written)\n", len(changed), len(all))
		return nil
	}
	backup := path + ".before-slackemoji-" + time.Now().Format("20060102-150405")
	if err := db.Snapshot(ctx, path, key, backup, key); err != nil {
		return fmt.Errorf("keep a copy first: %w", err)
	}
	say(out, "kept a copy of the cache as %s\n", backup)
	if err := write(ctx, sqlDB, changed); err != nil {
		return err
	}
	say(out, "changed %d of %d Slack messages and versions\n", len(changed), len(all))
	return nil
}

// say reports to out; a report that cannot be written changes nothing.
func say(out io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }

// slackBodies is every Slack message and kept version, with its formatting.
func slackBodies(ctx context.Context, sqlDB *sql.DB) ([]stored, error) {
	prefix := string(domain.NetworkRooms(domain.ProtocolSlack))
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT m.room_id, m.event_id, '', m.body, coalesce(h.html, '') FROM messages m
		  LEFT JOIN message_html h ON h.room_id = m.room_id AND h.event_id = m.event_id
		 WHERE substr(m.room_id, 1, length(?1)) = ?1
		UNION ALL
		SELECT room_id, event_id, revision_id, body, html FROM message_revisions
		 WHERE substr(room_id, 1, length(?1)) = ?1`, prefix)
	if err != nil {
		return nil, fmt.Errorf("read Slack messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var all []stored
	for rows.Next() {
		var s stored
		if err := rows.Scan(&s.room, &s.event, &s.revision, &s.body, &s.html); err != nil {
			return nil, fmt.Errorf("read Slack messages: %w", err)
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read Slack messages: %w", err)
	}
	return all, nil
}

// write stores the converted words and formatting in one transaction. The search
// index follows the body by its triggers; a new epoch of the change numbering tells
// every client to reload what it holds, as the cache's Clear does.
func write(ctx context.Context, sqlDB *sql.DB, changed []stored) error {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, s := range changed {
		var err error
		if s.revision == "" {
			_, err = tx.ExecContext(ctx, "UPDATE messages SET body = ? WHERE room_id = ? AND event_id = ?", s.body, s.room, s.event)
			if err == nil {
				_, err = tx.ExecContext(ctx, "UPDATE message_html SET html = ? WHERE room_id = ? AND event_id = ?", s.html, s.room, s.event)
			}
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE message_revisions SET body = ?, html = ? WHERE room_id = ? AND event_id = ? AND revision_id = ?",
				s.body, s.html, s.room, s.event, s.revision)
		}
		if err != nil {
			return fmt.Errorf("write %s %s: %w", s.table(), s.event, err)
		}
	}
	for _, q := range []string{
		"DELETE FROM message_touched",
		"UPDATE change_counter SET epoch = lower(hex(randomblob(16)))",
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("renumber changes: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
