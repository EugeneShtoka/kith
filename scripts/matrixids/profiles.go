package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// The profile step (step 5): an install that ran each [[profile]] as a daemon of its
// own becomes one daemon running every profile's account. The first profile's
// instance is kept (its other networks' stores and keyring entries keep their names);
// each profile becomes a [[matrix.account]], its crypto store, session file and
// keyring entries take that instance's per-account names, and the other profiles'
// caches, schedules and ledgers are merged into it.

// profile is one [[profile]] and the instance its daemon ran as.
type profile struct {
	Name, Homeserver, User string
	instance               string
}

// profileConfig is what the profile step reads of a config kith no longer loads.
type profileConfig struct {
	Profiles []profile `toml:"profile"`
	Storage  struct {
		Instance string `toml:"instance"`
	} `toml:"storage"`
}

// readProfiles is the config's profiles, each with the instance its daemon ran as: the
// base instance and its name, or, from before instances, its account's key when files
// of that name exist.
func readProfiles(text []byte, base string, dirs domain.Storage) ([]profile, error) {
	var cfg profileConfig
	if _, err := toml.Decode(string(text), &cfg); err != nil {
		return nil, fmt.Errorf("read the profiles: %w", err)
	}
	if cfg.Storage.Instance != "" {
		base = cfg.Storage.Instance
	}
	if base == "" && len(cfg.Profiles) > 0 {
		return nil, errors.New("the config names no [storage] instance: pass -instance, the <instance> of its cache-<instance>.db")
	}
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		p.instance = base + "-" + p.Name
		legacy := dirs
		legacy.Instance = domain.AccountKey(p.User)
		for _, f := range []string{legacy.CachePath(), legacy.CryptoPath(), legacy.SessionFile()} {
			if _, err := os.Stat(f); err == nil {
				p.instance = legacy.Instance
			}
		}
	}
	return cfg.Profiles, nil
}

// profilesToAccounts turns the config's [[profile]] blocks into [[matrix.account]]
// records and pins [storage] instance to the instance kept, keeping the rest of the
// file as written.
func profilesToAccounts(profiles []profile, instance string) func(ids, []byte) ([]byte, int, error) {
	return func(_ ids, in []byte) ([]byte, int, error) {
		var out strings.Builder
		inProfile, inStorage, pinned := false, false, false
		for line := range strings.Lines(string(in)) {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				inProfile = trimmed == "[[profile]]"
				inStorage = trimmed == "[storage]"
				if inProfile {
					continue
				}
				out.WriteString(line)
				if inStorage {
					out.WriteString(`instance = "` + instance + "\"\n")
					pinned = true
				}
				continue
			}
			if m := tomlKey.FindStringSubmatch(line); inStorage && m != nil && m[1] == "instance" {
				continue // pinned above
			}
			if !inProfile {
				out.WriteString(line)
			}
		}
		var text strings.Builder
		text.WriteString(strings.TrimRight(out.String(), "\n") + "\n")
		if !pinned {
			text.WriteString("\n[storage]\ninstance = \"" + instance + "\"\n")
		}
		for _, p := range profiles {
			fmt.Fprintf(&text, "\n[[matrix.account]]\nname = %q\nhomeserver = %q\nuser = %q\n", p.Name, p.Homeserver, p.User)
		}
		return []byte(text.String()), len(profiles), nil
	}
}

// migrateProfiles runs the profile step over the profiles of the config at
// configPath, their files in dirs (no instance) and its keyring service, and returns
// them with the instance kept, for the config's rewrite (profilesToAccounts).
func migrateProfiles(ctx context.Context, configPath, base string, dirs domain.Storage, dry bool) ([]profile, string, error) {
	text, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		return nil, "", fmt.Errorf("read the config: %w", err)
	}
	profiles, err := readProfiles(text, base, dirs)
	if err != nil || len(profiles) == 0 {
		return nil, "", err
	}
	kept := dirs
	kept.Instance = profiles[0].instance
	var errs []error
	for i, p := range profiles {
		from := dirs
		from.Instance = p.instance
		x := ids{account: domain.AccountKey(p.User)}
		// Its IDs first, unless done already; then today's schema, under which each
		// cache's mirrors are its one account's.
		if _, serr := os.Stat(from.CachePath() + ".pre-matrixids"); errors.Is(serr, fs.ErrNotExist) {
			key, kerr := cacheKey(from.KeyringService, from.CachePath())
			if kerr != nil {
				return nil, "", kerr
			}
			errs = append(errs, report("cache", from.CachePath())(migrateCache(ctx, from.CachePath(), key, x, dry)))
		}
		for _, f := range []struct{ what, path string }{{"schedule", from.SchedulePath()}, {"ledger", from.LedgerPath()}} {
			rewrite := rewriteTOML
			if f.what == "ledger" {
				rewrite = rewriteLedger
			}
			errs = append(errs, report(f.what, f.path)(migrateFile(f.path, x, rewrite, dry)))
		}
		errs = append(errs,
			report("files of", p.Name)(moveStores(from, kept, p.User, dry)),
			report("keyring entries of", p.Name)(copyKeys(from, kept, p.User, dry)))
		if i == 0 || dry {
			continue
		}
		errs = append(errs, report("merged rows of", p.Name)(mergeCache(ctx, dirs.KeyringService, kept.CachePath(), from.CachePath(), domain.AccountRooms(domain.ProtocolMatrix, x.account))))
		for _, f := range [][2]string{{from.SchedulePath(), kept.SchedulePath()}, {from.LedgerPath(), kept.LedgerPath()}} {
			errs = append(errs, report("appended", f[1])(appendFile(f[0], f[1])))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, "", err
	}
	fmt.Println("then: systemctl --user disable --now", strings.Join(profileUnits(profiles), " "), "&& systemctl --user enable --now kithd")
	return profiles, kept.Instance, nil
}

// profileUnits is each profile's systemd unit.
func profileUnits(profiles []profile) []string {
	units := make([]string, 0, len(profiles))
	for _, p := range profiles {
		units = append(units, "kithd@"+p.Name)
	}
	return units
}

// todaysSchema brings the caches at paths to today's schema, as kithd opens them, and
// returns each one's key from service's keyring.
func todaysSchema(ctx context.Context, service string, paths ...string) (map[string][]byte, error) {
	keys := map[string][]byte{}
	for _, path := range paths {
		key, err := cacheKey(service, path)
		if err != nil {
			return nil, err
		}
		keys[path] = key
		c, err := db.Open(ctx, path, key)
		if err != nil {
			return nil, fmt.Errorf("bring %s to today's schema: %w", path, err)
		}
		if err := c.Close(); err != nil {
			return nil, fmt.Errorf("close %s: %w", path, err)
		}
	}
	return keys, nil
}

// mergeCache copies one Matrix account's rows of the cache at src into the cache at
// dst, both first brought to today's schema; another network's rows in src (its own
// daemon's copy of an account dst has) are left. dst is copied aside first. Each is
// encrypted under its own instance's key in service's keyring.
func mergeCache(ctx context.Context, service, dst, src string, owner domain.RoomOwner) (int, error) {
	keys, err := todaysSchema(ctx, service, dst, src)
	if err != nil {
		return 0, err
	}
	conn, err := sealeddb.OpenWith(ctx, dst, keys[dst], cacheSetup)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = conn.Close() }()
	if _, cerr := conn.ExecContext(ctx, "VACUUM INTO ?", sealeddb.Target(dst+".pre-merge", keys[dst])); cerr != nil {
		return 0, fmt.Errorf("keep a copy: %w", cerr)
	}
	one, err := conn.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = one.Close() }()
	if _, xerr := one.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); xerr != nil {
		return 0, fmt.Errorf("turn foreign keys off: %w", xerr)
	}
	// Bound, not spliced: the name carries src's key.
	if _, xerr := one.ExecContext(ctx, "ATTACH DATABASE ? AS src", sealeddb.Target(src, keys[src])); xerr != nil {
		return 0, fmt.Errorf("attach %s: %w", src, xerr)
	}
	tx, err := one.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	tables, err := plainTables(ctx, tx)
	if err != nil {
		return 0, err
	}
	merged := 0
	for _, table := range tables {
		n, err := mergeTable(ctx, tx, table, owner)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		}
		merged += n
	}
	if err := foreignKeysHold(ctx, tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return merged, nil
}

// plainTables is the cache's tables but the full-text index and its shadows, which
// the messages' triggers keep.
func plainTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, sql FROM main.sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list the tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var names, virtual []string
	for rows.Next() {
		var name, create string
		if err := rows.Scan(&name, &create); err != nil {
			return nil, fmt.Errorf("list the tables: %w", err)
		}
		if strings.HasPrefix(create, "CREATE VIRTUAL") {
			virtual = append(virtual, name)
			continue
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the tables: %w", err)
	}
	return slices.DeleteFunc(names, func(n string) bool {
		return slices.ContainsFunc(virtual, func(v string) bool { return strings.HasPrefix(n, v+"_") })
	}), nil
}

// mergeTable copies owner's rows of one table, a row dst has already kept as it is.
// Which rows are owner's: those of its rooms and spaces, its mirrors and its bridge
// logins; a table of no account's (a bridge's refused reactions) is merged whole.
func mergeTable(ctx context.Context, tx *sql.Tx, table string, owner domain.RoomOwner) (int, error) {
	cols, err := copiedColumns(ctx, tx, table)
	if err != nil {
		return 0, err
	}
	owned := func(col string) string {
		return "substr(" + col + ", 1, " + fmt.Sprint(len(owner)) + ") = '" + string(owner) + "'"
	}
	var where string
	switch {
	case table == "rooms" || table == "spaces":
		where = owned("id")
	case slices.Contains(cols, "room_id"):
		where = owned("room_id")
	case slices.Contains(cols, "space_id"):
		where = owned("space_id")
	case table == "person_verdicts" || table == "ignored_users":
		where = "account = '" + string(owner) + "'"
	case table == "person_names" || table == "person_links":
		where = owned("substr(source, 8)") + " AND source LIKE 'bridge:%'"
	case table == "reaction_refusals":
		where = "1"
	default:
		return 0, nil // another network's (network_blocks): dst's own daemon kept it
	}
	list := strings.Join(cols, ", ")
	// #nosec G202 -- table and columns are the cache's own, read from its schema.
	res, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO main."+table+" ("+list+") SELECT "+list+" FROM src."+table+" WHERE "+where)
	if err != nil {
		return 0, fmt.Errorf("copy: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// copiedColumns is a table's columns but a rowid alias, which dst numbers itself.
func copiedColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name, type, pk FROM pragma_table_info(?, 'main')", table)
	if err != nil {
		return nil, fmt.Errorf("read the columns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	keys := 0
	alias := ""
	for rows.Next() {
		var name, typ string
		var pk int
		if err := rows.Scan(&name, &typ, &pk); err != nil {
			return nil, fmt.Errorf("read the columns: %w", err)
		}
		if pk > 0 {
			keys++
			if strings.EqualFold(typ, "INTEGER") {
				alias = name
			}
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the columns: %w", err)
	}
	if keys == 1 && alias != "" {
		cols = slices.DeleteFunc(cols, func(c string) bool { return c == alias })
	}
	return cols, nil
}

// appendFile appends the file at from to the one at to, which keeps a copy first; a
// missing from has nothing to add.
func appendFile(from, to string) (int, error) {
	data, err := os.ReadFile(filepath.Clean(from))
	if errors.Is(err, fs.ErrNotExist) || len(data) == 0 {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", from, err)
	}
	if had, rerr := os.ReadFile(filepath.Clean(to)); rerr == nil {
		if cerr := copyFile(to, to+".pre-merge"); cerr != nil {
			return 0, cerr
		}
		if len(had) > 0 && had[len(had)-1] != '\n' {
			data = append([]byte("\n"), data...) // a line of its own: the ledger is one entry a line
		}
	}
	f, err := os.OpenFile(filepath.Clean(to), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", to, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("write %s: %w", to, err)
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("write %s: %w", to, err)
	}
	return 1, nil
}
