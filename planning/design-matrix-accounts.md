# Design: several Matrix accounts in one kithd

Status: planned, 2026-10-24. Comes before profiles ([design-clients.md](design-clients.md)),
which reuse the name `[[profile]]` once the old one is gone.

## Goal

Matrix becomes a network like every other: any number of accounts in one kithd,
configured as `[[matrix.account]]`, and **every Matrix ID says its network and
account**, with no account treated specially. The old `[[profile]]` (a second Matrix
account run as its own `kithd@<name>`) is removed with everything that exists for
it.

Today `matrix.Adapter` wraps one session (`internal/matrix/adapter.go`), with one
crypto store (`Storage.CryptoPath()`), and Matrix IDs are bare (`!room`, `$event`,
`@user:server`). Two accounts in the same room would share a room ID and event IDs,
so their cache rows, unread and read state would collide. The router already serves
several accounts per network, and every other network's IDs already carry one: the
work is mostly inside `internal/matrix`, plus the calls that today go to "the only
Matrix".

## IDs

Same shape as every network ([design-adapters.md](design-adapters.md#ids-say-their-network-and-account)):

```
matrix:<account>/!room:server        a room, seen from that account
matrix:<account>/$event              an event
matrix:@user:server                  a person (global, as on every network)
```

**`<account>` is a hash of the MXID** (`domain.AccountKey`: 16 hex characters),
not the MXID itself. MXID localparts may contain `/` (and historical ones almost
anything but `:`), and `/` is kith's account separator. A hash never contains `/` or
`@`, so the generic split works with one rule added: a Matrix native starting with
`@` is a person and is never split. It is short, stable (an MXID never changes),
keeps the user's MXID out of every room and event ID (logs, configs, what kith-mcp
hands a model), and is the trade Google Messages already made. It also never reads
as a `matrix:` URI (`parseMatrixURI` accepts only `u/`, `r/`, `roomid/`, `e/`);
a test pins that both ways.

Native IDs may contain `/` (room version 3 event IDs are standard base64): the split
is at the first `/`, so only the account part must be free of it.

Bare IDs stop meaning Matrix: a string with no known prefix is an unknown network,
and config validators refuse bare `!…`/`$…` entries with a message, never read
them as "no entry".

## What is per account

| thing | today | after |
| --- | --- | --- |
| session, device, crypto store | one | one each: `crypto-<instance>-<account>.db`, keyring `<instance>\|matrix\|<account>` (session and pickle key) |
| rooms, spaces, events | bare | `matrix:<account>/…`; the same room joined by two accounts is two rooms |
| account data (`org.kith.starred`, `org.kith.tags`, `org.kith.people`, `m.direct`, `m.ignored_user_list`, marked unread) | native | stays native on the homeserver, translated at the adapter boundary; each account's data names only its own rooms |
| verification, key backup, ignore list, public rooms, knocking, event source, creating a room | "the only Matrix" | the call names its account; clients ask only when there is more than one |
| catch-up batch (no notifications for it) | one global counter | per account |
| cache mirrors replaced wholesale (`ignored_users`, `person_verdicts`, bridge-contact sources) | keyed by network | keyed by account, so one account's update never erases another's |
| people (`matrix:@…`), identities, phonebook | bare MXIDs | `matrix:@…`; phonebook card files stay native and app-neutral, only the reading side adds the prefix |

## Config

```toml
[[matrix.account]]
name       = "personal"
homeserver = "https://matrix.example.org"
user       = "@me:example.org"

[[matrix.account]]
name       = "work"
homeserver = "https://matrix.work.example"
user       = "@me:work.example"
```

Top-level `homeserver` / `user` and `[[profile]]` are gone (pre-release: unknown
keys, no hints). `kith login matrix <name>` as on every network. Matrix account
spaces show in the rail like other networks' accounts, so two accounts' rooms can be
told apart, and `L` signs a Matrix account out.

## Migration of existing data (one-way)

Existing caches, configs, schedules and ledgers hold bare Matrix IDs. A one-off
program in its own directory (`scripts/matrixids/`), run with the daemons stopped,
with a dry-run diff, shipped in the PR that needs it and deleted afterwards. No
product code carries a shim.

- **Cache: rewritten in place**, not rebuilt. With `messages_per_room = -1` the
  cache is the offline, searchable history; a resync would lose everything older
  than the initial sync, and Matrix drafts. About 70 ID columns in 44 tables, plus
  JSON in `drafts.mentions`, `rooms.heroes` and `message_polls.poll`: one
  transaction with foreign keys off, a (table, column, kind) map, then
  `PRAGMA foreign_key_check`.
- **Config**: string values rewritten (`"!x"` → `"matrix:<k>/!x"`, `"@x:y"` →
  `"matrix:@x:y"`, and inside `room:` / `thread:` values), reviewed as a diff.
- **Schedule queue and assistant ledger**: room and event fields rewritten.
- **When `[[profile]]` goes**: `[storage] instance` pinned to the current instance
  (so WhatsApp, Telegram, Messenger and Signal stores and their keyring entries keep
  their names); each profile becomes a `[[matrix.account]]`; each crypto store
  renamed and its keyring entries copied, so both devices stay verified; the second
  instance's cache rows, schedule and ledger merged into the first; `kithd@<name>`
  disabled. Old files are left for the user to delete once verified.

## Plan

Each PR lands working.

1. **refactor: one seam for Matrix IDs.** Every mautrix ↔ kith conversion and
   account-data codec in `internal/matrix` (about 260 lines in 29 files) goes
   through one per-account value that, for now, maps IDs to themselves. An AST test
   forbids ID conversions outside it, which makes step 2's flip complete by
   construction. Domain MXID helpers (bridge detection, phonebook, cards,
   mentions) accept a prefixed ID; sigil checks outside `internal/matrix` move to
   domain predicates. No behaviour change.
2. **feat!: Matrix IDs say `matrix:` and the account** (still one account per
   kithd). `matrix` joins the ID table; `MatrixRooms` becomes the account's room
   owner; the seam flips; the cache's rewind check is per account; validators
   refuse bare IDs; docs, the starter config and design-adapters updated. Ships the
   migration program (cache, config, schedule, ledger). **Failure review** (enforces
   scope): the scope generator gains a leftover bare entry, `room:matrix:<k>/!x`, a
   room of an unconfigured account, a `matrix:@…` person, and `room:` with a kith
   ID; the migration's test enumerates every text column from `sqlite_master` and
   asserts no bare ID survives in an ID column and free text is untouched, with a
   mutation check (drop one column from the map, the test fails).
3. **feat: Matrix-only calls name their account.** Verification, key backup,
   ignore list, public rooms, knocking, event source, join by alias and room
   creation take an account (API, proto, router; `sole[]` goes, since it would
   silently pick the first account). Matrix offers rooms and spaces through
   `ChatOffers` like other networks. Clients ask for the account only when there
   is more than one. Behaviour unchanged with one account.
4. **fix: Matrix cache mirrors and catch-up are per account.** `ignored_users` and
   `person_verdicts` gain an account (schema migration); bridge-contact sources are
   per account; the catch-up counter, sync state and key backup run per account.
   **Failure review** (merges several writers): the generator interleaves two Matrix
   accounts' room, space, ignore, verdict and people writes and expiry, and asserts
   neither erases the other; plus: B's catch-up batch never notifies after A has
   caught up. Each addition fails on today's code first.
5. **feat!: several Matrix accounts; `[[profile]]` removed.** `[[matrix.account]]`
   and `kith login matrix <name>`; the adapter routes by account, merges streams,
   and adds or removes accounts on reload; per-account crypto store and keyring
   entries; account spaces in the rail and sign-out. Removed with `[[profile]]`:
   `--profile` on kith, kithd and kith-mcp, `kithd@.service`, the per-profile
   launchd plist, `PROFILE=` in the Makefile, the nix modules' `profiles`, the
   storage instance's `-<name>` suffix, packaging hooks, and the docs. The
   migration program's second step. **Failure review** (concurrent): accounts added
   or removed by reload while another logs in, resumes or syncs; a stop during a
   resume; a login for B while A syncs; merged streams closing with no message lost
   after its cache write; every exit route (SIGINT, SIGTERM, reload).

Then profiles ([design-clients.md](design-clients.md), Phase 0) take the freed name.

## Decisions

Recommended answers in bold; the plan above assumes them.

1. **Account part: a hash of the MXID**, or the raw MXID with a Matrix-only split
   rule (first `/` after the first `:`), which is readable but long and puts the MXID
   in every ID.
2. **Cache: rewrite in place**, or drop Matrix rows and resync (cheaper; loses
   offline history and Matrix drafts).
3. Person-level state across accounts (ignore list, people marks): **applied to
   every logged-in account, read as the union**, or one account only.
4. Which account opens a `matrix:` link or joins an alias: **the account that has
   already joined the room, preferring the one of the message holding the link,
   then the open room's; otherwise ask** when there is more than one.
5. Typed bare MXIDs (prompts, `kith ignore`, `from:`, kith-mcp arguments): **turned
   into `matrix:@…`** as input normalisation (what people type, not stored data).
6. `{mxid}` / `KITH_MXID` in notification commands already carry non-Matrix IDs:
   **renamed** (`{sender_id}` / `KITH_SENDER_ID`) in step 2.
7. `allow_token_file` and `bridge_contacts`: **stay top-level** (bridges are
   already filtered by homeserver; only their sources become per account).
