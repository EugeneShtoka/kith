package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A new database is stamped with this build's version; reopening changes nothing.
func TestOpenStampsTheVersionAndReopeningIsANoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	cache, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got := stampedVersion(t, cache); got != schemaVersion+len(migrations) {
		t.Errorf("user_version = %d, want %d", got, schemaVersion+len(migrations))
	}
	if saveErr := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); saveErr != nil {
		t.Fatalf("SaveRooms() error = %v", saveErr)
	}
	if closeErr := cache.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}

	again, err := Open(ctx, path, testKey)
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

	// A pre-base shape: a `ts` column, no STRICT, no foreign keys, an unknown table.
	old, err := openRaw(path)
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	if _, seedErr := old.ExecContext(ctx, `
		CREATE TABLE messages(room_id TEXT, event_id TEXT, body TEXT, ts INTEGER);
		INSERT INTO messages VALUES('!a:x', '$1', 'from the old shape', 1700000000000);
		CREATE TABLE thread_reindex(room_id TEXT PRIMARY KEY, cursor TEXT, oldest_ts INTEGER, done INTEGER);
		PRAGMA user_version = 37`); seedErr != nil {
		t.Fatalf("seed old schema: %v", seedErr)
	}
	if closeErr := old.Close(); closeErr != nil {
		t.Fatalf("close old: %v", closeErr)
	}

	cache, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = cache.Close() }()

	// A rebuild runs every migration on top of the base.
	if want := schemaVersion + len(migrations); stampedVersion(t, cache) != want {
		t.Errorf("user_version = %d, want %d", stampedVersion(t, cache), want)
	}
	if tables := tableNames(t, cache); slices.Contains(tables, "thread_reindex") {
		t.Errorf("thread_reindex survived the rebuild: %v", tables)
	}
	if saveErr := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); saveErr != nil {
		t.Fatalf("SaveRooms() into the rebuilt cache: %v", saveErr)
	}
	mustSave(t, cache, mx+"!a:x", domain.Message{ID: mx + "$2", Body: "from the new shape", Timestamp: time.UnixMilli(1)})

	// The old file is kept aside.
	aside := asideFiles(t, path)
	if len(aside) != 1 {
		t.Fatalf("set-aside copies = %v, want exactly one", aside)
	}
	kept, err := openRaw(filepath.Join(filepath.Dir(path), aside[0]))
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
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	mustSave(t, cache, mx+"!a:x", domain.Message{
		ID: mx + "$1", Sender: "matrix:@bob:x", Body: "hello", Timestamp: time.UnixMilli(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.png"},
	})
	if err := cache.SaveMediaSource(ctx, mx+"$1", mx+"!a:x", "mxc://x/abc", ""); err != nil {
		t.Fatalf("SaveMediaSource() error = %v", err)
	}
	if err := cache.SaveMembers(ctx, mx+"!a:x", []domain.Member{{UserID: "matrix:@bob:x", DisplayName: "Bob"}}); err != nil {
		t.Fatalf("SaveMembers() error = %v", err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: mx + "!a:x", Notifications: 2, ReadEvent: mx + "$1"}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	if err := cache.SaveThreadRead(ctx, mx+"!a:x", mx+"$root", mx+"$1", 1); err != nil {
		t.Fatalf("SaveThreadRead() error = %v", err)
	}
	if err := cache.SaveReactions(ctx, []domain.Reaction{
		{ID: mx + "$r", RoomID: mx + "!a:x", Target: mx + "$1", Sender: "matrix:@bob:x", Key: "👍"},
	}); err != nil {
		t.Fatalf("SaveReactions() error = %v", err)
	}
	if err := cache.RecordMention(ctx, mx+"!a:x", "matrix:@bob:x", 1); err != nil {
		t.Fatalf("RecordMention() error = %v", err)
	}
	if err := cache.RecordEmoji(ctx, domain.EmojiReaction, mx+"!a:x", "👍", 1); err != nil {
		t.Fatalf("RecordEmoji() error = %v", err)
	}
	if err := cache.SaveSenderSlots(ctx, mx+"!a:x", map[string]int{"matrix:@bob:x": 3}); err != nil {
		t.Fatalf("SaveSenderSlots() error = %v", err)
	}
	if err := cache.SaveRoomParent(ctx, mx+"!a:x", mx+"!space:x"); err != nil {
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
	if _, err := cache.db.ExecContext(ctx, "DELETE FROM rooms WHERE id = ?", mx+"!a:x"); err != nil {
		t.Fatalf("DELETE FROM rooms: %v", err)
	}
	for _, table := range dependents {
		if n := countRows(t, cache, table); n != 0 {
			t.Errorf("%s kept %d row(s) after its room was deleted", table, n)
		}
	}
}

// Hot queries use their indices, checked from the query plan.
func TestTheHotQueriesUseTheirIndices(t *testing.T) {
	t.Parallel()

	cache := analyzedCache(t)
	mentions, _, ok := searchQuery(domain.SearchRequest{
		Filter: domain.SearchFilter{Mentioned: true}, Limit: 100,
		Rooms: domain.EveryRoom(),
	}, nil)
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
		// Every message that names me, newest first.
		{"mentions list", mentions, "messages_naming_me", false},
		// Who has posted here, ranked; the index covers it.
		{"sender ranking", "SELECT sender, COUNT(*) AS n, MAX(ts_ms) AS recent FROM messages " +
			"WHERE sender != '' GROUP BY sender ORDER BY n DESC, recent DESC", "messages_by_sender", false},
		// A name for each MXID, asked on every mention-dropdown keystroke.
		{"member names", "SELECT user_id, display_name FROM room_members " +
			"WHERE display_name != '' AND user_id IN (?, ?)", "room_members_by_user", false},
		{"recent speakers", "SELECT sender, max(ts_ms) AS recent FROM messages " +
			"WHERE room_id = ? AND sender != '' GROUP BY sender ORDER BY recent DESC LIMIT ?", "messages_by_room_sender", false},
		// Which message a redacted edit shows on (EditShownBy, revertShownEdit).
		{"edit a redaction names", "SELECT event_id FROM message_edit WHERE room_id = ? AND revision_id = ?",
			"message_edit_by_revision", false},
		// A room's newest edit, on every mark-read (NewestTS).
		{"newest revision", "SELECT max(ts_ms) FROM message_revisions WHERE room_id = ?", "message_revisions_by_time", false},
		// (time, event ID) order: the index carries the tie-breaker, so none sorts.
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

// Milliseconds survive the round trip (ts_ms is named for its unit; seconds in it
// read as 1970).
func TestTimestampsRoundTripAsMilliseconds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	at := time.Date(2026, 9, 3, 16, 45, 30, 123_000_000, time.UTC)
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	mustSave(t, cache, mx+"!a:x", domain.Message{ID: mx + "$1", Body: "when", Timestamp: at})

	msgs, err := cache.Messages(ctx, mx+"!a:x", 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Messages() = %v, %v; want the one message", msgs, err)
	}
	if got := msgs[0].Timestamp; !got.Equal(at) {
		t.Errorf("Timestamp = %v, want %v", got, at)
	}
	ts, ok, err := cache.MessageTS(ctx, mx+"!a:x", mx+"$1")
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
	// A truncated iteration would read as a missing table.
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

// queryPlan is EXPLAIN QUERY PLAN as one string. Parameters bind to NULL; the plan
// depends on the statement, not the values.
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

// analyzedCache is a cache shaped like a used one, with ANALYZE run as Open and
// Close do: the planner may choose differently on an empty file.
func analyzedCache(t *testing.T) *Cache {
	t.Helper()
	ctx := context.Background()
	cache := openTemp(t)
	const rooms, perRoom = 40, 60
	senders := []string{"matrix:@me:x", "matrix:@dana:x", "matrix:@sam:x", "matrix:@alex:x", "matrix:@noa:x"}
	joined := make([]domain.Room, rooms)
	for r := range joined {
		joined[r] = domain.Room{ID: domain.RoomID(fmt.Sprintf(mx+"!r%d:x", r)), Name: fmt.Sprintf("Room %d", r)}
	}
	if err := cache.SaveRooms(ctx, mxRooms, joined); err != nil {
		t.Fatal(err)
	}
	for r := range joined {
		room := &joined[r]
		msgs := make([]domain.Message, perRoom)
		for i := range msgs {
			msgs[i] = domain.Message{
				ID: domain.EventID(fmt.Sprintf(mx+"$r%dm%d", r, i)), RoomID: room.ID,
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

// Badge, search and thread-list queries use their indices; any sort is over index
// results, not the table. Search sorts FTS5 matches (rowid order) by time and
// excerpts only the kept page, since a snippet re-tokenizes the body.
func TestTheBadgeSearchAndThreadQueriesUseTheirIndices(t *testing.T) {
	t.Parallel()

	cache := analyzedCache(t)
	search := func(rooms int) string {
		ids := make([]domain.RoomID, rooms)
		for i := range ids {
			ids[i] = domain.RoomID(fmt.Sprintf(mx+"!r%d:x", i))
		}
		q, _, ok := searchQuery(domain.SearchRequest{Filter: domain.SearchFilter{Terms: "hello"}, Rooms: domain.TheseRooms(ids), Limit: 20}, nil)
		if !ok {
			t.Fatal("the search built no query")
		}
		return q
	}
	q := func(query string, _ []any) string { return query }
	me := []string{"matrix:@me:x", "matrix:@slack_me:x", "matrix:@whatsapp_me:x"} // an account and its bridge puppets
	hidden := []string{"matrix:@spam:x"}
	const ownFloor = "SEARCH s USING COVERING INDEX messages_by_room_sender (room_id=? AND sender=? AND thread_root=?)"
	for _, tc := range []struct {
		name  string
		query string
		uses  []string
	}{
		{"every room's badge", q(countUnreadQuery(me, hidden, "")), []string{
			ownFloor, "SEARCH m USING INDEX messages_in_thread (room_id=? AND thread_root=? AND ts_ms>?)",
		}},
		{"one room's badge", q(countUnreadQuery(me, hidden, mx+"!r1:x")), []string{
			ownFloor, "SEARCH m USING INDEX messages_in_thread (room_id=? AND thread_root=? AND ts_ms>?)",
		}},
		{"one room's threads", q(countThreadsQuery(me, hidden, mx+"!r1:x")), []string{
			"SEARCH m USING INDEX messages_threaded (room_id=?)", ownFloor,
		}},
		{"every thread", q(countThreadsQuery(me, hidden, "")), []string{"SCAN m USING INDEX messages_threaded", ownFloor}},
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
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "SCAN" && (f[1] == "m" || f[1] == "s" || f[1] == "p") && !strings.Contains(line, "USING INDEX") {
				t.Errorf("%s scans messages: %q\n%s", tc.name, line, plan)
			}
		}
	}
}

// Without terms, the starred and files lists start from their own rows, not a walk
// of every message.
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
		q, _, ok := searchQuery(domain.SearchRequest{Filter: tc.filter, Limit: 100, Rooms: domain.EveryRoom()}, nil)
		if !ok {
			t.Fatalf("%s: no query", tc.name)
		}
		plan := queryPlan(t, cache, q)
		if first := strings.TrimSpace(strings.SplitN(plan, "\n", 2)[0]); !strings.HasPrefix(first, tc.first) {
			t.Errorf("%s starts with %q, want %s:\n%s", tc.name, first, tc.first, plan)
		}
	}
}
