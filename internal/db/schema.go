package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/ncruces/go-sqlite3/driver"
)

// The database is a cache: every row is a copy of something the homeserver has.
// A version this binary does not recognize (either direction) is set aside and
// rebuilt; versions between the base and current are migrated forward. Every
// per-room table references rooms(id) ON DELETE CASCADE (foreign_keys=ON via DSN).

// schemaVersion is the base schema version stamped in PRAGMA user_version.
const schemaVersion = 1

// baseSchema is the whole database; referenced tables come first.
const baseSchema = `
CREATE TABLE rooms (
	id         TEXT    NOT NULL PRIMARY KEY,
	name       TEXT    NOT NULL DEFAULT '',
	is_direct  INTEGER NOT NULL DEFAULT 0,
	membership TEXT    NOT NULL DEFAULT 'join',
	invited_by TEXT    NOT NULL DEFAULT '',
	heroes     TEXT    NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX rooms_by_membership ON rooms(membership);

-- Side tables for optional per-row facts: a later migration can CREATE one safely,
-- but ALTER TABLE ADD COLUMN has no IF NOT EXISTS.
CREATE TABLE room_topics (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	topic   TEXT NOT NULL DEFAULT ''
) STRICT, WITHOUT ROWID;

-- Local mirror of org.kith.starred account data, so stars survive a rebuild.
CREATE TABLE starred (
	room_id  TEXT    NOT NULL,
	event_id TEXT    NOT NULL,
	at_ms    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- Local mirror of org.kith.tags account data: per room, the tags a filter moved it
-- into (rule by number, filter by name) and the ones a person took it out of
-- (released, which outranks every filter). tag is lower-cased.
CREATE TABLE tag_verdicts (
	room_id  TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	tag      TEXT    NOT NULL,
	rule     INTEGER NOT NULL DEFAULT 0,
	filter   TEXT    NOT NULL DEFAULT '',
	at_ms    INTEGER NOT NULL DEFAULT 0,
	released INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, tag)
) STRICT, WITHOUT ROWID;

-- Per person ID and tag, the tags a filter put someone into and the ones a person
-- took them out of. A Matrix ID's rows mirror the org.kith.people account data of the
-- account in account (its rooms' owner, "matrix:<key>/"), each account's replaced
-- whole; the rest are kept here alone, with no account. tag is lower-cased.
CREATE TABLE person_verdicts (
	account  TEXT    NOT NULL DEFAULT '',
	person   TEXT    NOT NULL,
	tag      TEXT    NOT NULL,
	rule     INTEGER NOT NULL DEFAULT 0,
	filter   TEXT    NOT NULL DEFAULT '',
	at_ms    INTEGER NOT NULL DEFAULT 0,
	released INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, person, tag)
) STRICT, WITHOUT ROWID;

CREATE TABLE spaces (
	id     TEXT NOT NULL PRIMARY KEY,
	name   TEXT NOT NULL DEFAULT '',
	bridge TEXT NOT NULL DEFAULT '',
	keeper TEXT NOT NULL DEFAULT ''
) STRICT;

-- room_id is not a foreign key: SaveSpaces and SaveRooms run concurrently, and a
-- spaces refresh landing first would fail the whole transaction.
CREATE TABLE space_children (
	space_id TEXT    NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	room_id  TEXT    NOT NULL,
	position INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (space_id, room_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX space_children_in_order ON space_children(space_id, position);

-- The network behind a room another network keeps for it (domain.Room.Network): a
-- Matrix room a bridge keeps, by its bridge. Read once and kept: a room never changes
-- network (KeepRoomNetworks).
CREATE TABLE room_networks (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	network TEXT NOT NULL
) STRICT, WITHOUT ROWID;

-- How a space is left (domain.Space.Leaving), a row only for one that is.
CREATE TABLE space_leaving (
	space_id TEXT    NOT NULL PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
	leaving  INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- space_id is not a foreign key: '' records "no canonical parent" (so it is not
-- re-asked), and SQLite exempts only NULL from FK checks.
CREATE TABLE room_parents (
	room_id     TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	space_id    TEXT    NOT NULL DEFAULT '',
	resolved_ms INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;
CREATE INDEX room_parents_by_space ON room_parents(space_id) WHERE space_id <> '';

-- Local mirror of each Matrix account's m.ignored_user_list: account is its rooms'
-- owner ("matrix:<key>/"), user_id a person's ID. Rebuild-friendly: the server copy is
-- the record.
CREATE TABLE ignored_users (
	account TEXT NOT NULL,
	user_id TEXT NOT NULL,
	PRIMARY KEY (account, user_id)
) STRICT, WITHOUT ROWID;

-- Local mirror of a network's server-side block list (WhatsApp's blocklist, Telegram's
-- contacts.blocked, Meta's). Rebuild-friendly: the server copy is the record. Pulled
-- on connect and after a blocklist-change event; the sync is one-way, server to local.
CREATE TABLE network_blocks (
	network TEXT NOT NULL,
	user_id TEXT NOT NULL,
	PRIMARY KEY (network, user_id)
) STRICT, WITHOUT ROWID;

-- Chats the network itself archived (Telegram's Archived folder, WhatsApp's archive).
-- Its own table, written only by that network (SetArchived), so no listing's whole-list
-- rewrite blanks it.
CREATE TABLE room_archived (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- Forums' own rooms (Telegram's General topics: the forum is a space of its topics'
-- rooms). Its own table, written only by that network's listing (SetForums), as
-- room_archived is.
CREATE TABLE room_forums (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- Chats whose messages go out as SMS or MMS through a phone's carrier (Google Messages).
-- Its own table, written only by that network (SetSMS), as room_archived is.
CREATE TABLE room_sms (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- Rooms whose history the cache holds back to its first message: the network had
-- nothing older (Google Messages, paging back from the phone). Its own table, written
-- only by that network (SetHistoryStart), as room_sms is.
CREATE TABLE room_history_start (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- Stretches of a room's history a sync skipped (a Matrix timeline cut short after
-- kith was away), from the newest message held below (boundary, since_ms) to the oldest
-- fetched above (until_ms); token is where paging back resumes. A row until filled.
CREATE TABLE room_gaps (
	id        INTEGER PRIMARY KEY,
	room_id   TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	boundary  TEXT    NOT NULL,
	since_ms  INTEGER NOT NULL,
	until_ms  INTEGER NOT NULL,
	token     TEXT    NOT NULL,
	announce  INTEGER NOT NULL CHECK (announce IN (0, 1)),
	capped    INTEGER NOT NULL DEFAULT 0 CHECK (capped IN (0, 1))
) STRICT;

-- Rooms this account may read but not post in (a Matrix room whose power levels put
-- messages above ours). Its own table, written only by the network (SetReadOnly).
CREATE TABLE room_read_only (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- The public @username that names a room on its network (Telegram public chats and
-- channels), lowercase and without "@". Its own table, written only by that network
-- (SetHandles) and read by link resolution (tg:// and the like).
CREATE TABLE room_handles (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	handle  TEXT NOT NULL
) STRICT, WITHOUT ROWID;

-- A chat's disappearing-messages timer, a row only while it has one: what kith sends
-- there must carry it (SetTimers).
CREATE TABLE room_timers (
	room_id TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	seconds INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- The version a chat's timer was set at, a row once a network has numbered one: Signal
-- counts a one-to-one chat's timer changes, and an older change is not taken
-- (KeepTimer). It outlives the timer's being turned off.
CREATE TABLE room_timer_versions (
	room_id TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	version INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- A disappearing message's timer (s) and when its countdown started (0: not yet, until
-- the account reads it), a row until it expires (SetExpiry, ExpireDue).
CREATE TABLE message_expiry (
	room_id   TEXT    NOT NULL,
	event_id  TEXT    NOT NULL,
	seconds   INTEGER NOT NULL,
	starts_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
CREATE INDEX message_expiry_due ON message_expiry(starts_ms + seconds * 1000) WHERE starts_ms > 0;

-- A message's poll, as JSON (domain.Poll): its question, answers and how the votes
-- stand, rewritten whole as results change (SetPoll).
CREATE TABLE message_polls (
	room_id  TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id TEXT NOT NULL,
	poll     TEXT NOT NULL,
	PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;

-- Who people are (domain.Directory): what each source (an account, a bridge login)
-- calls an identifier (a number as tel:<digits>, or a person's ID on a network), and
-- which identifiers it says are one person, each source's rows replaced whole when it
-- reads them again (SetPeople). The names bridges give their puppets are read from
-- room_members instead (Directory).
CREATE TABLE person_names (
	source TEXT    NOT NULL,
	id     TEXT    NOT NULL,
	name   TEXT    NOT NULL,
	rank   INTEGER NOT NULL,
	PRIMARY KEY (source, id)
) STRICT, WITHOUT ROWID;

CREATE TABLE person_links (
	source TEXT NOT NULL,
	id     TEXT NOT NULL,
	other  TEXT NOT NULL,
	PRIMARY KEY (source, id, other)
) STRICT, WITHOUT ROWID;

-- Tombstone/predecessor links. Its own table because SaveRooms is a whole-list
-- snapshot that would blank extra rooms columns. checked_ms records that we looked.
CREATE TABLE room_upgrades (
	room_id     TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	replacement TEXT    NOT NULL DEFAULT '',
	predecessor TEXT    NOT NULL DEFAULT '',
	checked_ms  INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE messages (
	room_id     TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id    TEXT    NOT NULL,
	sender      TEXT    NOT NULL DEFAULT '',
	sender_name TEXT    NOT NULL DEFAULT '',
	body        TEXT    NOT NULL DEFAULT '',
	ts_ms       INTEGER NOT NULL DEFAULT 0,
	reply_to    TEXT    NOT NULL DEFAULT '',
	thread_root TEXT    NOT NULL DEFAULT '',
	redacted    INTEGER NOT NULL DEFAULT 0,
	edited      INTEGER NOT NULL DEFAULT 0,
	mentioned   INTEGER NOT NULL DEFAULT 0,
	emote       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id)
) STRICT;
CREATE INDEX messages_by_time   ON messages(room_id, ts_ms, event_id);
CREATE INDEX messages_in_thread ON messages(room_id, thread_root, ts_ms, event_id);
CREATE INDEX messages_by_sender ON messages(sender, ts_ms);
CREATE INDEX messages_by_room_sender ON messages(room_id, sender, thread_root, ts_ms);
CREATE INDEX messages_naming_me ON messages(ts_ms DESC) WHERE mentioned = 1;
CREATE INDEX messages_recent   ON messages(ts_ms DESC);
CREATE INDEX messages_threaded ON messages(room_id, thread_root, ts_ms) WHERE thread_root <> '';

CREATE TABLE message_media (
	room_id   TEXT    NOT NULL,
	event_id  TEXT    NOT NULL,
	kind      TEXT    NOT NULL DEFAULT '',
	name      TEXT    NOT NULL DEFAULT '',
	mime      TEXT    NOT NULL DEFAULT '',
	width     INTEGER NOT NULL DEFAULT 0,
	height    INTEGER NOT NULL DEFAULT 0,
	size      INTEGER NOT NULL DEFAULT 0,
	mxc       TEXT    NOT NULL DEFAULT '',
	file_json TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- A message's place among those sharing its time (domain.Message.Seq), for a
-- network whose times are whole seconds and whose IDs carry no order. Only messages
-- given one have a row.
CREATE TABLE message_order (
	room_id  TEXT    NOT NULL,
	event_id TEXT    NOT NULL,
	seq      INTEGER NOT NULL,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- The people a message mentions (domain.Mention): who, and the text that names them
-- in the body, which a client draws with the name they are known by.
CREATE TABLE message_mentions (
	room_id  TEXT NOT NULL,
	event_id TEXT NOT NULL,
	user_id  TEXT NOT NULL,
	name     TEXT NOT NULL,
	PRIMARY KEY (room_id, event_id, user_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE message_redaction (
	room_id  TEXT NOT NULL,
	event_id TEXT NOT NULL,
	by       TEXT NOT NULL DEFAULT '',
	reason   TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE message_html (
	room_id  TEXT NOT NULL,
	event_id TEXT NOT NULL,
	html     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- The edit a message's body and formatting come from. Edits arrive in any order
-- (backfill runs newest page first), and only a newer one may replace it.
CREATE TABLE message_edit (
	room_id     TEXT    NOT NULL,
	event_id    TEXT    NOT NULL,
	revision_id TEXT    NOT NULL DEFAULT '',
	ts_ms       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
-- A redaction names the edit event, not the message it shows on (EditShownBy).
CREATE INDEX message_edit_by_revision ON message_edit(room_id, revision_id);

-- A redaction of a message not cached yet (ts_ms is when it was deleted): a copy of
-- it arriving later (an edit, which servers do not redact, or a page) is saved as
-- deleted. Dropped once applied, and by trim once older than what the room keeps.
CREATE TABLE message_tombstone (
	room_id  TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id TEXT    NOT NULL,
	by       TEXT    NOT NULL DEFAULT '',
	reason   TEXT    NOT NULL DEFAULT '',
	ts_ms    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;

-- Every version of a message, keyed by the event that carried it. Filled only while
-- [display.deleted] keep is on, and only for a message with more than one: a message
-- never edited is its own one version.
CREATE TABLE message_revisions (
	room_id     TEXT    NOT NULL,
	event_id    TEXT    NOT NULL,
	revision_id TEXT    NOT NULL,
	body        TEXT    NOT NULL DEFAULT '',
	html        TEXT    NOT NULL DEFAULT '',
	ts_ms       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id, revision_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
-- A room's newest edit, asked on every mark-read (NewestTS); the key alone walks
-- every revision in the room.
CREATE INDEX message_revisions_by_time ON message_revisions(room_id, ts_ms);

CREATE VIRTUAL TABLE messages_fts USING fts5(
	body,
	content='messages',
	content_rowid='rowid',
	tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER messages_fts_insert AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, body) VALUES (new.rowid, new.body);
END;
CREATE TRIGGER messages_fts_delete AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, body) VALUES('delete', old.rowid, old.body);
END;
-- OF body + WHEN: sync re-delivers unchanged messages constantly.
CREATE TRIGGER messages_fts_update AFTER UPDATE OF body ON messages
	WHEN old.body IS NOT new.body BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, body) VALUES('delete', old.rowid, old.body);
	INSERT INTO messages_fts(rowid, body) VALUES (new.rowid, new.body);
END;
` + messageChanges + `
CREATE INDEX message_changes_in_order ON message_changes(change);
INSERT INTO change_counter(epoch, last) VALUES (lower(hex(randomblob(16))), 0);
` + messageRedactionTimes + `

-- Persisted drafts, including ones an agent wrote while the client was closed.
CREATE TABLE drafts (
	room_id    TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	body       TEXT    NOT NULL DEFAULT '',
	caret      INTEGER NOT NULL DEFAULT 0,
	mentions   TEXT    NOT NULL DEFAULT '',
	reply_to   TEXT    NOT NULL DEFAULT '',
	editing    TEXT    NOT NULL DEFAULT '',
	edit_saved TEXT    NOT NULL DEFAULT '',
	author     TEXT    NOT NULL DEFAULT '',
	updated_ms INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

-- The thread a draft is written into: a row only for a draft in one.
CREATE TABLE draft_threads (
	room_id     TEXT NOT NULL PRIMARY KEY REFERENCES drafts(room_id) ON DELETE CASCADE,
	thread_root TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE reactions (
	event_id     TEXT NOT NULL PRIMARY KEY,
	room_id      TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	target_event TEXT NOT NULL,
	sender       TEXT NOT NULL DEFAULT '',
	emoji        TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX reactions_in_room  ON reactions(room_id);
CREATE INDEX reactions_by_sender ON reactions(sender, emoji);

CREATE TABLE room_members (
	room_id      TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	user_id      TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, user_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX room_members_by_name ON room_members(room_id, display_name);
CREATE INDEX room_members_by_user ON room_members(user_id);

CREATE TABLE room_unread (
	room_id       TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	notifications INTEGER NOT NULL DEFAULT 0,
	highlights    INTEGER NOT NULL DEFAULT 0,
	read_event    TEXT    NOT NULL DEFAULT '',
	marked        INTEGER NOT NULL DEFAULT 0,
	read_ts_ms    INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE thread_read (
	room_id    TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	root_event TEXT    NOT NULL,
	read_event TEXT    NOT NULL DEFAULT '',
	read_ts_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, root_event)
) STRICT, WITHOUT ROWID;

CREATE TABLE mention_usage (
	room_id      TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	user_id      TEXT    NOT NULL,
	count        INTEGER NOT NULL DEFAULT 0,
	last_used_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, user_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE emoji_usage (
	room_id      TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	kind         TEXT    NOT NULL,
	emoji        TEXT    NOT NULL,
	count        INTEGER NOT NULL DEFAULT 0,
	last_used_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, kind, emoji)
) STRICT, WITHOUT ROWID;
CREATE INDEX emoji_usage_by_kind ON emoji_usage(kind, emoji, count, last_used_ms);

CREATE TABLE sender_slots (
	room_id   TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	group_key TEXT    NOT NULL,
	slot      INTEGER NOT NULL,
	PRIMARY KEY (room_id, group_key)
) STRICT, WITHOUT ROWID;

CREATE TABLE quoted_messages (
	room_id  TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id TEXT NOT NULL,
	sender   TEXT NOT NULL DEFAULT '',
	body     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE reaction_refusals (
	protocol TEXT    NOT NULL,
	emoji    TEXT    NOT NULL,
	at_ms    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (protocol, emoji)
) STRICT, WITHOUT ROWID;
`

// migrations are post-base changes, append only: entry i stamps schemaVersion+i+1.
// A new one also updates baseSchema, uses IF NOT EXISTS, prefers a side table over
// ADD COLUMN, and is pinned in migrationFingerprints. Indexes need none (see
// ensureIndexes); removals are new DROP migrations. TestAnUpgradedCacheMatchesAFreshOne
// checks a migrated v1 cache against a fresh one.
var migrations = []string{
	// v2: drafts remember their thread.
	`CREATE TABLE IF NOT EXISTS draft_threads (
	room_id     TEXT NOT NULL PRIMARY KEY REFERENCES drafts(room_id) ON DELETE CASCADE,
	thread_root TEXT NOT NULL
) STRICT, WITHOUT ROWID;`,
	// v3: what a reply quotes, for a quoted message the cache does not hold.
	`CREATE TABLE IF NOT EXISTS quoted_messages (
	room_id  TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id TEXT NOT NULL,
	sender   TEXT NOT NULL DEFAULT '',
	body     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;`,
	// v4: chats the network archived.
	`CREATE TABLE IF NOT EXISTS room_archived (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;`,
	// v5: rooms made of topics.
	`CREATE TABLE IF NOT EXISTS room_forums (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;`,
	// v6: the names accounts know numbers by.
	`CREATE TABLE IF NOT EXISTS phone_names (
	source TEXT    NOT NULL,
	phone  TEXT    NOT NULL,
	name   TEXT    NOT NULL,
	rank   INTEGER NOT NULL,
	PRIMARY KEY (source, phone)
) STRICT, WITHOUT ROWID;`,
	// v7: messages' polls.
	`CREATE TABLE IF NOT EXISTS message_polls (
	room_id  TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id TEXT NOT NULL,
	poll     TEXT NOT NULL,
	PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;`,
	// v8: messages' places among those sharing their time.
	`CREATE TABLE IF NOT EXISTS message_order (
	room_id  TEXT    NOT NULL,
	event_id TEXT    NOT NULL,
	seq      INTEGER NOT NULL,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;`,
	// v9: the people messages mention.
	`CREATE TABLE IF NOT EXISTS message_mentions (
	room_id  TEXT NOT NULL,
	event_id TEXT NOT NULL,
	user_id  TEXT NOT NULL,
	name     TEXT NOT NULL,
	PRIMARY KEY (room_id, event_id, user_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;`,
	// v10: names by any identifier, not only numbers, and links between them.
	`CREATE TABLE IF NOT EXISTS person_names (
	source TEXT    NOT NULL,
	id     TEXT    NOT NULL,
	name   TEXT    NOT NULL,
	rank   INTEGER NOT NULL,
	PRIMARY KEY (source, id)
) STRICT, WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS person_links (
	source TEXT NOT NULL,
	id     TEXT NOT NULL,
	other  TEXT NOT NULL,
	PRIMARY KEY (source, id, other)
) STRICT, WITHOUT ROWID;
INSERT OR IGNORE INTO person_names(source, id, name, rank) SELECT source, 'tel:' || phone, name, rank FROM phone_names;
DROP TABLE phone_names;`,
	// v11: how a space is left.
	`CREATE TABLE IF NOT EXISTS space_leaving (
	space_id TEXT    NOT NULL PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
	leaving  INTEGER NOT NULL
) STRICT, WITHOUT ROWID;`,
	// v12: the network behind a bridged room.
	`CREATE TABLE IF NOT EXISTS room_networks (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	network TEXT NOT NULL
) STRICT, WITHOUT ROWID;`,
	// v13: filter verdicts per tag, replacing spam's.
	`CREATE TABLE IF NOT EXISTS tag_verdicts (
	room_id  TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	tag      TEXT    NOT NULL,
	rule     INTEGER NOT NULL DEFAULT 0,
	filter   TEXT    NOT NULL DEFAULT '',
	at_ms    INTEGER NOT NULL DEFAULT 0,
	released INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, tag)
) STRICT, WITHOUT ROWID;`,
	// v14, v15: spam's verdicts are tag verdicts now.
	`DROP TABLE IF EXISTS spam_rooms;`,
	`DROP TABLE IF EXISTS spam_released;`,
	// v16: filter marks per person and tag, and releases.
	`CREATE TABLE IF NOT EXISTS person_verdicts (
	person   TEXT    NOT NULL,
	tag      TEXT    NOT NULL,
	rule     INTEGER NOT NULL DEFAULT 0,
	filter   TEXT    NOT NULL DEFAULT '',
	at_ms    INTEGER NOT NULL DEFAULT 0,
	released INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (person, tag)
) STRICT, WITHOUT ROWID;`,
	// v17: a chat's disappearing-messages timer.
	`CREATE TABLE IF NOT EXISTS room_timers (
	room_id TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	seconds INTEGER NOT NULL
) STRICT, WITHOUT ROWID;`,
	// v18: the version a chat's timer was set at (Signal).
	`CREATE TABLE IF NOT EXISTS room_timer_versions (
	room_id TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	version INTEGER NOT NULL
) STRICT, WITHOUT ROWID;`,
	// v19: a disappearing message's timer and countdown.
	`CREATE TABLE IF NOT EXISTS message_expiry (
	room_id   TEXT    NOT NULL,
	event_id  TEXT    NOT NULL,
	seconds   INTEGER NOT NULL,
	starts_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;`,
	// v20: chats whose messages go out as SMS or MMS.
	`CREATE TABLE IF NOT EXISTS room_sms (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;`,
	// v21: stretches of history a sync skipped, until filled.
	`CREATE TABLE IF NOT EXISTS room_gaps (
	id        INTEGER PRIMARY KEY,
	room_id   TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	boundary  TEXT    NOT NULL,
	since_ms  INTEGER NOT NULL,
	until_ms  INTEGER NOT NULL,
	token     TEXT    NOT NULL,
	announce  INTEGER NOT NULL CHECK (announce IN (0, 1)),
	capped    INTEGER NOT NULL DEFAULT 0 CHECK (capped IN (0, 1))
) STRICT;`,
	// v22: rooms this account may read but not post in.
	`CREATE TABLE IF NOT EXISTS room_read_only (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;`,
	// v23: Matrix MXIDs this account ignores: mirror of m.ignored_user_list, so the
	// list survives a cache rebuild and is readable without a server round-trip.
	`CREATE TABLE IF NOT EXISTS ignored_users (
	network TEXT NOT NULL,
	user_id TEXT NOT NULL,
	PRIMARY KEY (network, user_id)
) STRICT, WITHOUT ROWID;`,
	// v24: a network's server-side block list, mirrored for offline reads and quick
	// person-card display. Server copy wins; this table is replaced on each pull.
	`CREATE TABLE IF NOT EXISTS network_blocks (
	network TEXT NOT NULL,
	user_id TEXT NOT NULL,
	PRIMARY KEY (network, user_id)
) STRICT, WITHOUT ROWID;`,
	// v25: the public @username that names a room on its network, for tg:// link
	// resolution and the like.
	`CREATE TABLE IF NOT EXISTS room_handles (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	handle  TEXT NOT NULL
) STRICT, WITHOUT ROWID;`,
	// v26: Matrix mirrors are per account. Until now one Matrix account wrote them: its
	// rooms' owner is read off the rooms (none cached, and its mirrored rows go, as the
	// server holds them). Only columns both shapes have are read, so this is a no-op on
	// baseSchema.
	`CREATE TEMP TABLE matrix_owner AS
	SELECT substr(id, 1, instr(id, '/')) AS owner FROM rooms WHERE id LIKE 'matrix:%/%' ORDER BY id LIMIT 1;
CREATE TEMP TABLE old_ignored AS SELECT user_id FROM ignored_users;
DROP TABLE ignored_users;
CREATE TABLE ignored_users (
	account TEXT NOT NULL,
	user_id TEXT NOT NULL,
	PRIMARY KEY (account, user_id)
) STRICT, WITHOUT ROWID;
INSERT INTO ignored_users(account, user_id) SELECT owner, user_id FROM old_ignored, matrix_owner;
CREATE TEMP TABLE old_person_verdicts AS SELECT person, tag, rule, filter, at_ms, released FROM person_verdicts;
DROP TABLE person_verdicts;
CREATE TABLE person_verdicts (
	account  TEXT    NOT NULL DEFAULT '',
	person   TEXT    NOT NULL,
	tag      TEXT    NOT NULL,
	rule     INTEGER NOT NULL DEFAULT 0,
	filter   TEXT    NOT NULL DEFAULT '',
	at_ms    INTEGER NOT NULL DEFAULT 0,
	released INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, person, tag)
) STRICT, WITHOUT ROWID;
INSERT INTO person_verdicts(account, person, tag, rule, filter, at_ms, released)
	SELECT '', person, tag, rule, filter, at_ms, released FROM old_person_verdicts WHERE person NOT LIKE 'matrix:%'
	UNION ALL SELECT owner, person, tag, rule, filter, at_ms, released FROM old_person_verdicts, matrix_owner WHERE person LIKE 'matrix:%';
UPDATE person_names SET source = 'bridge:' || (SELECT owner FROM matrix_owner) || substr(source, 8)
	WHERE source LIKE 'bridge:%' AND source NOT LIKE 'bridge:matrix:%' AND EXISTS (SELECT 1 FROM matrix_owner);
UPDATE person_links SET source = 'bridge:' || (SELECT owner FROM matrix_owner) || substr(source, 8)
	WHERE source LIKE 'bridge:%' AND source NOT LIKE 'bridge:matrix:%' AND EXISTS (SELECT 1 FROM matrix_owner);
DELETE FROM person_names WHERE source LIKE 'bridge:%' AND source NOT LIKE 'bridge:matrix:%';
DELETE FROM person_links WHERE source LIKE 'bridge:%' AND source NOT LIKE 'bridge:matrix:%';
DROP TABLE old_ignored;
DROP TABLE old_person_verdicts;
DROP TABLE matrix_owner;`,
	// v27: a message never edited keeps no version rows; the copy each one kept of
	// itself goes. Only rows reading exactly as the message does, so nothing is lost.
	`DELETE FROM message_revisions
 WHERE revision_id = event_id
   AND NOT EXISTS (SELECT 1 FROM message_revisions o
                    WHERE o.room_id = message_revisions.room_id AND o.event_id = message_revisions.event_id
                      AND o.revision_id <> message_revisions.revision_id)
   AND EXISTS (SELECT 1 FROM messages m
                 LEFT JOIN message_html h ON h.room_id = m.room_id AND h.event_id = m.event_id
                WHERE m.room_id = message_revisions.room_id AND m.event_id = message_revisions.event_id
                  AND m.edited = 0 AND m.body = message_revisions.body
                  AND coalesce(h.html, '') = message_revisions.html);`,
	// v28: rooms whose history is cached back to its first message.
	`CREATE TABLE IF NOT EXISTS room_history_start (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;`,
	// v29: messages numbered as they change, and the numbering's epoch.
	messageChanges + `
INSERT INTO change_counter(epoch, last) SELECT lower(hex(randomblob(16))), 0
 WHERE NOT EXISTS (SELECT 1 FROM change_counter);`,
	// v30: when a cached message was deleted.
	messageRedactionTimes,
}

// messageRedactionTimes is when each deleted message was deleted, the first time heard
// (domain.Message.RedactedAt): only messages deleted since it was kept have a row. Its
// triggers count a time as a change to the message (messageChanges). The base and v30
// share it.
const messageRedactionTimes = `
CREATE TABLE IF NOT EXISTS message_redaction_at (
	room_id  TEXT    NOT NULL,
	event_id TEXT    NOT NULL,
	ts_ms    INTEGER NOT NULL,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TRIGGER IF NOT EXISTS message_redaction_at_changed_insert AFTER INSERT ON message_redaction_at BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_redaction_at_changed_update AFTER UPDATE ON message_redaction_at
	WHEN old.ts_ms IS NOT new.ts_ms BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_redaction_at_changed_delete AFTER DELETE ON message_redaction_at BEGIN
	INSERT INTO message_touched VALUES (old.room_id, old.event_id);
END;
`

// messageChanges numbers every change to what the cache returns for a message, in the
// order they are written, for the message stream to resume from.
// The base and v29 share it, so a migrated cache is spelled as a fresh one.
//
// change_counter is one row: the last number given, and the epoch that names this
// numbering, made with the cache and anew when it is cleared, so a number from before
// means nothing. message_changes is each message's latest number. A trigger on every
// table a message is read from (messageSelect) records the message in message_touched,
// and the transaction numbers what it touched before it commits (numberTouched): a
// trigger that numbered would be compiled into every statement that writes a message,
// tripling the cost of a save. Writes that change nothing (sync re-delivers unchanged
// messages constantly) record nothing, nor do columns no read returns (a file's source).
const messageChanges = `
CREATE TABLE IF NOT EXISTS change_counter (
	epoch TEXT    NOT NULL,
	last  INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS message_changes (
	room_id  TEXT    NOT NULL,
	event_id TEXT    NOT NULL,
	change   INTEGER NOT NULL,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS message_touched (
	room_id  TEXT NOT NULL,
	event_id TEXT NOT NULL
) STRICT;

CREATE TRIGGER IF NOT EXISTS messages_changed_insert AFTER INSERT ON messages BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS messages_changed_update AFTER UPDATE ON messages
	WHEN old.sender IS NOT new.sender OR old.sender_name IS NOT new.sender_name
	  OR old.body IS NOT new.body OR old.ts_ms IS NOT new.ts_ms
	  OR old.redacted IS NOT new.redacted OR old.edited IS NOT new.edited
	  OR old.reply_to IS NOT new.reply_to OR old.mentioned IS NOT new.mentioned
	  OR old.thread_root IS NOT new.thread_root OR old.emote IS NOT new.emote BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;

CREATE TRIGGER IF NOT EXISTS message_media_changed_insert AFTER INSERT ON message_media
	WHEN new.kind <> '' BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_media_changed_update AFTER UPDATE ON message_media
	WHEN old.kind IS NOT new.kind OR old.name IS NOT new.name OR old.mime IS NOT new.mime
	  OR old.width IS NOT new.width OR old.height IS NOT new.height OR old.size IS NOT new.size BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_media_changed_delete AFTER DELETE ON message_media
	WHEN old.kind <> '' BEGIN
	INSERT INTO message_touched VALUES (old.room_id, old.event_id);
END;

CREATE TRIGGER IF NOT EXISTS message_html_changed_insert AFTER INSERT ON message_html BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_html_changed_update AFTER UPDATE ON message_html
	WHEN old.html IS NOT new.html BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_html_changed_delete AFTER DELETE ON message_html BEGIN
	INSERT INTO message_touched VALUES (old.room_id, old.event_id);
END;

CREATE TRIGGER IF NOT EXISTS message_redaction_changed_insert AFTER INSERT ON message_redaction BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_redaction_changed_update AFTER UPDATE ON message_redaction
	WHEN old.by IS NOT new.by OR old.reason IS NOT new.reason BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_redaction_changed_delete AFTER DELETE ON message_redaction BEGIN
	INSERT INTO message_touched VALUES (old.room_id, old.event_id);
END;

CREATE TRIGGER IF NOT EXISTS message_edit_changed_insert AFTER INSERT ON message_edit BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_edit_changed_update AFTER UPDATE ON message_edit
	WHEN old.revision_id IS NOT new.revision_id OR old.ts_ms IS NOT new.ts_ms BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_edit_changed_delete AFTER DELETE ON message_edit BEGIN
	INSERT INTO message_touched VALUES (old.room_id, old.event_id);
END;

CREATE TRIGGER IF NOT EXISTS message_polls_changed_insert AFTER INSERT ON message_polls BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_polls_changed_update AFTER UPDATE ON message_polls
	WHEN old.poll IS NOT new.poll BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_polls_changed_delete AFTER DELETE ON message_polls BEGIN
	INSERT INTO message_touched VALUES (old.room_id, old.event_id);
END;

CREATE TRIGGER IF NOT EXISTS message_order_changed_insert AFTER INSERT ON message_order BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_order_changed_update AFTER UPDATE ON message_order
	WHEN old.seq IS NOT new.seq BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_order_changed_delete AFTER DELETE ON message_order BEGIN
	INSERT INTO message_touched VALUES (old.room_id, old.event_id);
END;

CREATE TRIGGER IF NOT EXISTS message_mentions_changed_insert AFTER INSERT ON message_mentions BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_mentions_changed_update AFTER UPDATE ON message_mentions
	WHEN old.user_id IS NOT new.user_id OR old.name IS NOT new.name BEGIN
	INSERT INTO message_touched VALUES (new.room_id, new.event_id);
END;
CREATE TRIGGER IF NOT EXISTS message_mentions_changed_delete AFTER DELETE ON message_mentions BEGIN
	INSERT INTO message_touched VALUES (old.room_id, old.event_id);
END;
`

// forgetFrozenStats drops the row counts ANALYZE kept for tables without an index
// (sqlite_stat1 rows with no idx), reporting whether there were any. PRAGMA optimize
// never refreshes those, so the count taken when the cache was new stands for good:
// the search index's data table, counted at 2 rows, was scanned whole on every segment
// merge, ~6 ms per message saved. With no count the planner seeks by rowid. Best
// effort, like Close's optimize: only plans depend on it.
func forgetFrozenStats(ctx context.Context, sqlDB *sql.DB) bool {
	res, err := sqlDB.ExecContext(ctx, "DELETE FROM sqlite_stat1 WHERE idx IS NULL")
	if err != nil {
		return false // never analyzed: no sqlite_stat1
	}
	n, err := res.RowsAffected()
	return err == nil && n > 0
}

// ensureIndexes makes the file's indexes exactly baseSchema's (create missing,
// rebuild changed, drop undeclared), so index changes need no migration. Every
// explicit index is declared in baseSchema.
func ensureIndexes(ctx context.Context, sqlDB *sql.DB) error {
	_, err := reconcileIndexes(ctx, sqlDB)
	return err
}

// reconcileIndexes is ensureIndexes, reporting how many indexes it changed.
func reconcileIndexes(ctx context.Context, sqlDB *sql.DB) (int, error) {
	have, err := indexesPresent(ctx, sqlDB)
	if err != nil {
		return 0, err
	}
	want := baseIndexes()
	changed := 0
	for name, stmt := range want {
		if def, ok := have[name]; ok && sameIndex(def, stmt) {
			continue
		} else if ok {
			if _, execErr := sqlDB.ExecContext(ctx, "DROP INDEX "+quoteIdent(name)); execErr != nil {
				return changed, fmt.Errorf("db: drop changed index %s: %w", name, execErr)
			}
		}
		if _, execErr := sqlDB.ExecContext(ctx, stmt); execErr != nil {
			return changed, fmt.Errorf("db: restore index %s: %w", name, execErr)
		}
		changed++
	}
	for name := range have {
		if _, declared := want[name]; declared {
			continue
		}
		if _, execErr := sqlDB.ExecContext(ctx, "DROP INDEX "+quoteIdent(name)); execErr != nil {
			return changed, fmt.Errorf("db: drop undeclared index %s: %w", name, execErr)
		}
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	if _, err := sqlDB.ExecContext(ctx, "ANALYZE"); err != nil {
		return changed, fmt.Errorf("db: analyze after changing %d index(es): %w", changed, err)
	}
	return changed, nil
}

// quoteIdent quotes an identifier read from sqlite_master, doubling any quote in it.
// Every value in this file is bound; only these names are spliced into SQL.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sameIndex compares index SQL ignoring IF NOT EXISTS (SQLite drops it), spacing
// and case.
func sameIndex(stored, declared string) bool {
	norm := func(sql string) string {
		fields := strings.Fields(sql)
		out := fields[:0]
		for i := 0; i < len(fields); i++ {
			if i+2 < len(fields) && strings.EqualFold(fields[i], "IF") &&
				strings.EqualFold(fields[i+1], "NOT") && strings.EqualFold(fields[i+2], "EXISTS") {
				i += 2
				continue
			}
			out = append(out, fields[i])
		}
		return strings.Join(out, " ")
	}
	return strings.EqualFold(norm(stored), norm(declared))
}

// indexesPresent maps the file's explicit indexes to their stored SQL (auto-indexes
// have none).
func indexesPresent(ctx context.Context, sqlDB *sql.DB) (map[string]string, error) {
	return collectMap(ctx, sqlDB, "indices",
		`SELECT name, sql FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`,
		func(rows *sql.Rows) (string, string, error) {
			var name, def string
			err := rows.Scan(&name, &def)
			return name, def, err
		})
}

// baseIndexes maps each CREATE [UNIQUE] INDEX in baseSchema to its statement,
// parsed so there is no second list to keep in sync.
func baseIndexes() map[string]string {
	out := map[string]string{}
	for stmt := range strings.SplitSeq(stripSQLComments(baseSchema), ";") {
		fields := strings.Fields(stmt)
		if len(fields) < 3 || !strings.EqualFold(fields[0], "CREATE") {
			continue
		}
		rest := fields[1:]
		if strings.EqualFold(rest[0], "UNIQUE") {
			rest = rest[1:]
		}
		if len(rest) < 2 || !strings.EqualFold(rest[0], "INDEX") {
			continue
		}
		rest = rest[1:]
		if len(rest) >= 4 && strings.EqualFold(rest[0], "IF") {
			rest = rest[3:]
		}
		out[rest[0]] = strings.TrimSpace(stmt)
	}
	return out
}

// stripSQLComments removes `--` line comments, so prose cannot be read as SQL.
func stripSQLComments(script string) string {
	lines := strings.Split(script, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// schemaPlan is the schema a cache is brought to: the base, then migrations, and the
// shape they build, worked out once.
type schemaPlan struct {
	migrations []string
	shape      func() (map[string]map[string]columnShape, error)
}

func newSchemaPlan(migrations []string) schemaPlan {
	return schemaPlan{migrations: migrations, shape: sync.OnceValues(func() (map[string]map[string]columnShape, error) {
		return expectedShape(migrations)
	})}
}

// currentSchema is the schema this build opens every cache with.
var currentSchema = newSchemaPlan(migrations)

// prepare brings the database to plan's schema: current (by number and shape) only
// reconciles indexes, base..current migrates, anything else is copied aside and
// rebuilt. A rebuild is reported so the caller can reset the sync position.
func prepare(ctx context.Context, sqlDB *sql.DB, path string, key []byte, plan schemaPlan) (prepared, error) {
	version, err := userVersion(ctx, sqlDB)
	if err != nil {
		return prepared{}, err
	}
	current := schemaVersion + len(plan.migrations)
	if version == current {
		// The number alone is not trusted: a reused version or a hand-edited cache
		// would fail at the first query.
		ok, serr := shapeMatches(ctx, sqlDB, plan)
		if serr != nil {
			return prepared{}, serr
		}
		if ok {
			// Shape is right, so an index failure is transient (lock, disk); a rebuild
			// would lose cache-only data.
			return prepared{}, ensureIndexes(ctx, sqlDB)
		}
		version = -1 // not the shape it claims: rebuild, keeping a copy aside
	}
	var out prepared
	if version < schemaVersion || version > current {
		asideErr, fatal := rebuild(ctx, sqlDB, path, key)
		if fatal != nil {
			return prepared{}, fatal
		}
		out.asideErr = asideErr
		version = schemaVersion
		out.rebuilt = true
	}
	for i := version - schemaVersion; i < len(plan.migrations); i++ {
		if migrateErr := applyMigration(ctx, sqlDB, plan.migrations, i); migrateErr != nil {
			return out, migrateErr
		}
	}
	return out, ensureIndexes(ctx, sqlDB)
}

// columnShape is what the shape check compares per column. Order, defaults and
// SQL text are left out: a column added by ALTER sits last but is the same column.
type columnShape struct {
	typ     string
	notNull bool
	pk      int
}

// expectedShape is the tables and columns the base and migrations build, made in
// memory from the statements a rebuild runs.
func expectedShape(migrations []string) (map[string]map[string]columnShape, error) {
	ctx := context.Background()
	mem, err := driver.Open(":memory:", setup.Init)
	if err != nil {
		return nil, fmt.Errorf("db: open the reference schema: %w", err)
	}
	defer func() { _ = mem.Close() }()
	mem.SetMaxOpenConns(1)
	if _, err := mem.ExecContext(ctx, baseSchema); err != nil {
		return nil, fmt.Errorf("db: build the reference schema: %w", err)
	}
	for i := range migrations {
		if err := applyMigration(ctx, mem, migrations, i); err != nil {
			return nil, err
		}
	}
	return shapeOf(ctx, mem)
}

// shapeOf is every table with its columns and every trigger with its statement:
// without the FTS triggers search silently goes stale.
func shapeOf(ctx context.Context, sqlDB *sql.DB) (map[string]map[string]columnShape, error) {
	tables, err := tablesIn(ctx, sqlDB)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]columnShape, len(tables))
	type column struct {
		name  string
		shape columnShape
	}
	for _, table := range tables {
		list, cerr := collect(ctx, sqlDB, "the columns of "+table,
			`SELECT name, type, "notnull", pk FROM pragma_table_xinfo(?)`,
			func(rows *sql.Rows) (column, error) {
				var c column
				err := rows.Scan(&c.name, &c.shape.typ, &c.shape.notNull, &c.shape.pk)
				return c, err
			}, table)
		if cerr != nil {
			return nil, cerr
		}
		cols := make(map[string]columnShape, len(list))
		for _, c := range list {
			cols[c.name] = c.shape
		}
		out[table] = cols
	}
	triggers, terr := collect(ctx, sqlDB, "the triggers",
		`SELECT name, sql FROM sqlite_master WHERE type = 'trigger'`,
		func(rows *sql.Rows) ([2]string, error) {
			var t [2]string
			err := rows.Scan(&t[0], &t[1])
			return t, err
		})
	if terr != nil {
		return nil, terr
	}
	for _, t := range triggers {
		out["trigger "+t[0]] = map[string]columnShape{"": {typ: strings.Join(strings.Fields(t[1]), " ")}}
	}
	return out, nil
}

// shapeMatches reports whether every table of plan's schema exists with the same
// columns; extra tables are ignored.
func shapeMatches(ctx context.Context, sqlDB *sql.DB, plan schemaPlan) (bool, error) {
	want, err := plan.shape()
	if err != nil {
		return false, err
	}
	have, err := shapeOf(ctx, sqlDB)
	if err != nil {
		return false, err
	}
	for table, cols := range want {
		if got, ok := have[table]; !ok || !maps.Equal(got, cols) {
			return false, nil
		}
	}
	return true, nil
}

type prepared struct {
	rebuilt bool
	// asideErr is why the old cache could not be copied aside; not fatal.
	asideErr error
}

// rebuild copies the old cache aside (history a re-sync may not reproduce), sealed
// under key as the cache is, then creates the base schema. A failed copy is reported
// via asideErr, not fatal.
func rebuild(ctx context.Context, sqlDB *sql.DB, path string, key []byte) (asideErr, fatal error) {
	tables, err := tablesIn(ctx, sqlDB)
	if err != nil {
		return nil, err
	}
	// An empty file (first run) has nothing worth setting aside.
	if path != "" && len(tables) > 0 {
		asideErr = copyAside(ctx, sqlDB, path, key)
	}
	if dropErr := dropAll(ctx, sqlDB, tables); dropErr != nil {
		return asideErr, dropErr
	}
	// Shrink the file; dropped tables only free pages.
	if _, vacErr := sqlDB.ExecContext(ctx, "VACUUM"); vacErr != nil {
		return asideErr, fmt.Errorf("db: vacuum after rebuild: %w", vacErr)
	}
	// Create and stamp in one transaction. Drops stay outside: PRAGMA foreign_keys is
	// a no-op inside one, and an interrupted drop just rebuilds again.
	tx, txErr := sqlDB.BeginTx(ctx, nil)
	if txErr != nil {
		return asideErr, fmt.Errorf("db: begin rebuild: %w", txErr)
	}
	defer func() { _ = tx.Rollback() }()

	if _, execErr := tx.ExecContext(ctx, baseSchema); execErr != nil {
		return asideErr, fmt.Errorf("db: create schema: %w", execErr)
	}
	if stampErr := stampVersion(ctx, tx, schemaVersion); stampErr != nil {
		return asideErr, stampErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return asideErr, fmt.Errorf("db: commit rebuild: %w", commitErr)
	}
	// Without statistics the badge query drives from messages instead of read positions.
	if _, anErr := sqlDB.ExecContext(ctx, "ANALYZE"); anErr != nil {
		return asideErr, fmt.Errorf("db: analyze after rebuild: %w", anErr)
	}
	return asideErr, nil
}

// dropAll drops the named tables (with their indexes, triggers and FTS shadows).
func dropAll(ctx context.Context, sqlDB *sql.DB, tables []string) (err error) {
	if _, ferr := sqlDB.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); ferr != nil {
		return fmt.Errorf("db: suspend foreign keys: %w", ferr)
	}
	// Restored even if ctx is canceled, or the pool's one connection keeps cascades off.
	defer func() {
		if _, rerr := sqlDB.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys=ON"); rerr != nil {
			err = errors.Join(err, fmt.Errorf("db: restore foreign keys: %w", rerr))
		}
	}()

	for _, name := range tables {
		if _, derr := sqlDB.ExecContext(ctx, `DROP TABLE IF EXISTS `+quoteIdent(name)); derr != nil {
			return fmt.Errorf("db: drop %s: %w", name, derr)
		}
	}
	return nil
}

// tablesIn lists every table. Virtual tables come first: dropping one takes its
// shadow tables, which SQLite refuses to drop directly.
func tablesIn(ctx context.Context, sqlDB *sql.DB) ([]string, error) {
	return collect(ctx, sqlDB, "tables",
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' "+
			"ORDER BY (rootpage = 0) DESC, rowid",
		func(rows *sql.Rows) (string, error) {
			var name string
			err := rows.Scan(&name)
			return name, err
		})
}

// copyAside keeps the old cache beside the new one via VACUUM INTO, which is
// WAL-aware (a plain file copy misses the -wal) and refuses an existing file.
func copyAside(ctx context.Context, sqlDB *sql.DB, path string, key []byte) error {
	aside := fmt.Sprintf("%s%s%s", path, asideInfix, time.Now().Format(asideStamp))
	if err := sealeddb.Private(aside); err != nil {
		return fmt.Errorf("db: copy aside: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", sealeddb.Target(aside, key)); err != nil {
		return fmt.Errorf("db: copy aside to %s: %w", aside, err)
	}
	pruneAside(path, keptAside)
	return nil
}

// asideInfix marks a copy set aside by a rebuild, asideStamp when; keptAside is how
// many are kept (each is a whole cache).
const (
	asideInfix = ".pre-schema-"
	asideStamp = "20060102-150405"
	keptAside  = 2
)

// pruneAside deletes all but the newest keep copies beside path (names sort by
// time). Best effort.
func pruneAside(path string, keep int) {
	dir, base := filepath.Split(path)
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		return
	}
	var copies []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+asideInfix) && !e.IsDir() {
			copies = append(copies, e.Name())
		}
	}
	slices.Sort(copies)
	for _, name := range copies[:max(len(copies)-keep, 0)] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// sealAside encrypts the copies a rebuild set aside while the cache was plain, so no
// history stays beside the cache in the clear. Best effort, as setting them aside was.
func sealAside(ctx context.Context, path string, key []byte) error {
	dir, base := filepath.Split(path)
	entries, _ := os.ReadDir(filepath.Clean(dir)) // no folder yet: nothing set aside
	var errs []error
	for _, e := range entries {
		name := e.Name()
		stamp, isCopy := strings.CutPrefix(name, base+asideInfix)
		if _, perr := time.Parse(asideStamp, stamp); e.IsDir() || !isCopy || perr != nil {
			continue // not a copy, or a file beside one (-wal, .sealing)
		}
		if serr := sealeddb.Seal(ctx, filepath.Join(dir, name), key, setup); serr != nil {
			errs = append(errs, fmt.Errorf("db: encrypt %s: %w", name, serr))
		}
	}
	return errors.Join(errs...)
}

// userVersion reads the stamped schema version; an untouched file reads 0.
func userVersion(ctx context.Context, sqlDB *sql.DB) (int, error) {
	var version int
	if err := sqlDB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("db: read schema version: %w", err)
	}
	return version, nil
}

// stampVersion records the version; user_version takes no bind parameter.
func stampVersion(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, version int,
) error {
	if _, err := exec.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("db: stamp schema version %d: %w", version, err)
	}
	return nil
}

// applyMigration applies migrations[index] and stamps its version in one transaction.
func applyMigration(ctx context.Context, sqlDB *sql.DB, migrations []string, index int) error {
	version := schemaVersion + index + 1
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin migration %d: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, migrations[index]); err != nil {
		return fmt.Errorf("db: apply migration %d: %w", version, err)
	}
	if err := stampVersion(ctx, tx, version); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit migration %d: %w", version, err)
	}
	return nil
}
