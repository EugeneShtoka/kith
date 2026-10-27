package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// testAccount is an invented account part, AccountKey's shape.
const testAccount = "0123456789abcdef"

// testKey is the key the tests' caches are encrypted under.
var testKey = bytes.Repeat([]byte{7}, sealeddb.KeyLen)

// freeText is every text column of the cache that holds no ID: the migration must
// leave it alone. With idColumns, jsonColumns and tokenColumns it covers the schema.
var freeText = map[string][]string{
	"drafts":            {"body", "edit_saved", "author"},
	"emoji_usage":       {"kind", "emoji"},
	"ignored_users":     {"account"},
	"message_html":      {"html"},
	"message_media":     {"kind", "name", "mime", "mxc", "file_json"},
	"message_mentions":  {"name"},
	"message_redaction": {"reason"},
	"message_revisions": {"body", "html"},
	"message_tombstone": {"reason"},
	"messages":          {"sender_name", "body"},
	"network_blocks":    {"network"},
	"person_links":      {"source"},
	"person_names":      {"source", "name"},
	"person_verdicts":   {"account", "tag", "filter"},
	"quoted_messages":   {"body"},
	"reaction_refusals": {"protocol", "emoji"},
	"reactions":         {"emoji"},
	"room_handles":      {"handle"},
	"room_members":      {"display_name"},
	"room_networks":     {"network"},
	"room_topics":       {"topic"},
	"rooms":             {"name", "membership", "heroes"},
	"spaces":            {"name", "bridge"},
	"tag_verdicts":      {"tag", "filter"},
}

// seeds are the values the test writes where a bare ID would not fit the column.
var seeds = map[string]string{
	"drafts.mentions":    `[{"UserID":"@m:x","RoomID":"!r:x","Name":"M","Known":""}]`,
	"message_polls.poll": `{"ID":"p","Question":"q?","Ballots":{"@v:x":["a"]},"Rounds":{"@v:x":2}}`,
	"room_gaps.token":    "kith:chain:2:!old:x",
	"rooms.membership":   "join",
}

func contains(m map[string][]string, table, column string) bool {
	return slices.Contains(m[table], column)
}

// Every text column of the cache is classified, and the migration rewrites exactly the
// ID columns: a bare ID in one comes out in kith's form, the same text anywhere else
// comes out as it went in. A column added to the schema later fails here until it is
// put in one list or the other.
func TestTheCacheMigrationRewritesEveryIDColumnAndNoText(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	cache, err := db.Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sealeddb.OpenWith(ctx, path, testKey, cacheSetup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	type column struct{ table, name string }
	var text []column
	tables := query(t, conn, `SELECT name FROM sqlite_master WHERE type = 'table'
		AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'messages_fts%' ORDER BY name`)
	for _, table := range tables {
		names := query(t, conn, "SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
		types := query(t, conn, "SELECT type FROM pragma_table_info(?) ORDER BY cid", table)
		values := make([]any, len(names))
		marks := make([]string, len(names))
		for i, name := range names {
			marks[i] = "?"
			values[i] = 1
			if types[i] == "TEXT" {
				values[i] = "!seed:x"
				if v, ok := seeds[table+"."+name]; ok {
					values[i] = v
				}
				text = append(text, column{table, name})
			}
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO "+table+" ("+strings.Join(names, ", ")+") VALUES ("+
			strings.Join(marks, ", ")+")", values...); err != nil {
			t.Fatalf("seed %s: %v", table, err)
		}
	}
	_ = conn.Close()

	if _, err := migrateCache(ctx, path, testKey, ids{account: testAccount}, false); err != nil {
		t.Fatal(err)
	}

	read := func(c column) string {
		var v string
		if err := raw.QueryRowContext(ctx, "SELECT "+c.name+" FROM "+c.table).Scan(&v); err != nil {
			t.Fatalf("read %s.%s: %v", c.table, c.name, err)
		}
		return v
	}
	kith := "matrix:" + testAccount + "/!seed:x"
	for _, c := range text {
		got := read(c)
		switch {
		case contains(idColumns, c.table, c.name) && contains(freeText, c.table, c.name):
			t.Errorf("%s.%s is listed as both an ID column and text", c.table, c.name)
		case contains(idColumns, c.table, c.name):
			if got != kith {
				t.Errorf("%s.%s = %q, want %q", c.table, c.name, got, kith)
			}
		case contains(freeText, c.table, c.name):
			if want := "!seed:x"; c.table+"."+c.name != "rooms.membership" && got != want {
				t.Errorf("%s.%s is text but came out %q", c.table, c.name, got)
			}
		case jsonColumns[c.table][c.name] != nil, contains(tokenColumns, c.table, c.name):
		default:
			t.Errorf("%s.%s is neither an ID column nor text: classify it", c.table, c.name)
		}
	}
	var mentions []domain.Mention
	if err := json.Unmarshal([]byte(read(column{"drafts", "mentions"})), &mentions); err != nil ||
		mentions[0].UserID != "matrix:@m:x" || mentions[0].RoomID != domain.NativeID(domain.ProtocolMatrix, testAccount, "!r:x") {
		t.Errorf("draft mentions = %+v (%v), want both IDs in kith's form", mentions, err)
	}
	var poll domain.Poll
	if err := json.Unmarshal([]byte(read(column{"message_polls", "poll"})), &poll); err != nil ||
		len(poll.Ballots["matrix:@v:x"]) != 1 || poll.Rounds["matrix:@v:x"] != 2 || poll.Question != "q?" {
		t.Errorf("poll = %+v (%v), want its voters in kith's form", poll, err)
	}
	if got := read(column{"room_gaps", "token"}); got != "kith:chain:2:matrix:"+testAccount+"/!old:x" {
		t.Errorf("chained token = %q, want its room in kith's form", got)
	}
	if _, err := os.Stat(path + ".pre-matrixids"); err != nil {
		t.Errorf("no copy kept: %v", err)
	}
}

// A dry run reports what would change and writes nothing.
func TestADryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	cache, err := db.Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = cache.Close()
	raw, _ := sealeddb.OpenWith(ctx, path, testKey, cacheSetup)
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(ctx, "INSERT INTO rooms (id, name) VALUES ('!a:x', 'A')"); err != nil {
		t.Fatal(err)
	}
	if n, err := migrateCache(ctx, path, testKey, ids{account: testAccount}, true); err != nil || n != 1 {
		t.Fatalf("dry run = %d, %v; want one value counted", n, err)
	}
	var id string
	if err := raw.QueryRowContext(ctx, "SELECT id FROM rooms").Scan(&id); err != nil || id != "!a:x" {
		t.Errorf("after a dry run the room is %q (%v), want it untouched", id, err)
	}
	if _, err := os.Stat(path + ".pre-matrixids"); err == nil {
		t.Error("a dry run kept a copy")
	}
}

// The config keeps its comments, order and spacing; IDs alone or after room: and
// thread: are rewritten, the account's own MXID and text are not.
func TestTheConfigKeepsItsShape(t *testing.T) {
	in := `homeserver = "https://example.org"
user = "@me:example.org"

[display]
# rtl = ["!commented:x"]
rtl = ["space:Friends", "!family:x"] # Hebrew
[[display.identity]]
alias = "@me:example.org"
ids = [
  "@me:example.org",
  "whatsapp:15550100001",
]
[[display.name]]
target = "thread:$root"
name = "@not:an-id"
[[keys.jump]]
target = "room:!dana:x"
chord = "!"
`
	want := `homeserver = "https://example.org"
user = "@me:example.org"

[display]
# rtl = ["!commented:x"]
rtl = ["space:Friends", "matrix:0123456789abcdef/!family:x"] # Hebrew
[[display.identity]]
alias = "@me:example.org"
ids = [
  "matrix:@me:example.org",
  "whatsapp:15550100001",
]
[[display.name]]
target = "thread:matrix:0123456789abcdef/$root"
name = "@not:an-id"
[[keys.jump]]
target = "room:matrix:0123456789abcdef/!dana:x"
chord = "!"
`
	got, n, err := rewriteTOML(ids{account: testAccount}, []byte(in))
	if err != nil || string(got) != want || n != 4 {
		t.Errorf("rewriteTOML = %d, %v:\n%s\nwant 4:\n%s", n, err, got, want)
	}
}

// The schedule queue is TOML too: its room, thread, reply and mentions are IDs, its
// body is text even when it is only an MXID.
func TestTheScheduleQueueIsRewritten(t *testing.T) {
	in := `[[message]]
id = "1"
room = "!r:x"
body = "@dana:x"
reply_to = "$e"
at = "2026-10-24T10:00:00Z"
written = "2026-10-24T09:00:00Z"

[[message.mention]]
user_id = "@dana:x"
name = "Dana"
`
	got, n, err := rewriteTOML(ids{account: testAccount}, []byte(in))
	if err != nil || n != 3 || !strings.Contains(string(got), `room = "matrix:0123456789abcdef/!r:x"`) ||
		!strings.Contains(string(got), `body = "@dana:x"`) || !strings.Contains(string(got), `user_id = "matrix:@dana:x"`) {
		t.Errorf("rewriteTOML = %d, %v:\n%s", n, err, got)
	}
}

// Each ledger line's room is rewritten; a line kith did not write is left as it is.
func TestTheLedgerIsRewritten(t *testing.T) {
	line, err := json.Marshal(agent.Entry{Room: "!r:x", Author: "bot", Outcome: "sent", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	in := string(line) + "\nnot json\n"
	got, n, err := rewriteLedger(ids{account: testAccount}, []byte(in))
	if err != nil || n != 1 {
		t.Fatalf("rewriteLedger = %d, %v", n, err)
	}
	first, rest, _ := strings.Cut(string(got), "\n")
	var e agent.Entry
	if err := json.Unmarshal([]byte(first), &e); err != nil || e.Room != domain.RoomID("matrix:"+testAccount+"/!r:x") || e.Text != "hi" {
		t.Errorf("entry = %+v (%v)", e, err)
	}
	if rest != "not json\n" {
		t.Errorf("an unreadable line came out %q", rest)
	}
}

func query(t *testing.T, conn interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, q string, args ...any) []string {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
