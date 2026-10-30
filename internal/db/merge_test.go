package db

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp direct chat known by the person's LID, and by their number. Invented.
const (
	lidRoom     = domain.RoomID("whatsapp:15550100/88001234567@lid")
	numberRoom  = domain.RoomID("whatsapp:15550100/15550199@s.whatsapp.net")
	lidPerson   = "whatsapp:88001234567@lid"
	numberPeson = "whatsapp:15550199@s.whatsapp.net"
	mergeSpace  = "whatsapp:15550100/space"
)

// roomRow is one fixture row of a table with a room_id: ?1 the room, ?2 an event, ?3
// a time, ?4 a text that differs between the rooms. perEvent rows are written once for
// each message a room holds.
type roomRow struct {
	table    string
	insert   string
	perEvent bool
}

// roomRows writes a row in every table with a room_id. TestTheMergeFixtureFillsEveryRoomTable
// holds it complete: a table added later fails until it has a row here.
var roomRows = []roomRow{
	{"messages", `INSERT INTO messages (room_id, event_id, sender, body, ts_ms) VALUES (?1, ?2, ?4, ?4, ?3)`, true},
	{"message_media", `INSERT INTO message_media (room_id, event_id, kind) VALUES (?1, ?2, 'image')`, true},
	{"message_order", `INSERT INTO message_order (room_id, event_id, seq) VALUES (?1, ?2, ?3)`, true},
	{"message_mentions", `INSERT INTO message_mentions (room_id, event_id, user_id, name) VALUES (?1, ?2, ?4, ?4)`, true},
	{"message_redaction", `INSERT INTO message_redaction (room_id, event_id, by) VALUES (?1, ?2, ?4)`, true},
	{"message_redaction_at", `INSERT INTO message_redaction_at (room_id, event_id, ts_ms) VALUES (?1, ?2, ?3)`, true},
	{"message_html", `INSERT INTO message_html (room_id, event_id, html) VALUES (?1, ?2, ?4)`, true},
	{"message_edit", `INSERT INTO message_edit (room_id, event_id, revision_id, ts_ms) VALUES (?1, ?2, ?4, ?3)`, true},
	{"message_revisions", `INSERT INTO message_revisions (room_id, event_id, revision_id, body) VALUES (?1, ?2, ?4, ?4)`, true},
	{"message_expiry", `INSERT INTO message_expiry (room_id, event_id, seconds) VALUES (?1, ?2, 60)`, true},
	{"message_polls", `INSERT INTO message_polls (room_id, event_id, poll) VALUES (?1, ?2, '{}')`, true},
	{"message_tombstone", `INSERT INTO message_tombstone (room_id, event_id, ts_ms) VALUES (?1, ?2 || '-gone', ?3)`, true},
	{"quoted_messages", `INSERT INTO quoted_messages (room_id, event_id, body) VALUES (?1, ?2 || '-quoted', ?4)`, true},
	{"starred", `INSERT INTO starred (room_id, event_id, at_ms) VALUES (?1, ?2, ?3)`, true},
	{"reactions", `INSERT INTO reactions (event_id, room_id, target_event, emoji) VALUES (?2 || '-' || ?4, ?1, ?2, 'x')`, true},
	{"room_topics", `INSERT INTO room_topics (room_id, topic) VALUES (?1, ?4)`, false},
	{"tag_verdicts", `INSERT INTO tag_verdicts (room_id, tag, rule, filter, at_ms) VALUES (?1, 'work', 1, ?4, ?3)`, false},
	{"space_children", `INSERT INTO space_children (space_id, room_id) VALUES ('` + mergeSpace + `', ?1)`, false},
	{"room_networks", `INSERT INTO room_networks (room_id, network) VALUES (?1, ?4)`, false},
	{"room_parents", `INSERT INTO room_parents (room_id, resolved_ms) VALUES (?1, ?3)`, false},
	{"room_archived", `INSERT INTO room_archived (room_id) VALUES (?1)`, false},
	{"room_forums", `INSERT INTO room_forums (room_id) VALUES (?1)`, false},
	{"room_sms", `INSERT INTO room_sms (room_id) VALUES (?1)`, false},
	{"room_history_start", `INSERT INTO room_history_start (room_id) VALUES (?1)`, false},
	{"room_gaps", `INSERT INTO room_gaps (room_id, boundary, since_ms, until_ms, token, announce) VALUES (?1, ?4, 1, ?3, ?4, 0)`, false},
	{"room_read_only", `INSERT INTO room_read_only (room_id) VALUES (?1)`, false},
	{"room_handles", `INSERT INTO room_handles (room_id, handle) VALUES (?1, ?4)`, false},
	{"room_timers", `INSERT INTO room_timers (room_id, seconds) VALUES (?1, 60)`, false},
	{"room_timer_versions", `INSERT INTO room_timer_versions (room_id, version) VALUES (?1, ?3)`, false},
	{"room_upgrades", `INSERT INTO room_upgrades (room_id, checked_ms) VALUES (?1, ?3)`, false},
	{"room_members", `INSERT INTO room_members (room_id, user_id, display_name) VALUES (?1, ?4, ?4)`, false},
	{"room_unread", `INSERT INTO room_unread (room_id, read_event, read_ts_ms) VALUES (?1, ?2, ?3)`, false},
	{"thread_read", `INSERT INTO thread_read (room_id, root_event, read_event, read_ts_ms) VALUES (?1, 'root', ?2, ?3)`, false},
	{"drafts", `INSERT INTO drafts (room_id, body, updated_ms) VALUES (?1, 'words from ' || ?4, ?3)`, false},
	{"draft_threads", `INSERT INTO draft_threads (room_id, thread_root) VALUES (?1, 'root')`, false},
	{"mention_usage", `INSERT INTO mention_usage (room_id, user_id, count, last_used_ms) VALUES (?1, 'someone', 2, ?3)`, false},
	{"emoji_usage", `INSERT INTO emoji_usage (room_id, kind, emoji, count, last_used_ms) VALUES (?1, 'reaction', 'x', 3, ?3)`, false},
	{"sender_slots", `INSERT INTO sender_slots (room_id, group_key, slot) VALUES (?1, ?4, 1)`, false},
}

// notFixtured are the tables with a room_id the fixture writes no row of itself:
// numbered by the cache as it commits, empty between transactions, or written by the
// merge under test.
var notFixtured = []string{"message_changes", "message_touched", "room_aliases"}

// roomTables is every table with a room_id column, and each one's key columns but it.
func roomTables(t *testing.T, cache *Cache) map[string][]string {
	t.Helper()
	ctx := context.Background()
	tables, err := collect(ctx, cache.db, "tables", `
		SELECT m.name, c.name, c.pk FROM sqlite_master m, pragma_table_info(m.name) c
		 WHERE m.type = 'table' AND m.name IN
		       (SELECT m2.name FROM sqlite_master m2, pragma_table_info(m2.name) c2
		         WHERE m2.type = 'table' AND c2.name = 'room_id')
		 ORDER BY m.name, c.pk`,
		func(rows *sql.Rows) ([3]string, error) {
			var table, column string
			var pk int
			err := rows.Scan(&table, &column, &pk)
			return [3]string{table, column, fmt.Sprint(pk)}, err
		})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, row := range tables {
		if _, ok := out[row[0]]; !ok {
			out[row[0]] = nil
		}
		if row[2] != "0" && row[1] != "room_id" {
			out[row[0]] = append(out[row[0]], row[1])
		}
	}
	return out
}

func TestTheMergeFixtureFillsEveryRoomTable(t *testing.T) {
	t.Parallel()
	cache := openTemp(t)
	for table := range roomTables(t, cache) {
		if slices.Contains(notFixtured, table) {
			continue
		}
		if !slices.ContainsFunc(roomRows, func(r roomRow) bool { return r.table == table }) {
			t.Errorf("%s has a room_id but no fixture row: add it to roomRows, so the merge is checked to keep it", table)
		}
	}
}

// mergeSide is what one room holds in a generated case: its messages, and which of the
// one-per-room rows (by table) with what time.
type mergeSide struct {
	events []string
	rows   map[string]int64
	// membership is the room's row ("join", or "" for a placeholder); absent, it has none.
	membership string
	absent     bool
}

// keyed is a table's rows of one room, each by its key columns but room_id.
func keyed(t *testing.T, cache *Cache, table string, key []string, room domain.RoomID) map[string]bool {
	t.Helper()
	cols := "'row'"
	if len(key) > 0 {
		cols = strings.Join(key, " || '|' || ")
	}
	got, err := collect(context.Background(), cache.db, table,
		`SELECT `+cols+` FROM `+quoteIdent(table)+` WHERE room_id = ?`,
		func(rows *sql.Rows) (string, error) {
			var k string
			err := rows.Scan(&k)
			return k, err
		}, string(room))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, k := range got {
		out[k] = true
	}
	return out
}

// writeSide writes what a room holds, as the cache would have: in one transaction,
// numbered as it commits.
func writeSide(t *testing.T, cache *Cache, room domain.RoomID, text string, side mergeSide) {
	t.Helper()
	ctx := context.Background()
	if side.absent {
		return
	}
	err := cache.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rooms (id, name, is_direct, membership) VALUES (?, ?, 1, ?)`,
			string(room), text, side.membership); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO spaces (id) VALUES (?) ON CONFLICT DO NOTHING`, mergeSpace); err != nil {
			return err
		}
		// Only the parameters a statement names are bound.
		args := func(row roomRow, all ...any) []any {
			n := 0
			for i := range all {
				if strings.Contains(row.insert, fmt.Sprintf("?%d", i+1)) {
					n = i + 1
				}
			}
			return all[:n]
		}
		for _, row := range roomRows {
			if row.perEvent {
				for _, event := range side.events {
					// A message is sent once: both rooms' copies carry its time.
					at := int64(1000 + slices.Index(mergeEvents, event))
					if _, err := tx.ExecContext(ctx, row.insert, args(row, string(room), event, at, text)...); err != nil {
						return fmt.Errorf("%s: %w", row.table, err)
					}
				}
				continue
			}
			at, ok := side.rows[row.table]
			if !ok {
				continue
			}
			event := ""
			if len(side.events) > 0 {
				event = side.events[len(side.events)-1]
			}
			if _, err := tx.ExecContext(ctx, row.insert, args(row, string(room), event, at, text)...); err != nil {
				return fmt.Errorf("%s: %w", row.table, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("write %s: %v", room, err)
	}
}

// mergeEvents are the messages either room may hold.
var mergeEvents = []string{"whatsapp:15550100/3EB0A1", "whatsapp:15550100/3EB0A2", "whatsapp:15550100/3EB0A3"}

// randomSide is a room's holdings: some of the events, some of the per-room rows,
// each at a time from a small range so the two rooms tie at times.
func randomSide(r *rand.Rand) mergeSide {
	side := mergeSide{rows: map[string]int64{}, membership: "join"}
	for _, e := range mergeEvents {
		if r.IntN(2) == 0 {
			side.events = append(side.events, e)
		}
	}
	for _, row := range roomRows {
		if !row.perEvent && r.IntN(3) > 0 {
			side.rows[row.table] = int64(1 + r.IntN(4))
		}
	}
	if _, ok := side.rows["draft_threads"]; ok {
		side.rows["drafts"] = max(side.rows["drafts"], 1)
	}
	return side
}

// TestAMergedRoomKeepsWhatEitherHeld merges a LID room into its number's over every
// table with a room_id, each side holding a random part: both holding messages (some
// the same), drafts, read positions, verdicts, usage, members, rows of one side only or
// of neither; the person's verdicts under each ID; and the merge run again.
func TestAMergedRoomKeepsWhatEitherHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(60) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			r := rand.New(rand.NewPCG(seed, 43))
			cache := openTemp(t)
			lid, number := randomSide(r), randomSide(r)
			// The number's room not cached yet, or only as a placeholder a write registered.
			switch r.IntN(4) {
			case 0:
				number = mergeSide{absent: true}
			case 1:
				number.membership = ""
			}
			writeSide(t, cache, lidRoom, "lid", lid)
			writeSide(t, cache, numberRoom, "number", number)
			verdicts := map[string]int64{}
			for _, person := range []string{lidPerson, numberPeson} {
				if r.IntN(3) == 0 {
					continue
				}
				at := int64(1 + r.IntN(4))
				verdicts[person] = at
				if _, err := cache.db.ExecContext(ctx, `INSERT INTO person_verdicts (person, tag, rule, at_ms) VALUES (?, 'work', 1, ?)`, person, at); err != nil {
					t.Fatal(err)
				}
			}
			tables := roomTables(t, cache)
			before := map[string]map[string]bool{}
			for table, key := range tables {
				before[table] = keyed(t, cache, table, key, lidRoom)
				for k := range keyed(t, cache, table, key, numberRoom) {
					before[table][k] = true
				}
			}
			_, last, err := cache.LastChange(ctx)
			if err != nil {
				t.Fatal(err)
			}
			readBefore := max(readAt(t, cache, lidRoom), readAt(t, cache, numberRoom))

			merge := domain.RoomMerge{From: lidRoom, Into: numberRoom, FromPerson: lidPerson, IntoPerson: numberPeson}
			changed, err := cache.MergeRoom(ctx, merge)
			if err != nil || !changed {
				t.Fatalf("MergeRoom = %v, %v; want a change", changed, err)
			}

			for table, key := range tables {
				if gone := keyed(t, cache, table, key, lidRoom); len(gone) > 0 && table != "room_aliases" {
					t.Errorf("%s still holds the LID room's %v", table, gone)
				}
				// Members and a draft's thread are one side's: the person by the ID
				// the number's room knows them by, and the newer draft's targets.
				if slices.Contains([]string{"message_touched", "room_aliases", "room_members", "draft_threads"}, table) {
					continue
				}
				after := keyed(t, cache, table, key, numberRoom)
				for k := range before[table] {
					if !after[k] {
						t.Errorf("%s lost %q in the merge", table, k)
					}
				}
			}
			// Every message the LID room held is numbered anew, so a resuming client
			// reads it under the number's room.
			for _, event := range lid.events {
				var change int64
				if serr := cache.db.QueryRowContext(ctx, `SELECT change FROM message_changes WHERE room_id = ? AND event_id = ?`,
					string(numberRoom), event).Scan(&change); serr != nil || change <= last {
					t.Errorf("%s is numbered %d (%v), want past %d", event, change, serr, last)
				}
			}
			if got := readAt(t, cache, numberRoom); got != readBefore {
				t.Errorf("the read position is at %d, want the later of the two, %d", got, readBefore)
			}
			checkJoinedRows(t, cache, lid, number)
			checkPersonVerdicts(t, cache, verdicts)
			var membership string
			if serr := cache.db.QueryRowContext(ctx, `SELECT membership FROM rooms WHERE id = ?`, string(numberRoom)).Scan(&membership); serr != nil || membership != "join" {
				t.Errorf("the number's room is %q (%v), want joined as the LID's was", membership, serr)
			}

			// A window still on the LID room saves its draft: it joins the number's.
			late := domain.StoredDraft{RoomID: lidRoom, Body: "typed as it merged"}
			if r.IntN(2) == 0 {
				_, err = cache.ReplaceDraft(ctx, late, domain.StoredDraft{})
			} else {
				_, _, err = cache.RebaseDraft(ctx, late, domain.StoredDraft{}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if gone := keyed(t, cache, "drafts", nil, lidRoom); len(gone) > 0 {
				t.Errorf("a late draft wrote the LID room back")
			}

			// Run again (a pass cut off and started over): nothing more changes.
			again, err := cache.MergeRoom(ctx, merge)
			if err != nil || again {
				t.Fatalf("MergeRoom again = %v, %v; want no change", again, err)
			}
			aliases, err := cache.RoomAliases(ctx)
			if err != nil || aliases[lidRoom] != numberRoom {
				t.Fatalf("RoomAliases = %v, %v; want the LID room kept as merged", aliases, err)
			}
		})
	}
}

// readAt is where room's read position stands, 0 for none.
func readAt(t *testing.T, cache *Cache, room domain.RoomID) int64 {
	t.Helper()
	var at int64
	if _, err := optional(cache.db.QueryRowContext(context.Background(), `
		SELECT `+readPosition+` FROM room_unread u
		  LEFT JOIN messages r ON r.room_id = u.room_id AND r.event_id = u.read_event
		 WHERE u.room_id = ?`, string(room)).Scan(&at)); err != nil {
		t.Fatal(err)
	}
	return at
}

// checkJoinedRows checks the rows the merge joins rather than keeps one of.
func checkJoinedRows(t *testing.T, cache *Cache, lid, number mergeSide) {
	t.Helper()
	ctx := context.Background()
	scalar := func(query string) int64 {
		var v int64
		if _, err := optional(cache.db.QueryRowContext(ctx, query, string(numberRoom)).Scan(&v)); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if want := max(lid.rows["tag_verdicts"], number.rows["tag_verdicts"]); scalar(`SELECT at_ms FROM tag_verdicts WHERE room_id = ?`) != want {
		t.Errorf("the room's verdict is not the newer, %d", want)
	}
	if want := max(lid.rows["thread_read"], number.rows["thread_read"]); scalar(`SELECT read_ts_ms FROM thread_read WHERE room_id = ?`) != want {
		t.Errorf("the thread's read position is not the later, %d", want)
	}
	for table, each := range map[string]int64{"mention_usage": 2, "emoji_usage": 3} {
		want := int64(0)
		for _, side := range []mergeSide{lid, number} {
			if _, ok := side.rows[table]; ok {
				want += each
			}
		}
		if got := scalar(`SELECT count FROM ` + table + ` WHERE room_id = ?`); got != want {
			t.Errorf("%s counts %d, want both rooms' %d", table, got, want)
		}
	}
	var body string
	if _, err := optional(cache.db.QueryRowContext(ctx, `SELECT body FROM drafts WHERE room_id = ?`, string(numberRoom)).Scan(&body)); err != nil {
		t.Fatal(err)
	}
	// The newer draft's targets: the number's on a tie.
	newer := number
	if lid.rows["drafts"] > number.rows["drafts"] {
		newer = lid
	}
	_, threaded := newer.rows["draft_threads"]
	if got := scalar(`SELECT count(*) FROM draft_threads WHERE room_id = ?`) == 1; body != "" && got != threaded {
		t.Errorf("the draft is in a thread: %v, want the newer draft's: %v", got, threaded)
	}
	_, lidMembers := lid.rows["room_members"]
	_, numberMembers := number.rows["room_members"]
	if got := scalar(`SELECT count(*) FROM room_members WHERE room_id = ?`); (got == 1) != (lidMembers || numberMembers) || got > 1 {
		t.Errorf("the room has %d members, want one: the number's room's, else the LID's", got)
	}
	for side, words := range map[string]mergeSide{"words from lid": lid, "words from number": number} {
		if _, ok := words.rows["drafts"]; ok && !strings.Contains(body, side) {
			t.Errorf("the draft %q lost %q", body, side)
		}
	}
}

// checkPersonVerdicts checks the person's verdicts are kept under their number, the
// newer of the two IDs'.
func checkPersonVerdicts(t *testing.T, cache *Cache, verdicts map[string]int64) {
	t.Helper()
	ctx := context.Background()
	var lidRows int
	if err := cache.db.QueryRowContext(ctx, `SELECT count(*) FROM person_verdicts WHERE person = ?`, lidPerson).Scan(&lidRows); err != nil {
		t.Fatal(err)
	}
	if lidRows > 0 {
		t.Errorf("the LID's verdicts stayed under it")
	}
	want := max(verdicts[lidPerson], verdicts[numberPeson])
	var at int64
	if _, err := optional(cache.db.QueryRowContext(ctx, `SELECT at_ms FROM person_verdicts WHERE person = ?`, numberPeson).Scan(&at)); err != nil {
		t.Fatal(err)
	}
	if at != want {
		t.Errorf("the person's verdict is at %d, want the newer %d", at, want)
	}
}
