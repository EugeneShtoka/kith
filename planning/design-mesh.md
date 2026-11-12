# Design: the mesh CRDT, what is left (Phase 2b of design-clients.md)

Status: steps 1–4 built (#407, #422, #424, #427): bridges pair and sync, the roster is
kept as signed ops, a WhatsApp DM known by its LID joins its number's room, and read
positions cross bridges. How that works is in `internal/mesh` and
[docs/remote.md](../docs/remote.md). This file holds the steps still to build: the shared
config, drafts, verdicts, and key rotation. Failover between bridges stays Phase 3.

## What is left to cross

The test for each piece of state: **two bridges must agree on it, and no network
carries it between them.**

| state | today | crosses? | kind |
| --- | --- | --- | --- |
| config: rules, tags, filters, profiles, display, notifications, agent, assist | `config.toml` | **yes**, the shared keys (below) | register per key, conflicts kept |
| config: what describes this machine | `config.toml` | no: `[storage]`, `[remote]`, `[log]`, `allow_token_file`, `[backup]`, paths (`[commands] dir`, `[phonebook] dir`, `[spell] command`; `[complete.model]` command, model, threads and port), network accounts | (stays in the file) |
| drafts | `drafts`, `draft_threads` | **yes** | register keeping every concurrent version |
| tag and person verdicts, networks that keep none | `tag_verdicts`, `person_verdicts` (no account) | **yes** | register |
| stars, verdicts on Matrix | account data, mirrored in the cache | no: the homeserver already carries them to every bridge on the account | (decision 4) |
| scheduled messages | `scheduled-<instance>.toml` | no: the bridge that holds one sends it | (decision 2) |
| emoji and mention rankings | `emoji_usage`, `mention_usage` | no | (decision 3) |
| secrets a config names (`[assist] key_ref`'s key) | keyring | never: the reference crosses, the secret does not; a bridge without it says so | |

Each kind is a register keeping concurrent versions (`internal/mesh/crdt`): one sibling
is the plain case, two or more a conflict, and each kind says how it reads one (Config,
Drafts, Verdicts). A bridge joining a mesh adopts its own state of each kind as its own
ops, in the step that makes the kind cross.

## Config

**One file, two owners.** The shared keys come from the mesh; this machine's keys
(the table above) stay in the file and never leave it. `internal/config` lists the local
paths once (`config.Local`), and the rule that sorts a new key is: **it names this
machine (a path, an address, an account it runs, how it logs or backs up) → local;
everything else → shared.**

**Granularity** (decision 1): each leaf key of a table is one register, named by its
TOML path (`display.space_priority`, `notifications.max_per_room`); each array,
including each list of tables (`[[tag]]`, `[[filter]]`, `[[profile]]`,
`[[notifications.rule]]`, `[[display.space_rule]]`), is one register, whole. A
register's value is the key's canonical TOML (`config.Encode`'s), so a rewrite that
changes nothing writes nothing; an absent key is written as unset and reads as its
default. Keys are `config/<toml path>`.

**Rendering.** kithd writes the file as local keys from the file plus shared keys from
the mesh, through `config.Save` (so `.bak` still keeps the first hand-written copy),
whenever a shared entry changes. It writes only while the file still equals its last
rendering; a file changed since is first read as a hand edit (below). Each rendering is
kept in a `rendered` table of the mesh store until the next hand edit is read.

**What a write becomes:**

- **The settings screen, `:set`** (`UpdateConfig` on a base revision): as today the
  revision check refuses a stale write; what passes is diffed against the current
  config, and each changed shared key becomes a write in one op, each local key goes to
  the file. A window's `SetConfigSetting` already writes one key, by its path, over a
  revision.
- **A hand edit** (`WatchConfig` sees the file change): a three-way read. A key counts
  as edited by hand only if its value in the file differs from its value in **every**
  rendering since the last hand edit was read. So an editor that opened the file before a
  remote change arrived, and saves after it, does not undo that change by saving the old
  value of a key it never touched. Edited shared keys become one op; the file is then
  rendered again, which also drops comments as today.
- **A config that does not decode or check** after a merge (two edits each valid,
  together not: a profile naming a tag removed concurrently) is not applied: the bridge
  keeps running its last valid config, as for a bad hand edit today, and every bridge
  says the same thing, since every bridge merges alike. Any later write fixes it.

**Conflicts are shown, not resolved silently.** Two writes to one key, neither having
seen the other, with different values: the newest by `(clock, origin)` is applied, both
are kept. `kith config conflicts` lists each key, both values, which bridge wrote each
and when; the settings screen marks the key. Keeping either is a write that has seen
both, which ends the conflict on every bridge.

**Joining**: a bridge that joins a mesh takes the mesh's shared config, not merged key
by key, and says so in three places:

- **`kith join --bridge` prints it** once the mesh's config has arrived: every shared key
  whose value on this bridge differs from the mesh's, as `path: this bridge's value →
  the mesh's`, then `Set aside in <path>/config.toml.pre-mesh: N keys. Copy back what
  you want to keep (kith config, or by hand).` With none differing it says so.
- **kithd logs a warning** naming the file and the number of keys.
- **The TUI shows a popup**, as kithd raises one for a failed backup (`BackupsSay`,
  `internal/daemon/backupstate.go`) to every profile: the count, the file, and that the
  mesh's values are in force.

`config.toml.pre-mesh` is the whole file as it stood before the join, local keys
included; nothing reads it.

## Drafts

A draft is a register whose concurrent versions all survive. Many writers on one bridge
are already merged there (design-windows.md): any number of windows type in a room,
kith-mcp only appends, and `ReplaceDraft` takes the base a window wrote over and kithd
rebases onto it, keeping both when two windows wrote at once. Across bridges, two
windows on two bridges, or a window on one and kith-mcp on the other, can write the same
room's draft at once too.

- **Each version records the words it was written over** (`over`, as `ReplaceDraft`
  already takes): drafts are small, and the merge needs the base.
- **Reading siblings**: the newest is taken whole (targets: reply, thread, edit); each
  other sibling's words are added after it by the rule kithd's rebase applies today:
  what it added over its base, or the whole of it when it did not extend its base.
  Folded in clock order, so every bridge reads the same draft. Text may be repeated
  when both sides rewrote; none is lost.
- **The next write ends it**: a save covering every sibling replaces them all.
- **Sending empties the draft** with a delete over what that bridge holds; an append
  made concurrently elsewhere survives it, and the draft reappears with only the
  appended words.
- **The cadence**: the bridge writes a draft op when its composer save has been idle 5
  seconds, or the room is left, not per keystroke, which keeps the op log small.
- **Bounded**: a draft over an op's 1 MiB stays on its bridge, and says so.

## Verdicts

On networks that keep no verdicts of their own, `Router.keeperFor`'s store keeper writes
`tag/<room>/<tag>` and `person/<person>/<tag>`; Matrix's stay in account data (decision
4). Of concurrent verdicts the newest wins, silently: two bridges' filters firing on
the same message write the same verdict, and equal values are no conflict.

## Key rotation

`kith devices rotate` (on a device, or `--bridge` for its kithd) makes a new key, signs
the rotation `(old ID → new key)` with both, and has its bridge write it: the roster
entry keeps its name and role, and the old ID is removed. For a device, its next
connection uses the new key; the old one is refused once the rotation arrives. For a
bridge, the devices pinning its old ID must learn the new one: for the horizon the
bridge answers TLS with the old key when the client's SNI names the old ID, and with
the new one otherwise; every dialing client sends its pinned ID as SNI (`<id>.kith`),
and on connecting with the old ID it reads the roster and rewrites the pin in
`device.toml`. A lost key is not rotated: remove the device and pair again.

## Plan

Each PR lands working. A pure package lands with its first caller (`deadcode`).

5. **feat: the shared config crosses bridges.** `config.Local`, per-key registers,
   rendering, the three-way read of a hand edit, conflicts (`kith config conflicts`,
   the settings screen), a merged config that does not check, the join's
   `config.toml.pre-mesh` with its printed list, log warning and popup;
   `docs/configuration.md` marks each local key. **Failure
   review** (merges writers): a generator of settings writes, hand edits and remote
   writes in any order, with an editor buffer opened before any remote write and saved
   after, asserting that no remote change is undone by a save that did not touch its
   key and that every bridge renders the same shared keys; a hand edit to a local key
   (never crosses), a hand edit while kithd renders, an invalid hand edit, two concurrent
   writes of equal values (no conflict), a deleted key against an edited one, a join
   whose config differs in no key and in every key.
6. **feat: drafts cross bridges.** Drafts move from the cache into the mesh (the
   `drafts` tables become its read model; a rebuild no longer loses them), siblings, the
   5-second cadence. **Failure review** (merges writers):
   `TestDraftsSurviveEveryInterleaving` gains a second bridge with its own window and
   kith-mcp, partitions and a send on one side; it asserts no typed or appended word is
   lost and every bridge reads the same draft; plus a draft over 1 MiB, a send
   concurrent with an append, a room left mid-save.
7. **feat: verdicts cross bridges on networks that keep none.** **Failure review**
   (merges writers): a filter firing on both bridges for one message, a release
   concurrent with a move, a retag (`domain.Retagged`) concurrent with a move under the
   old name.
8. **feat: keys rotate.** `kith devices rotate`, the rotation op, a bridge answering on
   its old key by SNI for the horizon, clients rewriting their pin. **Failure review**
   (enforces scope): a rotation signed by one key only, a rotated-out key connecting
   after the rotation arrived, a rotation concurrent with the device's removal, a client
   offline past the grace (it re-pairs, and says so).

Steps 6 and 7 extend the LID merge (step 3) with their keys of the LID room: drafts as a
write over both, verdicts as writes.

## Left open

- **A client on several bridges**, showing everything its profile defines across them
  (decision 6), and **failover**: Phase 3.
- **Store-holding clients** (the desktop's partial cache, Android): they pair with a
  replica role and sync through their bridge; syncing device to device directly waits
  for them.
- **Bridges that are never online at the same time**: a mailbox, a mesh peer that holds
  sealed ops it cannot read, and the envelopes every op would need first
  ([design-mailbox.md](design-mailbox.md), proposed, undecided).
- **A text CRDT for drafts**: only if keeping every sibling repeats text in practice.
- **WAN reach between bridges**: as for devices, addresses or a tailnet
  (design-clients.md open question 2).
- **Notification IDs from the message**, for a device that hears two bridges: they are
  per bridge today (`<boot>-<count>`), so such a device would show one message twice
  (Phase 3, or Phase 5's push, where both bridges POST to a profile's endpoint).
- **The phonebook** stays a file on each bridge (design-clients.md open question 7).

## Decisions (2026-10-30)

Decided by the user on 2026-10-30.

1. **A config register is a leaf key, or an array whole**, `[[tag]]` and `[[filter]]`
   included. Rejected for now: lists of named tables keyed by `name` with an order
   register; it can follow if concurrent edits of two tags happen.
2. **Scheduled messages do not cross**: the bridge that holds one sends it. Rejected: an
   owner with a hand-over, which is failover (Phase 3).
3. **Emoji and mention rankings do not cross.** Rejected: per-replica counters.
4. **Matrix's stars and verdicts stay in account data**; the mesh carries verdicts only
   for networks that keep none. Rejected: everything in the mesh, a second source.
5. **A joining bridge takes the mesh's shared config**, its own set aside in
   `config.toml.pre-mesh`, **with an explicit warning**: `kith join --bridge` prints
   every key set aside with both values and the file's path, kithd logs it, and the TUI
   raises a popup (Config, Joining). Rejected: merging key by key.
6. **A client that has joined several bridges shows everything its profile defines,
   across all of them; no placeholder rows.** The user rejected all three options
   offered (nothing; an "elsewhere" row naming the account and its bridge; such a row
   with a count): "If the laptop client lists both servers, it shows everything
   according to its profile." It lands in **Phase 3**: it needs a client on several
   bridges, a stream position per bridge, dedup by network ID and sends routed by
   account (each bridge publishes the accounts it runs, `bridge/<ID>/accounts`). Until
   then a client with one bridge shows what that bridge holds, nothing synthetic.
7. **Bridges rotate keys too** (step 8), the old key answered by SNI for the horizon.
   Rejected: devices only, a bridge re-paired.
8. **No HTTP/2 on the socket (h2c) yet**: it lands when a replica first sits beside its
   bridge (the desktop's relay with a store). Bridge to bridge is TLS, already HTTP/2.
