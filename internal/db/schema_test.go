package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A database this build made is at this build's version, and opening it again
// changes nothing.
func TestOpenStampsTheVersionAndReopeningIsANoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got := stampedVersion(t, cache); got != schemaVersion+len(migrations) {
		t.Errorf("user_version = %d, want %d", got, schemaVersion+len(migrations))
	}
	if saveErr := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); saveErr != nil {
		t.Fatalf("SaveRooms() error = %v", saveErr)
	}
	if closeErr := cache.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}

	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = again.Close() }()
	rooms, err := again.Rooms(ctx)
	if err != nil || len(rooms) != 1 {
		t.Errorf("Rooms() after reopen = %v, %v; want the room to survive", rooms, err)
	}
	if entries := asideFiles(t, path); len(entries) != 0 {
		t.Errorf("reopening set the cache aside: %v", entries)
	}
}

// A cache from before the base schema is set aside and rebuilt.
func TestOpenRebuildsAnOlderCache(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	// A pre-base cache, in an older shape: a `ts` column, no
	// STRICT, no foreign keys, and a table this schema has never heard of.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	if _, seedErr := old.ExecContext(ctx, `
		CREATE TABLE messages(room_id TEXT, event_id TEXT, body TEXT, ts INTEGER);
		INSERT INTO messages VALUES('!a:x', '$1', 'from the old shape', 1700000000000);
		CREATE TABLE thread_reindex(room_id TEXT PRIMARY KEY, cursor TEXT, oldest_ts INTEGER, done INTEGER);
		PRAGMA user_version = 17`); seedErr != nil {
		t.Fatalf("seed old schema: %v", seedErr)
	}
	if closeErr := old.Close(); closeErr != nil {
		t.Fatalf("close old: %v", closeErr)
	}

	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = cache.Close() }()

	// The base, then every migration on top of it — a rebuilt database ends up at the
	// current schema, not parked at the base.
	if want := schemaVersion + len(migrations); stampedVersion(t, cache) != want {
		t.Errorf("user_version = %d, want %d", stampedVersion(t, cache), want)
	}
	// The table it had and this schema does not is gone, and the one they share has
	// this schema's columns.
	if tables := tableNames(t, cache); slices.Contains(tables, "thread_reindex") {
		t.Errorf("thread_reindex survived the rebuild: %v", tables)
	}
	if saveErr := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); saveErr != nil {
		t.Fatalf("SaveRooms() into the rebuilt cache: %v", saveErr)
	}
	mustSave(t, cache, "!a:x", domain.Message{ID: "$2", Body: "from the new shape", Timestamp: time.UnixMilli(1)})

	// The old file is still there, under a name that says what it is.
	aside := asideFiles(t, path)
	if len(aside) != 1 {
		t.Fatalf("set-aside copies = %v, want exactly one", aside)
	}
	kept, err := sql.Open("sqlite", filepath.Join(filepath.Dir(path), aside[0]))
	if err != nil {
		t.Fatalf("open the copy: %v", err)
	}
	defer func() { _ = kept.Close() }()
	var body string
	if err := kept.QueryRowContext(ctx, "SELECT body FROM messages").Scan(&body); err != nil {
		t.Fatalf("read the copy: %v", err)
	}
	if body != "from the old shape" {
		t.Errorf("copy holds %q, want the old row", body)
	}
}

// One DELETE empties everything about a room.
func TestDeletingARoomCascades(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	mustSave(t, cache, "!a:x", domain.Message{
		ID: "$1", Sender: "@bob:x", Body: "hello", Timestamp: time.UnixMilli(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.png"},
	})
	if err := cache.SaveMediaSource(ctx, "$1", "!a:x", "mxc://x/abc", ""); err != nil {
		t.Fatalf("SaveMediaSource() error = %v", err)
	}
	if err := cache.SaveMembers(ctx, "!a:x", []domain.Member{{UserID: "@bob:x", DisplayName: "Bob"}}); err != nil {
		t.Fatalf("SaveMembers() error = %v", err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", Notifications: 2, ReadEvent: "$1"}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	if err := cache.SaveThreadRead(ctx, "!a:x", "$root", "$1", 1); err != nil {
		t.Fatalf("SaveThreadRead() error = %v", err)
	}
	if err := cache.SaveReactions(ctx, []domain.Reaction{
		{ID: "$r", RoomID: "!a:x", Target: "$1", Sender: "@bob:x", Key: "👍"},
	}); err != nil {
		t.Fatalf("SaveReactions() error = %v", err)
	}
	if err := cache.RecordMention(ctx, "!a:x", "@bob:x", 1); err != nil {
		t.Fatalf("RecordMention() error = %v", err)
	}
	if err := cache.RecordEmoji(ctx, domain.EmojiReaction, "!a:x", "👍", 1); err != nil {
		t.Fatalf("RecordEmoji() error = %v", err)
	}
	if err := cache.SaveSenderSlots(ctx, "!a:x", map[string]int{"@bob:x": 3}); err != nil {
		t.Fatalf("SaveSenderSlots() error = %v", err)
	}
	if err := cache.SaveRoomParent(ctx, "!a:x", "!space:x"); err != nil {
		t.Fatalf("SaveRoomParent() error = %v", err)
	}

	dependents := []string{
		"messages", "message_media", "room_members", "room_unread", "thread_read",
		"reactions", "mention_usage", "emoji_usage", "sender_slots", "room_parents",
	}
	for _, table := range dependents {
		if n := countRows(t, cache, table); n == 0 {
			t.Fatalf("%s is empty before the delete, so it proves nothing", table)
		}
	}
	if _, err := cache.db.ExecContext(ctx, "DELETE FROM rooms WHERE id = ?", "!a:x"); err != nil {
		t.Fatalf("DELETE FROM rooms: %v", err)
	}
	for _, table := range dependents {
		if n := countRows(t, cache, table); n != 0 {
			t.Errorf("%s kept %d row(s) after its room was deleted", table, n)
		}
	}
}

// The three queries the live cache measurably scanned. Each has an index named
// after it now, and an index nobody's plan mentions is an index that is not
// earning its writes — so the plan is what the test reads.
func TestTheHotQueriesUseTheirIndices(t *testing.T) {
	t.Parallel()

	cache := analyzedCache(t)
	mentions, _, ok := searchQuery(domain.SearchRequest{
		Filter: domain.SearchFilter{Mentioned: true}, Limit: 100,
		Rooms: domain.EveryRoom(),
	})
	if !ok {
		t.Fatal("the mentions list built no query")
	}
	for _, tc := range []struct {
		name  string
		query string
		index string
		// inOrder is a query the index must return in order, with no sort step.
		inOrder bool
	}{
		// Every message that names me, newest first: 115 rows out of 13,355, which
		// used to mean reading all 13,355 and sorting them.
		{"mentions list", mentions, "messages_naming_me", false},
		// Who has posted here, ranked. The index covers it — sender and ts_ms are
		// both in it — so the ranking never touches the table.
		{"sender ranking", "SELECT sender, COUNT(*) AS n, MAX(ts_ms) AS recent FROM messages " +
			"WHERE sender != '' GROUP BY sender ORDER BY n DESC, recent DESC", "messages_by_sender", false},
		// A name for each MXID, which the mention dropdown asks on every keystroke.
		{"member names", "SELECT user_id, display_name FROM room_members " +
			"WHERE display_name != '' AND user_id IN (?, ?)", "room_members_by_user", false},
		{"recent speakers", "SELECT sender, max(ts_ms) AS recent FROM messages " +
			"WHERE room_id = ? AND sender != '' GROUP BY sender ORDER BY recent DESC LIMIT ?", "messages_by_room_sender", false},
		// Which message a redacted edit event shows on, on every redaction of an event
		// the cache holds no row for (EditShownBy, revertShownEdit).
		{"edit a redaction names", "SELECT event_id FROM message_edit WHERE room_id = ? AND revision_id = ?",
			"message_edit_by_revision", false},
		// A room's newest edit, on every mark-read (NewestTS).
		{"newest revision", "SELECT max(ts_ms) FROM message_revisions WHERE room_id = ?", "message_revisions_by_time", false},
		// The timeline, a window around one message, and a thread, in (time, event ID)
		// order: the index carries the tie-breaker, so none of them sorts.
		{"timeline page", messageSelect + " WHERE m.room_id = ? ORDER BY m.ts_ms DESC, m.event_id DESC LIMIT ?", "messages_by_time", true},
		{"window before", messageSelect + " WHERE m.room_id = ? AND (m.ts_ms, m.event_id) < (?, ?) " +
			"ORDER BY m.ts_ms DESC, m.event_id DESC LIMIT ?", "messages_by_time", true},
		{"window after", messageSelect + " WHERE m.room_id = ? AND (m.ts_ms, m.event_id) >= (?, ?) " +
			"ORDER BY m.ts_ms ASC, m.event_id ASC LIMIT ?", "messages_by_time", true},
		{"thread page", messageSelect + " WHERE m.room_id = ? AND m.thread_root = ? " +
			"ORDER BY m.ts_ms DESC, m.event_id DESC LIMIT ?", "messages_in_thread", true},
	} {
		plan := queryPlan(t, cache, tc.query)
		if !strings.Contains(plan, tc.index) {
			t.Errorf("%s does not use %s:\n%s", tc.name, tc.index, plan)
		}
		if strings.Contains(plan, "SCAN messages\n") || strings.Contains(plan, "SCAN room_members\n") ||
			strings.Contains(plan, "TEMP B-TREE FOR GROUP BY") {
			t.Errorf("%s still scans the table:\n%s", tc.name, plan)
		}
		if tc.inOrder && strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s sorts instead of reading the index in order:\n%s", tc.name, plan)
		}
	}
}

// Milliseconds survive the round trip. The column is named for its unit because
// the unnamed one caused this bug once already — a benchmark seeded seconds into
// `ts` and everything it wrote looked like 1970.
func TestTimestampsRoundTripAsMilliseconds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := time.Date(2026, 9, 3, 16, 45, 30, 123_000_000, time.UTC)
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	mustSave(t, cache, "!a:x", domain.Message{ID: "$1", Body: "when", Timestamp: at})

	msgs, err := cache.Messages(ctx, "!a:x", 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Messages() = %v, %v; want the one message", msgs, err)
	}
	if got := msgs[0].Timestamp; !got.Equal(at) {
		t.Errorf("Timestamp = %v, want %v", got, at)
	}
	ts, ok, err := cache.MessageTS(ctx, "!a:x", "$1")
	if err != nil || !ok {
		t.Fatalf("MessageTS() ok=%v err=%v", ok, err)
	}
	if ts != at.UnixMilli() {
		t.Errorf("MessageTS() = %d, want %d", ts, at.UnixMilli())
	}
}

// stampedVersion reads PRAGMA user_version off a cache.
func stampedVersion(t *testing.T, cache *Cache) int {
	t.Helper()
	version, err := userVersion(context.Background(), cache.db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}
	return version
}

// tableNames is every table the database has, whatever schema made it.
func tableNames(t *testing.T, cache *Cache) []string {
	t.Helper()
	rows, err := cache.db.QueryContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	// A truncated iteration would otherwise read as "the table is not there", which is
	// exactly what several of these assertions are about.
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	return names
}

// countRows counts one table, for the cascade assertions.
func countRows(t *testing.T, cache *Cache, table string) int {
	t.Helper()
	var n int
	// #nosec G202 -- table comes from this test's own list of names.
	if err := cache.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// queryPlan is EXPLAIN QUERY PLAN's answer as one string, so a test can say which
// index a query must use. Parameters are bound to NULL: the plan is chosen from the
// statement, not from the values.
func queryPlan(t *testing.T, cache *Cache, query string) string {
	t.Helper()
	args := make([]any, strings.Count(query, "?"))
	rows, err := cache.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	return plan.String()
}

// asideFiles is the set-aside copies next to path, by name.
func asideFiles(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".pre-schema-") {
			out = append(out, entry.Name())
		}
	}
	return out
}

// analyzedCache is a cache shaped like a used one — rooms of mixed chatter, a few
// senders of whom "me" is one, threads, mentions, read positions — with ANALYZE run,
// as Open and Close do on a real cache: the planner chooses by these statistics, and
// on an empty file it can choose differently.
func analyzedCache(t *testing.T) *Cache {
	t.Helper()
	ctx := context.Background()
	cache := openTemp(t)
	const rooms, perRoom = 40, 60
	senders := []string{"@me:x", "@dana:x", "@sam:x", "@alex:x", "@noa:x"}
	joined := make([]domain.Room, rooms)
	for r := range joined {
		joined[r] = domain.Room{ID: domain.RoomID(fmt.Sprintf("!r%d:x", r)), Name: fmt.Sprintf("Room %d", r)}
	}
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, joined); err != nil {
		t.Fatal(err)
	}
	for r := range joined {
		room := &joined[r]
		msgs := make([]domain.Message, perRoom)
		for i := range msgs {
			msgs[i] = domain.Message{
				ID: domain.EventID(fmt.Sprintf("$r%dm%d", r, i)), RoomID: room.ID,
				Sender: senders[(r+i)%len(senders)], Body: fmt.Sprintf("hello there, message %d", i),
				Timestamp: time.Unix(int64(1_700_000_000+r*perRoom+i), 0), Mentioned: i%17 == 0,
			}
			if i%6 == 5 {
				msgs[i].ThreadRoot = msgs[i-i%6].ID
			}
		}
		if err := cache.SaveMessages(ctx, room.ID, msgs); err != nil {
			t.Fatal(err)
		}
		read := domain.Unread{RoomID: room.ID, ReadEvent: msgs[perRoom/2].ID}
		if err := cache.SaveUnread(ctx, read, msgs[perRoom/2].Timestamp.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cache.db.ExecContext(ctx, "ANALYZE"); err != nil {
		t.Fatal(err)
	}
	return cache
}

// The badge, search and thread-list queries, each pinned to the index it reads. Where a
// plan sorts, the sort is over what the index found, not the table:
//   - your own newest message, the badges' floor, is one seek per room;
//   - a full-text search sorts its matches by time (FTS5 returns rowid order), and
//     excerpts only the page it keeps: a snippet costs a tokenizing of the body;
//   - a thread list orders by each thread's newest reply, an aggregate.
func TestTheBadgeSearchAndThreadQueriesUseTheirIndices(t *testing.T) {
	t.Parallel()

	cache := analyzedCache(t)
	search := func(rooms int) string {
		ids := make([]domain.RoomID, rooms)
		for i := range ids {
			ids[i] = domain.RoomID(fmt.Sprintf("!r%d:x", i))
		}
		q, _, ok := searchQuery(domain.SearchRequest{Filter: domain.SearchFilter{Terms: "hello"}, Rooms: domain.TheseRooms(ids), Limit: 20})
		if !ok {
			t.Fatal("the search built no query")
		}
		return q
	}
	q := func(query string, _ []any) string { return query }
	me := []string{"@me:x", "@slack_me:x", "@whatsapp_me:x"} // an account and its bridge puppets
	const ownFloor = "SEARCH s USING COVERING INDEX messages_by_room_sender (room_id=? AND sender=? AND thread_root=?)"
	for _, tc := range []struct {
		name  string
		query string
		uses  []string
	}{
		{"every room's badge", q(countUnreadQuery(me, "")), []string{
			ownFloor, "SEARCH m USING INDEX messages_in_thread (room_id=? AND thread_root=? AND ts_ms>?)",
		}},
		{"one room's badge", q(countUnreadQuery(me, "!r1:x")), []string{
			ownFloor, "SEARCH m USING INDEX messages_in_thread (room_id=? AND thread_root=? AND ts_ms>?)",
		}},
		{"one room's threads", q(countThreadsQuery(me, "!r1:x")), []string{
			"SEARCH m USING INDEX messages_threaded (room_id=?)", ownFloor,
		}},
		{"every thread", q(countThreadsQuery(me, "")), []string{"SCAN m USING INDEX messages_threaded", ownFloor}},
		{"search in some rooms", search(3), []string{"MATERIALIZE page", "SCAN f VIRTUAL TABLE", "MATERIALIZE excerpt"}},
		{"search in very many rooms", search(jsonListAt + 1), []string{"json_each", "SCAN f VIRTUAL TABLE", "MATERIALIZE excerpt"}},
	} {
		plan := queryPlan(t, cache, tc.query)
		for _, want := range tc.uses {
			if !strings.Contains(plan, want) {
				t.Errorf("%s: the plan lacks %q:\n%s", tc.name, want, plan)
			}
		}
		for line := range strings.SplitSeq(plan, "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "SCAN" && (f[1] == "m" || f[1] == "s") && !strings.Contains(line, "USING INDEX") {
				t.Errorf("%s scans messages: %q\n%s", tc.name, line, plan)
			}
		}
	}
}

// The starred list and the files list, with no terms, are driven from the few rows
// they name, not a walk of every message newest first.
func TestTheStarredAndFileListsStartFromTheirOwnRows(t *testing.T) {
	t.Parallel()
	cache := analyzedCache(t)
	for _, tc := range []struct {
		name   string
		filter domain.SearchFilter
		first  string
	}{
		{"starred", domain.SearchFilter{Starred: true}, "SCAN s"},
		{"files", domain.SearchFilter{HasFile: true}, "SCAN mm"},
		{"starred files", domain.SearchFilter{Starred: true, HasFile: true}, "SCAN s"},
	} {
		q, _, ok := searchQuery(domain.SearchRequest{Filter: tc.filter, Limit: 100, Rooms: domain.EveryRoom()})
		if !ok {
			t.Fatalf("%s: no query", tc.name)
		}
		plan := queryPlan(t, cache, q)
		if first := strings.TrimSpace(strings.SplitN(plan, "\n", 2)[0]); !strings.HasPrefix(first, tc.first) {
			t.Errorf("%s starts with %q, want %s:\n%s", tc.name, first, tc.first, plan)
		}
	}
}
