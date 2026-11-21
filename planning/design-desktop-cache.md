# Design: the client's local store (step 1c)

Status: designed 2026-11-21, nothing built. The draft (#611) was reopened on 2026-11-19
(#612) for a design phase of its own; the review page's decisions and the session of
2026-11-21 settled it (decisions, at the end). It adds to
[design-clients.md](design-clients.md) (Where messages are kept, Phasing) and to
[design-mesh.md](design-mesh.md), which it does not change except where
[What steps 5–8 must not assume](#what-steps-58-must-not-assume) says so.

## Goal

A client on a machine that reads a bridge elsewhere holds nothing of its own.
`kith-relay` forwards every call of the desktop window to kithd and keeps no state across
starts (`cmd/kith-relay/relay.go`, `forward`); main keeps attachments in memory only,
64 MiB at most (`mediaCacheBytes`, `desktop/src/main/media.ts`). `kith`, `kith-mcp` and
`kith-notify` each dial the bridge themselves (`setup.JoinedBridge` in each `main.go`)
and keep nothing either. This costs two things:

- **No offline reading.** With the bridge out of reach (a train, a laptop away from the
  tailnet) every client says that kithd is not reachable and shows nothing.
- **No instant start.** The first paint waits for a TLS handshake to the bridge and for
  `Rail`, `Rooms`, `CachedUnread` and the open room's `CachedTimeline` (up to 2000
  messages, `timelineCacheLimit` in `internal/local/service.go`) to cross the network.

The **local store** is kept by a **proxy**, `kith-proxy`, one per machine: a process every
client on the machine dials instead of the bridge. It forwards every call while the
bridge answers, keeps a copy of the bridge's answers and of every room's history, and
answers the read calls from that copy while the bridge is out of reach and at start.

Not goals:

- **Writing offline.** Sending, editing, reacting, filing and settings need a bridge, as
  design-clients.md decision 3 says for mobile. The one exception is reads made offline
  ([Offline](#offline)).
- **History the bridge never had.** The store copies the bridge's cache; network
  scrollback past it needs the bridge, as today.
- **Attachments in full.** Small image previews only.
- **A machine whose own kithd the clients read.** Its kithd's cache is the copy
  ([One cache or two](#one-cache-or-two-on-a-machine-that-runs-kithd)).

## What reaches a client, and how

design-clients.md decision 6: **messages never cross the mesh.** Each bridge reads the
networks itself and keeps them in its own cache. So whatever a client holds of messages
comes from its bridge's RPCs, never from mesh ops.

The mesh carries less than a client shows, and none of it in the form a client shows it:

| state | kept on the bridge in | reaches a client by | the store keeps |
| --- | --- | --- | --- |
| messages: edits, deletions, polls, attachment metadata | the cache | `CachedTimeline`, then the `Messages` stream (`internal/daemon/messagelog.go`) | every room's history, up to the text bound |
| reactions | the cache | `CachedReactions`, the `Reactions` stream | those on messages it holds |
| unread counts, the rail, rooms, last messages, invites | derived on the bridge: the network's counts, the mesh's reads, the config's rules | `CachedUnread`, `Rail`, `Rooms`, `LastMessages`, `CachedInvites`; `UnreadStream`, `Changed` | per room the last answer; per profile its room set and rail |
| read positions and unread marks made in kith | the mesh (`read/`, `thread/`, `marked/`, `internal/mesh/crdt/read.go`), mirrored into the cache | folded into `CachedUnread` and `UnreadStream` | inside the last answer, and reads made offline |
| reads the network reports (made on the phone) | the cache alone (docs/database.md, The mesh file) | the same | inside the last answer |
| drafts | the mesh (step 6, #616) | `Drafts`, `Changed` (`CHANGE_DRAFTS`) | the last answer |
| shared config | the mesh (step 5, #613) and `config.toml` | `Device`, `ConfigSettings`, `Theme`, `ConfigChanged` | the answers a client draws by |
| names, sender colors, pictures | derived on the bridge | names shaped into each message (`Names.changes`), `SenderSlots`, `SenderPictures`, `LoadPicture` | the last answers, and pictures |
| attachments | the bridge's media cache | `LoadImage`, called by the desktop's main | image previews, up to the preview bound |
| search | the cache's full-text index | `SearchMessages`, `SearchSenders` | a full-text index of what it holds |
| roster, other bridges, aliases | the mesh | `Devices`, `MeshStatus`; aliases applied by kithd | nothing |
| `device.toml` | this machine | read by the proxy and the relay | (it is already here) |

The read positions show why the mesh alone is not enough: an unread count comes from the
network's count, the mesh's reads and the reads the network reports, and two of those
three are only in the bridge's cache. A client needs the bridge's derived answers
whatever the store also holds.

## One cache or two on a machine that runs kithd

**Two caches, by what the clients read, not by what the machine is** (decision 1).
kithd's cache stays fed by its own network connections only. The proxy keeps a store
whenever its clients read a bridge on *another* machine. That covers a laptop with no
kithd and a laptop whose kithd is on standby (Phase 3). When the clients read this
machine's own active kithd, the store of that bridge is not written: kithd's cache is
the copy.

| machine | clients read | offline today | with the store |
| --- | --- | --- | --- |
| homelab, kithd active | its own kithd over the socket | its cache, all history | no proxy: clients dial kithd as today |
| laptop, joined only (no kithd) | the homelab over TLS | nothing: "kithd is not reachable" | the store: every room's history |
| laptop, kithd on standby (Phase 3) | the homelab; its own kithd is passed over | its own kithd, stale since its last keep-alive | the store, current to the moment the link dropped |
| laptop, kithd took over | its own kithd | its cache, current once caught up | the store not written; clients switch back through `read_again` |
| laptop, one account per bridge (design-clients.md question 9) | both: some accounts here, some on the homelab | only this kithd's accounts | the store holds the rooms read from the homelab |

Feeding a standby kithd's cache from the active bridge would make messages cross between
bridges (against design-clients.md decision 6), give the cache a second writer, and run
notifications, tracked words and filters a second time on copied messages. The price of
two caches is a second copy of the text on a laptop that is also a bridge, within the
text bound.

## A cache of the bridge's answers, not a replica

design-clients.md said a device holding a partial cache is a replica of the mesh,
syncing through its bridge, and design-mesh.md decision 8 tied HTTP/2 on the local
socket to it. Instead (decision 3): **the store is a cache of the answers its bridge
gives, and the machine stays a thin `device` in the roster.** Against a cache of
answers, a replica adds little:

- **It could not write.** The mesh takes an op only from a bridge (`State.take`,
  `internal/mesh/crdt/state.go`: `signer.Role != RoleBridge` is `ErrNotAMember`). A
  replica that wrote would need a role with signing authority, which is the question
  of offline writes again.
- **Offline it is as stale as a cached answer.** Neither hears anything without a
  bridge.
- **Online it is as current.** A cached answer is replaced on the `Changed` frame that
  says it changed, as the window already re-reads it.
- **It would cost the mesh** a role, a sealing key per device, wraps for it, a new epoch
  at its removal, a Sync served to a non-bridge, and h2c on the socket
  ([If a client were a replica](#if-a-client-were-a-replica)).

What it would buy that a cache does not: reading a mailbox's ops with no bridge in
reach, and passing ops between bridges. Neither gives a client a message.

## The proxy

**Every client on a machine that reads a bridge elsewhere dials `kith-proxy`** (decision
2): the desktop's relay, `kith`, `kith-mcp` and `kith-notify`. One process holds the
store, so there is one writer for it, one set of upstream streams for the machine
whatever runs, and offline reading for the terminal and for agents as much as for the
window.

- **`cmd/kith-proxy`**, a user service as `kith-notify` is
  (`packaging/systemd/kith-proxy.service`, its launchd twin under `packaging/macos`). It
  runs only where `device.toml` names a bridge (`setup.JoinedBridge`), and serves
  `BackendService` on a socket of its own in the runtime dir that only this user can
  open, as the relay's is.
- **The clients dial it when its socket answers, and the bridge as today when it does
  not.** A machine with no proxy running behaves exactly as now: no offline reading, no
  regression. Each client's `JoinedBridge` branch gains that one check.
- **The desktop's relay stays** the window's child: it still answers `DeviceService`
  itself (pairing, the profile in use, this device's settings) and now forwards
  `BackendService` to the proxy's socket instead of the bridge. The window does not
  change.
- **The bridge sees one client per machine.** The proxy dials the bridge with this
  device's key (`daemon.DialBridge`), and passes each call's `Kith-Profile` header on,
  so kithd's `profileGate` (`internal/daemon/profiles.go`) narrows every online answer as
  today.
- **Phase 3 lands in the proxy.** The `[[bridge]]` list, failing over on a stale
  heartbeat, resuming by time, buffering sends 3–5 s: one implementation for every client
  of the machine instead of four. A client on a machine without a proxy keeps today's
  single bridge.

## What the store holds

**One store per machine and bridge, shared by every profile** (decision 4). What is the
same in every profile is kept once per room; each profile keeps only what scopes it.

- **Per room, once**: its messages (each as the bridge's cache last sent it,
  `domain.Message`, names shaped) with their reactions; its unread count and read
  positions as last answered, and reads made offline; its last message; its pictures.
  A room read in one profile is read in all, as it is on the bridge.
- **Per profile**: its room set and rail, as its last `Rooms` and `Rail` answers name
  them. Offline, the proxy answers a profile only from the rooms its set holds, and
  refuses the rest.
- **Per bridge**: the answers clients draw from at start that are not per room, each as
  last answered: `Device` (the profiles), `CachedInvites`, `Drafts`, `Selves`,
  `SenderSlots`, `SenderPictures`, `Theme`, each under the profile it was asked in.
- **A full-text index** over the messages it holds, built by the same FTS5 tokenizer as
  kithd's cache (`internal/db`), so search works offline. Measured on a live cache:
  20 MB of index for 50 MB of text.
- **Image previews**: a 320 px JPEG per image, scaled by the bridge (decision 6).
- **Where it stands with its bridge**: the bridge's device ID, its change numbering's
  epoch, the last change taken, and when it was last heard.

**Rooms and senders by integer key from the start.** On a live cache of 238k messages,
most of the file is the network's room and user IDs repeated in every row and in the
indexes that lead with them (32 MB of IDs and names against 50 MB of text). The store's
`messages` and its indexes reference a rooms table and a senders table by integer;
network IDs appear once each. kithd's cache gets the same as a roadmap item of its own,
by a migration that keeps every row.

**Bounds** (decision 5): **text 1 GB, previews 256 MB**, each trimmed oldest first by
message time across all rooms. Both are defaults, set per machine in `device.toml`:

```toml
[cache]
text_max_mb = 1024
previews_max_mb = 256
```

A per-machine setting, never a shared mesh key (step 5's `config.Local` sorts only
`config.toml`). 0 keeps the default. Text counts the rows and the full-text index
together. Trimmed rows free pages, and the file is rewritten at open by the cache's own
rule (a fifth free and at least 32 MiB, docs/database.md, The file).

**A room leaving every profile** (filed elsewhere, left, the rules changed) is deleted
from the store at the first set of `Rooms` answers that all lack it, with its messages,
reactions and previews. A room leaving one profile only leaves that profile's set. A
room merged into another (`MergeRoom`, a WhatsApp chat known by a LID) goes the same way,
and the room it joined is filled again.

## Where it lives

- **The file** is `proxy-<hash>.db` in the data dir of this machine's client storage
  (`setup.ClientStorage`), `<hash>` naming the bridge's device ID as `domain.AccountKey`
  names an account. The proxy holds an exclusive lock on it: one writer per file
  (decision 7).
- **Sealed** like the cache and the mesh store: `internal/sealeddb` (Adiantum, every page
  and the log encrypted), owner-only.
- **Its key** is one 32-byte key per machine in this machine's keyring
  (`Storage.KeyringService`), made on first use as kithd's cache key is
  (`session.LoadOrCreateStoreKey`). It is not the device key and not derived from it, so a
  device key rotated (step 8) leaves the store readable. No keyring, a locked one, or one
  that does not answer within `keystore.Wait`: no store, and the proxy only forwards. A
  store key is never kept in a file, as kithd's are not.
- **Layering.** The store is a package of its own, `internal/clientstore`, over
  `internal/sealeddb` and `internal/domain`, with a depguard rule. `kith-proxy` links it;
  like the relay, it never links `internal/db`, `internal/mesh` or `internal/session`
  (`scripts/deps-check.sh`). The key's helper moves from `internal/session` to
  `internal/keystore`. The proxy never opens kithd's cache.
- **A cache, rebuilt on doubt.** Every row is a copy of the bridge's. A file of an
  unknown layout, or that does not open, is deleted and filled again, as kithd's cache is
  rebuilt (docs/database.md, Changing the schema), never migrated column by column.
- **Deleted** (decision 8) when the bridge refuses this device's key (`kithdAt.failure`
  says `PermissionDenied` only on the bridge's TLS alert, never on a reset), and when this
  machine leaves the bridge (its `[[bridge]]` entry removed from `device.toml`). A machine
  unpaired keeps nothing it was unpaired from; pairing again fills it again.

## How it is filled and kept current

The proxy keeps **one upstream stream of its own** to the bridge: `Messages`, resumed
from the store's last change, with no profile, so it carries the changes of every room
the device may read. Each change is written in one transaction with the new position, so
a proxy killed mid-write leaves a store that resumes where it stood.

- **Clients' own streams and calls are forwarded** while the bridge answers. The store's
  stream is a separate one; online, a client sees exactly what it sees now. Each
  forwarded answer of a listed call is also written to the store, under its profile.
- **Every profile's room set**: the proxy asks `Rooms` and `Rail` in each profile the
  last `Device` answer lists, at start and on `CHANGE_RAIL` and `CHANGE_ROOMS`, so a
  profile no client has opened yet is still held.
- **Filling history**: every room of every set, one at a time, newest last message first
  (`LastMessages`), each from its newest message back to the oldest the bridge's cache
  holds, or until the text bound. `CachedTimeline` gains `before` and `limit` (none keeps
  today's newest 2000), so a fill pages the bridge's cache backwards in pages of 500 and
  never asks a network. A room's newest page is filled first everywhere before any room's
  older pages, so every room opens offline early in a first fill.
- **A fill and the stream** may cross on one room: a page replaces only the rows of its
  own span, and a row the stream wrote after the page was asked is kept by
  `domain.SupersedesEdit`, as the cache and the window resolve history.
- **Previews** are fetched in the background for the images in held messages, newest
  first, until the preview bound.
- **`read_again`** (the store behind by more than 5000 changes, `tailBehind`, or the
  bridge's cache cleared): the stream resumes from the newest, the rooms held are marked
  stale, and each is filled again newest first. A stale room is still shown offline,
  marked as of when it was filled.
- **Names change with no message changing** (a name rule, a person merged): `Changed`
  says `CHANGE_NAMES`, and the held rooms are marked stale and their newest pages filled
  again; older pages follow.
- **Answers that change by a stream** (`UnreadStream`, `Reactions`, `Invites`): the proxy
  holds those streams too and writes each frame into the held answer. `CachedUnread`,
  read again each time the stream opens, replaces the held answers.

## Held and live

The proxy is in one of two states for its bridge, by its own stream:

- **Live**: the store's stream is open and has caught up with the bridge's last change
  as of opening. Everything is forwarded, as today.
- **Held**: the bridge has not answered yet, or redialing has failed for 10 s. The
  listed calls are answered from the store. Every other call fails as today,
  unavailable. A client's `Messages` stream is served from the store's own change
  numbering, with an epoch of its own.

**Hysteresis** (decision 10). Live goes held after 10 s of failing to reach the bridge,
and held goes live after the bridge has answered for 5 s. A connection idle for 15 s is
pinged and dropped 10 s later (docs/remote.md, Using a bridge), as now. Train Wi-Fi that
drops for a few seconds costs nothing; a real outage costs one switch each way.

**The switch costs one re-read, through a path every client already has.** A `Messages`
stream resumed with an epoch the server does not know answers `read_again`
(`resumeAt`): the desktop's main turns it into a `reload` (`resuming`,
`desktop/src/main/gate.ts`), and the Go clients' remote backend into a reload of its
messages (`internal/daemon/remote_sync.go`). So:

- held → live: the proxy first sends the reads made offline and waits for them
  ([Offline](#offline)); then it ends the clients' held streams; they reopen them, now
  forwarded, with the store's epoch; kithd answers `read_again`; the clients read again
  from the bridge, whose answers already hold those reads.
- live → held: the forwarded streams are cut, as today when a bridge goes; reopened, the
  proxy answers from the store with the bridge's epoch, which the store does not number
  in; it answers `read_again`; the clients read again from the store.

**Instant start.** The proxy runs as a service, so it is live before a client starts
whenever the bridge is in reach. Started with the bridge out of reach, it is held from
its first answer. A client never waits on a handshake: it reads the proxy's socket on
this machine either way.

**Clients say which.** `DeviceService.Device` gains `held_since`, which the relay reads
from the proxy (`Status` gains it): zero when live, else when the store was last current.
The window shows "Offline: showing what this device held at 14:02", the TUI the same in
its status line, kith-mcp in its answers' notes; what needs a bridge fails as it does now
when kithd is not reachable.

## Offline

- **Reading**: every room of the profile, as far back as the store holds. Older
  messages need the bridge: a client says so where `Timeline` (scrollback) fails.
- **Search**: `SearchMessages` and `SearchSenders` over what the store holds, narrowed to
  the profile's room set.
- **People's cards, the bridge's settings, attachments past their preview**: need the
  bridge. `Device`, `DeviceSettings` and `Theme` are answered, so the profiles, this
  device's settings and the colors hold.
- **Writing**: refused, unavailable, as today. A draft typed offline stays in its
  client, which says so, as a draft kithd does not take does today (docs/desktop.md,
  First run). kith-mcp's `send_message` says the bridge is out of reach.
- **Reads made offline** (opening a room, `m`): the store keeps them, every client shows
  the room read, and once the bridge answers the proxy sends each as `MarkRead` before it
  goes live (decision 9). A read position is a max register on every bridge
  (`Store.Read` writes a position only when the mesh holds it less far), so a late read
  never moves a position back. A read the bridge refuses (the room left meanwhile) is
  dropped. Marking unread is refused offline: a mark is newest-wins, and a late one could
  undo a newer one made elsewhere (`Store.TakeMarks`).
- **Profiles offline**: those the last `Device` answer listed. An empty `Kith-Profile`
  header is the profile the bridge last answered for one.

## Failover between bridges (Phase 3)

Phase 3 gives the proxy a `[[bridge]]` list in priority order. For the store:

- **A store per bridge**, as the file is named by the bridge. Positions are per bridge
  too: a change number means nothing at another bridge (each numbers its own cache).
  Failing over to a bridge whose store is empty fills it as at first start, and the
  clients read from the store of the bridge last live until it is.
- **A client on several bridges** (design-mesh.md decision 6) reads each room from its
  bridge's store, as `Rooms` named it; the proxy merges the profile's set across them.
- **A bridge that is this machine's own kithd** is in the list too, and its rooms are
  not stored (decision 1).
- **A bridge whose key rotated** (step 8) is the same bridge: the proxy renames the file
  and moves the position to the new ID when it rewrites its pin.

## If a client were a replica

If Android needs a replica later, the role's limits are these. Each is checked against
the code as it stands, with mesh step 5a (sealed ops, #609).

- **A new role, `replica`**, beside `bridge` and `device` (`crdt.Role`). It signs no op:
  `State.take` already refuses every signer but a bridge, so nothing changes there. Its
  writes are RPCs to its bridge, as a device's are.
- **It needs a sealing key and the epoch keys** to open ops sealed since step 5a, which
  wraps a content key only for bridges (`keyring.make` and `keyring.wrapFor` keep
  `m.Role == crdt.RoleBridge`) and makes a new epoch only after a bridge's removal
  (`State.Since` checks `m.Role == RoleBridge`). Both would widen to every member holding
  a sealing key, so a stolen laptop, once removed, reads nothing sealed after.
- **It syncs only with its bridge**, over the same `Sync`. Today Sync is refused over the
  socket (`errSyncOverSocket`, `internal/daemon/mesh.go`) and to any peer not a bridge
  (`stillPaired`); both would admit a replica to its own bridge. A replica beside its
  bridge needs h2c on the socket, since Sync streams both ways (design-mesh.md
  decision 8).
- **It never holds the log back**: it writes nothing, so it is passed over by `compact`,
  and one that is behind takes a snapshot. It is not counted when the mesh asks whether
  every replica has converged, as a mailbox is not.
- **It holds every profile's soft state**, which is why it would not replace the answers
  a client reads.

## What steps 5–8 must not assume

Steps 5 (config, #613), 6 (drafts, #616) and 7 (verdicts, #619) are built; 8 (key
rotation) is next. What the store relies on, and step 8 must keep:

1. **A client reads the merged state through kithd's calls.** Config through `Device`,
   `ConfigSettings`, `Theme` and `ConfigChanged`; drafts through `Drafts` and `Changed`.
   These answer the merged state, narrowed to the profile, and say when it changed
   (`ConfigChanged`, `CHANGE_DRAFTS`): the store re-reads on those frames and on nothing
   else.
2. **A draft's base may be old, and from another bridge.** A client that kept a draft
   through an hour offline sends it with `ReplaceDraft` over the words it read then; in
   Phase 3 it may send it to a bridge other than the one it read them from. Step 6 takes
   the base from the write (`over`) and never assumes it is the value the bridge holds.
3. **A read may arrive late.** `MarkRead` comes hours after the read, for an event the
   network may have moved past. The mesh's max register takes that. kithd must also not
   send the network a receipt behind the one it holds: that is kithd's receipt path, and
   the step that adds offline reads checks it.
4. **A rotated bridge is the same bridge.** Step 8's rotation op names the old ID and
   the new key, signed by both. A client rewriting its pin also gets the old → new pair,
   so the proxy moves its store to the new ID and does not fill everything again. A
   laptop offline past the grace meets a bridge key it does not pin, which is not the
   bridge refusing this device's key, so the store is kept; once paired again with the
   same bridge, its rooms are filled again in place.
5. **`device.toml` stays out of the mesh.** The store's bounds are this machine's.
6. **An older bridge does not skip what it does not know.** Each new key kind and each
   new role goes with a higher protocol version in Hello, which refuses the session
   rather than wedging the origin (`offer`, `internal/mesh/replica.go`). The store adds
   none.

## Failure review the build needs

Every step is in at least one of CONTRIBUTING.md's four classes. What each class test must
generate:

- **Caches a derived result** (the store holds the rail, counts, shaped names and a
  search index). The generator drives the bridge's side through every event the store
  reads, in any order, with the proxy online, offline, restarted or a profile's set
  changing between any two, and asserts that once live and settled, every listed answer
  from the store equals the bridge's answer to the same call, search included for the
  history held. Blind spots to put in it: a name rule changed with no message changing;
  a deletion the store misses because it was behind past `tailBehind`; a room trimmed on
  the bridge (`[storage] messages_per_room`); a room merged into another; a room leaving
  one profile but not another; a fill page and a stream change to the same room
  crossing; either bound trimming a room a client has open.
- **Enforces scope** (one store, many profiles: the proxy, not kithd, narrows offline).
  A test of `TestAFailedLookupNeverWidensTheScope`'s kind, generating: a `Kith-Profile`
  header naming another profile, none, or one the last `Device` lacked; a profile whose
  `Rooms` answer failed or was never had; a room leaving a profile between a call's
  arrival and its answer; a search with `all_rooms`, with room IDs outside the set, and
  with a sender only another profile's rooms hold. None may answer a room the profile's
  last good set lacks.
- **Concurrent** (the store's stream, the fill, many clients' calls, a held/live switch
  and an exit at once). `-race`, and every exit route: SIGTERM, the proxy killed between
  a change and its position or mid-page, the keyring not answering at open, a second
  proxy finding the lock held. The store after each resumes where it stood, or is
  rebuilt, never half-written.
- **Merges what more than one writer wrote** (reads made offline against reads made
  elsewhere and on the phone; in Phase 3, two bridges' copies of one message).
  `TestTheCacheAndTheClientResolveHistoryAlike` gains the store as a third layer: for
  every delivery order, the store's row equals the cache's. Ties at one millisecond, a
  deletion on one bridge only, a `Reverted` copy, a fill page arriving after newer stream
  changes.

## Decisions

Taken 2026-11-21, from the review page (2026-11-19) and the session of 2026-11-21.

1. **Two caches, by what the clients read.** kithd's cache fed only by its own networks;
   the proxy keeps a store of every bridge its clients read on another machine, standby
   laptops included. Rejected: a store only on machines with no kithd (a standby laptop
   falls back to its stale kithd offline); a standby kithd filled from the active bridge
   (messages cross bridges, a second writer, notifications on copies); one format with
   two writers.
2. **A local proxy every client dials**, `kith-proxy`, one per machine. Rejected: the
   store in `kith-relay` for the desktop only (the TUI and kith-mcp blind offline);
   kithd in a client-only mode.
3. **A cache of the bridge's answers**, not a mesh replica. design-mesh.md decision 8
   (h2c) waits for whatever first puts a replica beside its bridge.
4. **One store per machine and bridge, shared by every profile.** Messages, the search
   index, previews, read positions, unread counts and reads made offline once per room;
   each profile keeps its room set and rail; the proxy refuses offline what a profile's
   set does not hold. Rejected: a store per profile (a room in two profiles kept twice).
5. **Text 1 GB of all history, previews 256 MB**, oldest trimmed first, both defaults
   in `device.toml` `[cache]`. Rejected: 500 messages per room, none older than two
   weeks, 256 MiB in all (quiet rooms open empty offline).
6. **The bridge scales previews**: `LoadImageRequest` gains `max_side`; a 320 px JPEG,
   about 20 KB. Rejected: the networks' own thumbnails (only WhatsApp and Matrix have
   them); the proxy scaling the full file (moves the whole image over the link).
7. **One writer per file**: the proxy holds a lock on its store.
8. **Deleted when the bridge refuses this device's key, and when this machine leaves the
   bridge.** Rejected: also after 30 days without reaching the bridge (an empty client
   after a long trip).
9. **Reads made offline are sent before going live**, so a room read offline never shows
   unread again for a moment. Marking unread is refused offline.
10. **Held after 10 s failing, live after 5 s settled.**
11. **Rooms and senders by integer key** in the store's schema from the start.
12. **Offline scrollback past the store into a standby laptop's own kithd: not built.**
    With all history held up to 1 GB, the store's edge is rarely reached.

## Plan

Each PR lands working, with a failure review as above.

- **Step 1, feat: `kith-proxy` forwards.** `cmd/kith-proxy` and its user units; the
  clients (`kith`, `kith-mcp`, `kith-notify`, the relay's `BackendService`) dial its
  socket when it answers, the bridge otherwise; `Status` and `Device` gain `held_since`
  (always zero here). No store yet: every call forwarded, one TLS connection per
  machine. **Failure review**: concurrent (many clients' streams through one proxy, the
  bridge going and coming).
- **Step 2, feat: the proxy holds its bridge's answers.** `internal/clientstore` (the
  sealed file per bridge, integer keys, its key in the keyring, its own change
  numbering, the lock), the depguard rule and the key helper in `internal/keystore`; the
  proxy's own `Messages`, `UnreadStream`, `Reactions` and `Invites` streams; every
  profile's room set and rail; the listed answers kept as forwarded; rooms filled newest
  page first; held and live with hysteresis, `read_again` at each switch; the offline
  line in the window, the TUI and kith-mcp; the store deleted on a refusal or a leave.
  **Failure review**: caches a derived result, enforces scope, concurrent.
- **Step 3, feat: all history and offline search.** `CachedTimeline`'s `before` and
  `limit`; the fill back to the oldest the bridge holds; the text bound and its
  `device.toml` key; the full-text index and `SearchMessages`/`SearchSenders` answered
  held; stale rooms after `read_again` and `CHANGE_NAMES`; the rewrite at open.
  **Failure review**: caches a derived result (fills, trims, search), enforces scope
  (search).
- **Step 4, feat: image previews.** `LoadImageRequest.max_side`, scaled on the bridge
  with `golang.org/x/image`; the background fetch and the preview bound; the desktop's
  main asking the proxy for a preview offline. **Failure review**: caches a derived
  result.
- **Step 5, feat: reads made offline.** Kept, shown, sent before going live; marking
  unread refused offline; kithd's receipt never behind the network's. **Failure
  review**: merges writers.
- **With Phase 3 (step 1b): the proxy fails over.** The `[[bridge]]` list in the proxy, a
  store per bridge, this machine's own kithd not stored, the store moved on a rotation.
  **Failure review**: merges writers, as above.
