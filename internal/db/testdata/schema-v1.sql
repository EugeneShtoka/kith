
CREATE TABLE rooms (
	id         TEXT    NOT NULL PRIMARY KEY,
	name       TEXT    NOT NULL DEFAULT '',
	is_direct  INTEGER NOT NULL DEFAULT 0,
	membership TEXT    NOT NULL DEFAULT 'join',
	invited_by TEXT    NOT NULL DEFAULT '',
	heroes     TEXT    NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX rooms_by_membership ON rooms(membership);

-- Rooms merged into another (MergeRoom): a WhatsApp direct chat known by the person's
-- LID until its number was learned, and the number's room it joined. Not a foreign key:
-- the merged room is gone, and a later write naming it is filed under the other.
CREATE TABLE room_aliases (
	room_id TEXT NOT NULL PRIMARY KEY,
	into_id TEXT NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX room_aliases_by_into ON room_aliases(into_id);

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

-- A room's picture (a person's, in a direct chat) as its network last gave it, and when
-- the network was last asked (SetPicture): picture_id is the network's name for that
-- picture, '' for a room found to have none, so it is not asked again until a check is
-- due. A rowid table: WITHOUT ROWID suits rows smaller than a picture's kilobytes.
CREATE TABLE room_pictures (
	room_id    TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	picture_id TEXT    NOT NULL DEFAULT '',
	data       BLOB    NOT NULL DEFAULT x'',
	checked_ms INTEGER NOT NULL DEFAULT 0
) STRICT;

-- A person's picture, by the ID their messages name them by, as room_pictures keeps a
-- room's (SetPersonPicture). No room owns it: one person writes in many.
CREATE TABLE person_pictures (
	person_id  TEXT    NOT NULL PRIMARY KEY,
	picture_id TEXT    NOT NULL DEFAULT '',
	data       BLOB    NOT NULL DEFAULT x'',
	checked_ms INTEGER NOT NULL DEFAULT 0
) STRICT;

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
CREATE INDEX messages_by_event ON messages(event_id);
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

CREATE INDEX message_changes_in_order ON message_changes(change);
INSERT INTO change_counter(epoch, last) VALUES (lower(hex(randomblob(16))), 0);

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
