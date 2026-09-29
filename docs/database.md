# The local database

This page is for contributors working on `internal/db`: what the SQLite cache holds,
the conventions every table follows, and how to change the schema without stranding
the caches already on people's disks. For where the file lives and how to empty it
as a user, see [troubleshooting.md](troubleshooting.md). For how the package fits the
layering, see [ARCHITECTURE.md](../ARCHITECTURE.md#data-layer-internaldb).

## The database is a cache

Every row that matters in it is a **copy of something the homeserver still has**:
rooms, spaces, members, messages, reactions, read positions, media metadata. The
client has always had to work against a cold cache, because that is what a first run
is. So losing the cache costs a refetch, and that is what licenses the upgrade rule
further down: a file whose schema this binary does not recognize is set aside and
rebuilt, not migrated column by column.

What is **not** in it, and why:

| data | where it lives | why not here |
| --- | --- | --- |
| access token | OS keyring (`internal/session`), or an opt-in `0600` file | a credential, not a copy |
| E2EE crypto store and the sync position | its own SQLite file, `crypto-<hash>.db`, owned by mautrix-go | mautrix owns that schema; losing it loses the device's keys |
| configuration | `config.toml` | written by you, not fetched |
| scheduled messages | `scheduled-<hash>.toml` in the XDG state dir (`internal/schedule`) | an unsent message has no copy anywhere, so it cannot live under "rebuild on doubt" |
| the assistant ledger | `agent-sends-<hash>.jsonl` in the XDG state dir (`internal/agent`) | a record of what was sent on your behalf exists nowhere else |
| attachment files | the media cache directory (`internal/media`) | files an external viewer opens by path |

A few tables hold state that is local but cheap to lose: usage counters for the
emoji and mention rankings, per-room sender colours, what bridges have refused, and
unsent drafts. A rebuild resets them. Keep that list short: anything that must
survive a rebuild belongs outside this file, or in account data on the homeserver
with this table as its mirror (see [Account-data mirrors](#account-data-mirrors)).

## The file

- One file per account: `$XDG_DATA_HOME/kith/cache-<hash>.db`, where `<hash>` is
  `domain.AccountKey(mxid)` (the first 8 bytes of SHA-256 of the MXID, hex). See
  `db.DefaultPath`.
- Only the daemon opens it. The TUI and `kith-mcp` reach it through the daemon's
  socket; depguard keeps `internal/db` out of both.
- Driver: the pure-Go `modernc.org/sqlite`, so the build stays `CGO_ENABLED=0`.
- Connection settings live in the **DSN** (`dsn` in `db.go`), not in `Exec` calls
  after `sql.Open`: `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(1)`,
  `synchronous(NORMAL)` and `_txlock=immediate`. `NORMAL` under WAL can lose only the
  last commits before a power loss, which a cache fetches again, and saves an fsync per
  sync batch. `foreign_keys` and `busy_timeout` are per connection, and the
  pool may open a new connection at any time; the driver applies DSN pragmas to every
  one.
- `SetMaxOpenConns(1)` serializes all access within the process. The one full scan
  (language detection over every body) reads in pages and hands each page on between
  queries, so it never holds the connection while it works.
- `Close` runs `PRAGMA optimize` so the planner's statistics keep up with a growing
  cache.
- Opening is non-fatal for the daemon: if the cache cannot be opened it runs
  without one, losing only the instant startup.

## Tables

The whole schema is `baseSchema` in [`internal/db/schema.go`](../internal/db/schema.go),
and it is meant to be read: most tables carry a comment explaining a decision. This is
the map.

### Rooms and spaces

| table | holds |
| --- | --- |
| `rooms` | one row per room: name, `is_direct`, `membership`, who invited us, and the heroes (JSON). The **root** every per-room table hangs off. |
| `room_topics` | a room's `m.room.topic`, only for rooms that have one |
| `room_upgrades` | the tombstone link in both directions (`replacement`, `predecessor`) and when it was last checked |
| `room_parents` | a room's canonical parent space; `''` is a recorded "no parent", not a missing answer |
| `spaces` | one row per space: name, the bridge that owns it, who keeps it |
| `space_children` | each space's children in hierarchy order |

`rooms.membership` is `'join'`, `'invite'`, or `''`. The empty string is a room the
sync stream wrote about before a room-list refresh named it (see `registerRoom`); it
holds data but appears in neither list until the next refresh promotes it.

### Messages

| table | holds |
| --- | --- |
| `messages` | the timeline: sender, resolved name, body, timestamp, reply and thread links, and flags (`redacted`, `edited`, `mentioned`, `emote`). Bounded to the newest `messagesPerRoom` (2000) per room. |
| `message_html` | sanitized `formatted_body`, only for messages that carry formatting |
| `message_media` | the attachment: kind, name, MIME type, size, dimensions, `mxc` URI and the encrypted-file JSON |
| `message_redaction` | who deleted a message and why, which outlives the content |
| `message_edit` | the edit a message's body and formatting come from (its event ID and send time), so an older edit delivered later never replaces a newer one |
| `message_tombstone` | a redaction whose message was not cached yet (who, why, when): a copy arriving later, such as an edit, which servers do not redact, is saved as deleted. Spent when applied, and trimmed with the room |
| `message_revisions` | every version of an edited message, keyed by the event that carried it |
| `messages_fts` | the FTS5 index over `messages.body` (see [Search](#search-and-word-completion)) |

### Per-room state

| table | holds |
| --- | --- |
| `room_members` | members and display names, per room |
| `room_unread` | notification and highlight counts, our read position, and the `m.marked_unread` flag |
| `thread_read` | our read position in each thread |
| `reactions` | `m.annotation` events: target, sender, and the key (column `emoji`) |
| `drafts` | the unsent message per room, including reply/edit state, mentions, the caret, and who wrote it (you, or an assistant) |
| `starred` | mirror of the `org.kith.starred` account data |
| `spam_rooms` | mirror of the `org.kith.spam` account data: the rule and filter that caught a room |
| `spam_released` | mirror of the releases in the same account data: rooms taken out of Spam, which no rule may put back |

### Rankings and bookkeeping

| table | holds |
| --- | --- |
| `emoji_usage` | how often and how recently you used each emoji, per room and kind (`reaction` or `compose`) |
| `mention_usage` | how often and how recently you mentioned each person, per room |
| `sender_slots` | the colour slot assigned to each sender identity in a room, so colours stay put |
| `reaction_refusals` | reactions a bridge's network has refused, per protocol, so the palette stops offering them |

## Conventions

- **`STRICT` tables.** A value of the wrong type is an error, not a coercion.
- **No `NULL`s.** Columns are `NOT NULL` with a `DEFAULT` of `''` or `0`. Booleans
  are `INTEGER` 0/1. Timestamps are Unix milliseconds in `INTEGER` columns named
  `*_ms`.
- **`WITHOUT ROWID`** for tables keyed by their primary key. The exceptions are
  deliberate: `messages` must stay a rowid table because the FTS5 index is an
  external-content table keyed on its `rowid`.
- **Side tables over sparse columns.** A fact most rows do not have (formatting,
  an attachment, a topic, a deletion reason) gets its own table with a row only when
  there is something to store. This also makes the fact addable later with
  `CREATE TABLE IF NOT EXISTS`, which `ALTER TABLE ADD COLUMN` cannot offer.
- **Indexes exist for queries that exist**, and are named after the question they
  answer (`messages_by_time`, `messages_naming_me`, `emoji_usage_by_kind`). An index
  nothing reads is a write cost on every insert; the schema says so where one was
  removed.
- **Writes that must agree go in one transaction** (`Cache.inTx`). Room-list and
  space writes are whole-snapshot reconciliations inside one transaction, so a reader
  never sees half of one.

### Foreign keys and cascades

Constraints do the bookkeeping, and `foreign_keys=ON` is what makes them real.

- **Every per-room table references `rooms(id) ON DELETE CASCADE`.** Leaving a room
  deletes its row, and its messages, members, unread row, read positions, drafts and
  the rest go with it. This is why `SaveRooms` is a reconciliation (upsert every room,
  then delete only the ones that have gone) and never a delete-and-reinsert, which
  would take every cached message with it.
- **Per-message tables reference `messages(room_id, event_id) ON DELETE CASCADE`**
  (`message_media`, `message_html`, `message_redaction`, `message_edit`,
  `message_revisions`), so
  trimming a room to its newest 2000 messages trims their side rows too.
- **`space_children` references `spaces(id)`.**
- **A writer that meets an unknown room registers it first.** The sync stream
  delivers messages, receipts and counts for a room before the next room-list refresh
  names it, so per-room writers call `registerRoom` (an `INSERT … ON CONFLICT DO
  NOTHING` with `membership = ''`) rather than failing the foreign key.
- **Two columns are deliberately not foreign keys**, and the schema explains each:
  `space_children.room_id` (the space and room refreshes run concurrently, and a key
  would fail the whole spaces transaction on a cold cache) and `room_parents.space_id`
  (its `''` sentinel means "asked, no parent", which a key cannot express).
  `reactions.target_event` is not one either: a reaction can arrive before, or
  outlive, the message it annotates.
- `Clear` (behind `kith --clear-cache`, which also resets the sync position)
  deletes from the two roots, `rooms` and `spaces`, and lets the cascades empty the
  rest. `reaction_refusals` hangs off
  nothing, so it is named explicitly. **A new table that does not cascade from a root
  must be added to `Clear`.**

## Search and word completion

`messages_fts` is an FTS5 table with **external content** (`content='messages'`,
`content_rowid='rowid'`) and the `unicode61 remove_diacritics 2` tokenizer. It stores
the index only; the text stays in `messages`. Three triggers keep it in step:

- `messages_fts_insert` and `messages_fts_delete` mirror inserts and deletes.
- `messages_fts_update` fires only `AFTER UPDATE OF body` and only `WHEN old.body IS
  NOT new.body`. Sync re-delivers overlapping batches constantly, and without both
  guards every re-save of an unchanged message would reindex it.

`searchQuery` in `search.go` joins the index back to `messages` for filters (sender,
date span, scope) and time order, then takes snippets for the page it keeps. SQLite
computes a result column before it sorts, and a snippet tokenizes the body, so taking
them in the same pass cost one per match: about 1 s for `th…` at 200k messages, against
0.2 s now. A search with a filter but no terms (for example everything one person said
last week) drops the FTS5 join, and the `messages_recent` index serves its newest-first
order instead. A sender is matched as a substring of the MXID or display name, which no
index can serve: with no terms and no room scope, finding someone who rarely posts reads
the whole cache.

Word completion does not query the index. Ranking over it cost time proportional to
how often the matching words occur (about 180 ms for `the…` at 200k messages), so the
daemon counts recent windows in memory instead (`internal/vocab`,
`internal/matrix/recentvocab.go`). They are read through `Cache.RecentBodies`, one index
walk per window: `messages_by_time`, `messages_by_sender` or `messages_recent`. A
redaction or an edit drops the windows it touches.
Completion folds and splits words the way the index does (`vocab.Fold`,
`vocab.Words`); `TestRecentVocabTokensAgreeWithTheIndex` checks it against the index's
own terms.

If you change the tokenizer, the vocabulary changes with it, and every existing index
was built with the old one: that is a rebuild, not a migration.

## Account-data mirrors

Some things belong to this client and have no Matrix event of their own. They live in
**room account data on the homeserver**, under this client's namespace, and the cache
holds a mirror:

- `starred` mirrors `org.kith.starred`: your private bookmarks per room.
- `spam_rooms` mirrors `org.kith.spam`: which filter caught a room, and which rule.
- `spam_released` mirrors the releases in `org.kith.spam`: a room here stays out of
  Spam whatever the rules say.

The homeserver has the record; the table exists because search and the room list
have to join against something, offline. That split is what makes a rebuild
survivable: the stars and verdicts come back from account data on the next sync, and
they reach this account's other machines. A mirror write replaces the room's whole
set, because account data always carries the whole value. If you add another
client-specific fact that must outlive a rebuild, follow the same pattern.

## Changing the schema

### How a file is brought up to date

`Open` calls `prepare`, and `prepare` goes by **`PRAGMA user_version`**, checked
against the shape when it claims to be current. With
`current = schemaVersion + len(migrations)`:

| `user_version` | what happens |
| --- | --- |
| `== current`, and every table `baseSchema` (plus migrations) creates is there with the same columns | nothing, except `ensureIndexes` (below) |
| `== current`, but a table is missing or its columns differ, or an index will not build | rebuild, as below: the number was reused for another shape |
| `schemaVersion ≤ v < current` | the pending migrations `migrations[v-schemaVersion:]` run in order, each in its own transaction that applies the SQL **and** stamps its version; rows are kept |
| anything else: `0` (a new file), below the base, or **above `current`** | rebuild: if the file has tables, copy it aside with `VACUUM INTO '<path>.pre-schema-<YYYYMMDD-HHMMSS>'` (the two newest copies are kept); drop every table; `VACUUM`; create `baseSchema` and stamp `schemaVersion` in one transaction; `ANALYZE`; then run **every** migration on top |

The shape check (`shapeMatches`) compares each table's columns by name, type, `NOT
NULL` and primary-key position, against the same statements built once in memory.
Column order, defaults and SQL text are left out: a column added by a hand `ALTER`
sits last and reads differently in `sqlite_master`, and is the same column. Tables the
schema does not create are ignored. `TestACacheOfTheCurrentNumberButAnotherShapeIsRebuilt`,
`TestAMissingTableWithoutAnIndexIsRebuilt` and `TestTheShapeCheckIgnoresColumnOrderAndText`
hold it.

A few consequences are worth knowing before you write anything:

- **A version from the future is rebuilt too.** An older binary opening a newer cache
  starts it over rather than refusing. Two builds alternating would each rebuild after
  the other: a slow start, not corruption, and the price of one rule.
- **Every migration also runs on a brand-new file**, on top of a `baseSchema` that
  already contains its change. So every migration must be a no-op against the current
  base: `CREATE … IF NOT EXISTS`, `DROP … IF EXISTS`.
- **The copy aside is best effort.** `VACUUM INTO` is WAL-aware and consistent, and it
  refuses to overwrite an existing file. If it fails, the rebuild goes ahead anyway
  (an empty cache is a working client) and the daemon logs "the previous cache could
  not be kept" (`Cache.AsideFailed`).
- **A rebuild must also reset the sync position**, which lives in the crypto store.
  Before the sync loop starts, `InProc` resets the position when `Cache.Rebuilt` says
  so *or* when the cache holds no joined room (`Cache.HoldsRooms`). Otherwise the
  client would resume from a token that predates the wipe and never refetch what was
  discarded. Emptiness is the durable signal: a crash between a rebuild or
  `--clear-cache` and the reset leaves the cache empty, so the next start resets. A
  reset that fails stops the sync instead of resuming from the old token.

`schemaVersion` is `1` and `migrations` is empty: the base is the whole schema as of
the first public release, so a current cache is at version 1.

### Indexes need no migration

Indexes are derived state. On every open, including the "already current" path,
`ensureIndexes` makes the file's explicit indexes exactly the ones `baseSchema` declares
(parsed out of the constant by `baseIndexes`). It creates a missing one, rebuilds one
whose definition in `sqlite_master` differs, drops one the base no longer declares, and
runs `ANALYZE` if it changed anything. So **adding, changing or removing an index means
editing `baseSchema` and nothing else.** `TestABaseIndexComesBackOnOpen`,
`TestBaseIndexesFindsExactlyTheDeclaredOnes`, `TestAFreshCacheNeedsNoIndexChanges` and
`TestOpenRebuildsAChangedIndexAndDropsAnUndeclaredOne` hold this in place.

Two rules keep the parser honest: write each index as its own `CREATE [UNIQUE] INDEX
name ON …;` statement, and do not put `--` inside a string literal in `baseSchema`
(line comments are stripped before parsing).

Every explicit index must be declared in `baseSchema`: one created anywhere else is
dropped on the next open.

### Adding a migration

For any change that is not an index:

1. **Change `baseSchema`**, so it stays the complete, readable description of the
   database. A fresh cache is built from it.
2. **Append one statement to `migrations`.** Never insert, reorder, edit or delete a
   shipped entry. Entry `i` stamps version `schemaVersion + i + 1`; a cache resumes at
   the version it holds, so anything that moves below that number is never seen.
   Removing something is another migration (`DROP TABLE IF EXISTS …`), never a
   deleted entry.
3. **Guard it** so it is a no-op against the base: `CREATE TABLE IF NOT EXISTS`,
   `DROP … IF EXISTS`.
4. **Spell a new table exactly as the base does.** `TestMigratedTablesAreSpelledLikeTheBase`
   builds a cache without the table, migrates it, and compares the stored `CREATE`
   text with a fresh cache's, whitespace-collapsed. SQLite drops the `IF NOT EXISTS`
   from the stored text but keeps everything else, including `--` comments inside the
   parentheses, so keep comments outside the `CREATE TABLE` body. The test also fails
   if a migration creates a table the base does not have.
5. **Append a row to `migrationFingerprints`** in `internal/db/fingerprint_test.go`.
   `TestTheMigrationLedgerIsAppendOnly` fails until you do, and its message prints the
   fingerprint to paste (the first 12 hex characters of SHA-256 over the statement
   with whitespace collapsed, so reformatting a migration is free and changing its SQL
   is not).
6. **If the new table does not cascade from `rooms` or `spaces`, add it to
   `Cache.Clear`.**
7. Run `make test` (or `go test ./internal/db/`).

**Prefer a side table to `ALTER TABLE ADD COLUMN`.** SQLite has no `ADD COLUMN IF NOT
EXISTS`, so an `ADD COLUMN` migration fails with "duplicate column" on every new cache,
whose base already has the column. A side table keyed like its parent, with a row only
when there is a value, is guardable, and is usually the better shape anyway (see
[Conventions](#conventions)). If a column really is needed, that is an argument for a
fold or a rebuild (below).

### Worked example

Suppose the cache should hold each room's pinned events. The table goes into
`baseSchema` beside the other per-room tables, with the index its query needs:

```sql
CREATE TABLE room_pins (
    room_id  TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    event_id TEXT    NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX room_pins_in_order ON room_pins(room_id, position);
```

The migration carries the same table, guarded, and no index, because `ensureIndexes`
creates `room_pins_in_order` on the next open:

```go
var migrations = []string{
    // v2: room_pins, a room's pinned events (m.room.pinned_events).
    `CREATE TABLE IF NOT EXISTS room_pins (
    room_id  TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    event_id TEXT    NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;`,
}
```

The ledger row pins it:

```go
var migrationFingerprints = []struct {
    version int    // the user_version this entry stamps
    what    string // what it does, for the failure message
    sha     string // first 12 hex of sha256 over the statement, whitespace collapsed
}{
    {version: 2, what: "room_pins: a room's pinned events", sha: "1d21ac7696fa"},
}
```

The table cascades from `rooms`, so `Clear` needs nothing. After this, a cache at 1
migrates to 2 and keeps its rows; a new file is built from the base, stamped 1, and
the migration runs as a no-op to stamp 2; a cache at 2 only gets `ensureIndexes`.

### Folding, and bumping `schemaVersion`

Some changes cannot be migrated in SQLite: a new primary key, a changed column type,
a new tokenizer. There are two ways to reset the list, and they are not the same.

- **Fold (no rebuild).** Once `baseSchema` already describes exactly what `migrations`
  produce, set `schemaVersion` to the old `current` (`schemaVersion + len(migrations)`),
  empty `migrations`, and empty `migrationFingerprints`. Caches at the old `current`
  open as current and keep everything; caches older than that are rebuilt. (Resetting
  the number to 1 instead, as the 2026-09-26 fold did, rebuilds every cache stamped
  above 1 unless each is re-stamped by hand. A cache stamped 1 by the old chain has the
  old shape: the shape check rebuilds it rather than opening it as current.)
- **Bump (rebuild everyone).** When the shape itself changes, set `schemaVersion` to a
  number **greater than any version ever stamped** (above the old `current`), with an
  empty `migrations`. A lower number would be read as current by a cache the old chain
  stamped with it; the shape check catches a missing table or column and rebuilds, but
  not a change it cannot see (a new tokenizer, a changed constraint), so do not rely on it. Every user's
  cache is then copied aside and rebuilt from the homeserver on the next start.

A bump costs every user a full refetch, resets the local-only tables (drafts, usage
rankings, sender colours), and leaves bridged history that the homeserver can no longer
serve only in the `.pre-schema-*` copy. Use it when the shape changes too much to
migrate, and never for an index.
