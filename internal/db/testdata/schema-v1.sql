
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

-- Local mirror of org.kith.spam account data: rule by number, filter by name.
CREATE TABLE spam_rooms (
	room_id TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	rule    INTEGER NOT NULL,
	filter  TEXT    NOT NULL DEFAULT '',
	at_ms   INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

-- Rooms released from Spam; a room is here or in spam_rooms, never both. Kept
-- because a release outranks every rule.
CREATE TABLE spam_released (
	room_id TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	at_ms   INTEGER NOT NULL DEFAULT 0
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

-- space_id is not a foreign key: '' records "no canonical parent" (so it is not
-- re-asked), and SQLite exempts only NULL from FK checks.
CREATE TABLE room_parents (
	room_id     TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	space_id    TEXT    NOT NULL DEFAULT '',
	resolved_ms INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;
CREATE INDEX room_parents_by_space ON room_parents(space_id) WHERE space_id <> '';

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
CREATE INDEX messages_by_room_sender ON messages(room_id, sender, ts_ms);
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
-- [display.deleted] keep is on.
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

CREATE TABLE reaction_refusals (
	protocol TEXT    NOT NULL,
	emoji    TEXT    NOT NULL,
	at_ms    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (protocol, emoji)
) STRICT, WITHOUT ROWID;
