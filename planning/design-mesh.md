# Design: bridges pair, and the mesh CRDT (Phase 2b of design-clients.md)

Status: planned 2026-10-30, decisions at the end. Phase 2b of
[design-clients.md](design-clients.md): a second kithd pairs with the first, and the
state no network holds for kith crosses between them. It builds on Phase 2a
([design-remote.md](design-remote.md)): device keys, pinned mutual TLS, pairing by QR,
the roster file. Failover between bridges stays Phase 3.

## Goal

Two kithd on two machines (a home server and a laptop, a VPS and a desktop) run as one
kith: the same tags, filters, notification rules and profiles; a room read on one is
read on the other; a draft started on one is there on the other; a device unpaired on
one is cut off by both. Today each kithd is alone:

- **The roster is one file per kithd** (`devices-<instance>.toml`,
  `internal/daemon/devices.go`): a device paired with one bridge is unknown to another,
  and unpairing a lost laptop on one leaves it admitted by the rest.
- **The config is one file per kithd** (`config.toml`), written whole by kithd behind a
  revision check (`ConfigFile.Write`, `internal/daemon/configfile.go`) and edited by
  hand (`WatchConfig` reloads it; `.bak` keeps the first hand-written copy). Nothing
  carries an edit to another machine.
- **Soft state lives in the cache** (`internal/db`): read positions (`room_unread`,
  `thread_read`), drafts (`drafts`, `draft_threads`), and for networks that keep none
  of their own, tag and person verdicts (`tag_verdicts`, `person_verdicts` rows with no
  account). The cache is rebuilt on doubt ([docs/database.md](../docs/database.md)), so
  it cannot be a replica's only copy, and it has no history to merge from.
- **No bridge can dial another.** `kith join` pairs a client device; a kithd has no
  call that syncs, and every call today is unary or server-streaming.

Not a goal: messages. Each bridge syncs from the networks on its own
(design-clients.md decision 12); the mesh carries only what no network holds.

## What crosses, and what does not

The test for each piece of state: **two bridges must agree on it, and no network
carries it between them.** Replicas are every store-holder: in this phase, the bridges
(design-clients.md decision 12). A thin client (the TUI, `kith-notify`, `kith-mcp`,
`kith-relay`) writes through its bridge, and the bridge stamps the write as its own.

| state | today | crosses? | kind |
| --- | --- | --- | --- |
| the roster: devices and bridges | `devices-<instance>.toml` | **yes**: an unpairing must reach every bridge | signed set |
| config: rules, tags, filters, profiles, display, notifications, agent, assist | `config.toml` | **yes**, the shared keys (below) | register per key, conflicts kept |
| config: what describes this machine | `config.toml` | no: `[storage]`, `[remote]`, `[log]`, `allow_token_file`, `[backup]`, paths (`[commands] dir`, `[phonebook] dir`, `[spell] command`; `[complete.model]` command, model, threads and port), network accounts | (stays in the file) |
| read position per room and per thread | `room_unread.read_event`/`read_ts_ms`, `thread_read` | **yes** | max register |
| marked unread | `room_unread.marked` | **yes** | register |
| drafts | `drafts`, `draft_threads` | **yes** | register keeping every concurrent version |
| tag and person verdicts, networks that keep none | `tag_verdicts`, `person_verdicts` (no account) | **yes** | register |
| stars, verdicts on Matrix | account data, mirrored in the cache | no: the homeserver already carries them to every bridge on the account | (decision 6) |
| scheduled messages | `scheduled-<instance>.toml` | no: the bridge that holds one sends it | (decision 4) |
| emoji and mention rankings | `emoji_usage`, `mention_usage` | no | (decision 5) |
| network sessions, crypto stores, sync positions | keyring, `crypto-*.db`, per-network stores | never: one process owns a device's keys | |
| the cache: messages, rooms, members, media | `cache-*.db`, media folder | never: each bridge fetches its own | |
| the assistant ledger, logs, backups, uploads | state and cache dirs | never: a record of this machine | |
| network archive state | the network (`room_archived`) | no: the network carries it; `push_archive` pushes from each bridge alike | |
| the phonebook | its folder | no: a file (open question 10 of design-clients.md) | |
| secrets a config names (`[assist] key_ref`'s key) | keyring | never: the reference crosses, the secret does not; a bridge without it says so | |

Archive is decided by config (profiles' `archive`, tags' `push_archive`) and pushed to
the network by `archiveWatch` (`internal/daemon/archivewatch.go`), in memory and
idempotent: two bridges pushing the same archive agree. No archive flag crosses.

## The store

The mesh's state is a SQLite file of its own, `mesh-<instance>.db` in the data
directory, sealed under the cache's key (`internal/sealeddb`, as `internal/meta` and the
WhatsApp store are). Not the cache: a rebuild must not lose a replica's unsent writes or
unpair anyone. kithd's backup takes it beside the cache. It holds:

- `ops`: the op log, each replica's ops in order (below), kept for the horizon;
- `entries`: the current entries per key, with the dot and clock of the op that wrote
  each;
- `replaced`: what an op still inside the horizon replaced, so a voided op can be undone
  (Revocation);
- `vv`: this replica's version vector, with the hash of the last op held from each
  origin;
- `peers`: each replica's last reported version vector and when it was heard;
- `rendered`: the config renderings kithd wrote since the file was last edited by hand
  (Config).

The cache stays the read model: `room_unread`, `thread_read`, `drafts` and the verdict
tables are rewritten from the mesh when an entry changes, inside the mesh's apply, and
the unread counts follow as today. The cache never writes back.

## The data model

Rolled in kith, in two packages: `internal/mesh/crdt` (pure: clocks, dots, version
vectors, ops, the merge of each kind, the roster rules; imports the standard library and
`internal/devicekey`) and `internal/mesh` (the store, compaction, snapshots and the sync
session over any pair of frame streams). Each needs a depguard rule;
`scripts/deps-check.sh` keeps both out of the TUI, the wire and `kith-mcp`.

### Clock and IDs

- **A replica** is a device ID (`devicekey.ID`). In the store, an index into a
  `replicas` table; on the wire, the full ID.
- **The clock** is a hybrid logical clock: 48 bits of Unix milliseconds and a 16-bit
  counter in one `uint64`. Send/receive rules as published (Kulkarni et al.): a write
  takes `max(now, last)+1`; a received op moves `last` up to its clock. Ops are ordered
  by `(clock, origin ID)`, so no two ops tie. The physical time is passed in (`Tick(now)`),
  so tests drive it without a seam.
- **A dot** is `(origin, seq)`: each replica numbers its own ops 1, 2, 3…, assigned in
  the transaction that writes the op, as the cache numbers its changes
  (`change_counter`, [design-replay.md](design-replay.md)).
- **A version vector** maps origin to the highest seq held, contiguous: holding seq 7 of
  an origin means holding 1–7. Each entry carries the hash of that op, so two replicas
  holding different ops under one dot see it in the first exchange (Equivocation).
- **Keys** are strings by kind: `config/<toml path>`, `read/<room>`,
  `thread/<room>/<root>`, `marked/<room>`, `draft/<room>`, `tag/<room>/<tag>`,
  `person/<person>/<tag>`, `roster/<device ID>`.

### An op

One transaction of one replica: its writes to one or more keys, applied together or
not at all.

```proto
message Op {
  bytes  origin = 1;          // device ID
  uint64 seq    = 2;
  uint64 clock  = 3;          // HLC
  repeated Dot context = 4;   // origin's version vector when it wrote: what it had seen
  repeated Write writes = 5;  // key, kind, value (absent: delete)
  bytes  sig    = 6;          // origin's device key over 1–5 (decision 7)
}
```

- **Causal delivery.** A replica applies an op only when it holds everything in the op's
  context, and holds back the rest until it does. A peer sends ops in the order it
  applied them, which is causal, so holding back is rare (a relay restarting mid-batch).
- **Exactly once.** An op whose dot the version vector already covers is a duplicate and
  is dropped, whoever relays it. This is what lets a deletion leave no tombstone: an op
  it replaced can never come back.
- **Bounded.** An op is at most 1 MiB; a write larger is refused at its source and the
  caller told (a draft that long stays on its bridge, and says so).

### The kinds

- **Register, keeping concurrent versions** (config, marked, drafts, verdicts). A write
  to a key replaces every entry of that key its context covers; an entry it had not seen
  is concurrent and stays beside it as a sibling. One sibling is the plain case; two or
  more is a conflict, and each kind says how it reads one:
  - **config**: the newest by `(clock, origin)` wins; the others are kept as the
    conflict and shown (Config). A write that has seen them all ends it.
  - **marked, verdicts**: the newest wins, silently. Two bridges' filters firing on the
    same message write the same verdict; equal values are no conflict.
  - **drafts**: every sibling's words survive (Drafts).
- **Max register** (read positions). The value is `(read_ts_ms, event ID)`, ordered by
  time, then ID; merge is the larger. **A message read never becomes unread again**
  (design-clients.md decision 7): no write lowers it, and revocation never voids one.
  A position for a room a bridge does not run is kept and passed on, inert there.
- **Signed set** (the roster): below.

Counters are not built until something counts (decision 5).

### Size, compaction and snapshots

- **The op log is kept for the horizon**, 30 days by clock (decision 10), and longer
  for an op some live replica has not acknowledged. Older ops fold into the entries and
  leave the log; `replaced` rows go with the op that replaced them.
- **A peer behind the horizon gets a snapshot** instead of ops: every entry with its
  dot and clock, the sender's version vector, and its roster. The receiver merges entry
  by entry, and **drops each of its own entries whose dot the snapshot's vector covers
  but the snapshot lacks**: deleted meanwhile (the causal-context rule of delta CRDTs).
  Its entries the vector does not cover are its own unsent writes; they go back as ops.
- **A replica silent past the horizon is dormant**: it no longer holds the log back. It
  comes back through a snapshot.
- **What grows**: config keys (hundreds), one read position per room ever read
  (thousands, ~100 bytes), drafts in progress, verdicts, the roster with its removals.
  The draft cadence keeps the log small: the bridge writes a draft op when its composer
  save has been idle 5 seconds, or the room is left, not per keystroke. `kith mesh`
  shows the store's size; past 64 MiB kithd says so in its log.

## The roster

Today's roster entries (name, ID, added when and by whom) become ops under
`roster/<device ID>`, each **signed by the device that made it**, with two more fields:
a role (`bridge` or `device`) and, for a bridge, the addresses it is dialed at (its
`[remote] listen`, wildcards expanded as `Devices.dialable` does). Phase 3's failover
reads those addresses; this phase only dials bridges with them.

- **Add** `(subject, its public key, name, role, addresses)`, signed by a member. The
  public key travels so any replica can check the subject's signatures; its ID must be
  the key's.
- **Remove** `(subject, cut)`, signed by a member; `cut` is the remover's vector entry
  for the subject. Any member may remove any other, itself included (design-remote: any
  paired device may unpair a lost one). A removed ID stays listed as removed; an add
  whose context covers the removal re-pairs it (a new pairing, never a stale op).
- **Rotate** `(old ID → new key)`, signed by both keys: the entry keeps its name and
  role, the old ID is cut where it stood (Key rotation).
- **Membership is checked in causal order**: an op counts only if its signer was a
  member in the op's context. The first bridge's own entry is its genesis, self-signed;
  every other entry chains back to it.

**Concurrent changes**: a remove and an add of the same device, concurrent: the remove
wins (removing is the safer error; re-pair). Two members removing each other at once:
both are removed, and each, seeing itself removed, stops syncing and says so.

**The file stays**: `devices-<instance>.toml` is rendered from the mesh after every
change. A hand edit is read as today (on the next change, listing or reload) and turned
into ops signed by this bridge: an entry taken out is a removal, one put in an add. A
file that does not read keeps kithd from listening, as today. `Devices`
(`internal/daemon/devices.go`) keeps admitting by ID in `VerifyConnection`, now against
the mesh's members, and `cutLocked` runs for a removal that arrives by sync as for one
made here.

### Revocation

A thin device is cut off when its removal reaches the bridge it is connected to; what it
wrote before then is that bridge's own op and stands, as an unpairing does today.

A removed **replica's** ops after the cut are void on every replica (decision 8): they
are taken out, and what they replaced comes back from `replaced`. Every replica voids
the same ops, so they converge. Read positions are never voided. A removal reaches back
only within the horizon of its own clock; a replica that was dormant comes back through
a snapshot first, so its later removals cut at what it now holds.

### Equivocation

A replica holding the key could sign two ops with one dot and send each to a different
peer. The hash in each vector entry shows it on the next exchange: the origin is
treated as removed at the last seq both agree on, and `kith mesh` and the log name it. A
paired replica can do anything anyway (paired = full authority); this only keeps such
a fork from leaving bridges disagreeing for good.

### Key rotation

`kith devices rotate` (on a device, or `--bridge` for its kithd) makes a new key, signs
the rotation with both, and has its bridge write it. For a device, its next connection
uses the new key; the old one is refused once the rotation arrives. For a bridge, the
devices pinning its old ID must learn the new one: for the horizon the bridge answers
TLS with the old key when the client's SNI names the old ID, and with the new one
otherwise; every dialing client sends its pinned ID as SNI (`<id>.kith`), and on
connecting with the old ID it reads the roster and rewrites the pin in `device.toml`. A
lost key is not rotated: remove the device and pair again.

## Config

**One file, two owners.** The shared keys come from the mesh; this machine's keys
(the table above) stay in the file and never leave it. `internal/config` lists the local
paths once (`config.Local`), and the rule that sorts a new key is: **it names this
machine (a path, an address, an account it runs, how it logs or backs up) → local;
everything else → shared.**

**Granularity** (decision 2): each leaf key of a table is one register, named by its
TOML path (`display.space_priority`, `notifications.max_per_room`); each array,
including each list of tables (`[[tag]]`, `[[filter]]`, `[[profile]]`,
`[[notifications.rule]]`, `[[display.space_rule]]`), is one register, whole. A
register's value is the key's canonical TOML (`config.Encode`'s), so a rewrite that
changes nothing writes nothing; an absent key is written as unset and reads as its
default.

**Rendering.** kithd writes the file as local keys from the file plus shared keys from
the mesh, through `config.Save` (so `.bak` still keeps the first hand-written copy),
whenever a shared entry changes. It writes only while the file still equals its last
rendering; a file changed since is first read as a hand edit (below). Each rendering is
kept in `rendered` until the next hand edit is read.

**What a write becomes:**

- **The settings screen, `:set`** (`UpdateConfig` on a base revision): as today the
  revision check refuses a stale write; what passes is diffed against the current
  config, and each changed shared key becomes a write in one op, each local key goes to
  the file.
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
seen the other, with different values: the newer is applied, both are kept. `kith
config conflicts` lists each key, both values, which bridge wrote each and when; the
settings screen marks the key. Keeping either is a write that has seen both, which ends
the conflict on every bridge.

**Joining** (decision 3): a bridge that joins a mesh takes its shared config; its own
shared keys are kept aside as `config.toml.pre-mesh` and listed by the join, not merged
key by key.

## Drafts

A draft is a register whose concurrent versions all survive. Two writers on one bridge
are already merged there: the seat lets one window type per room (`Seat`), kith-mcp only
appends, and `ReplaceDraft` compares and sets (`internal/db/drafts.go`), with the
window's rebase on a refusal (`rebaseOnto`, `internal/tui/draft.go`). Across bridges
there is no seat, and two windows on two bridges, or a window on one and kith-mcp on the
other, can write the same room's draft at once.

- **Each version records the words it was written over** (`over`, as `ReplaceDraft`
  already takes): drafts are small, and the merge needs the base.
- **Reading siblings**: the newest is taken whole (targets: reply, thread, edit); each
  other sibling's words are added after it by the same rule `rebaseOnto` applies today:
  what it added over its base (`addedOver`), or the whole of it when it did not extend
  its base. Folded in clock order, so every bridge reads the same draft. Text may be
  repeated when both sides rewrote; none is lost.
- **The next write ends it**: a save covering every sibling replaces them all.
- **Sending empties the draft** with a delete over what that bridge holds; an append
  made concurrently elsewhere survives it, and the draft reappears with only the
  appended words.
- **One rule**: [design-electron.md](design-electron.md) step 4 moves the rebase into
  kithd (`ReplaceDraft` takes the base and kithd rebases). The mesh reads siblings with
  that same function, in `internal/domain`. If step 4 has not landed, the drafts PR
  moves it.

## Read positions

A read on a bridge (`MarkRead`, `MarkRoomsRead`, `MarkThreadRead`) is saved locally
first, as today (local-first, #207–#210), and written to the mesh in the same apply. A
position arriving from a peer is merged by max into `room_unread`/`thread_read` and the
room recounted. **Receipts**: every bridge that runs the room's account and holds a
position the network has not acknowledged sends the receipt, retried as today; two
bridges sending the same receipt is harmless, and a receipt never moves back
(design-clients.md). `MarkRoomUnread` writes `marked/<room>`; a later read writes it
false.

## Sync

**Who dials.** Each bridge dials every member bridge that has addresses, over the
listener's pinned mutual TLS (`pinnedTLS`, `dialBridge` in
`internal/daemon/dialbridge.go`), with backoff from 1 second to 5 minutes and HTTP/2
PING as clients have. A bridge with no `[remote] listen` (a laptop behind NAT) only
dials. Between two bridges that both dial, the stream dialed by the lower ID is kept and
the other closed.

**One bidirectional stream**, because often only one side can dial the other:

```proto
service MeshService {
  rpc Sync(stream SyncFrame) returns (stream SyncFrame);
}
// SyncFrame is one of: Hello{replica, wall clock, version vector with hashes,
// protocol version}, Ops{ops}, Snapshot{chunk, last}, Ack{version vector}.
```

`api/proto/mesh/v1/mesh.proto`, a service of its own on the listener's mux, behind a
gate that serves it only to a connection authenticated as a member bridge (as
`pairingGate` serves `Pair` alone): over the socket, or from a device, it is refused.

1. **Hello** both ways. A peer whose wall clock is more than 60 seconds off is refused
   with a status naming the gap (`kith mesh`: "laptop's clock is 3 h ahead").
2. **Catch-up**: each side sends, per origin, the ops after the other's vector, in its
   apply order; or a snapshot when the other is behind the horizon.
3. **Live**: the stream tails the op log, as the message stream tails the cache
   ([design-replay.md](design-replay.md)): after each commit that wrote ops, each sync
   stream sends what its peer has not acknowledged. A slow peer lags and catches up;
   nothing is dropped.
4. **Ack** after each applied batch; `peers` records it, which is what lets the log
   shrink.

**Relaying**: a bridge sends every origin's ops, not only its own, so two bridges that
cannot reach each other converge through a third. **An op from the future** (clock past
the receiver's now plus 60 seconds) is held with every later op of its origin until the
receiver's clock reaches it: never applied early, so a fast clock cannot take every
register for an hour. **Reconnection** is the same Hello: the vectors say what is
missing. **A peer back after weeks** gets a snapshot, then its own writes go back as ops.

**HTTP/2.** Bridge to bridge is TLS, so ALPN already gives HTTP/2 (`ServeTLS`,
`ForceAttemptHTTP2`). The socket stays HTTP/1.1 until a replica sits on the bridge's own
machine (decision 9).

## Pairing a bridge

The same flow as a device (docs/remote.md), with the new bridge's kithd as the device:

1. On a device the mesh trusts, `kith pair` opens a pairing, as today.
2. On the new bridge's machine, `kith join --bridge '<uri>'` asks that machine's kithd,
   over its socket, to join (`JoinMesh`, server-streaming the digits and the outcome).
   That kithd dials the URI's addresses with its own `device` key, pinned to the URI's
   ID, and calls `Pair` with the token, role `bridge` and its dialable addresses.
3. Both screens show the six digits (`pairing.Digits`); the person confirms on the
   trusted device. The confirming bridge writes the add, signed.
4. The new bridge opens `Sync`, receives a snapshot, and adopts it: its genesis is
   dropped, its shared config set aside (decision 3), its roster entries, drafts, read
   positions and verdicts written again as its own ops, so they chain from its add. Its
   devices are now admitted by every bridge, and every bridge's devices by it; the join
   lists them before confirming.

A device keeps dialing the bridge its `device.toml` names; that it is admitted by the
others too is what Phase 3 builds on. Store-holding clients (Electron, Android) pair the
same way with a replica role when they exist; nothing in this phase makes one.

## Two bridges, and what is Phase 3

- **Different networks** (one runs WhatsApp and Matrix, the other Telegram and Slack):
  their rooms do not overlap. Each client sees its own bridge's rooms (a client merging
  bridges is open question 7). Rules, profiles, verdicts and drafts are shared; entries
  for rooms a bridge does not run are kept and passed on, inert. A profile naming
  `protocol:telegram` shows nothing on the bridge that does not run Telegram.
- **The same accounts** (each bridge its own linked device): room IDs are the same on
  both, since `<account>` is the network's own ID (design-adapters.md), so read
  positions, drafts and verdicts meet on the same keys. Each bridge notifies its own
  devices; each sends a scheduled message only if it holds it.
- **Accounts are each bridge's own** (decision 1): which accounts a bridge logs into
  stays in its file. Logging in is per machine anyway.
- **Out of scope, Phase 3**: heartbeats, a client choosing or falling over between
  bridges, dedup by network ID, buffering sends, resuming at another bridge. This phase
  writes each bridge's addresses into the roster and admits every member's devices
  everywhere; nothing here moves a client.

## Plan

Each PR lands working. A pure package lands with its first caller (`deadcode`).

1. **feat: the roster is kept as signed ops.** `internal/mesh/crdt` (clock, dots,
   vectors, ops, the signed set) and `internal/mesh` (the store, a replica of one); the
   roster moves into it: genesis from today's file, the file rendered from it, hand
   edits read into signed ops, `Devices` admitting from it, `kith devices` showing roles.
   Migration is the file read once into ops; the old file kept as `.pre-mesh`. Depguard
   rules, `docs/database.md` (the mesh file), `docs/remote.md`. **Failure review**
   (enforces scope, merges writers): a hand edit racing `kith devices add`, a hand edit
   adding an ID that does not read, a store that does not open (no listening, as for a
   roster that does not read), a migration cut off midway and run again, an op signed by
   a non-member, a forged signature, a removal of the last device, a store from a newer
   kith.
2. **feat: bridges pair and sync.** `MeshService.Sync`, its gate, `kith join --bridge`
   and `JoinMesh`, adoption, the dial loop, causal delivery, the horizon, snapshots,
   revocation and equivocation, `kith mesh` (peers, last heard, lag, store size, clock
   gaps); SECURITY.md's "A listening kithd" gains the sync. Only the roster crosses
   yet, which is enough to unpair everywhere. **Failure review** (concurrent, merges
   writers, enforces scope): a model test of three to five replicas whose generator
   interleaves writes, partitions, reconnects, duplicated, reordered and replayed
   frames, a relay restarting mid-batch, clock skew within and past the bound, a replica
   dormant past the horizon, a removal concurrent with the removed replica's writes and
   with an add of it, two members removing each other, an equivocating origin; it
   asserts every replica converges to the same entries and the same voided set, and
   that no removed replica's op after its cut survives. Plus: both bridges dialing at
   once, an unpaired or device-role key calling `Sync`, the socket calling it, a peer
   unpaired mid-stream, a join whose confirmation never comes.
3. **feat: read positions cross bridges.** `read/`, `thread/`, `marked/`; the cache's
   unread rewritten from the mesh; receipts. **Failure review** (merges writers, caches a
   derived result): the generator of step 2 with reads on every replica asserts that a
   position never moves back on any replica and that `room_unread` equals what the mesh
   holds after every step; a network echoing an older receipt, a mark unread concurrent
   with a read, a room a bridge does not run, a cache rebuilt under a mesh that holds
   positions, a voided replica's reads (kept).
4. **feat: the shared config crosses bridges.** `config.Local`, per-key registers,
   rendering, the three-way read of a hand edit, conflicts (`kith config conflicts`,
   the settings screen), a merged config that does not check, the join's
   `config.toml.pre-mesh`; `docs/configuration.md` marks each local key. **Failure
   review** (merges writers): a generator of settings writes, hand edits and remote
   writes in any order, with an editor buffer opened before any remote write and saved
   after, asserting that no remote change is undone by a save that did not touch its
   key and that every bridge renders the same shared keys; a hand edit to a local key
   (never crosses), a hand edit while kithd renders, an invalid hand edit, two concurrent
   writes of equal values (no conflict), a deleted key against an edited one.
5. **feat: drafts cross bridges.** Drafts move from the cache into the mesh (the
   `drafts` tables become its read model; a rebuild no longer loses them), siblings, the
   5-second cadence. Depends on design-electron.md step 4 or does its move. **Failure
   review** (merges writers): `TestDraftsSurviveEveryInterleaving` gains a second bridge
   with its own window and kith-mcp, partitions and a send on one side; it asserts no
   typed or appended word is lost and every bridge reads the same draft; plus a draft
   over 1 MiB, a send concurrent with an append, a room left mid-save.
6. **feat: verdicts cross bridges on networks that keep none.** `Router.keeperFor`'s
   store keeper writes `tag/` and `person/`; Matrix's stay in account data. **Failure
   review** (merges writers): a filter firing on both bridges for one message, a release
   concurrent with a move, a retag (`domain.Retagged`) concurrent with a move under the
   old name.
7. **feat: keys rotate.** `kith devices rotate`, the rotation op, a bridge answering on
   its old key by SNI for the horizon, clients rewriting their pin. **Failure review**
   (enforces scope): a rotation signed by one key only, a rotated-out key connecting
   after the rotation arrived, a rotation concurrent with the device's removal, a client
   offline past the grace (it re-pairs, and says so).

## Left open

- **A client on several bridges** (design-clients.md open question 7) and **failover**
  (Phase 3).
- **Store-holding clients** (Electron's partial cache, Android): they pair with a
  replica role and sync through their bridge; syncing device to device directly waits
  for them.
- **A text CRDT for drafts**: only if keeping every sibling repeats text in practice.
- **WAN reach between bridges**: as for devices, addresses or a tailnet (design-remote).
- **Scheduled sends and rankings**, if decisions 4 and 5 go the recommended way.
- **The phonebook** stays a file on each bridge (open question 10).

## Decisions

Each needs the user's answer; the plan above follows the recommendation.

1. **Do network accounts cross?** (a) Each bridge's own: `[[<network>.account]]` stays
   in its file; (b) shared, each bridge running every account. **Recommend (a)**: a
   session belongs to one machine, and the hybrid shape (design-clients.md) is bridges
   on different networks.
2. **How fine is a config register?** (a) Each leaf key, and each array whole, `[[tag]]`
   and `[[filter]]` included; (b) lists of named tables keyed by `name`, with an order
   register. **Recommend (a)**: one person rarely edits two tags on two bridges at once,
   and a conflict is shown with both values; (b) can follow if it happens.
3. **What does a joining bridge do with its own config?** (a) Takes the mesh's, its own
   shared keys set aside as `config.toml.pre-mesh`; (b) merges key by key, newest wins.
   **Recommend (a)**: the merge of two configs written apart is arbitrary; joining is
   joining.
4. **Do scheduled messages cross?** (a) No, the bridge that holds one sends it; (b) yes,
   with an owner and a hand-over when the owner is down. **Recommend (a)**: (b) is
   failover (Phase 3) and needs sends idempotent across bridges.
5. **Do emoji and mention rankings cross?** (a) No; (b) per-replica counters. **Recommend
   (a)**: nice to have (design-clients.md), and a counter kind with no data is dead code.
6. **Where do Matrix's stars and verdicts live?** (a) In account data, as now, the mesh
   carrying only networks that keep none; (b) all in the mesh. **Recommend (a)**: the
   homeserver already carries them to every bridge on the account, and a second source
   would have to agree with the first.
7. **Which ops are signed?** (a) Every op; (b) roster ops only, the rest trusted to the
   connection they came over. **Recommend (a)**: ops are relayed, and a signature is what
   lets a relay be checked and a removed replica's writes be named; at human write
   rates it costs nothing measurable.
8. **What does removing a replica do to what it wrote?** (a) Its ops after the cut are
   void everywhere, within the horizon; (b) nothing: each bridge stops taking its ops
   when the removal arrives. **Recommend (a)**: (b) leaves bridges disagreeing about ops
   relayed before the removal reached them. Read positions are never voided.
9. **HTTP/2 on the socket (h2c)?** (a) When a replica first sits beside its bridge
   (Phase 4's relay with a store); (b) now, in step 2. **Recommend (a)**: bridge to
   bridge is TLS, already HTTP/2, and nothing in this phase calls `Sync` over the socket.
   (b) is small (`Protocols` on both ends) if wanted for symmetry.
10. **The horizon.** (a) 30 days; (b) 7; (c) 90. **Recommend (a)**: a laptop bridge
    unused for a month is plausible; past it, a snapshot costs little.
11. **Key rotation for bridges.** (a) In step 7, with the old key answered by SNI for
    the horizon; (b) devices only, a bridge re-paired. **Recommend (a)**: a bridge's key
    is the one most often pinned, by every device.
12. **Clock bound.** (a) 60 seconds, a peer past it refused and named; (b) 5 minutes.
    **Recommend (a)**: every machine here runs NTP; a minute catches a wrong timezone
    or a dead clock battery without false alarms.
