# Design: the desktop's partial cache (step 1c)

Status: a draft, reopened 2026-11-19. The desktop's partial cache gets a design and
planning phase of its own before any build, starting from
[One cache or two](#one-cache-or-two-on-a-machine-that-runs-kithd), which this draft did
not answer. The answers given so far to its questions are at the end, provisional until
that phase ends. It adds to [design-clients.md](design-clients.md) (Where messages are kept,
Phasing) and to [design-mesh.md](design-mesh.md), which it does not change except where
[What steps 5–8 must not assume](#what-steps-58-must-not-assume) says so. Nothing here is
built yet.

## Goal

The desktop window holds nothing of its own. `kith-relay` forwards every call to kithd
and keeps no state across starts (`cmd/kith-relay/relay.go`, `forward`); main keeps
attachments in memory only, 64 MiB at most (`mediaCacheBytes`,
`desktop/src/main/media.ts`); the window's state lives in the page. On a device that
joined a bridge elsewhere this costs two things:

- **No offline reading.** With the bridge out of reach (a train, a laptop away from the
  tailnet) the window says that kithd is not reachable and shows nothing.
- **No instant start.** The first paint waits for a TLS handshake to the bridge and for
  `Rail`, `Rooms`, `CachedUnread` and the open room's `CachedTimeline` (up to 2000
  messages, `timelineCacheLimit` in `internal/local/service.go`) to cross the network.

The **partial cache** is a store the relay keeps on this machine: the window's profile's
rooms and a recent window of each room's messages, enough to draw the window offline
and at start.
Scrollback past it comes from the bridge, as today.

Not goals:

- **Writing offline.** Sending, editing, reacting, filing and settings need a bridge, as
  design-clients.md decision 3 says for mobile. One exception is proposed, reads made
  offline (question 5).
- **Search offline.** Search over all history is the bridge's (design-clients.md, Where
  messages are kept).
- **The terminal client.** It keeps nothing either; it can use the same store later.
- **A machine running its own kithd.** Its kithd's cache is on the same disk (question 2).

## What reaches the window, and how

design-clients.md decision 6: **messages never cross the mesh.** Each bridge reads the
networks itself and keeps them in its own cache. So whatever the desktop holds of
messages comes from its bridge's RPCs, never from mesh ops.

The mesh carries less than the window shows, and none of it in the form the window
shows it:

| state | kept on the bridge in | reaches the window by | the store keeps |
| --- | --- | --- | --- |
| messages: edits, deletions, polls, attachment metadata | the cache | `CachedTimeline`, then the `Messages` stream (`internal/daemon/messagelog.go`) | a recent window per room |
| reactions | the cache | `CachedReactions`, the `Reactions` stream | those on messages it holds |
| unread counts, the rail, rooms, last messages, invites | derived on the bridge: the network's counts, the mesh's reads, the config's rules | `CachedUnread`, `Rail`, `Rooms`, `LastMessages`, `CachedInvites`; `UnreadStream`, `Changed` | the last answer |
| read positions and unread marks made in kith | the mesh (`read/`, `thread/`, `marked/`, `internal/mesh/crdt/read.go`), mirrored into the cache | folded into `CachedUnread` and `UnreadStream` | inside the last answer |
| reads the network reports (made on the phone) | the cache alone (docs/database.md, The mesh file) | the same | inside the last answer |
| drafts | the cache; the mesh after step 6 | `Drafts`, `Changed` (`CHANGE_DRAFTS`) | the last answer |
| shared config | `config.toml`; the mesh after step 5 | `Device`, `ConfigSettings`, `Theme`, `ConfigChanged` | the answers the window draws by |
| names, sender colors, pictures | derived on the bridge | names shaped into each message (`Names.changes`), `SenderSlots`, `SenderPictures`, `LoadPicture` | the last answers, and pictures |
| attachments | the bridge's media cache | `LoadImage`, called by main | nothing (question 4) |
| roster, other bridges, aliases | the mesh | `Devices`, `MeshStatus`; aliases applied by kithd | nothing |
| `device.toml` | this machine | read by the relay | (it is already here) |

The read positions show why the mesh alone is not enough: an unread count comes from the
network's count, the mesh's reads and the reads the network reports, and two of those
three are only in the bridge's cache. The window needs the bridge's derived
answers whatever the store also holds.

## One cache or two on a machine that runs kithd

The question this draft left out, and the first the design phase answers (Eugene,
2026-11-19). A laptop that runs kithd as a bridge keeps every message in kithd's cache
while that kithd runs, so a window there needs no store of its own. But with standby
bridges (design-clients.md decision 8) the laptop's kithd is often not the active one:
it holds no network connection and is a client of the homelab. Then the window reads
from another bridge, and the laptop's own cache is falling behind. So:

- **Is it one cache or two?** kithd's cache and the desktop's store on one machine are
  two copies of the same messages, kept by two writers, with two keys and two schemas.
- **Does kithd's cache take what a client reads while kithd is on standby,** or only
  what kithd's own network connections bring? If it does, a standby kithd is itself the
  "client cache", and the window always reads from the kithd on its own machine.
- **A machine with no kithd** (a laptop that only joined a bridge) still needs a store
  of its own, or a kithd running only as a client.

Shapes to compare in the design phase, none chosen:

1. **Two caches**, as drafted below: kithd's as today, and the relay's store on a device
   that runs no kithd (question 2's recommendation).
2. **One cache, kept by kithd**: a standby kithd fills its own cache from the active
   bridge's answers, and the window always talks to its machine's kithd. A machine that
   should not run a bridge runs kithd as a client only.
3. **One store format, two writers**: the relay's store and kithd's cache share a layout,
   and whichever runs keeps it current.

Each answers differently what the relay may open (today it never reads kithd's cache,
`scripts/deps-check.sh`), how a standby kithd that takes over catches up, and what
question 1 (answers or replica) means.

## A cache of the bridge's answers, not a replica

design-clients.md says a device holding a partial cache is a replica of the mesh, syncing
through its bridge, and design-mesh.md decision 8 ties HTTP/2 on the local socket to it.
This design proposes otherwise (question 1): **the store is a cache of the answers its
bridge gives, and the device stays a thin `device` in the roster.**

Against a cache of answers, a replica adds little:

- **It could not write.** The mesh takes an op only from a bridge (`State.take`,
  `internal/mesh/crdt/state.go`: `signer.Role != RoleBridge` is `ErrNotAMember`). A
  replica that wrote would need a role with signing authority, which is the question
  of offline writes again. A replica that does not write only relays what bridges
  wrote.
- **Offline it is as stale as a cached answer.** Neither hears anything without a
  bridge.
- **Online it is as current.** A cached answer is replaced on the `Changed` frame that
  says it changed, as the window already re-reads it.
- **It would hold more than its profile.** Mesh state is not per profile, and an op is
  sealed and signed whole: a replica in profile `work` would hold every profile's drafts
  and reads. kithd's `profileGate` (`internal/daemon/profiles.go`) narrows every answer to
  the profile instead.
- **It would cost the mesh** a role, a sealing key per device, wraps for it, a new epoch
  at its removal, a Sync served to a non-bridge, and h2c on the socket
  ([If the desktop were a replica](#if-the-desktop-were-a-replica)).

What it would buy that a cache does not: reading a mailbox's ops with no bridge in
reach, and passing ops between bridges. Neither gives the window a message.

## What the store holds

- **One profile per file.** The rooms of the profile the window runs in, as kithd's last
  `Rooms` and `Rail` answers in that profile name them. The file is the scope: an answer
  from one profile's store cannot hold another profile's room, and a profile switch opens
  another file (Profile switch).
- **A recent window per room**: its newest 500 messages (question 3), each as the
  bridge's cache last sent it (`domain.Message`, names shaped), with their reactions.
  Threads' replies are messages of the room, so they are in the window when recent.
- **The answers the window draws from at start**, each as last answered: `Device` (the
  profiles; only the relay can answer it, and on a joined device it reads the bridge's
  config, so it is unavailable offline today), `Rail`, `Rooms`, `LastMessages`,
  `CachedUnread`, `CachedInvites`, `Drafts`, `Selves`, `SenderSlots`, `SenderPictures` and
  `Theme` (whose shared half is the bridge's config). `DeviceSettings` needs no copy: the
  relay reads it from `device.toml`.
- **Pictures**: rooms' and people's (`LoadPicture`), which every row of the room list
  draws. Attachments are not kept (question 4).
- **Where it stands with each bridge**: the bridge's device ID, its change numbering's
  epoch, the last change taken, and when it was last heard.

**Bounds.** Each room is trimmed to its newest 500 after every write to it. A store past
256 MiB trims rooms to their newest 50, the room whose last message is oldest first, until
it is under (question 3). Trimmed rows free pages, and the file is rewritten at open by the
cache's own rule (a fifth free and at least 32 MiB, docs/database.md, The file).

**A room leaving the profile** (filed elsewhere, left, the profile's rules changed) is
deleted from the store at the first `Rooms` answer that lacks it, with its messages and
reactions. A room merged into another (`MergeRoom`, a WhatsApp chat known by a LID) goes
the same way, and the room it joined is filled again.

## Where it lives

- **Only on a device that joined a bridge** (`setup.JoinedBridge`, the relay's
  `loadBridged`), question 2.
- **The file** is `desktop-<instance>-<hash>.db` in the data dir of this machine's client
  storage (`setup.ClientStorage`), `<hash>` naming the profile as `domain.AccountKey` names
  an account, so a profile's name never becomes a path.
- **Sealed** like the cache and the mesh store: `internal/sealeddb` (Adiantum, every page
  and the log encrypted), owner-only.
- **Its key** is one 32-byte key per machine in this machine's keyring
  (`Storage.KeyringService`), made on first use as kithd's cache key is
  (`session.LoadOrCreateStoreKey`). It is not the device key and not derived from it, so a
  device key rotated (step 8) leaves the store readable. No keyring, a locked one, or one
  that does not answer within `keystore.Wait`: no store, and the relay runs as today. A
  store key is never kept in a file, as kithd's are not.
- **Layering.** `scripts/deps-check.sh` keeps `internal/db`, `internal/mesh` and
  `internal/session` out of the relay ("it forwards to the daemon, never reading the
  cache"). The store is a package of its own, `internal/devicecache`, over
  `internal/sealeddb` and `internal/domain`, with a depguard rule; the relay's rule gains
  it. The key's helper moves from `internal/session` to `internal/keystore`, which the
  relay already links. The relay still never opens kithd's cache.
- **A cache, rebuilt on doubt.** Every row is a copy of the bridge's. A file of an
  unknown layout, or that does not open, is deleted and filled again, as kithd's cache is
  rebuilt (docs/database.md, Changing the schema), never migrated column by column.
- **Deleted when the bridge refuses this device's key** (`kithdAt.failure` says
  `PermissionDenied` only on the bridge's TLS alert, never on a reset): an unpaired laptop
  keeps nothing it was unpaired from (question 6).

## How it is filled and kept current

The relay keeps **one upstream stream of its own** to the bridge for the profile in use:
`Messages` with `Kith-Profile` set, resumed from the store's last change for that bridge.
kithd sends only the profile's rooms' changes, and a frame with no message for a run it
left out, so the position always moves (`sendChanges`). Each change is written in one
transaction with the new position, so a relay killed mid-write (the window gone, its
stdin closed) leaves a store that resumes where it stood.

- **The window's own streams and calls are forwarded as today** while the bridge
  answers. The store's stream is a second one; online, the window sees exactly what it
  sees now. Each forwarded answer of a listed call is also written to the store.
- **Filling a room**: when the window opens it (its `CachedTimeline` answer), and in the
  background for every room of the profile, one at a time, newest last message first
  (`LastMessages`), until each is filled or the size bound is reached. `CachedTimeline`
  gains a `limit` (0 keeps 2000) so a fill moves 500 messages, not 2000. A fill replaces
  the room's rows whole: a message deleted or trimmed on the bridge does not survive in
  the store.
- **`read_again`** (the store behind by more than 5000 changes, `tailBehind`, or the
  bridge's cache cleared): the stream resumes from the newest, the rooms held are marked
  stale, and each is filled again in the same order. A stale room is still shown offline,
  marked as of when it was filled.
- **Names change with no message changing** (a name rule, a person merged): `Changed`
  says `CHANGE_NAMES`, and the held rooms are marked stale and filled again. The same for
  `CHANGE_RAIL` and `CHANGE_ROOMS`: `Rail` and `Rooms` are read again, and rooms gone are
  deleted.
- **Answers that change by a stream** (`UnreadStream`, `Reactions`, `Invites`): the relay
  holds those streams too for the profile in use, and writes each frame into the held
  answer. `CachedUnread`, read again each time the stream opens, as the window reads it,
  replaces the held answer.

## Held and live

The relay is in one of two states, by its own stream to the bridge:

- **Live**: the store's stream is open and has caught up with the bridge's last change
  as of opening. Everything is forwarded, as today.
- **Held**: the bridge has not answered yet, or its connection was lost and redialing
  fails (a connection idle for 15 s is pinged and dropped 10 s later, docs/remote.md,
  Using a bridge). The listed calls are answered from the store. Every other call fails
  as today, unavailable. The window's `Messages` stream is served from the store's own
  change numbering, with an epoch of its own.

**The switch costs one re-read, through a path the window already has.** A `Messages`
stream resumed with an epoch the server does not know answers `read_again`
(`resumeAt`), which main turns into a `reload` (`resuming`, `desktop/src/main/gate.ts`),
and the window reads its timeline, thread and rooms again (`renderer/session.ts`). So:

- held → live: the relay ends the window's held streams; the window reopens them, now
  forwarded, with the store's epoch; kithd answers `read_again`; the window reads again
  from the bridge.
- live → held: the forwarded streams are cut, as today when a bridge goes; reopened, the
  relay answers from the store with the bridge's epoch, which the store does not number
  in; it answers `read_again`; the window reads again from the store.

**Instant start.** The relay opens the store, prints its socket, and starts held when the
store holds the profile; the window draws from it at once. The relay dials meanwhile, and
once live the window reads again. On a LAN that is one extra read a moment after start;
across the internet it is the difference between a window and a blank.

**The window says which.** `DeviceService.Device` gains `held_since`: zero when live,
else when the store was last current. The window shows "Offline: showing what this device
held at 14:02"; what needs a bridge fails as it does now when kithd is not reachable.

## Offline

- **Reading**: every room of the profile, to the edge of its window. Older messages need
  the bridge: the window says so where `Timeline` (scrollback) fails.
- **Search, people's cards, the bridge's settings**: need the bridge. `Device`,
  `DeviceSettings` and `Theme` are answered, so the profiles, this device's settings and
  the colors hold.
- **Writing**: refused, unavailable, as today. A draft typed offline stays in the window,
  which says so, as a draft kithd does not take does today (docs/desktop.md, First run).
- **Reads made offline** (opening a room, `m`): question 5. Proposed: the store keeps
  them, the window shows the room read, and once live the relay sends each as `MarkRead`.
  A read position is a max register on every bridge (`Store.Read` writes a position only
  when the mesh holds it less far), so a late read never moves a position back. Marking unread is
  refused offline: a mark is newest-wins, and a late one could undo a newer one made
  elsewhere (`Store.TakeMarks`).
- **Profile switch offline**: among the profiles the last `Device` answer listed that
  have a store.

## Profile switch

The window switches by `ChooseProfile`; main ends the streams opened in the old profile
(`Gate`, `desktop/src/main/gate.ts`). The relay does the same with its own: it ends the
store's streams, closes that profile's file, opens the next and resumes it from its own
position. A profile's store is filled only while the window runs in it. One idle for long
comes back through `read_again`: what it held is shown at once and filled again.

A room in two profiles is in both files. That is the price of the file being the scope.

## Failover between bridges (Phase 3)

Phase 3 gives the relay a `[[bridge]]` list in priority order. For the store:

- **Positions are per bridge.** A change number means nothing at another bridge (each
  numbers its own cache). Resuming at another bridge sends the time last heard from the
  old one less a grace window (design-clients.md, Failover), which `MessagesRequest` gains
  in Phase 3. The overlap comes again.
- **Dedup by network ID** is the store's key: a message is `(room, event ID)`, so the
  overlap rewrites rows in place. Room IDs name the network and the account
  (`whatsapp:<account>/<chat>`, `internal/domain/kithlink.go`), so two bridges running
  one account name its rooms alike.
- **Two bridges' copies of one message** may differ: the second bridge may not have the
  edit yet. A copy replaces the held one only as `domain.SupersedesEdit` says (later
  edit, event ID breaking ties), a deletion is kept against any undeleted copy, and a
  `Reverted` copy replaces regardless, as kithd's cache and the window resolve history
  (`TestTheCacheAndTheClientResolveHistoryAlike`).
- **A client on several bridges** (design-mesh.md decision 6) holds the profile's rooms
  from all of them in one file, each room filled from whichever bridge `Rooms` named it.
- **A bridge whose key rotated** (step 8) is the same bridge: the relay moves the
  position to the new ID when it rewrites its pin.

## If the desktop were a replica

If question 1 is answered the other way, or Android needs a replica later, the role's
limits are these. Each is checked against the code as it stands, with mesh step 5a
(sealed ops) as built on `feat/mesh-sealed-ops`.

- **A new role, `replica`**, beside `bridge` and `device` (`crdt.Role`). It signs no op:
  `State.take` already refuses every signer but a bridge, so nothing changes there. Its
  writes are RPCs to its bridge, as a device's are. It cannot pair, confirm or remove
  anything a paired device cannot already ask its bridge for.
- **It needs a sealing key and the epoch keys** to open ops sealed since step 5a. Step
  5a wraps a content key only for bridges (`keyring.make` and `keyring.wrapFor` keep
  `m.Role == crdt.RoleBridge`) and makes a new epoch only after a bridge's removal
  (`State.Since` checks `m.Role == RoleBridge`). Both would widen to every member holding
  a sealing key, so a stolen laptop, once removed, reads nothing sealed after.
- **It syncs only with its bridge**, over the same `Sync`. Today Sync is refused over the
  socket (`errSyncOverSocket`, `internal/daemon/mesh.go`) and to any peer not a bridge
  (`stillPaired`); both would admit a replica to its own bridge. A replica beside its
  bridge needs h2c on the socket, since Sync streams both ways (design-mesh.md
  decision 8). Remote, the relay's TLS already speaks HTTP/2
  (`internal/daemon/dialbridge.go`).
- **It never holds the log back.** `compact` keeps an op until every peer heard within
  the horizon holds it. A replica writes nothing, so it has nothing to lose by being
  passed over: one that is behind takes a snapshot, like a bridge back after the horizon.
  It is not counted when the mesh asks whether every replica has converged, as a mailbox
  is not.
- **It holds every profile's soft state**, which is why it would not replace the answers
  the window reads.

## What steps 5–8 must not assume

Steps 5 (config), 6 (drafts), 7 (verdicts) and 8 (key rotation) are next. To keep the
desktop's store possible, as proposed here or as a replica, without reworking them:

1. **A client reads the merged state through kithd's calls.** The window reads config
   through `Device`, `ConfigSettings`, `Theme` and `ConfigChanged`, and drafts through
   `Drafts` and `Changed`. Steps 5 and 6 keep these answering the merged state, narrowed to
   the profile, and keep saying when it changed (`ConfigChanged`, `CHANGE_DRAFTS`): the
   store re-reads on those frames and on nothing else. A merged config or draft that only
   a mesh replica can compute is out of the desktop's reach.
2. **A draft's base may be old, and from another bridge.** A window that kept a draft
   through an hour offline sends it with `ReplaceDraft` over the words it read then; in
   Phase 3 it may send it to a bridge other than the one it read them from. Step 6 takes
   the base from the write (`over`, design-mesh.md, Drafts) and never assumes it is the
   value the bridge holds, or one it ever held. The rebase then keeps both, as it does
   for two windows today.
3. **A read may arrive late.** If question 5 is answered yes, `MarkRead` comes hours after
   the read, for an event the network may have moved past. The mesh's max register takes
   that. kithd must also not send the network a receipt behind the one it holds: that is
   kithd's receipt path, not the mesh, and the step that adds offline reads checks it.
4. **A rotated bridge is the same bridge.** Step 8's rotation op names the old ID and
   the new key, signed by both. A client rewriting its pin also gets the old → new pair,
   so the relay moves its stored position to the new ID and does not fill everything
   again. A laptop is the client most likely to be offline past the grace. It then meets a
   bridge key it does not pin, which is not the bridge refusing this device's key, so the
   store is kept; once paired again with the same bridge, its rooms are filled again in
   place.
5. **`device.toml` stays out of the mesh.** The store's bounds, if made settings, are this
   machine's, in `device.toml`. Step 5's `config.Local` sorts only `config.toml`'s keys,
   and nothing there names the desktop's store.
6. **An older bridge does not skip what it does not know.** A roster value of an unknown
   role, or a write of an unknown key kind, refuses the op (`readRosterWrite`, `readWrite`);
   a refused op is dropped, and every later op of its origin waits behind it, held as not
   ready, at that bridge for good (`offer`, `internal/mesh/replica.go`). So each new key
   kind (steps 5–7) and each new role (`mailbox` in step 9, `replica` if ever) goes with a
   higher protocol version in Hello, which refuses the session rather than wedging the
   origin. This holds whatever question 1 decides.
7. **If a replica role comes, step 5a's role checks widen** (If the desktop were a
   replica): wraps for every member with a sealing key, and a new epoch after any such
   member's removal. Step 5a is not merged; under question 1's recommended answer it does
   not change.

## Failure review the build needs

Every step is in at least one of CONTRIBUTING.md's four classes. What each class test must
generate:

- **Caches a derived result** (the store holds the rail, counts and shaped names). The
  generator drives the bridge's side through every event the store reads, in any order,
  with the relay online, offline, restarted or switching profile between any two, and
  asserts that once live and settled, every listed answer from the store equals the
  bridge's answer to the same call. Blind spots to put in it: a name rule changed with no
  message changing; a deletion the store misses because it was behind past `tailBehind`;
  a room trimmed on the bridge (`[storage] messages_per_room`); a room merged into another;
  a room leaving the profile by a config edit, not a filing; a fill and a stream change to
  the same room crossing; the store's size bound trimming a room the window has open.
- **Enforces scope** (the file is the profile). A test of
  `TestAFailedLookupNeverWidensTheScope`'s kind, generating: a `Kith-Profile` header
  naming another profile, none, or one the config lacks; a store that fails to open; a profile switch between a call's arrival and its
  answer; a `Rooms` answer that fails. None may answer from another profile's file or keep
  a room the last good `Rooms` answer lacked.
- **Concurrent** (the store's stream, the window's calls, a switch and an exit at once).
  `-race`, and every exit route: the window crashing (stdin closing), SIGTERM, the relay
  killed between a change and its position, the keyring not answering at open. The store
  after each resumes where it stood, or is rebuilt, never half-written.
- **Merges what more than one writer wrote** (Phase 3: two bridges' copies of one
  message). `TestTheCacheAndTheClientResolveHistoryAlike` gains the store as a third
  layer: for every delivery order across two bridges, the store's row equals the cache's.
  Ties at one millisecond, a deletion on one bridge only, a `Reverted` copy, and the
  resume's overlap arriving after newer copies.

## Decisions (proposed, not taken)

1. **The store is a cache of its bridge's answers**, not a mesh replica (question 1).
2. **The relay answers a fixed list of read calls from the store**, and only while held;
   every other call is forwarded or fails as today. The window's calls and streams keep
   their shape; `Device` gains `held_since`, `CachedTimeline` a `limit`.
3. **One sealed file per profile**, keyed by a store key of this machine's keyring; no
   keyring, no store.
4. **The store numbers its own changes**, so held and live switch through `read_again`,
   which the window already handles.
5. **The relay starts held** when the store holds the profile, and reads again once live.
6. **A fill replaces a room whole**; the room set is the last `Rooms` answer.
7. **Messages are keyed by network ID** and resolved by `domain.SupersedesEdit`, as the
   cache and the window resolve them.
8. **Nothing is written offline** but reads (question 5).

## Open questions

Answers given 2026-11-19, provisional until the design phase above settles one cache or
two. Questions 1 and 2 stay open and are part of it.

1. **A cache of answers, or a mesh replica?** *Open: part of the design phase.* *Recommended: a cache of answers.* A
   replica signs nothing, so it only holds what bridges wrote; offline it is as stale as
   a cached answer, and the window needs the bridge's derived answers anyway (unread
   counts include reads only the bridge's cache holds). A replica would also hold every
   profile's drafts, and need a role, sealing keys, wraps, epochs at its removal, Sync to
   a non-bridge and h2c. With this answer the desktop's store no longer waits for mesh
   steps 5–8, and design-mesh.md decision 8 waits for whatever first puts a replica
   beside its bridge.
2. **Which machines keep a store?** *Open: part of the design phase.* *Recommended: only a device that joined a bridge.*
   Beside a local kithd the cache is on the same disk and answers in milliseconds; a
   second copy adds a writer and nothing to read. (The other option: every machine
   running the window, so it still reads when the local kithd is down.)
3. **How much?** *Answered, not final:* each room's newest 500 messages, and none older
   than two weeks; 256 MiB per profile. Settled with one cache or two.
   *Recommended in the draft: each room's newest 500 messages, 256 MiB per profile,
   pictures included.* 500 is several screens of any room and a quarter of what
   `CachedTimeline` returns. (Others: 2000, `CachedTimeline`'s own; or by age, such as 30
   days.)
4. **Attachments on disk?** *Answered:* small previews of images only.
   *Recommended in the draft: none in the first build.* They are most of a
   cache's size, main already keeps 64 MiB of them in memory, and pictures (the rows'
   faces) are kept. Offline an attachment says it needs the bridge. (Others: the ones
   the window opened, up to a bound; small previews of images.)
5. **Reads made offline?** *Answered:* kept, and sent as `MarkRead` once the bridge is
   back; marking unread stays refused offline. *Recommended in the draft: kept in the store and sent as `MarkRead` once
   live.* Read state is local-first: a room read stays read. A position is a max register
   everywhere, so a late one moves nothing back. Marking unread stays refused offline.
   (The other option: reads refused offline like every other write.)
6. **The store when the bridge refuses this device's key?** *Answered:* delete it.
   *Recommended in the draft: delete it.*
   Only a definite refusal (a TLS alert) counts. A lost laptop, once unpaired, deletes
   what it held the first time it is opened in reach of the bridge; until then the keyring
   guards it. It is a cache: pairing again fills it again. (The other option: keep it,
   read-only.)

## Plan

A draft, not the plan: it is rewritten once the design phase settles one cache or two.
Each PR lands working, with a failure review as above.

- **Step 1, feat: the relay holds its profile's answers.** `internal/devicecache` (the
  sealed file per profile, its key in the keyring, its own change numbering), the depguard
  rule and the key helper in `internal/keystore`; the relay's own `Messages`,
  `UnreadStream`, `Reactions` and `Invites` streams for the profile in use; the listed
  answers kept as they are forwarded; held and live, `read_again` at each switch;
  `Device.held_since` and the window's offline line; rooms filled when opened, each
  trimmed to its newest 500; the store deleted on a refusal. **Failure review**: caches a derived result, enforces scope,
  concurrent.
- **Step 2, feat: every room of the profile, within bounds.** `CachedTimeline`'s `limit`,
  the background fill by last message, the per-profile bound, stale rooms
  after `read_again` and `CHANGE_NAMES`, pictures, the rewrite at open. **Failure
  review**: caches a derived result (fills, trims and the stream crossing).
- **Step 3, feat: reads made offline** (if question 5 says so). Kept, shown, sent once
  live; marking unread refused offline; kithd's receipt never behind the network's.
  **Failure review**: merges writers (a read offline against reads elsewhere and on the
  phone).
- **With Phase 3 (step 1b): the store across bridges.** Positions per bridge, resuming by
  time less grace, two bridges' copies resolved alike, the pin moved on a rotation.
  **Failure review**: merges writers, as above.
