// Command matrixids rewrites an install's bare Matrix IDs into kith's form, once, for
// the change that made every Matrix ID say its network and account
// (planning/design-matrix-accounts.md, step 2): "!room" and "$event" become
// "matrix:<account>/!room" and "matrix:<account>/$event", "@user:server" becomes
// "matrix:@user:server". <account> is domain.AccountKey of the account's MXID.
//
// Run it with kithd and kith stopped, first with -dry-run. It rewrites the cache in
// place (keeping offline history and drafts), the config, the schedule queue and the
// assistant ledger; each file it changes is copied to <file>.pre-matrixids first.
//
// For several Matrix accounts (step 5) the config's top-level homeserver and user
// become a [[matrix.account]], and with -instance the account's crypto store and
// session file are renamed and its keyring entries copied to the names that carry its
// key. With -profiles, an install that ran each [[profile]] as its own daemon becomes
// one daemon running every profile's account (profiles.go). Delete this program once
// every install has run it.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// options are the command line's.
type options struct {
	user, cache, config, schedule, ledger string
	instance, dataDir, stateDir, service  string
	profiles, dry                         bool
}

func main() {
	var o options
	flag.StringVar(&o.user, "user", "", "the Matrix account's MXID, as the config's user names it (required but with -profiles)")
	flag.StringVar(&o.cache, "cache", "", "the cache database")
	flag.StringVar(&o.config, "config", "", "the config file")
	flag.StringVar(&o.schedule, "schedule", "", "the schedule queue")
	flag.StringVar(&o.ledger, "ledger", "", "the assistant ledger")
	flag.StringVar(&o.instance, "instance", "", "the [storage] instance whose Matrix files and keyring entries move "+
		"(with -profiles: the base instance, when the config names none)")
	flag.StringVar(&o.dataDir, "data-dir", "", "the data directory, holding crypto-<instance>.db (with -instance or -profiles)")
	flag.StringVar(&o.stateDir, "state-dir", "", "the state directory, holding session-<instance>.toml (with -instance or -profiles)")
	flag.StringVar(&o.service, "keyring-service", "kith", "the [storage] keyring service (with -instance or -profiles)")
	flag.BoolVar(&o.profiles, "profiles", false, "each [[profile]] of -config becomes a [[matrix.account]] of one daemon, "+
		"its cache, schedule and ledger merged into the first's (with -data-dir and -state-dir)")
	flag.BoolVar(&o.dry, "dry-run", false, "report what would change and write nothing")
	flag.Parse()
	var err error
	if o.profiles {
		err = runProfiles(o)
	} else {
		err = runAccount(o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "matrixids:", err)
		os.Exit(1)
	}
}

// errUsage is a command line that names too little.
var errUsage = errors.New("usage")

// runAccount migrates the one account an install ran before profiles or several
// accounts: its IDs, and with -instance its files and keyring entries.
func runAccount(o options) error {
	if !strings.HasPrefix(o.user, "@") || !strings.Contains(o.user, ":") {
		return fmt.Errorf("%w: -user must be the account's MXID, @name:server", errUsage)
	}
	if o.instance != "" && (o.dataDir == "" || o.stateDir == "") {
		return fmt.Errorf("%w: -instance needs -data-dir and -state-dir", errUsage)
	}
	x := ids{account: domain.AccountKey(o.user)}
	var errs []error
	if o.instance != "" {
		storage := domain.Storage{Instance: o.instance, DataDir: o.dataDir, StateDir: o.stateDir, KeyringService: o.service}
		errs = append(errs,
			report("files of", o.instance)(moveStores(storage, storage, o.user, o.dry)),
			report("keyring entries of", o.instance)(copyKeys(storage, storage, o.user, o.dry)))
	}
	if o.cache != "" {
		key, err := cacheKey(o.service, o.cache)
		if err != nil {
			return err
		}
		errs = append(errs, report("cache", o.cache)(migrateCache(context.Background(), o.cache, key, x, o.dry)))
	}
	for _, f := range []struct {
		what, path string
		rewrite    func(ids, []byte) ([]byte, int, error)
	}{
		{"config", o.config, then(rewriteTOML, moveAccount(o.user))},
		{"schedule", o.schedule, rewriteTOML},
		{"ledger", o.ledger, rewriteLedger},
	} {
		if f.path != "" {
			errs = append(errs, report(f.what, f.path)(migrateFile(f.path, x, f.rewrite, o.dry)))
		}
	}
	return errors.Join(errs...)
}

// runProfiles is the profile step, then the config's rewrite: its IDs read as the
// first profile's account's, as kithd.service served that one.
func runProfiles(o options) error {
	if o.config == "" || o.dataDir == "" || o.stateDir == "" {
		return fmt.Errorf("%w: -profiles needs -config, -data-dir and -state-dir", errUsage)
	}
	dirs := domain.Storage{DataDir: o.dataDir, StateDir: o.stateDir, KeyringService: o.service}
	profiles, kept, err := migrateProfiles(context.Background(), o.config, o.instance, dirs, o.dry)
	if err != nil || len(profiles) == 0 {
		return err
	}
	x := ids{account: domain.AccountKey(profiles[0].User)}
	return report("config", o.config)(migrateFile(o.config, x, then(rewriteTOML, profilesToAccounts(profiles, kept)), o.dry))
}

// then is first's rewrite followed by next's, their counts added.
func then(first, next func(ids, []byte) ([]byte, int, error)) func(ids, []byte) ([]byte, int, error) {
	return func(x ids, in []byte) ([]byte, int, error) {
		out, n, err := first(x, in)
		if err != nil {
			return nil, 0, err
		}
		out, m, err := next(x, out)
		return out, n + m, err
	}
}

// report prints what one store's migration changed.
func report(what, path string) func(int, error) error {
	return func(changed int, err error) error {
		if err != nil {
			return fmt.Errorf("%s %s: %w", what, path, err)
		}
		fmt.Printf("%s %s: %d values rewritten\n", what, path, changed)
		return nil
	}
}

// ids rewrites one account's bare Matrix IDs.
type ids struct{ account string }

// id is s in kith's form when it is a bare Matrix ID; ok is false for anything else.
func (x ids) id(s string) (string, bool) {
	switch {
	case len(s) < 2:
		return s, false
	case s[0] == '!' || s[0] == '$':
		return domain.NativeID(domain.ProtocolMatrix, x.account, s), true
	case s[0] == '@' && strings.Contains(s, ":"):
		return domain.NativePerson(domain.ProtocolMatrix, s), true
	}
	return s, false
}

// chainPrefix starts kith's own paging token for a room's predecessor chain
// (internal/matrix), whose rest is a room ID.
const chainPrefix = "kith:chain:"

// token is a paging token with a chained room in kith's form.
func (x ids) token(s string) (string, bool) {
	rest, ok := strings.CutPrefix(s, chainPrefix)
	if !ok {
		return s, false
	}
	depth, room, ok := strings.Cut(rest, ":")
	if !ok {
		return s, false
	}
	if room, ok = x.id(room); !ok {
		return s, false
	}
	return chainPrefix + depth + ":" + room, true
}

// idColumns are the cache's columns that hold an ID; every other text column is free
// text (main_test.go holds the two lists to the schema).
var idColumns = map[string][]string{
	"draft_threads":       {"room_id", "thread_root"},
	"drafts":              {"room_id", "reply_to", "editing"},
	"emoji_usage":         {"room_id"},
	"ignored_users":       {"user_id"},
	"mention_usage":       {"room_id", "user_id"},
	"message_edit":        {"room_id", "event_id", "revision_id"},
	"message_expiry":      {"room_id", "event_id"},
	"message_html":        {"room_id", "event_id"},
	"message_media":       {"room_id", "event_id"},
	"message_mentions":    {"room_id", "event_id", "user_id"},
	"message_order":       {"room_id", "event_id"},
	"message_polls":       {"room_id", "event_id"},
	"message_redaction":   {"room_id", "event_id", "by"},
	"message_revisions":   {"room_id", "event_id", "revision_id"},
	"message_tombstone":   {"room_id", "event_id", "by"},
	"messages":            {"room_id", "event_id", "sender", "reply_to", "thread_root"},
	"network_blocks":      {"user_id"},
	"person_links":        {"id", "other"},
	"person_names":        {"id"},
	"person_verdicts":     {"person"},
	"quoted_messages":     {"room_id", "event_id", "sender"},
	"reactions":           {"event_id", "room_id", "target_event", "sender"},
	"room_archived":       {"room_id"},
	"room_forums":         {"room_id"},
	"room_gaps":           {"room_id", "boundary"},
	"room_handles":        {"room_id"},
	"room_members":        {"room_id", "user_id"},
	"room_networks":       {"room_id"},
	"room_parents":        {"room_id", "space_id"},
	"room_read_only":      {"room_id"},
	"room_sms":            {"room_id"},
	"room_timer_versions": {"room_id"},
	"room_timers":         {"room_id"},
	"room_topics":         {"room_id"},
	"room_unread":         {"room_id", "read_event"},
	"room_upgrades":       {"room_id", "replacement", "predecessor"},
	"rooms":               {"id", "invited_by"},
	"sender_slots":        {"room_id", "group_key"},
	"space_children":      {"space_id", "room_id"},
	"space_leaving":       {"space_id"},
	"spaces":              {"id", "keeper"},
	"starred":             {"room_id", "event_id"},
	"tag_verdicts":        {"room_id"},
	"thread_read":         {"room_id", "root_event", "read_event"},
}

// jsonColumns hold IDs inside JSON; tokenColumns hold paging tokens.
var (
	jsonColumns = map[string]map[string]func(ids, string) (string, bool, error){
		"drafts":        {"mentions": ids.mentions},
		"message_polls": {"poll": ids.poll},
	}
	tokenColumns = map[string][]string{"room_gaps": {"token"}}
)

// mentions rewrites a draft's mentions (a JSON []domain.Mention).
func (x ids) mentions(raw string) (string, bool, error) {
	if raw == "" {
		return raw, false, nil
	}
	var ms []domain.Mention
	if err := json.Unmarshal([]byte(raw), &ms); err != nil {
		return raw, false, nil //nolint:nilerr // unreadable mentions are dropped by kith anyway; keep them as they are
	}
	changed := false
	for i := range ms {
		if v, ok := x.id(ms[i].UserID); ok {
			ms[i].UserID, changed = v, true
		}
		if v, ok := x.id(ms[i].RoomID); ok {
			ms[i].RoomID, changed = v, true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(ms)
	if err != nil {
		return raw, false, fmt.Errorf("draft mentions: %w", err)
	}
	return string(out), true, nil
}

// poll rewrites a poll's voters (a JSON domain.Poll).
func (x ids) poll(raw string) (string, bool, error) {
	var p domain.Poll
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return raw, false, fmt.Errorf("poll %q: %w", raw, err)
	}
	changed := false
	if len(p.Ballots) > 0 {
		ballots := make(map[string][]string, len(p.Ballots))
		for voter, answers := range p.Ballots {
			if v, ok := x.id(voter); ok {
				voter, changed = v, true
			}
			ballots[voter] = answers
		}
		p.Ballots = ballots
	}
	if len(p.Rounds) > 0 {
		rounds := make(map[string]uint32, len(p.Rounds))
		for voter, n := range p.Rounds {
			if v, ok := x.id(voter); ok {
				voter, changed = v, true
			}
			rounds[voter] = n
		}
		p.Rounds = rounds
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(p)
	if err != nil {
		return raw, false, fmt.Errorf("poll: %w", err)
	}
	return string(out), true, nil
}

// cacheSetup is how kithd opens its cache: the full-text index needs FTS5.
var cacheSetup = sealeddb.Setup{Init: fts5.Register}

// cacheKey is the key the cache at path (cache-<instance>.db) is encrypted under, in
// service's keyring, where kithd keeps it.
func cacheKey(service, path string) ([]byte, error) {
	instance := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "cache-"), ".db")
	key, err := session.LoadOrCreateStoreKey(service, instance, "cache")
	if err != nil {
		return nil, fmt.Errorf("the cache's key: %w", err)
	}
	return key, nil
}

// migrateCache rewrites every ID column of the cache at path, encrypted under key, in
// one transaction, with foreign keys checked after. A cache still plain is encrypted
// first, as kithd would on its next start, even on a dry run.
func migrateCache(ctx context.Context, path string, key []byte, x ids, dry bool) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("find the cache: %w", err)
	}
	db, err := sealeddb.OpenMigratingWith(ctx, path, key, cacheSetup)
	if err != nil {
		return 0, fmt.Errorf("open the cache: %w", err)
	}
	defer func() { _ = db.Close() }()
	if !dry {
		// A consistent copy, write-ahead log included, encrypted as the cache is.
		if _, cerr := db.ExecContext(ctx, "VACUUM INTO ?", sealeddb.Target(path+".pre-matrixids", key)); cerr != nil {
			return 0, fmt.Errorf("keep a copy: %w", cerr)
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("open the cache: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// Primary and foreign keys change together: checked once at the end instead.
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return 0, fmt.Errorf("turn foreign keys off: %w", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := rewriteTables(ctx, tx, x)
	if err != nil {
		return 0, err
	}
	if ferr := foreignKeysHold(ctx, tx); ferr != nil {
		return 0, ferr
	}
	if dry {
		return changed, nil
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return changed, nil
}

// rewriteTables rewrites every table the column maps name.
func rewriteTables(ctx context.Context, tx *sql.Tx, x ids) (int, error) {
	changed := 0
	for _, table := range sortedKeys(idColumns, jsonColumns, tokenColumns) {
		cols := map[string]func(string) (string, bool, error){}
		for _, c := range idColumns[table] {
			cols[c] = func(s string) (string, bool, error) { v, ok := x.id(s); return v, ok, nil }
		}
		for _, c := range tokenColumns[table] {
			cols[c] = func(s string) (string, bool, error) { v, ok := x.token(s); return v, ok, nil }
		}
		for c, f := range jsonColumns[table] {
			cols[c] = func(s string) (string, bool, error) { return f(x, s) }
		}
		n, err := rewriteTable(ctx, tx, table, cols)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		}
		changed += n
	}
	return changed, nil
}

// foreignKeysHold fails when a rewritten key no longer meets the key it refers to.
func foreignKeysHold(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("check foreign keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return errors.New("foreign keys broken after the rewrite; nothing written")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check foreign keys: %w", err)
	}
	return nil
}

// row is one row's key and its rewritten columns.
type row struct{ key, set []any }

// rewriteTable rewrites cols of every row of table.
func rewriteTable(ctx context.Context, tx *sql.Tx, table string, cols map[string]func(string) (string, bool, error)) (int, error) {
	names := make([]string, 0, len(cols))
	for c := range cols {
		names = append(names, c)
	}
	sort.Strings(names)
	keys, err := primaryKey(ctx, tx, table)
	if err != nil {
		return 0, err
	}
	changed, err := changedRows(ctx, tx, table, keys, names, cols)
	if err != nil {
		return 0, err
	}
	set := make([]string, len(names))
	for i, c := range names {
		set[i] = c + " = ?"
	}
	where := make([]string, len(keys))
	for i, k := range keys {
		where[i] = k + " = ?"
	}
	//nolint:gosec // table and column names come from this program's maps and the schema, never from input
	stmt, err := tx.PrepareContext(ctx, "UPDATE "+table+" SET "+strings.Join(set, ", ")+" WHERE "+strings.Join(where, " AND "))
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range changed {
		if _, err := stmt.ExecContext(ctx, append(r.set, r.key...)...); err != nil {
			return 0, fmt.Errorf("update: %w", err)
		}
	}
	return len(changed), nil
}

// changedRows reads every row of table and returns those a column function changes.
func changedRows(ctx context.Context, tx *sql.Tx, table string, keys, names []string,
	cols map[string]func(string) (string, bool, error),
) ([]row, error) {
	//nolint:gosec // table and column names come from this program's maps and the schema, never from input
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(append(slices.Clone(keys), names...), ", ")+" FROM "+table)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []row
	for rows.Next() {
		vals := make([]sql.NullString, len(keys)+len(names))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		r, changed := row{}, false
		for i := range keys {
			r.key = append(r.key, vals[i].String)
		}
		for i, c := range names {
			v, ok, err := cols[c](vals[len(keys)+i].String)
			if err != nil {
				return nil, err
			}
			changed = changed || ok
			r.set = append(r.set, v)
		}
		if changed {
			out = append(out, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return out, nil
}

// primaryKey is how a row of table is named: its primary key columns, else its rowid.
func primaryKey(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk", table)
	if err != nil {
		return nil, fmt.Errorf("read the key: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("read the key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the key: %w", err)
	}
	if len(keys) == 0 {
		keys = []string{"rowid"}
	}
	return keys, nil
}

// tomlString is one double-quoted TOML string on a line.
var tomlString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// tomlKey is a line's key, when it assigns one.
var tomlKey = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+)\s*=`)

// nativeKeys hold the account's own MXID, or text: never rewritten.
var nativeKeys = map[string]bool{"user": true, "homeserver": true, "body": true, "name": true, "alias": true, "text": true}

// rewriteTOML rewrites every quoted bare Matrix ID in a TOML file, alone or after
// room: or thread:, keeping the file as written: comments, order and spacing. A value
// that is only part of a string (a message's text) is left alone.
func rewriteTOML(x ids, in []byte) ([]byte, int, error) {
	var out bytes.Buffer
	changed := 0
	key := "" // the latest key: a multi-line array's lines are its value
	for line := range strings.Lines(string(in)) {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			out.WriteString(line)
			continue
		}
		if m := tomlKey.FindStringSubmatch(line); m != nil {
			key = m[1][strings.LastIndex(m[1], ".")+1:]
		}
		code, comment := line, ""
		if i := strings.Index(line, " #"); i >= 0 && strings.Count(line[:i], `"`)%2 == 0 {
			code, comment = line[:i], line[i:]
		}
		if !nativeKeys[key] {
			code = tomlString.ReplaceAllStringFunc(code, func(q string) string {
				v := q[1 : len(q)-1]
				for _, prefix := range []string{"", "room:", "thread:"} {
					rest, ok := strings.CutPrefix(v, prefix)
					if !ok {
						continue
					}
					if id, ok := x.id(rest); ok {
						changed++
						return `"` + prefix + id + `"`
					}
				}
				return q
			})
		}
		out.WriteString(code + comment)
	}
	return out.Bytes(), changed, nil
}

// rewriteLedger rewrites each ledger line's room.
func rewriteLedger(x ids, in []byte) ([]byte, int, error) {
	var out bytes.Buffer
	changed := 0
	for line := range bytes.Lines(in) {
		var e agent.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			out.Write(line) // not one kith wrote: left as it is
			continue
		}
		id, ok := x.id(string(e.Room))
		if !ok {
			out.Write(line)
			continue
		}
		e.Room = domain.RoomID(id)
		data, err := json.Marshal(e)
		if err != nil {
			return nil, 0, fmt.Errorf("ledger entry: %w", err)
		}
		out.Write(append(data, '\n'))
		changed++
	}
	return out.Bytes(), changed, nil
}

// migrateFile rewrites the file at path; a missing file has nothing to rewrite.
func migrateFile(path string, x ids, rewrite func(ids, []byte) ([]byte, int, error), dry bool) (int, error) {
	in, err := os.ReadFile(path) //nolint:gosec // the operator's own file, named on the command line
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}
	out, changed, err := rewrite(x, in)
	if err != nil || dry || changed == 0 {
		return changed, err
	}
	if cerr := copyFile(path, path+".pre-matrixids"); cerr != nil {
		return 0, cerr
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}
	if err = os.WriteFile(path, out, info.Mode().Perm()); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}
	return changed, nil
}

// copyFile keeps a copy of from at to, which must not exist yet.
func copyFile(from, to string) error {
	src, err := os.Open(from) //nolint:gosec // the operator's own file, named on the command line
	if err != nil {
		return fmt.Errorf("keep a copy: %w", err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // beside the operator's file
	if err != nil {
		return fmt.Errorf("keep a copy: %w", err)
	}
	if _, err = io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("keep a copy: %w", err)
	}
	if err = dst.Close(); err != nil {
		return fmt.Errorf("keep a copy: %w", err)
	}
	return nil
}

// sortedKeys is every table any of the maps names, in order.
func sortedKeys(maps ...any) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		switch m := m.(type) {
		case map[string][]string:
			for k := range m {
				seen[k] = true
			}
		case map[string]map[string]func(ids, string) (string, bool, error):
			for k := range m {
				seen[k] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
