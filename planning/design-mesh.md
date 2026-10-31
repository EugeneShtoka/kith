# Design: bridges pair, and the mesh CRDT (Phase 2b of design-clients.md)

Status: planned 2026-10-30; every decision taken the same day (at the end). Phase 2b of
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

- `ops`: the op log, each replica's ops in order (below), kept for the horizon; the
  roster's ops for good (The roster);
- `entries`: the current entries per key, one per write made concurrently, with the dot
  and clock of the op that wrote each;
- `vv`: this replica's version vector, with the hash of the last op held from each
  origin;
- `peers`: each replica's last reported version vector, when it was heard and its
  clock's gap then;
- `forks`: each origin found to have signed two ops under one dot, with both
  (Equivocation);
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
  `person/<person>/<tag>`, `roster/<device ID>`, `bridge/<device ID>/accounts`,
  `alias/<room>` (a room merged into another, step 3).

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
  (design-clients.md decision 7): no write lowers it. A position for a room a bridge does not run is kept and passed on, inert there.
- **Signed set** (the roster): below.

Counters are not built until something counts (decision 5).

### Size, compaction and snapshots

- **The op log is kept for the horizon**, 30 days by clock (decision 10), and longer
  for an op some live replica has not acknowledged. Older ops fold into the entries and
  leave the log.
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

- **Add** `(subject, name, role, addresses)`, and the subject's public key when it is
  known, signed by a member. A bridge's is always known (it pairs over TLS, and it
  signs), and travels so any replica can check its signatures; its ID must be the
  key's. A device paired by its pasted ID, by QR or in the file has only its ID, which
  is all the TLS check needs.
- **Remove** `(subject)`, signed by a member. Any member may remove any other, itself
  included (design-remote: any paired device may unpair a lost one). A removed ID stays
  listed as removed; an add whose context covers the removal re-pairs it (a new pairing,
  never a stale op).
- **Rotate** `(old ID → new key)`, signed by both keys: the entry keeps its name and
  role, and the old ID is removed (Key rotation).
- **Membership is judged by the op's own context**: an op is taken if and only if its
  signer is a bridge of the roster as of the op's context, the roster ops that context
  (and the origin's own earlier ops) holds, their concurrent writes resolved as below.
  Every replica holding the op holds those same roster ops, so every replica decides
  alike, whatever order ops reach it in. Judging by the roster a replica holds when the
  op arrives would not: a removed bridge's op written concurrently with its removal
  would be taken where it arrived first and refused where the removal did. So the
  roster's ops are kept for good (they are few), and a snapshot carries them. The first
  bridge's own entry is its genesis, self-signed; every other entry chains back to it.

**Concurrent changes**: each entry keeps every write no other write to it has seen; of
those, a removal wins (removing is the safer error; re-pair), and of two of a kind the
later by `(clock, origin)`. Keeping one entry instead, the winner so far, does not
converge: a removal, a concurrent add, and a re-add that has seen the removal but not
the add end differently in different orders (step 2's model test found it). Two
members removing each other at once: both are removed (What a removal does).

**The file stays**: `devices-<instance>.toml` is rendered from the mesh after every
change. A hand edit is read as today (on the next change, listing or reload) and turned
into ops signed by this bridge: an entry taken out is a removal, one put in an add. It
is read against the entries kithd last wrote or read there, not against the roster: a
device another bridge paired, arrived by sync but not rendered yet, is not "taken out"
by a file that never listed it (step 2's two-bridge test found a listing doing so). A
file that does not read keeps kithd from listening, as today. `Devices`
(`internal/daemon/devices.go`) keeps admitting by ID in `VerifyConnection`, now against
the mesh's members, and `cutLocked` runs for a removal that arrives by sync as for one
made here.

### What a removal does

**It stops new writes; it undoes nothing** (decision 8). Which of a removed replica's
ops count is the causal-context rule's: an op it wrote before it had seen its removal
counts on every replica, however late it arrives; one written after does not, anywhere.
Nothing is voided, and every replica judges alike. Apart from that, each replica, once
the removal reaches it, refuses the removed key in the TLS handshake (new connections)
and closes its connections, as an unpairing does today; that decides only who it talks
to, not which ops count. A thin device's writes were always its bridge's own ops. A
removed replica's ops reach the mesh only through members that have not yet heard of the
removal, which closes as the removal spreads.

A replica that finds itself removed stops listening, but **keeps syncing with every
member that still admits it**, so the removal reaches each of them (who then refuse it).
Stopping at once, as first planned, can strand the removal: two members removing each
other, the one whose removal reaches the rest first is refused everywhere, and the
removal it signed of the other may be held by the two of them alone; the rest would list
as a member a bridge that holds itself removed (step 2's model test found it). What a
removed replica wrote is cleaned up by hand, like any other change: a config key written
again, a draft cleared.

### Equivocation

A replica holding the key could sign two ops with one dot and send each to a different
peer. The hash in each vector entry shows it on the next exchange: the side holding the
other op sends it, and a replica receiving a validly signed op under a dot it holds
otherwise has the proof, keeps both (`forks`), and answers with its own. `kith mesh` and
the log name the origin, and **the replica that finds it removes it from the roster**,
signed as its own: its further ops are then judged by the causal-context rule like any
removed replica's. Refusing its ops locally instead, from when each replica finds out,
splits the mesh: an honest op written over one of its later ops is never ready where
those were refused (step 2's model test found it). Keys its two ops wrote may differ
between bridges until they are written again. A fork is seen only at a vector's head:
once both sides hold later ops of the origin, the hashes compare equal and it goes
unseen. A paired replica can do anything anyway (paired = full authority); this only
names the fork instead of hiding it.

### Why every op is signed

With nothing voided, a signature no longer decides which writes count; what it still
buys (decision 7):

- **The roster's authority.** An add or remove counts only if a member signed it, and a
  replica that bootstraps from a snapshot checks the chain back to the genesis instead
  of taking a relay's word for who is paired.
- **Attribution through relays.** Ops are relayed. `kith config conflicts` and `kith
  mesh` name the writer of a value, and only a signature makes that the writer's and not
  the claim of whichever bridge passed it on. A relay cannot write as another member.
- **Equivocation is provable**: two signed ops under one dot are the origin's fork, not
  a relay's corruption.
- **One rule**: every op verified the same way is simpler than two kinds of op, and at
  human write rates a P-256 signature costs nothing measurable.

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

**Joining** (decision 3): a bridge that joins a mesh takes the mesh's shared config, not
merged key by key, and says so in three places:

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

A read made in kith (`MarkRead`, `MarkRoomsRead`, `MarkThreadRead`) is saved locally
first, as today (local-first, #207–#210), and written to the mesh in the same apply. A
position arriving from a peer is merged by max into `room_unread`/`thread_read` and the
room recounted; it sends nothing to the network. **The bridge where the read was made
sends the receipt**, retried as today (decision 13). A read the network reports (made
on the phone's own app, or echoed from another bridge's receipt) is merged locally by
max and not written to the mesh: every bridge on that account hears it from the
network itself (decision 14). `MarkRoomUnread` writes `marked/<room>`; a later read
writes it false. A mark is set in the cache once, as its write arrives (step 4): every
network that marks rooms carries the mark itself, and setting the mesh's again at each
start would bring back a mark cleared since by a read on the phone.

## Sync

**Who dials.** Each bridge dials every member bridge that has addresses, over the
listener's pinned mutual TLS (`pinnedTLS`, `dialBridge` in
`internal/daemon/dialbridge.go`), with backoff from 1 second to 5 minutes and HTTP/2
PING as clients have. A bridge with no `[remote] listen` (a laptop behind NAT) only
dials. Between two bridges that both dial, the stream dialed by the lower ID is kept and
the other closed; a stream counts once both Hellos are said, so a dial still failing
holds none.

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
register for an hour. A snapshot holding such a write is refused until then, and sent
again by the next session. **Reconnection** is the same Hello: the vectors say what is
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
   lists them before confirming, and prints the config it set aside (Config, Joining).
   Each kind is adopted by the step that makes it cross: step 2 the roster (a device
   whose name the mesh has taken keeps it with its ID's first group added), step 5 the
   config with its warning, steps 4, 6 and 7 the rest. Adoption is one transaction, so
   a join cut off leaves the bridge as it was; a confirmation whose answer was lost is
   finished by joining again (the mesh lets in a bridge it lists, without a new
   pairing).

A device keeps dialing the bridge its `device.toml` names; that it is admitted by the
others too is what Phase 3 builds on. Store-holding clients (Electron, Android) pair the
same way with a replica role when they exist; nothing in this phase makes one.

## Two bridges on the same networks

Accounts are each bridge's own (decision 1), but bridges may run the same networks,
even the same account. The example this section works through: **homelab** runs
WhatsApp, Telegram and Slack; **laptop** runs WhatsApp and Telegram on the same
accounts, each bridge its own linked device or session; Slack runs on homelab only. A
profile `work` (shared config) includes `protocol:slack` and `space:Work`; `default`
includes everything.

### The same account on two bridges

- **Both receive every message**, each from its own session; neither sends the other
  anything message-level (design-clients.md decision 12).
- **A room has one ID on both**, since `<account>` is the network's own ID of the account
  (design-adapters.md), so read positions, drafts and verdicts meet on the same keys.
  Checked per network against the adapters:

  | network | room ID | the same on both bridges? |
  | --- | --- | --- |
  | Matrix | `matrix:<hash of MXID>/!room` | yes |
  | Telegram | `telegram:<user ID>/<marked peer ID>` (`internal/telegram/rooms.go`); a message's ID is per chat and per account, shared by the account's sessions | yes |
  | Slack | `slack:<team>/<channel>`, messages by `ts` | yes |
  | Signal, Messenger, Instagram, LinkedIn | the server's chat or thread ID | yes |
  | WhatsApp | groups by group JID: yes. A DM is keyed today by the person's phone-number JID when this linked device's store maps the chat's LID to one, else by the LID (`chatOf`, `internal/whatsapp/messages.go`) | **not today**: one bridge may know a number the other does not yet, and key one DM two ways. Step 3 merges a LID's room into its number's once the number is known, and the mapping crosses the mesh (decision 17) |
  | Google Messages | one pairing only (design-clients.md) | cannot run on two bridges |

- **Read receipts are sent once.** The bridge where the read was made sends it; it
  always runs the account, since a client sees only its bridge's rooms. A position
  arriving through the mesh is merged locally and sends nothing; the network's own echo
  of the receipt reaches the other bridge and is merged by max, and a read the network
  reports is not written to the mesh (decisions 13, 14).
- **Notifications are shown once per device.** Both bridges decide alike (one config)
  and each publishes to its own clients; a device dials one bridge (its first
  `[[bridge]]`), so `kith-notify` there shows each message once.
- **Sends go through the client's bridge only.** Nothing is sent through the mesh; a
  scheduled message is sent by the bridge that holds it (decision 4); `kith-mcp` sends
  through the bridge it dialed.
- **Filters and archive run on both.** A filter firing on the same message on both
  bridges writes the same verdict twice (no conflict); `push_archive` pushes the same
  archive from both, which the network takes once.

### A profile across bridges

**A bridge answers for what it holds.** `Rail`, `Rooms`, unread and notifications for a
profile are evaluated by each bridge over its own rooms, as today. Place lists, tag
rules and `room:` IDs naming rooms a bridge does not hold match nothing there, as a room
left does today.

**What the user wants** (decision 16): a client that has joined several bridges (several
`[[bridge]]` in its `device.toml`) shows everything its profile defines, across all of
them: on laptop, `work` shows laptop's WhatsApp and Telegram rooms and homelab's Slack,
as one rail. No placeholder rows stand for what is elsewhere.

**That is Phase 3, not 2b** (decision 16): it is open question 7 of design-clients.md,
and it needs what Phase 3 builds anyway: a client holding connections to several
bridges, a stream position per bridge, dedup by network ID for an account two bridges
run, and sends routed by account to a bridge that runs it. **Meanwhile a client with
one bridge shows what that bridge holds, and nothing synthetic**: on laptop, `work` has
no Slack rooms.

This phase must not preclude the merge:

- **One room, one ID on every bridge**, so a merging client dedupes by room ID (step 3
  closes the WhatsApp gap for every contact whose number is known).
- **Mesh state is keyed by room, never by bridge**: the draft and read position of a
  room are the same whichever bridge a client asks.
- **A bridge's answer is about its own rooms only**, with no synthetic rows standing for
  another bridge's, so a merging client takes the union of each bridge's answer, per
  account.
- **Each bridge publishes the accounts it runs** (decision 15) to
  `bridge/<device ID>/accounts` (network, the account's ID, its label), written by that
  bridge alone as it logs in and out; `kith mesh` lists them per bridge. A merging client
  routes a room's sends by it, and the roster gives that bridge's addresses; a device is
  already admitted by every member.
- **A stream position is per bridge** (`(epoch, change)`, design-replay.md); a merging
  client keeps one per bridge.
- **Notification IDs are per bridge today** (`<boot>-<count>`, `notifications.go`), so a
  device hearing two bridges would show one message twice. Deriving the ID from the
  room and event instead is for the phase where a device first hears two bridges
  (Phase 3, or Phase 5's push, where both bridges POST to a profile's endpoint); nothing
  in 2b depends on the ID's shape.

**Out of scope, Phase 3**: heartbeats, a client choosing or falling over between
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
   removal and equivocation, `bridge/<ID>/accounts`, `kith mesh` (peers, the accounts
   each runs, last heard, lag, store size, clock gaps); SECURITY.md's "A listening kithd" gains the sync. Only the roster crosses
   yet, which is enough to unpair everywhere. **Failure review** (concurrent, merges
   writers, enforces scope): a model test of three to five replicas whose generator
   interleaves writes, partitions, reconnects, duplicated, reordered and replayed
   frames, a relay restarting mid-batch, clock skew within and past the bound, a replica
   dormant past the horizon, a removal concurrent with the removed replica's writes
   (some relayed before the removal, some after) and with an add of it, two members
   removing each other, an equivocating origin; it asserts every replica converges to
   the same entries, and that once every replica holds the removal no new op of the
   removed replica is taken directly. Plus: both bridges dialing at
   once, an unpaired or device-role key calling `Sync`, the socket calling it, a peer
   unpaired mid-stream, a join whose confirmation never comes.
3. **fix: a WhatsApp DM known by its LID joins its number's room.** The phone-number
   JID stays the canonical DM key, as `person` (`internal/whatsapp/messages.go`) has it
   today: the number is kith's identity for a person across networks (the phonebook's
   `phone:`, Signal and SMS by digits, starting a chat from a number), and `lidLinks`
   already links a LID to it. A DM known only by its LID is keyed by the LID meanwhile.
   Once the bridge learns that LID's number, the LID room is merged into the number's
   room: messages, side rows and change numbers, drafts, read positions, unread,
   verdicts; the config's `room:` place lists and tags' `picked` naming the LID room are
   rewritten through `UpdateConfig`, queued scheduled messages re-pointed. The cache
   keeps the merge (`room_aliases`, LID room → number room, a guarded migration with its
   fingerprint row, `docs/database.md`), so a message arriving later under the LID is
   filed under the number. A one-time pass at upgrade merges every LID room whose number
   is already known. A contact who never exposes a number (a hidden number, a WhatsApp
   username) stays keyed by the LID on every bridge alike. Group chats are untouched;
   event IDs carry no chat and do not change.
   **Across bridges** (from step 4 on, when a room's state first crosses): the merge is
   one op that writes `alias/<LID room>` = the number's room, writes the LID room's
   entries under the number's keys (positions by max, drafts as a write over both, verdicts
   as writes) and deletes the LID keys. A bridge applying an alias for an account it
   runs merges its own LID room the same way, without waiting to learn the number
   itself, so two bridges converge on the number key as soon as either learns it; two
   bridges merging at once write equal values (positions by max, equal drafts adding
   nothing, equal verdicts no conflict). **An op for the LID key that raced the merge**
   (written by a bridge that had not yet seen the alias) is applied as signed, under
   the LID key, so every replica holds the same op; each bridge that holds the alias
   then re-issues its write under the number's key as its own op and deletes the LID
   key. Ops are never rewritten in flight: they are signed, and a relay's copy must
   equal the origin's. **Failure review** (merges writers, caches a derived result): a
   DM known by its LID and by its number, both rooms holding messages, merging LID →
   number with none lost or doubled (a message in both keeps one row, at a new change
   number, so a resuming client gets it); a draft in each (joined, as `rebaseOnto` keeps
   a draft it does not extend); read positions in each (the later wins); tag and person
   verdicts in each; the LID room in a profile's place list and a tag's `picked`; a
   scheduled message to the LID room; the number learned mid-migration, and a message
   arriving under the LID during it; the migration cut off and run again; a contact
   who never exposes a number (stays on its LID, on every bridge); the number learned
   on one bridge before the other (the other merges on the alias, and again harmlessly
   when it learns the number itself); both bridges learning it at once; an op for the
   LID key racing the merge, in both orders, on both bridges; an alias for an account a
   bridge does not run (kept, inert); the room open in a window when it merges (the
   window follows the new ID on `Changed`).
4. **feat: read positions cross bridges.** `read/`, `thread/`, `marked/`; the cache's
   unread rewritten from the mesh; the receipt sent by the bridge where the read was
   made; reads the network reports kept out of the mesh (decisions 13, 14). Step 3's
   merge op (today `alias/` alone, written for every LID→number pair a bridge sees)
   gains these keys of the LID room, and the re-issue of a LID-key op that raced it.
   **Failure review**
   (merges writers, caches a derived result): the generator of step 2 with reads on
   every replica asserts that a position never moves back on any replica, that
   `room_unread` equals what the mesh holds after every step, and that each read is
   receipted by one bridge; a read on the LID key racing the merge, in both orders,
   on both bridges; a network echoing an older receipt, a mark unread
   concurrent with a read, a room a bridge does not run, a cache rebuilt under a mesh
   that holds positions, a removed replica's reads (kept).
5. **feat: the shared config crosses bridges.** `config.Local`, per-key registers (a
   window's `SetConfigSetting` already writes one, by its path, over a revision),
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
   5-second cadence. Depends on design-electron.md step 4 or does its move. **Failure
   review** (merges writers): `TestDraftsSurviveEveryInterleaving` gains a second bridge
   with its own window and kith-mcp, partitions and a send on one side; it asserts no
   typed or appended word is lost and every bridge reads the same draft; plus a draft
   over 1 MiB, a send concurrent with an append, a room left mid-save.
7. **feat: verdicts cross bridges on networks that keep none.** `Router.keeperFor`'s
   store keeper writes `tag/` and `person/`; Matrix's stay in account data. **Failure
   review** (merges writers): a filter firing on both bridges for one message, a release
   concurrent with a move, a retag (`domain.Retagged`) concurrent with a move under the
   old name.
8. **feat: keys rotate.** `kith devices rotate`, the rotation op, a bridge answering on
   its old key by SNI for the horizon, clients rewriting their pin. **Failure review**
   (enforces scope): a rotation signed by one key only, a rotated-out key connecting
   after the rotation arrived, a rotation concurrent with the device's removal, a client
   offline past the grace (it re-pairs, and says so).

## Left open

- **A client on several bridges**, showing everything its profile defines across them
  (decision 16; design-clients.md open question 7), and **failover**: Phase 3.
- **Store-holding clients** (Electron's partial cache, Android): they pair with a
  replica role and sync through their bridge; syncing device to device directly waits
  for them.
- **Bridges that are never online at the same time**: a mailbox, a mesh peer that holds
  sealed ops it cannot read, and the envelopes every op would need first
  ([design-mailbox.md](design-mailbox.md), proposed, undecided).
- **A text CRDT for drafts**: only if keeping every sibling repeats text in practice.
- **WAN reach between bridges**: as for devices, addresses or a tailnet (design-remote).
- **Scheduled sends and rankings** stay on each bridge (decisions 4 and 5).
- **Notification IDs from the message**, for a device that hears two bridges (Phase 3
  or 5; see [A profile across bridges](#a-profile-across-bridges)).
- **The phonebook** stays a file on each bridge (open question 10).

## Decisions (2026-10-30)

Decided by the user on 2026-10-30.

1. **Network accounts are each bridge's own**: `[[<network>.account]]` stays in its
   file. Bridges may still run the same networks, even the same account (Two bridges on
   the same networks). Rejected: shared accounts, every bridge running every one.
2. **A config register is a leaf key, or an array whole**, `[[tag]]` and `[[filter]]`
   included. Rejected for now: lists of named tables keyed by `name` with an order
   register; it can follow if concurrent edits of two tags happen.
3. **A joining bridge takes the mesh's shared config**, its own set aside in
   `config.toml.pre-mesh`, **with an explicit warning**: `kith join --bridge` prints
   every key set aside with both values and the file's path, kithd logs it, and the TUI
   raises a popup (Config, Joining). Rejected: merging key by key.
4. **Scheduled messages do not cross**: the bridge that holds one sends it. Rejected: an
   owner with a hand-over, which is failover (Phase 3).
5. **Emoji and mention rankings do not cross.** Rejected: per-replica counters.
6. **Matrix's stars and verdicts stay in account data**; the mesh carries verdicts only
   for networks that keep none. Rejected: everything in the mesh, a second source.
7. **Every op is signed.** Re-checked after decision 8: with nothing voided, signatures
   no longer decide which writes count, but they carry the roster's authority, make the
   writer named in conflicts the writer and not a relay, and prove a fork (Why every op
   is signed). Rejected: signing roster ops only.
8. **A removal stops new writes and undoes nothing.** An op of the removed replica
   counts if its signer was a bridge as of the op's own context: what it wrote before it
   saw its removal counts everywhere, relayed like any op, and is cleaned up by hand;
   nothing it writes after does. Once the removal arrives, a replica refuses the removed
   key at the TLS handshake (new connections), which decides who it talks to, not which
   ops count. (Corrected in step 2: "takes no more of its ops once the removal arrives"
   made replicas that took the same ops in different orders disagree.) Rejected (the
   user: a corner case that would need far too much logic): voiding a removed replica's
   ops after a cut, with what they replaced kept to restore it.
9. **No HTTP/2 on the socket (h2c) yet**: it lands when a replica first sits beside its
   bridge (Phase 4's relay with a store). Bridge to bridge is TLS, already HTTP/2.
10. **The horizon is 30 days.** Rejected: 7 or 90.
11. **Bridges rotate keys too** (step 8), the old key answered by SNI for the horizon.
    Rejected: devices only, a bridge re-paired.
12. **The clock bound is 60 seconds**: a peer further off is refused and named.
    Rejected: 5 minutes.
13. **The bridge where a read was made sends its receipt**; a position arriving through
    the mesh sends nothing. Rejected: another bridge sending it after a grace when the
    network has not echoed it; every bridge on the account sending it.
14. **A read the network reports is not written to the mesh**: each bridge on the
    account hears it from the network. Rejected: writing every position.
15. **Each bridge publishes the accounts it runs** (`bridge/<ID>/accounts`), for
    routing sends once a client merges bridges, and for `kith mesh`. Rejected: not
    publishing them.
16. **A client that has joined several bridges shows everything its profile defines,
    across all of them; no placeholder rows.** The user rejected all three options
    offered (nothing; an "elsewhere" row naming the account and its bridge; such a row
    with a count): "If the laptop client lists both servers, it shows everything
    according to its profile." This is the intended behaviour of design-clients.md open
    question 7, and it lands in **Phase 3**, not 2b: it needs a client on several
    bridges, a stream position per bridge, dedup by network ID and sends routed by
    account, all Phase 3's. Until then a client with one bridge shows what that bridge
    holds, nothing synthetic, and 2b keeps the merge possible (A profile across
    bridges).
17. **The phone-number JID stays the canonical WhatsApp DM key** (step 3): a DM known
    only by its LID is keyed by the LID until a bridge learns its number, then merged
    into the number's room, the mapping crossing the mesh so every bridge converges on
    the number key; a contact who never exposes a number stays LID-keyed everywhere.
    Rejected: the LID as the canonical key (decided first, then reversed the same day):
    it makes an opaque alias the person's identity, so every cross-network match (the
    phonebook's `phone:`, Signal and SMS by digits, a chat started from a number) would
    depend on the mapping. Also rejected: checking two bridges live first and fixing
    only on a difference.
